package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StateFile is written by the running agent (into its state directory,
// the spool dir) when the server reports a newer release: one line, the
// version. `watchfor-agent upgrade` reads it so an operator — or the
// opt-in update timer — never has to ask GitHub what is current.
const StateFile = "update-available"

// WriteState records that version is available. Atomic: a reader never
// sees a half-written file.
func WriteState(dir, version string) error {
	if dir == "" {
		return errors.New("no state directory")
	}
	tmp := filepath.Join(dir, "."+StateFile+".tmp")
	if err := os.WriteFile(tmp, []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, StateFile))
}

// ClearState removes the hint; a missing file is not an error.
func ClearState(dir string) error {
	if dir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(dir, StateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ReadState returns the reported version and when it was written; ok is
// false when there is no usable hint.
func ReadState(dir string) (version string, at time.Time, ok bool) {
	if dir == "" {
		return "", time.Time{}, false
	}
	p := filepath.Join(dir, StateFile)
	st, err := os.Stat(p)
	if err != nil {
		return "", time.Time{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", time.Time{}, false
	}
	v := strings.TrimSpace(string(b))
	if !IsRelease(v) {
		return "", time.Time{}, false
	}
	return v, st.ModTime(), true
}
