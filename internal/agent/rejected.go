package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RejectedMarker is written next to the spool when the server rejects
// the token (host removed in WatchFor, or token rotated). It holds the
// token's fingerprint, so the block lifts by itself once a different
// token is configured — and until then neither the service on its next
// boot nor a cron `once` keeps knocking on ingest every minute.
const RejectedMarker = "token-rejected"

// RejectedAt reports when the token with this fingerprint was last
// rejected; ok is false when there is no such record.
func RejectedAt(dir, fingerprint string) (at time.Time, ok bool) {
	if dir == "" || fingerprint == "" {
		return time.Time{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, RejectedMarker))
	if err != nil {
		return time.Time{}, false
	}
	fp, ts, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	if fp != fingerprint {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// MarkRejected records the rejection (best effort; a read-only state dir
// just means the agent will ask the server again next time).
func MarkRejected(dir, fingerprint string, at time.Time) error {
	if dir == "" || fingerprint == "" {
		return errors.New("no state directory or token")
	}
	tmp := filepath.Join(dir, "."+RejectedMarker+".tmp")
	if err := os.WriteFile(tmp, []byte(fingerprint+" "+at.UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, RejectedMarker))
}

// ClearRejected removes the record (a token that works again).
func ClearRejected(dir string) {
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, RejectedMarker))
	}
}
