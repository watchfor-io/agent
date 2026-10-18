//go:build linux

package disk

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"time"

	"github.com/watchfor-io/agent/internal/procfs"
)

// statfs(2) on a hard NFS/CIFS mount whose server is gone blocks in the
// kernel, uninterruptibly, and no context can cancel it. So each call runs
// in its own goroutine and the tick waits at most statfsTimeout for it. A
// mount that did not answer is remembered: no second call is started while
// the first is still stuck (one goroutine per mount point at most, and at
// most maxStuck overall), and it is not asked again for hungBackoff.
const (
	statfsTimeout = 2 * time.Second
	hungBackoff   = 5 * time.Minute
	statfsWorkers = 4  // mounts asked at once
	maxStuck      = 16 // goroutines allowed to sit in a hung statfs
)

// errHung is a mount that did not answer in time, is still busy with an
// earlier call, or is in its backoff.
var errHung = errors.New("statfs: mount not answering")

type statResult struct {
	st  syscall.Statfs_t
	err error
}

type statfser struct {
	fn       func(path string, st *syscall.Statfs_t) error
	timeout  time.Duration
	backoff  time.Duration
	workers  int
	maxStuck int
	now      func() time.Time

	mu        sync.Mutex
	inflight  map[string]bool      // a statfs goroutine is running for this point
	hungUntil map[string]time.Time // do not ask before this
}

func newStatfser() *statfser {
	return &statfser{
		fn:        syscall.Statfs,
		timeout:   statfsTimeout,
		backoff:   hungBackoff,
		workers:   statfsWorkers,
		maxStuck:  maxStuck,
		now:       time.Now,
		inflight:  map[string]bool{},
		hungUntil: map[string]time.Time{},
	}
}

// statAll asks every mount, statfsWorkers at a time, and returns the
// results in the same order. It returns within about
// ceil(len/workers)*timeout even when every mount hangs, sooner when ctx
// ends (mounts not yet asked then carry ctx's error).
func (s *statfser) statAll(ctx context.Context, mounts []procfs.Mount) []statResult {
	s.prune()
	out := make([]statResult, len(mounts))
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(s.workers, len(mounts)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				out[i].st, out[i].err = s.stat(ctx, mounts[i].Point)
			}
		}()
	}
	for i := range mounts {
		if ctx.Err() != nil {
			for ; i < len(mounts); i++ {
				out[i].err = ctx.Err()
			}
			break
		}
		idx <- i
	}
	close(idx)
	wg.Wait()
	return out
}

// stat is one statfs bounded by the timeout and ctx.
func (s *statfser) stat(ctx context.Context, point string) (syscall.Statfs_t, error) {
	s.mu.Lock()
	if s.inflight[point] || s.now().Before(s.hungUntil[point]) {
		s.mu.Unlock()
		return syscall.Statfs_t{}, errHung
	}
	if len(s.inflight) >= s.maxStuck {
		// Too many calls are stuck already (mounts coming and going under
		// a dead server): do not pile up more threads in the kernel.
		s.mu.Unlock()
		return syscall.Statfs_t{}, errHung
	}
	s.inflight[point] = true
	s.mu.Unlock()

	ch := make(chan statResult, 1) // buffered: a late answer never blocks the goroutine
	go func() {
		var r statResult
		r.err = s.fn(point, &r.st)
		s.mu.Lock()
		delete(s.inflight, point)
		s.mu.Unlock()
		ch <- r
	}()
	t := time.NewTimer(s.timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		s.mu.Lock()
		delete(s.hungUntil, point)
		s.mu.Unlock()
		return r.st, r.err
	case <-t.C:
		s.mu.Lock()
		s.hungUntil[point] = s.now().Add(s.backoff)
		s.mu.Unlock()
		return syscall.Statfs_t{}, errHung
	case <-ctx.Done():
		// The budget ran out; the call may still answer. It stays in
		// flight, so the next tick does not start a second one meanwhile.
		return syscall.Statfs_t{}, ctx.Err()
	}
}

// prune forgets backoffs that have run out for mounts with nothing in
// flight, so mount points that came and went do not accumulate.
func (s *statfser) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for p, until := range s.hungUntil {
		if !now.Before(until) && !s.inflight[p] {
			delete(s.hungUntil, p)
		}
	}
}

// stuck is the number of statfs calls still running (tests).
func (s *statfser) stuck() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inflight)
}
