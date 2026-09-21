// Package push delivers encoded batches to the ingest endpoint over HTTPS
// and classifies failures: a rejected token is final, everything transient
// is the spool's problem.
package push

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
)

// Path is the ingest endpoint, appended to server.url.
const Path = "/v1/agent/metrics"

// ErrUnauthorized is ingest's own verdict on the token: unknown, revoked,
// or a plan without hosts. The agent backs off from it and, if it holds,
// stops — an outage to retry through it is not.
var ErrUnauthorized = errors.New("token rejected by the server")

// ReasonHeader is set by WatchFor's ingest on every 401/403/503 it produces
// (token_unknown, plan_forbidden, lookup_unavailable). A 401 without it did
// not come from ingest — a proxy, CDN or captive portal answered — and is
// treated as a transient failure, never as a revoked token.
const ReasonHeader = "X-WatchFor-Reason"

// StatusError is any answer that is not a 2xx, with the start of the
// body so the log line can say what the server said.
type StatusError struct {
	Code int
	Body string
	// RetryAfter is the server's own advice on when to try again, if any.
	RetryAfter time.Duration
}

// Error is the log line's wording: the status, and the body when there
// was one.
func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("server answered %d", e.Code)
	}
	return fmt.Sprintf("server answered %d: %s", e.Code, e.Body)
}

// Retryable reports whether a Send failure should be spooled and retried:
// network trouble, 429, 5xx, a 404/405 (an ingest mid-deploy or a proxy's
// default backend answering for it), and a 401/403 that did not come from
// ingest (no reason header: something in between answered). A 4xx about
// the batch itself (413, 400) is not retried.
func Retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusMethodNotAllowed:
			return true
		}
		return se.Code >= 500
	}
	return err != nil && !errors.Is(err, ErrUnauthorized)
}

// RetryAfter is how long the server asked us to wait, when it did.
func RetryAfter(err error) time.Duration {
	var se *StatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

// Ack is the server's answer to an accepted batch. Interval is the shortest
// push interval the account allows, in seconds; the agent stretches to it
// when its own setting is faster. Zero means the server has no opinion.
type Ack struct {
	Accepted int `json:"accepted"`
	Dropped  int `json:"dropped"`
	Interval int `json:"interval"`
	// LatestVersion is the newest agent release the server knows of, so a
	// host learns about updates through the one connection it already has.
	// Empty when the server has no opinion.
	LatestVersion string `json:"latest_version"`
}

// Client posts batches to one ingest endpoint with one token, over a
// transport that insists on TLS 1.2+ and, when given, a private CA.
type Client struct {
	endpoint  string
	token     string
	userAgent string
	http      *http.Client
}

// New builds a Client for baseURL. caFile, when set, replaces the system
// roots — for an ingest behind an internal CA.
func New(baseURL, token, caFile string, timeout time.Duration, version string) (*Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	// The token goes only where agent.yml says. Go drops the Authorization
	// header on a cross-host redirect anyway; refusing redirects outright
	// makes the rule visible and keeps a batch from being replayed elsewhere.
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file: no certificates found in %s", caFile)
		}
		tlsCfg.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg
	transport.ForceAttemptHTTP2 = true
	return &Client{
		endpoint:  strings.TrimRight(baseURL, "/") + Path,
		token:     token,
		userAgent: "watchfor-agent/" + version,
		http:      &http.Client{CheckRedirect: noRedirect, Transport: transport, Timeout: timeout},
	}, nil
}

// TokenFingerprint identifies the token without revealing it (first 12
// hex digits of its SHA-256): the rejected-token marker is keyed on it, so
// a new token clears the block by itself.
func (c *Client) TokenFingerprint() string {
	sum := sha256.Sum256([]byte(c.token))
	return hex.EncodeToString(sum[:6])
}

// Encode is the wire form of a payload: JSON, gzipped. The spool keeps
// these bytes as they are, so a spooled batch is resent unchanged.
func Encode(p *metric.Payload) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gz).Encode(p); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode is the inverse of Encode, for `check` (which prints the batch)
// and tests.
func Decode(body []byte) (*metric.Payload, error) {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var p metric.Payload
	if err := json.NewDecoder(gz).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Send posts one encoded batch. A 2xx is an Ack even with an empty body;
// a 401/403 carrying ingest's reason header is ErrUnauthorized; anything
// else is a StatusError for Retryable to sort.
func (c *Client) Send(ctx context.Context, body []byte) (Ack, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Ack{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("User-Agent", c.userAgent)

	res, err := c.http.Do(req)
	if err != nil {
		return Ack{}, err
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	// let the connection be reused, but never read an unbounded body
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))

	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		var ack Ack
		// An empty or non-JSON body is still a success; only the hint is lost.
		_ = json.Unmarshal(msg, &ack)
		return ack, nil
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		// Only ingest's own verdict counts as a rejected token; a 401 from a
		// proxy or CDN in the way is a transient failure like any other.
		// Any reason at all means ingest answered (a proxy never sets the
		// header); today it says token_unknown or plan_forbidden, and a
		// value added later must not be mistaken for an outage.
		if strings.TrimSpace(res.Header.Get(ReasonHeader)) == "" {
			return Ack{}, &StatusError{Code: res.StatusCode, Body: strings.TrimSpace(string(msg))}
		}
		// Keep the server's reason: "unknown or revoked token" and "hosts are
		// not included in this plan" call for different fixes.
		if reason := strings.TrimSpace(string(msg)); reason != "" {
			return Ack{}, fmt.Errorf("%w: %s", ErrUnauthorized, reason)
		}
		return Ack{}, ErrUnauthorized
	default:
		return Ack{}, &StatusError{Code: res.StatusCode, Body: strings.TrimSpace(string(msg)), RetryAfter: retryAfter(res)}
	}
}

// retryAfter reads a Retry-After header given in seconds; a date form or
// nonsense means "no advice".
func retryAfter(res *http.Response) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && s > 0 {
		return time.Duration(min(s, 3600)) * time.Second
	}
	return 0
}
