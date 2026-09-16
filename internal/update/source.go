package update

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultRepo is the GitHub repository releases are published from — the
// storage for release files. Which release is current is watchfor.io's
// word (DefaultReleasesURL), never GitHub's.
const (
	DefaultRepo        = "watchfor-io/agent"
	DefaultReleasesURL = "https://watchfor.io/agent"
)

// Source fetches release files. Every request — and every redirect — must
// be HTTPS to one of Hosts; a page that sends the client anywhere else is
// treated as an attack, not followed. Files are verified after download
// (signature with the built-in key, then sha256), so the hosts only need
// to be ours or GitHub's, not trusted.
type Source struct {
	Repo        string
	Releases    string // https://github.com — release files
	ReleasesURL string // https://watchfor.io/agent — /latest
	Hosts       []string
	Client      *http.Client

	insecure bool // tests: allow plain http to a local server
}

// NewSource is the production source: TLS 1.2+, system roots, at most
// five redirects, all within watchfor.io and GitHub's file hosts.
// WATCHFOR_RELEASES_URL points a staging agent at a staging site.
func NewSource() *Source {
	s := &Source{
		Repo:        DefaultRepo,
		Releases:    "https://github.com",
		ReleasesURL: DefaultReleasesURL,
		Hosts: []string{
			"watchfor.io",
			"github.com",
			"objects.githubusercontent.com",
			"release-assets.githubusercontent.com",
		},
	}
	if v := os.Getenv("WATCHFOR_RELEASES_URL"); v != "" {
		if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
			s.ReleasesURL = strings.TrimRight(v, "/")
			s.Hosts = append(s.Hosts, strings.ToLower(u.Hostname()))
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	s.Client = &http.Client{Transport: tr, Timeout: 5 * time.Minute, CheckRedirect: s.checkRedirect}
	return s
}

func (s *Source) allowed(u *url.URL) error {
	if u.Scheme != "https" && !(s.insecure && u.Scheme == "http") {
		return fmt.Errorf("refusing non-HTTPS URL %s", u.Redacted())
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range s.Hosts {
		if host == h {
			return nil
		}
	}
	return fmt.Errorf("refusing to talk to %s (not a release host)", host)
}

func (s *Source) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	return s.allowed(req.URL)
}

// get fetches one URL into memory, refusing bodies larger than max.
func (s *Source) get(ctx context.Context, rawURL, accept string, max int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if err := s.allowed(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "watchfor-agent-upgrade")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u.Redacted(), res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("%s: larger than %d bytes, refusing", u.Redacted(), max)
	}
	return body, nil
}

// Latest asks watchfor.io for the newest release it has verified and
// returns its version without the "v".
func (s *Source) Latest(ctx context.Context) (string, error) {
	body, err := s.get(ctx, s.ReleasesURL+"/latest", "text/plain", 1<<10)
	if err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	tag := strings.TrimSpace(string(body))
	if !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("latest release: unexpected answer %q", tag)
	}
	v, err := Parse(tag)
	if err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	return v.String(), nil
}

// Asset downloads one file of the release for version (without "v").
func (s *Source) Asset(ctx context.Context, version, name string, max int64) ([]byte, error) {
	body, err := s.get(ctx, s.Releases+"/"+s.Repo+"/releases/download/v"+version+"/"+name, "", max)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return body, nil
}
