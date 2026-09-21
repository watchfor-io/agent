//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/watchfor-io/agent/internal/config"
)

// The unit's restart rules are written against the agent's exit codes;
// the two must never drift apart.
func TestUnitFileMatchesTheExitCodes(t *testing.T) {
	unit, err := os.ReadFile("../../packaging/watchfor-agent.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"RestartPreventExitStatus=" + strconv.Itoa(exitAuth),
		"RestartForceExitStatus=" + strconv.Itoa(config.ExitReload),
	} {
		if !strings.Contains(string(unit), want+"\n") {
			t.Errorf("the unit lacks %q", want)
		}
	}
}
