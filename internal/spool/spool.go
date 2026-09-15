// Package spool is the on-disk buffer for batches the endpoint could not
// take: one file per batch, oldest first, bounded by size. The oldest batch
// is dropped when the cap is reached — a host that has been offline for a
// week should catch up on recent data, not replay the whole week first.
package spool

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Spool struct {
	dir string
	max int64
	mu  sync.Mutex
}

func Open(dir string, maxBytes int64) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return nil, fmt.Errorf("spool dir not writable: %w", err)
	}
	probe.Close()
	os.Remove(probe.Name())
	return &Spool{dir: dir, max: maxBytes}, nil
}

func (s *Spool) Put(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := filepath.Join(s.dir, fmt.Sprintf("%020d.gz", time.Now().UnixNano()))
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		os.Remove(tmp.Name())
		return err
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
		return "", nil, err
	}
	return names[0], data, nil
}

func (s *Spool) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(filepath.Join(s.dir, filepath.Base(name)))
}

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
