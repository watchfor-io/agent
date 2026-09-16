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
	"strings"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
)

const Path = "/v1/agent/metrics"

var ErrUnauthorized = errors.New("token rejected by the server")

type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("server answered %d", e.Code)
	}
	return fmt.Sprintf("server answered %d: %s", e.Code, e.Body)
}

// Retryable reports whether a Send failure should be spooled and retried:
// network trouble, 429 and 5xx. A 4xx means the batch itself is the problem.
func Retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusTooManyRequests || se.Code >= 500
	}
	return !errors.Is(err, ErrUnauthorized) && err != nil
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

type Client struct {
	endpoint  string
	token     string
	userAgent string
	http      *http.Client
}

func New(baseURL, token, caFile string, timeout time.Duration, version string) (*Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
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
		http:      &http.Client{Transport: transport, Timeout: timeout},
	}, nil
}

// TokenFingerprint identifies the token without revealing it (first 12
// hex digits of its SHA-256): the rejected-token marker is keyed on it, so
// a new token clears the block by itself.
func (c *Client) TokenFingerprint() string {
	sum := sha256.Sum256([]byte(c.token))
	return hex.EncodeToString(sum[:6])
}

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
	io.Copy(io.Discard, res.Body)

	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		var ack Ack
		// An empty or non-JSON body is still a success; only the hint is lost.
		_ = json.Unmarshal(msg, &ack)
		return ack, nil
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		// Keep the server's reason: "unknown or revoked token" and "hosts are
		// not included in this plan" call for different fixes.
		if reason := strings.TrimSpace(string(msg)); reason != "" {
			return Ack{}, fmt.Errorf("%w: %s", ErrUnauthorized, reason)
		}
		return Ack{}, ErrUnauthorized
	default:
		return Ack{}, &StatusError{Code: res.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
}
