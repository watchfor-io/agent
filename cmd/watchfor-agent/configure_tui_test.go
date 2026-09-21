//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// The screen is a plain model: drive it with key messages and look at
// the state, which is what a person would see.
func TestConfigureScreenKeys(t *testing.T) {
	dir := t.TempDir()
	w := newWizardWith(filepath.Join(dir, "agent.yml"), testSummary())
	w.tty = true
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 36})

	// cursor moves and stays in range
	s.Update(tea.KeyMsg{Type: tea.KeyDown})
	s.Update(tea.KeyMsg{Type: tea.KeyDown})
	if s.cursor != 2 {
		t.Fatalf("cursor = %d after two downs", s.cursor)
	}
	s.Update(tea.KeyMsg{Type: tea.KeyUp})
	if s.cursor != 1 {
		t.Fatalf("cursor = %d after up", s.cursor)
	}

	// Enter opens the section's form; Esc inside it goes back, nothing written
	s.cursor = menuIndex("host")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.form == nil {
		t.Fatal("enter did not open a form")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if s.form != nil {
		t.Fatal("esc did not close the form")
	}
	if _, err := os.Stat(w.path); err == nil {
		t.Fatal("a file was written without a confirmed change")
	}

	// confirming a form: run the returned commands the way the runtime
	// would, and the form must be gone (no blank pane) with the file written
	s.cursor = menuIndex("interval")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.form == nil {
		t.Fatal("interval form did not open")
	}
	// Enter through every field (the Sending form has three), draining the
	// commands the way the runtime would
	for presses := 0; presses < 6 && s.form != nil; presses++ {
		_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
		drain(s, cmd)
	}
	if s.form != nil {
		t.Fatal("form still open after confirming — the pane would sit blank")
	}
	if _, err := os.Stat(w.path); err != nil {
		t.Fatalf("confirmed change not written: %v", err)
	}

	// Space on a two-way switch flips it, Enter confirms and writes it
	s.cursor = menuIndex("cloud")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.form == nil {
		t.Fatal("cloud form did not open")
	}
	before := w.o.CloudMetadata
	s.Update(keyRunes(" "))
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	drain(s, cmd)
	if s.form != nil || w.o.CloudMetadata == before {
		t.Fatalf("space+enter did not flip cloud lookup (form open=%v, value=%v, was %v)", s.form != nil, w.o.CloudMetadata, before)
	}

	// q asks first; n stays; q then y leaves at once
	s.Update(keyRunes("q"))
	if !s.leaving {
		t.Fatal("q did not ask before leaving")
	}
	if _, cmd := s.Update(keyRunes("n")); s.leaving || isQuit(cmd) {
		t.Fatal("n should stay")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !s.leaving {
		t.Fatal("esc did not ask before leaving")
	}
	if _, cmd := s.Update(keyRunes("y")); !isQuit(cmd) {
		t.Fatal("y did not quit")
	}

	// the view renders every part without panicking
	out := s.View()
	for _, want := range []string{"WatchFor", "Server", "Disks", "Quit?"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q", want)
		}
	}
}

// promptly runs a command and gives up on it after a moment: a timer —
// the text cursor's blink, a refresh tick — is not something a test
// should sit through, and the message it would eventually send changes
// nothing a test looks at.
func promptly(cmd tea.Cmd) tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case m := <-done:
		return m
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

// drain runs a command chain the way the Bubble Tea runtime would,
// feeding each resulting message back into the screen.
func drain(s *screen, cmd tea.Cmd) {
	for i := 0; i < 8 && cmd != nil; i++ {
		msg := promptly(cmd)
		if batch, ok := msg.(tea.BatchMsg); ok {
			var next []tea.Cmd
			for _, c := range batch {
				if c != nil {
					if m := promptly(c); m != nil {
						next = append(next, func() tea.Msg { return m })
					}
				}
			}
			cmd = tea.Batch(next...)
			continue
		}
		if msg == nil {
			return
		}
		_, cmd = s.Update(msg)
	}
}

// Caps Lock is not a reason to be stuck: the few letter keys the screen
// has answer in either case. The vim pair g/G is the one deliberate
// exception, so it is left alone.
func TestLetterKeysIgnoreCapsLock(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Q'}})
	if !s.leaving {
		t.Fatal("Q did not offer to quit the way q does")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if s.leaving {
		t.Fatal("N did not cancel the way n does")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	if s.preview == nil {
		t.Fatal("P did not open the file the way p does")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Q'}})
	if s.preview != nil {
		t.Fatal("Q did not close the preview the way q does")
	}
}
