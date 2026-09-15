package update

import (
	"context"
	"os/exec"
)

// execScript runs a test "binary" (a shell script) the way Run does.
func execScript(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
