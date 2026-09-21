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
	// A file that is not there is not invented: a one-line agent.yml would
	// run on defaults nobody chose.
	if _, err := Set(p, "updates.auto", "false"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Set on a missing file: %v", err)
	}
	if err := os.WriteFile(p, []byte("server:\n  url: https://ingest.watchfor.io\n  token_file: /etc/watchfor-agent/token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Set(p, "updates.auto", "false"); err != nil {
		t.Fatal(err)
	}
	if v, _ := Get(p, "updates.auto"); v != "false" {
		t.Fatalf("got %q", v)
	}
}

func TestSetKeepsCommentsAligned(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.yml")
	text := "server:\n  url: https://ingest.watchfor.io   # where batches go\n  token_file: /etc/watchfor-agent/token\n\ninterval: 60s                             # how often\n\nlog:\n  level: info                             # debug | info\n\nupdates:\n  auto: false                             # timer\n"
	if err := os.WriteFile(p, []byte(text), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"interval", "30s"}, {"log.level", "debug"}, {"updates.auto", "true"}} {
		if _, err := Set(p, kv[0], kv[1]); err != nil {
			t.Fatalf("set %s: %v", kv[0], err)
		}
	}
	got, _ := os.ReadFile(p)
	for _, want := range []string{
		"interval: 30s                             # how often",
		"  level: debug                            # debug | info",
		"  auto: true                              # timer",
		"  url: https://ingest.watchfor.io   # where batches go",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// A key without its own line still lands (node editor), in a valid file.
	if _, err := Set(p, "spool.max_mb", "32"); err != nil {
		t.Fatal(err)
	}
	if v, _ := Get(p, "spool.max_mb"); v != "32" {
		t.Fatalf("spool.max_mb = %q", v)
	}
}

// The first save on a machine the installer never touched: the folder is
// made when it can be, and when it cannot the message names the folder
// and what to do — not a temp file the person never asked for.
func TestWriteMakesTheFolderOrSaysWhy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "etc", "watchfor-agent", "agent.yml")
	if _, err := WriteConfig(path, "server:\n  url: https://ingest.watchfor.io\n  token: x\n"); err != nil {
		t.Fatalf("a folder that can be made was not: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nothing written: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root can create anything; the refusal cannot be provoked")
	}
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	_, err := WriteConfig(filepath.Join(locked, "watchfor-agent", "agent.yml"), "server:\n  url: https://ingest.watchfor.io\n  token: x\n")
	if err == nil {
		t.Fatal("wrote into a folder that cannot be created")
	}
	msg := err.Error()
	for _, want := range []string{filepath.Join(locked, "watchfor-agent"), "sudo", "-config"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, ".agent.yml.") {
		t.Errorf("the message leaks the temp file name: %s", msg)
	}
}
