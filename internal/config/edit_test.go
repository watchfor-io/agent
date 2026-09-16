package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetKeepsCommentsAndValidates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.yml")
	if err := os.WriteFile(p, []byte("# my server\nserver:\n  url: https://ingest.watchfor.io # prod\n  token_file: /etc/watchfor-agent/token\ninterval: 15s\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg, err := Set(p, "updates.auto", "true")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Updates.Auto == nil || !*cfg.Updates.Auto {
		t.Fatalf("updates.auto not applied: %+v", cfg.Updates)
	}
	if _, err := Set(p, "interval", "30s"); err != nil {
		t.Fatal(err)
	}
	if _, err := Set(p, "host.tags.env", "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := Set(p, "host.name", "true"); err != nil { // a string that looks like a bool stays a string
		t.Fatal(err)
	}
	out, _ := os.ReadFile(p)
	text := string(out)
	for _, want := range []string{"# my server", "# prod", "interval: 30s", "updates:", "auto: true", "env: prod", `name: "true"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %o", st.Mode().Perm())
	}
	for key, want := range map[string]string{"interval": "30s", "updates.auto": "true", "host.tags.env": "prod", "host.name": "true", "spool.max_mb": "64"} {
		got, err := Get(p, key)
		if err != nil || got != want {
			t.Fatalf("Get(%s) = %q, %v; want %q", key, got, err, want)
		}
	}
}

func TestSetRefusesBadValuesAndSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.yml")
	for _, kv := range [][2]string{
		{"interval", "fast"},
		{"interval", "1s"}, // below the agent's 5 s floor → validation refuses
		{"spool.max_mb", "-3"},
		{"updates.auto", "maybe"},
		{"server.token", "wfh_x"},
		{"nope", "x"},
		{"host.name", "bad name!"},
	} {
		if _, err := Set(p, kv[0], kv[1]); err == nil {
			t.Fatalf("Set(%s=%s) accepted", kv[0], kv[1])
		}
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("a refused Set created the file")
	}
	// A fresh file is created for a valid value.
	if _, err := Set(p, "updates.auto", "false"); err != nil {
		t.Fatal(err)
	}
	if v, _ := Get(p, "updates.auto"); v != "false" {
		t.Fatalf("got %q", v)
	}
}
