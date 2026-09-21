//go:build linux

package health

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/watchfor-io/agent/internal/config"
)

func stubExec(replies map[string]string) Exec {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte(replies[strings.Join(append([]string{name}, args...), " ")]), nil
	}
}

// A healthy install reports nothing to fix; a token anyone can read is a
// failure with a fix that actually repairs it.
func TestChecksAndFixes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { // a repair refuses a parent that others may write
		t.Fatal(err)
	}
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("wfh_x\n"), 0o644); err != nil { // too open on purpose
		t.Fatal(err)
	}
	spool := filepath.Join(dir, "spool")
	cfgPath := filepath.Join(dir, "agent.yml")
	text := "server:\n  url: https://ingest.watchfor.io\n  token_file: " + token + "\nspool:\n  dir: " + spool + "\n"
	if err := os.WriteFile(cfgPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseText(text)
	if err != nil {
		t.Fatal(err)
	}
	o := Options{ConfigPath: cfgPath, Config: cfg, Version: "1.2.3", Root: true,
		Layout: Layout{Config: cfgPath, Token: token, Spool: spool},
		Exec: stubExec(map[string]string{
			"systemctl is-active watchfor-agent":  "active",
			"systemctl is-enabled watchfor-agent": "enabled",
		}),
		Dial:          func(context.Context, string, string) error { return nil },
		HaveSystemctl: func() bool { return true },
		UnitFile:      filepath.Join(dir, "watchfor-agent.service"),
		LookupUser:    func(string) (*user.User, error) { return nil, user.UnknownUserError("none") },
	}
	if err := os.WriteFile(o.UnitFile, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	byName := func(cs []Check) map[string]Check {
		m := map[string]Check{}
		for _, c := range cs {
			m[c.Name] = c
		}
		return m
	}
	checks := byName(Run(context.Background(), o))
	tok := checks["host token"]
	if tok.State != Fail || tok.Fix == nil {
		t.Fatalf("world-readable token: state=%v fix=%v (%s)", tok.State, tok.Fix != nil, tok.Detail)
	}
	if sp := checks["spool folder"]; sp.State != Fail || sp.Fix == nil {
		t.Fatalf("missing spool: state=%v fix=%v", sp.State, sp.Fix != nil)
	}
	// apply the offered fixes and check the machine really changed
	for _, name := range []string{"host token", "spool folder"} {
		if err := checks[name].Fix(context.Background()); err != nil && !os.IsPermission(err) && !strings.Contains(err.Error(), "unknown user") {
			t.Fatalf("%s fix: %v", name, err)
		}
	}
	if st, err := os.Stat(token); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token still %v (%v)", st.Mode().Perm(), err)
	}
	if st, err := os.Stat(spool); err != nil || !st.IsDir() {
		t.Fatalf("spool not created: %v", err)
	}
	after := byName(Run(context.Background(), o))
	if after["host token"].State != OK || after["spool folder"].State != OK {
		t.Fatalf("still unhealthy after the fixes: %+v %+v", after["host token"], after["spool folder"])
	}
	if after["config file"].State != OK {
		t.Errorf("config check: %+v", after["config file"])
	}
	// the deep run adds the release comparison; a dev build skips it
	deep := byName(Run(context.Background(), Options{ConfigPath: cfgPath, Config: cfg, Version: "dev", Exec: o.Exec, Deep: true, Dial: o.Dial, HaveSystemctl: o.HaveSystemctl, UnitFile: o.UnitFile, LookupUser: o.LookupUser}))
	rc, ok := deep["release checksum"]
	if !ok {
		t.Fatal("a deep run has no release checksum check")
	}
	if rc.State != Skipped {
		t.Errorf("a development build should skip the release comparison, got %v (%s)", rc.State, rc.Detail)
	}
	if _, ok := byName(Run(context.Background(), o))["release checksum"]; ok {
		t.Error("the quick run downloaded the release")
	}

	// every check is present even without a config
	names := byName(Run(context.Background(), Options{ConfigPath: filepath.Join(dir, "gone.yml"), Version: "1.2.3", Exec: o.Exec, Dial: o.Dial, HaveSystemctl: o.HaveSystemctl, UnitFile: o.UnitFile, LookupUser: o.LookupUser}))
	for _, want := range []string{"config file", "agent binary", "host token", "spool folder", "systemd service", "auto-update timer", "WatchFor reachable"} {
		if _, ok := names[want]; !ok {
			t.Errorf("check %q missing when there is no config", want)
		}
	}
}

