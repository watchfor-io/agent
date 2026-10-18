//go:build linux

package disk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/testutil"
)

// fakeMounts points procfs at a mounts table of our own.
func fakeMounts(t *testing.T, lines ...string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "self", "mounts"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := procfs.Root
	procfs.Root = root
	t.Cleanup(func() { procfs.Root = old })
}

// hangingStatfs blocks for the points in hang until release is closed and
// answers everything else at once; calls counts per point.
type hangingStatfs struct {
	hang    map[string]bool
	release chan struct{}
	mu      sync.Mutex
	calls   map[string]int
	running atomic.Int32
	peak    atomic.Int32
}

func (h *hangingStatfs) statfs(p string, st *syscall.Statfs_t) error {
	h.mu.Lock()
	h.calls[p]++
	h.mu.Unlock()
	n := h.running.Add(1)
	defer h.running.Add(-1)
	for {
		if old := h.peak.Load(); n <= old || h.peak.CompareAndSwap(old, n) {
			break
		}
	}
	if h.hang[p] {
		<-h.release
	}
	st.Blocks, st.Bfree, st.Bavail, st.Bsize, st.Frsize = 100, 40, 30, 4096, 4096
	return nil
}

func (h *hangingStatfs) count(p string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[p]
}

func newHangTest(t *testing.T, hang ...string) (*module, *hangingStatfs, *time.Time) {
	t.Helper()
	m, err := New(testutil.Node(t, "io: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	mod := m.(*module)
	h := &hangingStatfs{hang: map[string]bool{}, release: make(chan struct{}), calls: map[string]int{}}
	for _, p := range hang {
		h.hang[p] = true
	}
	now := time.Now()
	mod.stat.fn = h.statfs
	mod.stat.timeout = 50 * time.Millisecond
	mod.stat.now = func() time.Time { return now }
	t.Cleanup(func() {
		select {
		case <-h.release:
		default:
			close(h.release)
		}
	})
	return mod, h, &now
}

// A hung NFS mount costs one tick a bounded wait, the other disks are
// still reported, and the next ticks neither wait nor pile up goroutines.
func TestHungMountDoesNotBlockTheTick(t *testing.T) {
	fakeMounts(t,
		"/dev/sda1 / ext4 rw 0 0",
		"srv:/export /nfs nfs4 rw,hard 0 0",
		"/dev/sdb1 /data xfs rw 0 0",
	)
	m, h, now := newHangTest(t, "/nfs")

	for tick := 0; tick < 5; tick++ {
		b := metric.NewBatch(time.Now())
		start := time.Now()
		err := m.Collect(context.Background(), b)
		if took := time.Since(start); took > time.Second {
			t.Fatalf("tick %d took %s", tick, took)
		}
		if err == nil || !strings.Contains(err.Error(), "/nfs") {
			t.Fatalf("tick %d: err = %v, want the hung mount named", tick, err)
		}
		for _, mp := range []string{"/", "/data"} {
			if _, ok := testutil.Find(b, "disk.used_pct", metric.Labels{"mount": mp}); !ok {
				t.Fatalf("tick %d: %s missing while /nfs hangs", tick, mp)
			}
		}
		if _, ok := testutil.Find(b, "disk.used_pct", metric.Labels{"mount": "/nfs"}); ok {
			t.Fatalf("tick %d: hung mount reported", tick)
		}
	}
	if n := h.count("/nfs"); n != 1 {
		t.Fatalf("hung mount asked %d times, want once while the first call is stuck", n)
	}
	if n := m.stat.stuck(); n != 1 {
		t.Fatalf("%d calls in flight, want 1", n)
	}

	// The server comes back: the stuck call returns, but the mount stays
	// in its backoff until that runs out.
	close(h.release)
	deadline := time.Now().Add(2 * time.Second)
	for m.stat.stuck() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if m.stat.stuck() != 0 {
		t.Fatal("the released call did not finish")
	}
	if err := m.Collect(context.Background(), metric.NewBatch(time.Now())); err == nil {
		t.Fatal("mount reported before its backoff ran out")
	}
	*now = now.Add(hungBackoff + time.Second)
	b := metric.NewBatch(time.Now())
	if err := m.Collect(context.Background(), b); err != nil {
		t.Fatalf("after the backoff: %v", err)
	}
	if _, ok := testutil.Find(b, "disk.used_pct", metric.Labels{"mount": "/nfs"}); !ok {
		t.Fatal("recovered mount not reported")
	}
	if len(m.stat.hungUntil) != 0 {
		t.Fatalf("backoff not cleared: %v", m.stat.hungUntil)
	}
}

// Many hung mounts (autofs churn under a dead server) never have more
// than maxStuck goroutines sitting in the kernel, however many ticks pass.
func TestHungMountsAreBounded(t *testing.T) {
	var lines, points []string
	for i := 0; i < 40; i++ {
		p := fmt.Sprintf("/nfs/%02d", i)
		points = append(points, p)
		lines = append(lines, fmt.Sprintf("srv:/e%d %s nfs4 rw,hard 0 0", i, p))
	}
	fakeMounts(t, lines...)
	m, h, now := newHangTest(t, points...)
	m.stat.maxStuck = 5
	for tick := 0; tick < 10; tick++ {
		_ = m.Collect(context.Background(), metric.NewBatch(time.Now()))
		*now = now.Add(hungBackoff + time.Second) // backoffs run out every tick
	}
	if n := m.stat.stuck(); n > 5 {
		t.Fatalf("%d statfs calls stuck, cap is 5", n)
	}
	if p := h.peak.Load(); p > 5 {
		t.Fatalf("peak of %d concurrent statfs calls, cap is 5", p)
	}
}

// A budget that ends mid-way returns at once with ctx's error.
func TestStatAllHonoursContext(t *testing.T) {
	fakeMounts(t, "srv:/a /a nfs4 rw 0 0", "srv:/b /b nfs4 rw 0 0")
	m, _, _ := newHangTest(t, "/a", "/b")
	m.stat.timeout = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := m.Collect(ctx, metric.NewBatch(time.Now()))
	if time.Since(start) > time.Second {
		t.Fatal("Collect ignored the context")
	}
	if err == nil {
		t.Fatal("want the context's error")
	}
}

// `config init` (run by the installer) lists the mounts with the same
// guard: a hung mount is left out and the listing still finishes.
func TestDiscoverSkipsAHungMount(t *testing.T) {
	fakeMounts(t,
		"/dev/sda1 / ext4 rw 0 0",
		"srv:/export /nfs nfs4 rw,hard 0 0",
		"/dev/sdb1 /data xfs rw 0 0",
	)
	m, _, _ := newHangTest(t, "/nfs")
	start := time.Now()
	mounts, _, err := m.discover()
	if took := time.Since(start); took > time.Second {
		t.Fatalf("discover took %s with a hung mount", took)
	}
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range mounts {
		got = append(got, d.Point)
	}
	if strings.Join(got, ",") != "/,/data" {
		t.Fatalf("discovered %v, want / and /data without the hung /nfs", got)
	}
}
