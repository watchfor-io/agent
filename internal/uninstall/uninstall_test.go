package uninstall

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeExec struct {
	calls   []string
	hasUser bool
}

func (f *fakeExec) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "id" && !f.hasUser {
		return nil, errors.New("no such user")
	}
	if name == "userdel" {
		f.hasUser = false
	}
	return nil, nil
}

// lay out an installed agent under root: config dir, state dir, units,
// cron, lock, binary. The config names the state dir and the token file.
func install(t *testing.T, root, stateDir string) Options {
	t.Helper()
	etc := filepath.Join(root, "etc", "watchfor-agent")
	units := filepath.Join(root, "etc", "systemd", "system")
	lock := filepath.Join(root, "run", "lock")
	bin := filepath.Join(root, "usr", "local", "bin")
	for _, d := range []string{etc, units, lock, bin, stateDir, filepath.Join(root, "etc", "cron.d")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	token := filepath.Join(etc, "token")
	must := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must(token, "wfh_secret")
	must(filepath.Join(etc, "agent.yml"), "server:\n  url: https://ingest.example\n  token_file: "+token+"\nspool:\n  dir: "+stateDir+"\n")
	must(filepath.Join(stateDir, "spool.bin"), "x")
	must(filepath.Join(stateDir, "update-available"), "0.9.0")
	for _, u := range []string{"watchfor-agent.service", "watchfor-agent-update.service", "watchfor-agent-update.timer"} {
		must(filepath.Join(units, u), "[Unit]")
	}
	must(filepath.Join(root, "etc", "cron.d", "watchfor-agent"), "* * * * *")
	must(filepath.Join(lock, "watchfor-agent-upgrade.lock"), "")
	must(filepath.Join(bin, "watchfor-agent"), "#!binary")
	return Options{
		ConfigPath: filepath.Join(etc, "agent.yml"),
		ExePath:    filepath.Join(bin, "watchfor-agent"),
		UnitDir:    units,
		CronFile:   filepath.Join(root, "etc", "cron.d", "watchfor-agent"),
		LockDir:    lock,
	}
}

func TestRunRemovesEverythingTheInstallerCreated(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "var", "lib", "watchfor-agent")
	o := install(t, root, stateDir)
	fx := &fakeExec{hasUser: true}
	var out bytes.Buffer
	o.Exec, o.Out = fx.run, &out

	plan := Plan(context.Background(), o)
	if len(plan) != 10 { // 3 units, cron, state, token, config, lock, binary, user
		t.Fatalf("plan has %d lines:\n%s", len(plan), strings.Join(plan, "\n"))
	}
	if err := Run(context.Background(), o); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	for _, p := range []string{o.ConfigPath, filepath.Dir(o.ConfigPath), stateDir, o.ExePath, o.CronFile,
		filepath.Join(o.UnitDir, "watchfor-agent.service"), filepath.Join(o.UnitDir, "watchfor-agent-update.timer"),
		filepath.Join(o.LockDir, "watchfor-agent-upgrade.lock")} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still there (%v)", p, err)
		}
	}
	joined := strings.Join(fx.calls, "\n")
	for _, want := range []string{"systemctl disable -q --now watchfor-agent.service", "systemctl disable -q --now watchfor-agent-update.timer", "systemctl daemon-reload", "userdel watchfor-agent"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing command %q in:\n%s", want, joined)
		}
	}
	if !strings.Contains(out.String(), "(overwritten first)") || !strings.Contains(out.String(), "removed system user watchfor-agent") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestRunKeepsForeignDirectoriesAndConfigOnRequest(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "srv", "shared-spool") // not ours by name
	o := install(t, root, stateDir)
	fx := &fakeExec{}
	var out bytes.Buffer
	o.Exec, o.Out, o.KeepConfig = fx.run, &out, true
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "spool.bin")); err != nil {
		t.Errorf("foreign state dir was removed: %v", err)
	}
	if !strings.Contains(out.String(), "kept "+stateDir) {
		t.Errorf("no kept line for the state dir:\n%s", out.String())
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(o.ConfigPath), "token")); err != nil || string(b) != "wfh_secret" {
		t.Errorf("token touched under -keep-config: %q %v", b, err)
	}
	if _, err := os.Stat(o.ExePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("binary kept: %v", err)
	}
	if strings.Contains(strings.Join(fx.calls, "\n"), "userdel") {
		t.Error("userdel run for a user that does not exist")
	}
}

func TestSafeDir(t *testing.T) {
	for path, want := range map[string]bool{
		"/var/lib/watchfor-agent":  true,
		"/etc/watchfor-agent/":     true,
		"/":                        false,
		"/var/lib":                 false,
		"var/lib/watchfor-agent":   false,
		"/data/watchfor-agent/../": false,
	} {
		if got := safeDir(path); got != want {
			t.Errorf("safeDir(%q) = %v, want %v", path, got, want)
		}
	}
}
