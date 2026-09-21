package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"time"
)

// ErrReload is returned by Watch when agent.yml changed to something the
// agent accepts: the daemon exits with ExitReload and systemd starts it
// again on the new file. A change that does not validate is reported
// through onInvalid and otherwise ignored, so a half-written or broken
// file never takes a running agent down.
var ErrReload = errors.New("config changed")

// ExitReload is the exit status for "restart me on the new config"; the
// unit's RestartForceExitStatus lists it.
const ExitReload = 75

// WatchInterval is how often the file's size and mtime are checked — one
// stat() call, a few microseconds; the file is only read when they moved.
// Half a minute: a change shows within one push interval, and nobody
// edits agent.yml often enough for the wait to matter.
var WatchInterval = 30 * time.Second // a variable so tests can hurry it

// Watch blocks until ctx ends (nil) or the file at path changes into a
// valid config (ErrReload). withToken says whether the token source must
// resolve for the file to count as valid — true for the daemon.
func Watch(ctx context.Context, path string, withToken bool, onInvalid func(err error)) error {
	last, lastSum := statAndSum(path)
	var badSum []byte // the last version that was refused
	t := time.NewTicker(WatchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			st, err := os.Stat(path)
			if err != nil || (st.Size() == last.size && st.ModTime().Equal(last.mtime)) {
				continue
			}
			cur, sum := statAndSum(path)
			last = cur
			if sum == nil || bytes.Equal(sum, lastSum) {
				continue // touched, not changed (Ansible, editors) — or unreadable
			}
			if _, err := parseConfig(bytes.NewReader(cur.data), withToken); err != nil {
				if !bytes.Equal(sum, badSum) { // say it once per broken version
					badSum = sum
					if onInvalid != nil {
						onInvalid(err)
					}
				}
				continue // the running config stays; putting the old text back is then no change
			}
			return ErrReload
		}
	}
}

type fileState struct {
	size  int64
	mtime time.Time
	data  []byte
}

func statAndSum(path string) (fileState, []byte) {
	st, err := os.Stat(path)
	if err != nil {
		return fileState{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileState{size: st.Size(), mtime: st.ModTime()}, nil
	}
	sum := sha256.Sum256(data)
	return fileState{size: st.Size(), mtime: st.ModTime(), data: data}, sum[:]
}
