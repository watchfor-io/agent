package agent

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/safefile"
)

// RejectedMarker is written next to the spool when the server rejects
// the token. It keeps the token's fingerprint, how many times it was
// rejected and when: a single 401 can be the server having a bad minute,
// so the agent only gives up — and stops knocking on ingest from boot or
// cron — once the rejection has held for a while. A different token clears
// the block by itself.
const RejectedMarker = "token-rejected"

// Sticky after this many rejections spread over at least this long. The
// retry gaps grow to an hour, so a token has to be refused for close to
// two hours before the agent gives up: long enough to roll back a bad
// deploy of ours before a fleet stops itself.
const (
	// StickyRejections is the count a rejection must reach to be final.
	StickyRejections = 5
	// StickySpan is how far apart the first and last rejection must be.
	StickySpan = time.Hour
)

// Rejection is the recorded state for one token fingerprint.
type Rejection struct {
	Fingerprint string
	Count       int
	First, Last time.Time
	// Reason is ingest's X-WatchFor-Reason for the last rejection
	// (token_unknown, …). Empty for a record an agent before 0.9 wrote:
	// those did not keep it, and counted plan_forbidden here too.
	Reason string
}

// Sticky reports whether the token should be treated as gone for good.
// Only a rejection this release recorded, with a reason that means the
// token itself is refused, can be: a record without a reason may be a
// plan pause an earlier release miscounted, so it has to be confirmed by
// one more rejection first (which RecordRejection then stamps).
func (r Rejection) Sticky() bool {
	if r.Reason == "" || r.Reason == push.PlanForbidden {
		return false
	}
	return r.Count >= StickyRejections && r.Last.Sub(r.First) >= StickySpan
}

// Why is the rejection in words, by its reason, for the log and health.
func (r Rejection) Why() string { return RejectionWhy(r.Reason) }

// RejectionWhy explains a rejection reason the way the log and `health`
// say it.
func RejectionWhy(reason string) string {
	switch reason {
	case "token_unknown":
		return "WatchFor does not know this token: the host was removed in WatchFor or its token was rotated"
	case push.PlanForbidden:
		return "WatchFor paused this host: the plan does not include server monitoring, or the organization is over its host limit"
	case "":
		return "the server rejected the token (recorded by an agent before 0.9, which did not keep the reason)"
	}
	return "the server rejected the token (reason: " + reason + ")"
}

// Backoff is how long the daemon waits before trying the token again:
// 1, 5, 15, 30 minutes, then an hour.
func (r Rejection) Backoff() time.Duration {
	switch {
	case r.Count <= 1:
		return time.Minute
	case r.Count == 2:
		return 5 * time.Minute
	case r.Count == 3:
		return 15 * time.Minute
	case r.Count == 4:
		return 30 * time.Minute
	}
	return time.Hour
}

// ReadRejection returns the record for fingerprint; ok is false when
// there is none (or it belongs to another token).
func ReadRejection(dir, fingerprint string) (rec Rejection, ok bool) {
	if dir == "" || fingerprint == "" {
		return Rejection{}, false
	}
	b, err := safefile.ReadFile(filepath.Join(dir, RejectedMarker), stateCap)
	if err != nil {
		return Rejection{}, false
	}
	rec, ok = parseRejection(b)
	if !ok || rec.Fingerprint != fingerprint {
		return Rejection{}, false
	}
	return rec, true
}

// ReadAnyRejection returns the record in dir whatever token it is for —
// `health` shows a leftover from an earlier token too.
func ReadAnyRejection(dir string) (Rejection, bool) {
	if dir == "" {
		return Rejection{}, false
	}
	b, err := safefile.ReadFile(filepath.Join(dir, RejectedMarker), stateCap)
	if err != nil {
		return Rejection{}, false
	}
	return parseRejection(b)
}

// parseRejection reads a marker line: fingerprint, count, first, last
// and — from 0.9 on — the reason.
func parseRejection(b []byte) (rec Rejection, ok bool) {
	f := strings.Fields(string(b))
	if len(f) < 4 {
		return Rejection{}, false
	}
	rec.Fingerprint = f[0]
	rec.Count, _ = strconv.Atoi(f[1])
	rec.First, _ = time.Parse(time.RFC3339, f[2])
	rec.Last, _ = time.Parse(time.RFC3339, f[3])
	if len(f) >= 5 && validReason(f[4]) {
		rec.Reason = f[4]
	}
	if rec.Count <= 0 || rec.First.IsZero() {
		return Rejection{}, false
	}
	return rec, true
}

// validReason keeps a reason to a word: it is written into the marker
// from a server header.
func validReason(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// StaleRejection looks at whatever rejected-token record is in dir —
// for any token — and says why it no longer counts: it was written for a
// plan pause (which 0.8 counted as a rejected token), or the token file
// was written after the last rejection (a new or re-pasted token). "" when
// the record is not stale, or there is none.
func StaleRejection(dir, tokenFile string) string {
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, RejectedMarker)
	f, st, err := safefile.Open(path, stateCap)
	if err != nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, stateCap))
	_ = f.Close()
	if err != nil {
		return ""
	}
	rec, ok := parseRejection(b)
	switch {
	case !ok:
		return "the record cannot be read"
	case rec.Reason == push.PlanForbidden:
		return "it was written for a plan pause, which is not a rejected token"
	}
	if tokenFile != "" {
		if tst, err := os.Stat(tokenFile); err == nil && tst.ModTime().After(st.ModTime()) {
			return "the token file was written after the last rejection"
		}
	}
	return ""
}

// ClearStaleRejection removes the record when StaleRejection finds it
// stale, and returns why ("" when it was left alone).
func ClearStaleRejection(dir, tokenFile string) string {
	why := StaleRejection(dir, tokenFile)
	if why != "" {
		ClearRejected(dir)
	}
	return why
}

// RecordRejection adds one rejection at `at`, for reason, and returns the
// new state. Best effort on disk: a read-only state dir only means the
// count starts over next time.
func RecordRejection(dir, fingerprint, reason string, at time.Time) (Rejection, error) {
	rec, ok := ReadRejection(dir, fingerprint)
	if !ok {
		rec = Rejection{Fingerprint: fingerprint, First: at}
	}
	rec.Count++
	rec.Last = at
	if !validReason(reason) {
		reason = "unknown"
	}
	rec.Reason = reason
	if dir == "" || fingerprint == "" {
		return rec, errors.New("no state directory or token")
	}
	line := strings.Join([]string{fingerprint, strconv.Itoa(rec.Count), rec.First.UTC().Format(time.RFC3339), rec.Last.UTC().Format(time.RFC3339), rec.Reason}, " ") + "\n"
	return rec, writeState(dir, RejectedMarker, []byte(line))
}

// ClearRejected removes the record (a token that works again).
func ClearRejected(dir string) {
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, RejectedMarker))
	}
}