// Root repairs the installer's paths and nothing else. A token that
// agent.yml points at somewhere unusual is reported, never touched; a
// symlink is refused before anything looks through it; and a parent
// directory that others can write is off limits, however standard the
// path. These are the moves a hostile local user would make.
func TestRepairsTouchOnlyTheInstalledPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { // a repair refuses a parent that others may write
		t.Fatal(err)
	}
	text := func(token, spool string) *config.Config {
		cfg, err := config.ParseText("server:\n  url: https://ingest.watchfor.io\n  token_file: " + token + "\nspool:\n  dir: " + spool + "\n")
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	byName := func(cs []Check) map[string]Check {
		m := map[string]Check{}
		for _, c := range cs {
			m[c.Name] = c
		}
		return m
	}
	stub := Options{Version: "1.2.3", Root: true,
		Exec:          stubExec(nil),
		Dial:          func(context.Context, string, string) error { return nil },
		HaveSystemctl: func() bool { return false },
		LookupUser:    func(string) (*user.User, error) { return nil, user.UnknownUserError("none") },
	}

	// 1. the same wrong token, at the installed path and elsewhere
	loose := filepath.Join(dir, "token")
	if err := os.WriteFile(loose, []byte("wfh_x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := stub
	o.Config, o.Layout = text(loose, filepath.Join(dir, "spool")), Layout{Token: loose, Spool: filepath.Join(dir, "spool")}
	if c := byName(Run(context.Background(), o))["host token"]; c.State != Fail || c.Fix == nil {
		t.Fatalf("at the installed path the fix must be offered: %+v", c)
	}
	o.Layout = Layout{Token: "/etc/watchfor-agent/token"} // the file is somewhere else
	if c := byName(Run(context.Background(), o))["host token"]; c.State != Fail || c.Fix != nil || !strings.Contains(c.Detail, "not the installed location") {
		t.Fatalf("elsewhere it must be reported and left alone: %+v", c)
	}

	// 2. a symlink where the token should be: never followed, never fixed
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	o.Config, o.Layout = text(link, filepath.Join(dir, "spool")), Layout{Token: link}
	if c := byName(Run(context.Background(), o))["host token"]; c.State != Fail || c.Fix != nil || !strings.Contains(c.Detail, "symlink") {
		t.Fatalf("a symlink must be called out with no fix: %+v", c)
	}
	if err := Private(link, ServiceUser); err == nil {
		t.Fatal("Private followed a symlink")
	}
	if st, _ := os.Stat(victim); st.Mode().Perm() != 0o644 {
		t.Fatalf("the file behind the link was touched: %v", st.Mode().Perm())
	}

	// 3. a parent that others can write: nothing is created or changed there
	open := filepath.Join(dir, "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := createDir(filepath.Join(open, "spool"), ServiceUser); err == nil || !strings.Contains(err.Error(), "writable by others") {
		t.Fatalf("a spool was created under a world-writable parent: %v", err)
	}
	inOpen := filepath.Join(open, "token")
	if err := os.WriteFile(inOpen, []byte("wfh_x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Private(inOpen, ServiceUser); err == nil {
		t.Fatal("a file under a world-writable parent was repaired")
	}
	if err := WritePrivate(inOpen, []byte("new\n"), ServiceUser); err == nil {
		t.Fatal("a token was written under a world-writable parent")
	}

	// 4. the honest case still works end to end
	good := filepath.Join(dir, "good-token")
	if err := WritePrivate(good, []byte("wfh_y\n"), ServiceUser); err != nil {
		t.Fatalf("WritePrivate: %v", err)
	}
	if st, _ := os.Stat(good); st.Mode().Perm() != 0o600 {
		t.Fatalf("token written with %v", st.Mode().Perm())
	}
}
