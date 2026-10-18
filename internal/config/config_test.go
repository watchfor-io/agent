package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func parse(t *testing.T, src string) (*Config, error) {
	t.Helper()
	return Parse(strings.NewReader(src))
}

func TestDefaults(t *testing.T) {
	cfg, err := parse(t, "server:\n  url: https://ingest.watchfor.io\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != DefaultInterval || cfg.Server.Timeout != DefaultTimeout {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.Host.Name == "" {
		t.Error("host.name should default to the short hostname")
	}
	if len(cfg.Modules) != len(DefaultModules) {
		t.Errorf("default modules = %v", cfg.Modules)
	}
	if cfg.HasToken() {
		t.Error("no token configured, HasToken should be false")
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	_, err := parse(t, "server:\n  url: https://x\n  tokne: abc\n")
	if err == nil || !strings.Contains(err.Error(), "tokne") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestIntervalFloor(t *testing.T) {
	_, err := parse(t, "interval: 1s\n")
	if err == nil || !strings.Contains(err.Error(), "minimum") {
		t.Fatalf("expected interval error, got %v", err)
	}
}

func TestHTTPOnlyForLoopback(t *testing.T) {
	if _, err := parse(t, "server:\n  url: http://ingest.example.com\n"); err == nil {
		t.Error("plain http to a remote host must be rejected")
	}
	if _, err := parse(t, "server:\n  url: http://127.0.0.1:8090\n"); err != nil {
		t.Errorf("loopback http should be allowed: %v", err)
	}
}

func TestTokenFromEnv(t *testing.T) {
	t.Setenv("WF_TOKEN", "  abc123\n")
	cfg, err := parse(t, "server:\n  url: https://x\n  token: env:WF_TOKEN\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Token != "abc123" {
		t.Errorf("token = %q", cfg.Server.Token)
	}
	if _, err := parse(t, "server:\n  token: env:WF_MISSING\n"); err == nil {
		t.Error("empty env var must be an error")
	}
}

func TestTokenFilePermissions(t *testing.T) {
	dir := t.TempDir()
	loose := filepath.Join(dir, "loose")
	tight := filepath.Join(dir, "tight")
	for p, mode := range map[string]os.FileMode{loose: 0o644, tight: 0o600} {
		if err := os.WriteFile(p, []byte("tok\n"), mode); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := parse(t, "server:\n  token_file: "+loose+"\n"); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("world-readable token file must be rejected, got %v", err)
	}
	cfg, err := parse(t, "server:\n  token_file: "+tight+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Token != "tok" {
		t.Errorf("token = %q", cfg.Server.Token)
	}
}

func TestTokenAndFileExclusive(t *testing.T) {
	if _, err := parse(t, "server:\n  token: a\n  token_file: /x\n"); err == nil {
		t.Error("token and token_file together must be rejected")
	}
}

func TestTags(t *testing.T) {
	if _, err := parse(t, "host:\n  tags: {Env: prod}\n"); err == nil {
		t.Error("uppercase tag key must be rejected")
	}
	cfg, err := parse(t, "host:\n  name: web-01\n  tags: {env: prod, role: web}\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host.Tags["role"] != "web" {
		t.Errorf("tags = %v", cfg.Host.Tags)
	}
}

func TestModulesKeepTheirNodes(t *testing.T) {
	cfg, err := parse(t, "interval: 30s\nmodules:\n  system: {per_core: true}\n  disk:\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != 30*time.Second {
		t.Errorf("interval = %s", cfg.Interval)
	}
	if _, ok := cfg.Modules["system"]; !ok {
		t.Error("system module node missing")
	}
	if _, ok := cfg.Modules["disk"]; !ok {
		t.Error("disk module node missing (null subtree must still register the module)")
	}
}

func TestEmptyFile(t *testing.T) {
	if _, err := parse(t, ""); err != nil {
		t.Fatalf("an empty config should mean all defaults: %v", err)
	}
}

// A token file with a second line or a character no header may carry is
// a config error at load — not a failing push retried into the spool —
// and a FIFO in its place is refused at once instead of blocking.
func TestTokenFileContents(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "token")
		_ = os.Remove(p)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	load := func(tokenFile string) (*Config, error) {
		return Parse(strings.NewReader("server:\n  url: https://ingest.example\n  token_file: " + tokenFile + "\n"))
	}
	if c, err := load(write("\n  wfh_abc \n\n")); err != nil || c.Server.Token != "wfh_abc" {
		t.Fatalf("one token with blank lines around it: %v", err)
	}
	if _, err := load(write("wfh_abc\nwfh_def\n")); err == nil || !strings.Contains(err.Error(), "2 lines") {
		t.Errorf("two lines: %v", err)
	}
	if _, err := load(write("wfh_a\x01bc\n")); err == nil {
		t.Error("a control character was accepted")
	}
	if _, err := load(write("wfh abc\n")); err == nil {
		t.Error("a space inside the token was accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() { _, err := load(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a plain file") {
			t.Errorf("FIFO token: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a FIFO token_file blocked the load")
	}
}

// run/once refuse an agent.yml, or a token folder, that another local
// user could change: they could point server.url elsewhere and receive
// the token.
func TestCheckOwnership(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte("wfh_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent.yml")
	if err := os.WriteFile(path, []byte("server:\n  url: https://ingest.example\n  token_file: "+tok+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckOwnership(path); err != nil {
		t.Fatalf("0644 in a 0755 folder: %v", err)
	}
	for _, mode := range []os.FileMode{0o666, 0o664, 0o646} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := cfg.CheckOwnership(path); err == nil || !strings.Contains(err.Error(), "server.url") {
			t.Errorf("agent.yml %04o: %v", mode, err)
		}
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckOwnership(path); err == nil {
		t.Error("a world-writable folder was accepted")
	}
	if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckOwnership(path); err != nil {
		t.Errorf("a sticky folder like /tmp: %v", err)
	}
}
