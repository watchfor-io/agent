//go:build linux

package main

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/watchfor-io/agent/internal/modules/processes"
	"github.com/watchfor-io/agent/internal/procfs"
)

// Everything shown on the screen about another user's process has been
// through procfs.Clean by the time the collector hands it over; this
// pins the belt to the braces: even a name that somehow arrived raw is
// cleaned again before it is drawn.
func TestHostileProcessNameCannotReachTheTerminal(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	hostile := "evil\x1b]0;pwned\x07\x1b[2J"
	w.sum.Processes = []processes.Running{{Name: procfs.Clean(hostile), Cmdline: procfs.Clean("/usr/bin/" + hostile + " --x"), Count: 1, RSS: 1 << 20, User: "someone"}}
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	view := s.details("processes")
	if strings.ContainsAny(view, "\x1b\x07") {
		t.Fatalf("an escape sequence reached the screen:\n%q", view)
	}
	if !strings.Contains(view, "evil") {
		t.Errorf("the name itself should still be shown:\n%s", view)
	}
}
