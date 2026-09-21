// Package spool is the on-disk buffer for batches the endpoint could not
// take: one file per batch, oldest first, bounded by size. The oldest batch
// is dropped when the cap is reached — a host that has been offline for a
// week should catch up on recent data, not replay the whole week first.
package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Spool is the buffer directory. Every operation takes the one lock, so
// a trim never runs underneath a read of the oldest file.
type Spool struct {
	dir string
	max int64
	mu  sync.Mutex
	seq uint64 // tie-breaker for two batches in one clock tick
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
	return &Spool{dir: dir, max: maxBytes}, nil
}

// ErrTooLarge is a batch bigger than the whole spool.
var ErrTooLarge = errors.New("spool: batch larger than the spool")

// Put stores one encoded batch as a file named by the time, so the
// oldest sorts first. Write, fsync, rename: a crash never leaves half a
// batch. The size cap is enforced afterwards, oldest first.
func (s *Spool) Put(data []byte) error {
	if int64(len(data)) > s.max {
		return ErrTooLarge // it would only be trimmed away again; say so instead
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	name := filepath.Join(s.dir, fmt.Sprintf("%020d-%06d.gz", time.Now().UnixNano(), s.seq%1000000))
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
	if err := os.Rename(tmp.Name(), name); err != nil {
		return discard(err)
	}
	s.trim()
	return nil
}

// Next returns the oldest batch, or "" when the spool is empty. The caller
// removes it after a successful send so a crash mid-send replays it.
func (s *Spool) Next() (name string, data []byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := s.names()
	if len(names) == 0 {
		return "", nil, nil
	}
	data, err = os.ReadFile(filepath.Join(s.dir, names[0]))
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
		if info, err := os.Stat(filepath.Join(s.dir, n)); err == nil {
			count++
			bytes += info.Size()
		}
	}
	return count, bytes
}

func (s *Spool) names() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".gz") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func (s *Spool) trim() {
	names := s.names()
	var total int64
	sizes := make([]int64, len(names))
	for i, n := range names {
		if info, err := os.Stat(filepath.Join(s.dir, n)); err == nil {
			sizes[i] = info.Size()
			total += sizes[i]
		}
	}
	for i := 0; total > s.max && i < len(names); i++ {
		if os.Remove(filepath.Join(s.dir, names[i])) == nil {
			total -= sizes[i]
		}
	}
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".gz") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, err := e.Info(); err == nil {
			count++
			bytes += info.Size()
		}
	}
	return count, bytes
}
