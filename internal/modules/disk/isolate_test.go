//go:build linux

package disk

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/testutil"
)

// The test binary doubles as the helper: `disk.test __statfs <points>`,
// and with WF_TEST_HANG=<point> it hangs on that point the way a dead
// hard mount does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		if hang := os.Getenv("WF_TEST_HANG"); hang != "" {
			for i, p := range os.Args[2:] {
				if p == hang {
					Helper(os.Args[2 : 2+i])
					time.Sleep(time.Hour) // stuck, as in the kernel
				}
			}
		}
		os.Exit(Helper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// A command that must end asks through helpers: a hung mount costs one
// timeout, the helper stuck on it is left behind and remembered, the other
// mounts are still reported, and the next run (a new process, as cron
// starts) does not start another helper for the hung mount while the
// stuck one is still there.
func TestHelperLeavesAHungMountBehindAndRemembersIt(t *testing.T) {
	root := t.TempDir()
	good1, hang, good2 := filepath.Join(root, "a"), filepath.Join(root, "nfs"), filepath.Join(root, "b")
	for _, d := range []string{good1, hang, good2} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fakeMounts(t,
		"/dev/sda1 "+good1+" ext4 rw 0 0",
		"srv:/export "+hang+" nfs4 rw,hard 0 0",
		"/dev/sdb1 "+good2+" xfs rw 0 0",
	)
	t.Setenv("WF_TEST_HANG", hang)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	Isolate(exe)
	RememberHungIn(state)
	oldKill := killHelper
	killHelper = func(*os.Process) {} // as in an uninterruptible wait: SIGKILL does nothing
	t.Cleanup(func() { killHelper = oldKill })
	t.Cleanup(func() { Isolate(""); RememberHungIn("") })

	run := func() (*module, error, time.Duration) {
		m, err := New(testutil.Node(t, "io: false\nmounts: ["+good1+", "+hang+", "+good2+"]\n"))
		if err != nil {
			t.Fatal(err)
		}
		mod := m.(*module)
		mod.stat.timeout = 300 * time.Millisecond
		b := metric.NewBatch(time.Now())
		start := time.Now()
		err = mod.Collect(context.Background(), b)
		for _, p := range []string{good1, good2} {
			if _, ok := findMount(b, p); !ok {
				t.Fatalf("%s not reported while %s hangs", p, hang)
			}
		}
		return mod, err, time.Since(start)
	}
	_, err, took := run()
	if err == nil || !strings.Contains(err.Error(), hang) || took > 3*time.Second {
		t.Fatalf("first run: %v after %s", err, took)
	}
	recs := (&hungStore{dir: state}).read()
	if len(recs) != 1 || recs[0].point != hang {
		t.Fatalf("record = %+v", recs)
	}
	stuckPid := recs[0].pid
	t.Cleanup(func() {
		if p, err := os.FindProcess(stuckPid); err == nil {
			_ = p.Kill()
			_, _ = p.Wait()
		}
	})
	if !stuckProcess(stuckPid) {
		t.Fatalf("the stuck helper %d is not recognised", stuckPid)
	}
	helpers := func() int {
		out, _ := exec.Command("pgrep", "-fc", HelperArg+" .*"+filepath.Base(hang)).Output()
		n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		return n
	}
	before := helpers()
	if _, err, took = run(); err == nil || took > 2*time.Second {
		t.Fatalf("second run: %v after %s", err, took)
	}
	if after := helpers(); after > before {
		t.Fatalf("the second run started another helper on the hung mount (%d → %d)", before, after)
	}
	// the stuck helper goes away (the mount came back): asked again, and the record goes
	if p, err := os.FindProcess(stuckPid); err == nil {
		_ = p.Kill()
		_, _ = p.Wait()
	}
	t.Setenv("WF_TEST_HANG", "")
	if _, err, _ := run(); err != nil {
		t.Fatalf("after the mount came back: %v", err)
	}
	if recs := (&hungStore{dir: state}).read(); len(recs) != 0 {
		t.Fatalf("record kept after recovery: %+v", recs)
	}
}

func findMount(b *metric.Batch, point string) (metric.Sample, bool) {
	for _, s := range b.Samples() {
		if s.M == "disk.used_pct" && s.L["mount"] == point {
			return s, true
		}
	}
	return metric.Sample{}, false
}

// The daemon asks local disks itself and only the mounts that can hang
// through a helper, so a dead NFS server never leaves a thread of its own
// stuck in the kernel (which would keep it from finishing a stop).
func TestDaemonSendsOnlyNetworkMountsToTheHelper(t *testing.T) {
	root := t.TempDir()
	local, nfs := filepath.Join(root, "data"), filepath.Join(root, "nfs")
	for _, d := range []string{local, nfs} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fakeMounts(t, "/dev/sda1 "+local+" ext4 rw 0 0", "srv:/export "+nfs+" nfs4 rw,hard 0 0")
	t.Setenv("WF_TEST_HANG", nfs)
	exe, _ := os.Executable()
	state := t.TempDir()
	IsolateNetwork(exe)
	RememberHungIn(state)
	t.Cleanup(func() { Isolate(""); RememberHungIn("") })
	oldKill := killHelper
	killHelper = func(*os.Process) {}
	t.Cleanup(func() { killHelper = oldKill })
	var asked []string
	m, err := New(testutil.Node(t, "io: false\nmounts: ["+local+", "+nfs+"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	mod := m.(*module)
	mod.stat.timeout = 300 * time.Millisecond
	mod.stat.fn = func(p string, st *syscall.Statfs_t) error { // in-process calls
		asked = append(asked, p)
		st.Blocks, st.Bfree, st.Bavail, st.Bsize, st.Frsize = 100, 50, 50, 4096, 4096
		return nil
	}
	b := metric.NewBatch(time.Now())
	if err := mod.Collect(context.Background(), b); err == nil || !strings.Contains(err.Error(), nfs) {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(asked, ",") != local {
		t.Fatalf("asked in process: %v, want only the local disk", asked)
	}
	recs := (&hungStore{dir: state}).read()
	if len(recs) != 1 || recs[0].pid == os.Getpid() {
		t.Fatalf("record %+v: the stuck call should be a helper's", recs)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(recs[0].pid); err == nil {
			_ = p.Kill()
			_, _ = p.Wait()
		}
	})
	for _, fs := range []string{"nfs", "nfs4", "cifs", "fuse.sshfs", "fuseblk", "ceph", "9p"} {
		if !mayHang(fs) {
			t.Errorf("%s: may hang", fs)
		}
	}
	for _, fs := range []string{"ext4", "xfs", "btrfs", "zfs", "vfat"} {
		if mayHang(fs) {
			t.Errorf("%s: a local filesystem", fs)
		}
	}
}
