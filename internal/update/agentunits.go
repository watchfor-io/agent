package update

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The agent's own service unit and cron file, as this release ships them.
// `upgrade` swaps only the binary, so without this a host would keep the
// unit (and its hardening) of whichever release first installed it.
//
//go:embed units/watchfor-agent.service units/watchfor-agent.cron
var agentUnitFS embed.FS

const (
	// ServiceUnit is the agent's systemd service.
	ServiceUnit = "watchfor-agent.service"
	// CronPath is where the installer and the README put the cron entry.
	CronPath = "/etc/cron.d/watchfor-agent"
)

// shipped lists the sha256 of every version of each file a release has
// shipped. A file on disk with one of these hashes is ours and unmodified,
// so replacing it loses nothing; any other content is the operator's and
// is left alone. Add the old hash here whenever packaging/ changes.
var shipped = map[string]map[string]bool{
	ServiceUnit: {
		"0498223390961050a360a0842c7c92480ef712ef5ee4f662d30d0507bedeb16d": true, // 0.7.0 – 0.8.2
	},
	"watchfor-agent.cron": {
		"4b01f2bbb6eba1a3f53a34794d868cddcb79614c123ac4f0a5c7cabbac8c23f8": true, // 0.7.0 – 0.8.2
		"fc173875181f7f6bae5db20b4045cab3b653ee770ab2a4c2478bdce621f07ec8": true, // 0.9.0: MAILTO=""
	},
}

// FileState is how an installed unit or cron file compares with this
// release's.
type FileState int

const (
	// FileMissing: not installed (a cron-only host has no unit, and the
	// other way round).
	FileMissing FileState = iota
	// FileCurrent: exactly this release's file.
	FileCurrent
	// FileOutdated: an earlier release's file, untouched — safe to replace.
	FileOutdated
	// FileModified: edited locally (or from a release before 0.7.0); left
	// alone — drop-ins are the way to change the unit.
	FileModified
)

func shippedFile(name string) []byte {
	b, _ := agentUnitFS.ReadFile("units/" + name)
	return b
}

// CompareFile says how the file at path compares with name as shipped.
func CompareFile(path, name string) (FileState, error) {
	cur, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return FileMissing, nil
	}
	if err != nil {
		return FileMissing, err
	}
	if string(cur) == string(shippedFile(name)) {
		return FileCurrent, nil
	}
	sum := sha256.Sum256(cur)
	if shipped[name][hex.EncodeToString(sum[:])] {
		return FileOutdated, nil
	}
	return FileModified, nil
}

// SyncUnits brings the installed service unit and cron file up to this
// release, but only where they are an untouched earlier release's: a
// missing file stays missing (the host chose the other way to run) and a
// modified one stays as it is and is named in kept. systemd is reloaded
// when the unit changed; the caller restarts the service.
func SyncUnits(ctx context.Context, unitDir, cronPath string, run Exec) (changed, kept []string, err error) {
	if unitDir == "" {
		unitDir = UnitDir
	}
	if cronPath == "" {
		cronPath = CronPath
	}
	files := []struct{ path, name string }{
		{filepath.Join(unitDir, ServiceUnit), ServiceUnit},
		{cronPath, "watchfor-agent.cron"},
	}
	for _, f := range files {
		st, err := CompareFile(f.path, f.name)
		if err != nil {
			return changed, kept, err
		}
		switch st {
		case FileModified:
			kept = append(kept, f.path)
		case FileOutdated:
			if err := replaceFile(f.path, shippedFile(f.name)); err != nil {
				return changed, kept, err
			}
			changed = append(changed, f.path)
		}
	}
	for _, c := range changed {
		if strings.HasSuffix(c, ".service") {
			if out, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
				return changed, kept, fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}
	}
	return changed, kept, nil
}

// replaceFile writes data next to path and renames it over: a reader (or
// cron, or systemd) never sees half a file.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // gone after the rename; cleans up otherwise
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil { // #nosec G302 -- systemd units and cron files are read by everyone
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
