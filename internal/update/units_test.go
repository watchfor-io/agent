package update

import (
	"context"
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
