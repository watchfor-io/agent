package update

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultRepo is the GitHub repository releases are published from.
const DefaultRepo = "watchfor-io/agent"

// Source fetches release assets. Every request — and every redirect — must
// be HTTPS to one of Hosts; a release page that sends the client anywhere
// else is treated as an attack, not followed.
type Source struct {
	Repo     string
	API      string // https://api.github.com
	Releases string // https://github.com
	Hosts    []string
	Client   *http.Client

	insecure bool // tests: allow plain http to a local server
}

// NewSource is the production source: GitHub over TLS 1.2+, system roots,
// at most five redirects, all within GitHub's asset hosts.
func NewSource() *Source {
	s := &Source{
		Repo:     DefaultRepo,
		API:      "https://api.github.com",
		Releases: "https://github.com",
		Hosts: []string{
			"api.github.com",
			"github.com",
			"objects.githubusercontent.com",
			"release-assets.githubusercontent.com",
		},
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

// Latest asks the API for the newest published (non-draft, non-pre)
// release and returns its version without the "v".
func (s *Source) Latest(ctx context.Context) (string, error) {
	body, err := s.get(ctx, s.API+"/repos/"+s.Repo+"/releases/latest", "application/vnd.github+json", 1<<20)
	if err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	var rel struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	if rel.Draft || rel.Prerelease || !strings.HasPrefix(rel.TagName, "v") {
		return "", fmt.Errorf("latest release: unexpected tag %q", rel.TagName)
	}
	v, err := Parse(rel.TagName)
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
