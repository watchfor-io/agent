package config

import (
	"os"
	"path/filepath"
	"strings"
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
	os.WriteFile(loose, []byte("tok\n"), 0o644)
	os.WriteFile(tight, []byte("tok\n"), 0o600)

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
