//go:build linux

package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A command that outlives its deadline is killed and reaped, and one that
// leaves a grandchild holding the output pipe does not hold the caller.
func TestCommandIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Command(ctx, "sleep", "30"); err == nil {
		t.Error("a command past its deadline must fail")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("deadline ignored: took %s", d)
	}

	script := filepath.Join(t.TempDir(), "leave-a-sleeper")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30 &\necho started\n"), 0o755); err != nil { // #nosec G306 -- a test script must be executable
		t.Fatal(err)
	}
	start = time.Now()
	out, _ := Command(context.Background(), script)
	if d := time.Since(start); d > commandWaitDelay+5*time.Second {
		t.Errorf("a grandchild holding the pipe kept the caller for %s", d)
	}
	if string(out) != "started\n" {
		t.Errorf("output = %q", out)
	}
}
