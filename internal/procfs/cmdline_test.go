//go:build linux

package procfs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A process may give itself megabytes of argv; only what is kept is read.
func TestCmdlineReadsNoMoreThanItKeeps(t *testing.T) {
	root := t.TempDir()
	old := Root
	Root = root
	t.Cleanup(func() { Root = old })
	dir := filepath.Join(root, "4242")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	huge := append([]byte("python3\x00"), bytes.Repeat([]byte("a"), 8<<20)...)
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), huge, 0o644); err != nil {
		t.Fatal(err)
	}
	got := Cmdline(4242, 256)
	if len(got) > 256 || len(got) < 200 {
		t.Fatalf("Cmdline kept %d bytes of an 8 MB argv", len(got))
	}
	if got[:8] != "python3 " {
		t.Fatalf("the NULs were not turned into spaces: %q", got[:8])
	}
}
