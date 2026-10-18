// Package spool is the on-disk buffer for batches the endpoint could not
// take: one file per batch, oldest first, bounded by size. The oldest batch
// is dropped when the cap is reached, or when the disk is full — a host
// that has been offline for a week should catch up on recent data, not
// replay the whole week first.
//
// Files are named by a sequence number that only grows (continued from
// the highest name on disk when the spool is opened), never by the clock:
// a clock stepped backwards must not make a new batch sort as the oldest —
// trimmed at once, or replayed out of order.
package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/watchfor-io/agent/internal/safefile"
)

// Spool is the buffer directory. Every operation takes the one lock, so
// a trim never runs underneath a read of the oldest file.
type Spool struct {
	dir string
	max int64
	mu  sync.Mutex
	seq uint64 // the last sequence number used
	// dropped counts batches given up to make room (cap or full disk)
	// since TakeDropped last asked.
	dropped int
	// fault, in tests, fails a write before it starts.
	fault func() error
}

// Open creates the directory and proves it is writable at once, so a
// spool that cannot take a batch fails at start rather than during the
// first outage.
func Open(dir string, maxBytes int64) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return nil, fmt.Errorf("spool dir not writable: %w", err)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name()) // the probe only had to be created
	// a .tmp-* left by a crash mid-Put is not a batch and would never go away
	if stale, _ := filepath.Glob(filepath.Join(dir, ".tmp-*")); len(stale) > 0 {
		for _, p := range stale {
			_ = os.Remove(p)
		}
	}
	sp := &Spool{dir: dir, max: maxBytes}
	for _, n := range sp.names() {
		if seq := seqOf(n); seq > sp.seq {
			sp.seq = seq
		}
	}
	return sp, nil
}

// seqOf is the number a batch name starts with (0 when it does not):
// the sequence for names this release writes, the clock in nanoseconds
// for names older releases wrote — which this sequence then continues,
// so their batches still sort first.
func seqOf(name string) uint64 {
	var n uint64
	for i := 0; i < len(name) && name[i] >= '0' && name[i] <= '9'; i++ {
		d := uint64(name[i] - '0')
		if n > (^uint64(0)-d)/10 {
			return 0 // not one of ours
		}
		n = n*10 + d
	}
	return n
}

// ErrTooLarge is a batch bigger than the whole spool.
var ErrTooLarge = errors.New("spool: batch larger than the spool")

// Put stores one encoded batch under the next sequence number, so the
// oldest sorts first. Write, fsync, rename: a crash never leaves half a
// batch. The size cap is enforced afterwards, oldest first — never on the
// batch just written. When the disk is full the oldest batches make room
// for it: recent data is worth more after an outage than old.
func (s *Spool) Put(data []byte) error {
	if int64(len(data)) > s.max {
		return ErrTooLarge // it would only be trimmed away again; say so instead
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	name := fmt.Sprintf("%020d.gz", s.seq)
	for {
		err := s.write(name, data)
		if err == nil {
			break
		}
		if !diskFull(err) || !s.dropOldest(name) {
			return err
		}
	}
	s.trim(name)
	return nil
}

// write puts data at dir/name through a temp file.
func (s *Spool) write(name string, data []byte) error {
	if s.fault != nil {
		if err := s.fault(); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return err
	}
	discard := func(err error) error { // the half-written file is not a batch
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
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
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, name)); err != nil {
		return discard(err)
	}
	return nil
}

func diskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

// dropOldest removes the oldest batch other than keep; false when there
// is none left to remove.
func (s *Spool) dropOldest(keep string) bool {
	for _, n := range s.names() {
		if n == keep {
			continue
		}
		if os.Remove(filepath.Join(s.dir, n)) == nil {
			s.dropped++
			return true
		}
	}
	return false
}

// TakeDropped is how many older batches were given up to make room (the
// cap, or a full disk) since the last call.
func (s *Spool) TakeDropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.dropped
	s.dropped = 0
	return n
}

