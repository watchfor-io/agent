//go:build linux

package processes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/testutil"
)

func TestCountsTopAndWatch(t *testing.T) {
	testutil.UseProcRoot(t, &procfs.Root)
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
	b := metric.NewBatch(time.Now())
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

// A cmdline watch matches the whole command line (up to 4 KB), not only
// its first 256 bytes: a JVM's main class comes after a long classpath.
func TestCmdlineWatchSeesPastTheFirst256Bytes(t *testing.T) {
	src := testutil.ProcRoot(t)
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	argv := "java\x00-cp\x00" + strings.Repeat("/opt/lib/x.jar:", 40) + "\x00com.example.NeedleLate\x00"
	if err := os.WriteFile(filepath.Join(root, "4242", "cmdline"), []byte(argv), 0o644); err != nil {
		t.Fatal(err)
	}
	old := procfs.Root
	procfs.Root = root
	t.Cleanup(func() { procfs.Root = old })
	m, err := New(testutil.Node(t, "watch:\n  - cmdline: com.example.NeedleLate\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := metric.NewBatch(time.Now())
	if err := m.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if s, ok := testutil.Find(b, "procwatch.count", metric.Labels{"watch": "com.example.NeedleLate"}); !ok || s.V != 1 {
		t.Errorf("watch on a late argument = %+v", s)
	}
}
