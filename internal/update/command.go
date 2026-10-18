package update

import (
	"context"
	"os/exec"
	"time"
)

const (
	// CommandTimeout bounds a command whose caller gave no deadline. The
	// slowest one the agent runs is `systemctl restart`, which may wait
	// out the unit's 90 s stop timeout.
	CommandTimeout = 2 * time.Minute
	// commandWaitDelay is how long the output pipes may stay open after
	// the command was killed or exited: a grandchild holding them (a
	// systemctl that forked a pager, a script that left a sleeper) must
	// not keep the caller, or the child unreaped, for ever.
	commandWaitDelay = 5 * time.Second
)

// Command is the Exec every caller uses by default: the combined output
// of one command, never longer than ctx allows or CommandTimeout when ctx
// has no deadline, and the child always reaped.
func Command(ctx context.Context, name string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, CommandTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = commandWaitDelay
	return cmd.CombinedOutput()
}
