//go:build linux

package processes

import (
	"context"
	"testing"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/testutil"
)

func TestCountsTopAndWatch(t *testing.T) {
	procfs.Root = testutil.ProcRoot(t)
	m, err := New(testutil.Node(t, `
top: 5
watch:
  - name: "Web Content (2)"
  - cmdline: contentproc
  - name: nginx
`))
	if err != nil {
		t.Fatal(err)
	}
	b := metric.NewBatch(testutil.Now())
	if err := m.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if s, ok := testutil.Find(b, "proc.total", nil); !ok || s.V != 1 {
		t.Errorf("proc.total = %+v", s)
	}
	if s, ok := testutil.Find(b, "proc.sleeping", nil); !ok || s.V != 1 {
		t.Errorf("proc.sleeping = %+v", s)
	}
	if s, ok := testutil.Find(b, "proc.rss_bytes", metric.Labels{"pid": "4242", "name": "Web Content (2)"}); !ok || s.V == 0 {
		t.Errorf("top rss entry = %+v", s)
	}
	if s, ok := testutil.Find(b, "procwatch.count", metric.Labels{"watch": "Web Content (2)"}); !ok || s.V != 1 {
		t.Errorf("name watch = %+v", s)
	}
	if s, ok := testutil.Find(b, "procwatch.count", metric.Labels{"watch": "contentproc"}); !ok || s.V != 1 {
		t.Errorf("cmdline watch = %+v", s)
	}
	if s, ok := testutil.Find(b, "procwatch.count", metric.Labels{"watch": "nginx"}); !ok || s.V != 0 {
		t.Errorf("absent watch must report 0, got %+v", s)
	}
	if _, ok := testutil.Find(b, "procwatch.rss_bytes", metric.Labels{"watch": "nginx"}); ok {
		t.Error("absent watch must not report rss")
	}
	if testutil.Names(b)["proc.cpu_pct"] {
		t.Error("cpu needs a second sample")
	}
}

func TestWatchValidation(t *testing.T) {
	if _, err := New(testutil.Node(t, "watch:\n  - user: root\n")); err == nil {
		t.Error("a watch without name or cmdline must be rejected")
	}
	if _, err := New(testutil.Node(t, "top: 500")); err == nil {
		t.Error("top out of range must be rejected")
	}
}