// Next returns the oldest batch, or "" when the spool is empty. The caller
// removes it after a successful send so a crash mid-send replays it.
//
// Only regular files are batches. A symlink, FIFO or device that turns up
// under a batch name was not written by Put — it is unlinked (the link,
// never its target) instead of read, so a FIFO cannot hang the agent and
// a link cannot make it upload a file it was pointed at. The read itself
// does not follow links, does not block, and is capped at the spool size.
func (s *Spool) Next() (name string, data []byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeIrregular()
	names := s.names()
	if len(names) == 0 {
		return "", nil, nil
	}
	data, err = safefile.ReadFile(filepath.Join(s.dir, names[0]), s.max)
	if err != nil {
		return names[0], nil, err // the caller may want to get rid of it
	}
	return names[0], data, nil
}

// Remove deletes a batch by the name Next returned, once it was sent.
func (s *Spool) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(filepath.Join(s.dir, filepath.Base(name)))
}

// Stats counts the batches waiting and their total size, for the
// start-up log line and the configure screen.
func (s *Spool) Stats() (count int, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.names() {
		if info, err := os.Lstat(filepath.Join(s.dir, n)); err == nil {
			count++
			bytes += info.Size()
		}
	}
	return count, bytes
}

// batchName is a name Put could have written; whether the entry is a
// regular file is checked separately.
func batchName(n string) bool {
	return strings.HasSuffix(n, ".gz") && !strings.HasPrefix(n, ".")
}

// names lists the batches, oldest first: regular files only (the type
// comes from the directory entry, i.e. lstat — a symlink is not followed).
func (s *Spool) names() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() && batchName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// purgeIrregular unlinks entries with a batch name that are not regular
// files (symlinks, FIFOs, sockets, devices). Directories are left alone.
func (s *Spool) purgeIrregular() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if batchName(e.Name()) && !e.Type().IsRegular() && !e.IsDir() {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

// trim drops the oldest batches until the spool fits its cap; keep (the
// batch just written) is never one of them.
func (s *Spool) trim(keep string) {
	names := s.names()
	var total int64
	sizes := make([]int64, len(names))
	for i, n := range names {
		if info, err := os.Lstat(filepath.Join(s.dir, n)); err == nil {
			sizes[i] = info.Size()
			total += sizes[i]
		}
	}
	for i := 0; total > s.max && i < len(names); i++ {
		if names[i] == keep {
			continue
		}
		if os.Remove(filepath.Join(s.dir, names[i])) == nil {
			total -= sizes[i]
			s.dropped++
		}
	}
}

// Oldest lists up to n batch names, oldest first (irregular entries are
// purged first, as in Next).
func (s *Spool) Oldest(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeIrregular()
	names := s.names()
	return names[:min(n, len(names))]
}

// Read returns one batch by the name Oldest gave, read the way Next reads.
func (s *Spool) Read(name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return safefile.ReadFile(filepath.Join(s.dir, filepath.Base(name)), s.max)
}

// Split replaces one batch with parts that sort exactly where it did, in
// their order — for a batch too big for the server to take at once. The
// parts are all written before the original goes, so a crash in between
// sends some samples twice rather than losing any.
func (s *Spool) Split(name string, parts [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = filepath.Base(name)
	base := strings.TrimSuffix(name, ".gz")
	if len(parts) > 999 {
		return fmt.Errorf("spool: %d parts", len(parts))
	}
	var written []string
	for i, p := range parts {
		part := fmt.Sprintf("%s.p%03d.gz", base, i+1)
		if err := s.write(part, p); err != nil {
			for _, w := range written {
				_ = os.Remove(filepath.Join(s.dir, w))
			}
			return err
		}
		written = append(written, part)
	}
	return os.Remove(filepath.Join(s.dir, name))
}

// Stats counts the batches waiting in dir and their size without opening
// the spool — for a screen that only looks, and must not create the
// directory or leave a probe file behind.
func Stats(dir string) (count int, bytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !batchName(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil {
			count++
			bytes += info.Size()
		}
	}
	return count, bytes
}
