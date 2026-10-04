package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The installer runs `config init -server … -token-file … -spool-dir … -out …`.
// Those flags belong to init, not to config; 0.7.0–0.8.0 parsed them with
// config's flag set and every fresh install stopped at "could not write
// agent.yml".
func TestConfigInitAcceptsInstallerFlags(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "agent.yml")
	code := runConfig([]string{
		"init",
		"-server", "https://ingest.example.com",
		"-token-file", filepath.Join(dir, "token"),
		"-spool-dir", filepath.Join(dir, "spool"),
		"-out", out,
	})
	if code != exitOK {
		t.Fatalf("config init with installer flags: exit %d, want %d", code, exitOK)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "https://ingest.example.com") {
		t.Errorf("agent.yml does not carry the -server value:\n%s", b)
	}
}

// -config before the subcommand still names the file init writes.
func TestConfigInitHonoursConfigFlag(t *testing.T) {
	out := filepath.Join(t.TempDir(), "agent.yml")
	if code := runConfig([]string{"-config", out, "init", "-server", "https://ingest.example.com"}); code != exitOK {
		t.Fatalf("config -config X init: exit %d", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("-config path not written: %v", err)
	}
}
