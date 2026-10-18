package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded units must be the very files packaging/ ships, so the
// installer's curl path and `auto-update on` produce identical systemd
// units.
func TestEmbeddedUnitsMatchPackaging(t *testing.T) {
	for _, name := range []string{UpdateService, UpdateTimer} {
		embedded, err := unitFS.ReadFile("units/" + name)
		if err != nil {
			t.Fatal(err)
		}
		shipped, err := os.ReadFile(filepath.Join("..", "..", "packaging", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(embedded) != string(shipped) {
			t.Fatalf("%s differs between internal/update/units and packaging", name)
		}
	}
}

func TestEnableDisableTimer(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	if err := EnableTimer(context.Background(), dir, run); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{UpdateService, UpdateTimer} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s not written: %v", name, err)
		}
	}
	if strings.Join(calls, ";") != "systemctl daemon-reload;systemctl enable -q --now "+UpdateTimer {
		t.Fatalf("calls: %v", calls)
	}
	calls = nil
	if err := DisableTimer(context.Background(), dir, run); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("units left behind: %v", entries)
	}
	if calls[0] != "systemctl disable -q --now "+UpdateTimer {
		t.Fatalf("calls: %v", calls)
	}
	// Disabling twice is fine.
	if err := DisableTimer(context.Background(), dir, run); err != nil {
		t.Fatal(err)
	}
}

// The agent's unit and cron file embedded for SyncUnits are the very files
// packaging/ ships, and the current ones are known as shipped (so the next
// release can replace them).
func TestEmbeddedAgentUnitsMatchPackaging(t *testing.T) {
	for _, name := range []string{ServiceUnit, "watchfor-agent.cron"} {
		pkg, err := os.ReadFile(filepath.Join("..", "..", "packaging", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(shippedFile(name)) != string(pkg) {
			t.Fatalf("%s differs between internal/update/units and packaging", name)
		}
		sum := sha256.Sum256(pkg)
		if !shipped[name][hex.EncodeToString(sum[:])] {
			t.Errorf("%s: the current file's hash %x is not in the shipped list; add it", name, sum)
		}
	}
}

// An untouched old unit is replaced, a locally edited one is kept, a
// missing one stays missing.
func TestSyncUnits(t *testing.T) {
	dir := t.TempDir()
	unit, cron := filepath.Join(dir, ServiceUnit), filepath.Join(dir, "cron")
	old := []byte("[Unit]\nDescription=an older release\n")
	sum := sha256.Sum256(old)
	shipped[ServiceUnit][hex.EncodeToString(sum[:])] = true
	t.Cleanup(func() { delete(shipped[ServiceUnit], hex.EncodeToString(sum[:])) })
	if err := os.WriteFile(unit, old, 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	changed, kept, err := SyncUnits(context.Background(), dir, cron, run)
	if err != nil || len(changed) != 1 || len(kept) != 0 {
		t.Fatalf("changed %v, kept %v, err %v", changed, kept, err)
	}
	if got, _ := os.ReadFile(unit); string(got) != string(shippedFile(ServiceUnit)) {
		t.Error("the outdated unit was not replaced")
	}
	if _, err := os.Stat(cron); !os.IsNotExist(err) {
		t.Error("a cron file appeared on a host that had none")
	}
	if strings.Join(calls, ";") != "systemctl daemon-reload" {
		t.Errorf("calls = %v", calls)
	}

	if err := os.WriteFile(unit, append(shippedFile(ServiceUnit), "# mine\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ := CompareFile(unit, ServiceUnit); st != FileModified {
		t.Errorf("edited unit = %v, want FileModified", st)
	}
	changed, kept, err = SyncUnits(context.Background(), dir, cron, run)
	if err != nil || len(changed) != 0 || len(kept) != 1 {
		t.Fatalf("edited unit: changed %v, kept %v, err %v", changed, kept, err)
	}
}
