package push

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
)

func payload() *metric.Payload {
	return &metric.Payload{
		Version: metric.PayloadVersion, Agent: "test",
		Host:    metric.Host{ID: "abc", Name: "web-01", Arch: "amd64"},
		Samples: []metric.Sample{{T: 1, M: "cpu.usage_pct", V: 12.5}},
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	body, err := Encode(payload())
	if err != nil {
		t.Fatal(err)
	}
	p, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if p.Host.Name != "web-01" || len(p.Samples) != 1 || p.Samples[0].V != 12.5 {
		t.Errorf("round trip lost data: %+v", p)
	}
}

func TestSendHeadersAndStatuses(t *testing.T) {
	var got http.Header
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if r.URL.Path != Path {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(status)
		w.Write([]byte("nope"))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "tok-123", "", time.Second, "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := Encode(payload())

	status = http.StatusAccepted
	if _, err := c.Send(context.Background(), body); err != nil {
		t.Fatalf("202 should be success: %v", err)
	}
	if got.Get("Authorization") != "Bearer tok-123" || got.Get("Content-Encoding") != "gzip" || !strings.HasPrefix(got.Get("User-Agent"), "watchfor-agent/9.9.9") {
		t.Errorf("headers = %v", got)
	}

	status = http.StatusUnauthorized
	if _, err := c.Send(context.Background(), body); !errors.Is(err, ErrUnauthorized) || Retryable(err) {
		t.Errorf("401: err=%v retryable=%v", err, Retryable(err))
	}
	status = http.StatusTooManyRequests
	if _, err := c.Send(context.Background(), body); !Retryable(err) {
		t.Errorf("429 must be retryable, got %v", err)
	}
	status = http.StatusBadGateway
	if _, err := c.Send(context.Background(), body); !Retryable(err) {
		t.Errorf("502 must be retryable, got %v", err)
	}
	status = http.StatusRequestEntityTooLarge
	_, err = c.Send(context.Background(), body)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 413 || Retryable(err) || !strings.Contains(err.Error(), "nope") {
		t.Errorf("413: err=%v retryable=%v", err, Retryable(err))
	}
}

func TestNetworkErrorIsRetryable(t *testing.T) {
	c, _ := New("http://127.0.0.1:1", "t", "", 200*time.Millisecond, "x")
	if _, err := c.Send(context.Background(), []byte("x")); !Retryable(err) {
		t.Errorf("connection refused must be retryable, got %v", err)
	}
}

func TestBadCAFile(t *testing.T) {
	if _, err := New("https://x", "t", "/nonexistent", time.Second, "x"); err == nil {
		t.Error("missing ca_file must fail at construction")
	}
}
