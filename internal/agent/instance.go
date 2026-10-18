//go:build linux

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockFile is the name of the single-instance lock in the state directory.
const LockFile = "agent.lock"

// ErrBusy means another agent holds the state directory: the service, a
// cron run, or someone's manual `run`.
var ErrBusy = errors.New("another watchfor-agent is running with this state directory")

// Instance is the held lock; Release gives it back (exiting does too —
// the kernel drops a flock with the last descriptor, so a crash or a
// SIGKILL never leaves a stale lock behind, unlike a pid file).
type Instance struct{ f *os.File }

// Release lets the next run in.
func (i *Instance) Release() {
	if i != nil && i.f != nil {
		_ = i.f.Close()
	}
}

// Lock takes the state directory for this process. wait is how long to
// keep trying while someone else holds it (0: give up at once, ErrBusy).
// The file is opened read-only and left world-readable: whoever creates
// it (the service account, or root running `once` by hand), the other
// may still open it to take the lock — an empty file says nothing.
func Lock(ctx context.Context, dir string, wait time.Duration) (*Instance, error) {
	f, err := os.OpenFile(filepath.Join(dir, LockFile), os.O_RDONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o644) // #nosec G302 -- an empty lock file; readable so root and the service account can both lock it
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil && st.Mode().Perm() != 0o644 {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) == os.Geteuid() {
			_ = f.Chmod(0o644) // #nosec G302 -- a 0077 umask made it 0600; see above
		}
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Instance{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, ErrBusy
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
