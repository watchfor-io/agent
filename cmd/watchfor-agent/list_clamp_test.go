//go:build linux

package main

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// Up at the first option and Down at the last must not wrap; Space at the
// last option wraps on purpose (a switch flips back). The log-level list
// (4 options, cursor starting on the current level) is the probe.
func TestListsStopAtTheirEnds(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	w.o.LogLevel = "info" // first option
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	s.cursor = menuIndex("log")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sel, ok := s.form.GetFocusedField().(*huh.Select[string])
	if !ok {
		t.Fatalf("focused field is %T", s.form.GetFocusedField())
	}
	hovered := func() string { v, _ := sel.Hovered(); return v }
	s.Update(tea.KeyMsg{Type: tea.KeyUp}) // at the top: must stay
	if got := hovered(); got != "info" {
		t.Fatalf("up at the top wrapped to %q", got)
	}
	for i := 0; i < 6; i++ { // more downs than options: must stop at the last
		s.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if got := hovered(); got != "error" {
		t.Fatalf("down past the end landed on %q", got)
	}
	s.Update(keyRunes(" ")) // space at the last option wraps to the first
	if got := hovered(); got != "info" {
		t.Fatalf("space at the end did not wrap, got %q", got)
	}
}

// A list stands still: every option is on screen from the first paint and
// moving the cursor never shifts the rows (the current value starts the
// cursor, not the window).
func TestListsDoNotScroll(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	w.o.Interval = "10m" // deep in the list: a windowed list would open here and hide 30s
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 45})
	s.cursor = menuIndex("interval")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	before := s.View()
	for _, want := range []string{"every 30 seconds", "every minute", "every 10 minutes", "every hour"} {
		if !strings.Contains(before, want) {
			t.Fatalf("option %q not on screen on open", want)
		}
	}
	s.Update(tea.KeyMsg{Type: tea.KeyDown})
	s.Update(tea.KeyMsg{Type: tea.KeyDown})
	after := s.View()
	for _, want := range []string{"every 30 seconds", "every minute", "every hour"} {
		if !strings.Contains(after, want) {
			t.Fatalf("option %q left the screen after moving the cursor", want)
		}
	}
}
