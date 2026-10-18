// Package safefile reads and writes small files in a directory that a less
// trusted account may also write to — the agent's state/spool directory,
// which belongs to the service user but is sometimes touched by root
// (`run`/`once` started by hand, the update unit). A name planted there
// as a symlink or a FIFO must neither redirect a write nor stall a read.
package safefile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ErrNotRegular is a path that is a symlink, FIFO, socket, device or
// directory where a plain file was expected.
var ErrNotRegular = errors.New("not a regular file")

// ErrTooLarge is a file bigger than the caller is willing to read.
var ErrTooLarge = errors.New("file too large")

// WriteAtomic replaces dir/name with data. The bytes go to a fresh temp
// file created with O_EXCL in dir (so nothing planted under a predictable
// name is followed or reused), are fsynced, and the temp is renamed over
// name — rename replaces a symlink at name instead of writing through it.
// A reader never sees a half-written file.
func WriteAtomic(dir, name string, data []byte, perm os.FileMode) error {
	if dir == "" || name == "" || filepath.Base(name) != name {
		return fmt.Errorf("safefile: bad target %q in %q", name, dir)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*") // O_RDWR|O_CREATE|O_EXCL, 0600
	if err != nil {
		return err
	}
	discard := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if perm != 0o600 {
		if err := tmp.Chmod(perm); err != nil {
			return discard(err)
		}
	}
	if _, err := tmp.Write(data); err != nil {
		return discard(err)
	}
	if err := tmp.Sync(); err != nil {
		return discard(err)
	}
	if err := tmp.Close(); err != nil {
		return discard(err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir makes the rename durable; best effort, as not every filesystem
// supports fsync on a directory.
func syncDir(dir string) {
	if d, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// Open opens path for reading without following a final symlink and
// without blocking on a FIFO, and returns it only when it is a regular,
// singly linked file of at most limit bytes (limit <= 0: no size check).
func Open(path string, limit int64) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, nil, fmt.Errorf("%s: %w (symlink)", path, ErrNotRegular)
		}
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s: %w (%s)", path, ErrNotRegular, st.Mode().Type())
	}
	// A second name for the inode is something nobody here created: with
	// protected_hardlinks off it could be another user's file.
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && uint64(sys.Nlink) > 1 {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s: %w (hard-linked)", path, ErrNotRegular)
	}
	if limit > 0 && st.Size() > limit {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s: %w (%d > %d bytes)", path, ErrTooLarge, st.Size(), limit)
	}
	return f, st, nil
}

// ReadFile is Open plus a read capped at limit bytes, so a file that grows
// after the size check still cannot make the caller read more.
func ReadFile(path string, limit int64) ([]byte, error) {
	f, _, err := Open(path, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if limit <= 0 {
		return io.ReadAll(f)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: %w (grew past %d bytes)", path, ErrTooLarge, limit)
	}
	return b, nil
}
