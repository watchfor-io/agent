package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RejectedMarker is written next to the spool when the server rejects
// the token. It keeps the token's fingerprint, how many times it was
// rejected and when: a single 401 can be the server having a bad minute,
// so the agent only gives up — and stops knocking on ingest from boot or
// cron — once the rejection has held for a while. A different token clears
// the block by itself.
const RejectedMarker = "token-rejected"

// Sticky after this many rejections spread over at least this long.
const (
	StickyRejections = 3
	StickySpan       = 10 * time.Minute
)

// Rejection is the recorded state for one token fingerprint.
type Rejection struct {
	Fingerprint string
	Count       int
	First, Last time.Time
}

// Sticky reports whether the token should be treated as gone for good.
func (r Rejection) Sticky() bool {
	return r.Count >= StickyRejections && r.Last.Sub(r.First) >= StickySpan
}

// Backoff is how long the daemon waits before trying the token again.
func (r Rejection) Backoff() time.Duration {
	switch {
	case r.Count <= 1:
		return time.Minute
	case r.Count == 2:
		return 5 * time.Minute
	}
	return 10 * time.Minute
}

// ReadRejection returns the record for fingerprint; ok is false when
// there is none (or it belongs to another token).
func ReadRejection(dir, fingerprint string) (rec Rejection, ok bool) {
	if dir == "" || fingerprint == "" {
		return Rejection{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, RejectedMarker))
	if err != nil {
		return Rejection{}, false
	}
	f := strings.Fields(string(b))
	if len(f) < 4 || f[0] != fingerprint {
		return Rejection{}, false
	}
	rec.Fingerprint = f[0]
	rec.Count, _ = strconv.Atoi(f[1])
	rec.First, _ = time.Parse(time.RFC3339, f[2])
	rec.Last, _ = time.Parse(time.RFC3339, f[3])
	if rec.Count <= 0 || rec.First.IsZero() {
		return Rejection{}, false
	}
	return rec, true
}

// RecordRejection adds one rejection at `at` and returns the new state.
// Best effort on disk: a read-only state dir only means the count starts
// over next time.
func RecordRejection(dir, fingerprint string, at time.Time) (Rejection, error) {
	rec, ok := ReadRejection(dir, fingerprint)
	if !ok {
		rec = Rejection{Fingerprint: fingerprint, First: at}
	}
	rec.Count++
	rec.Last = at
	if dir == "" || fingerprint == "" {
		return rec, errors.New("no state directory or token")
	}
	line := strings.Join([]string{fingerprint, strconv.Itoa(rec.Count), rec.First.UTC().Format(time.RFC3339), rec.Last.UTC().Format(time.RFC3339)}, " ") + "\n"
	tmp := filepath.Join(dir, "."+RejectedMarker+".tmp")
	if err := os.WriteFile(tmp, []byte(line), 0o644); err != nil {
		return rec, err
	}
	return rec, os.Rename(tmp, filepath.Join(dir, RejectedMarker))
}

// ClearRejected removes the record (a token that works again).
func ClearRejected(dir string) {
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, RejectedMarker))
	}
}
