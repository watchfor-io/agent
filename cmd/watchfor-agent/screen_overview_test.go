//go:build linux

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// One screen for everything: the overview panes render with live data,
// the maintenance panes react to their keys, and no key changes screens.
func TestOverviewPanes(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	if v := s.View(); !strings.Contains(v, "Overview") || !strings.Contains(v, "Settings") || !strings.Contains(v, "Maintenance") {
		t.Fatalf("menu groups missing:\n%s", v)
	}
	// two samples 1 s apart, as Init would do
	for i := 0; i < 2; i++ {
		s.Update(s.sample()())
		time.Sleep(120 * time.Millisecond)
	}
	s.cursor = menuIndex("live")
	v := s.View()
	for _, want := range []string{"Resources", "cpu", "memory", "load", "Disks", "Network", "Busiest processes"} {
		if !strings.Contains(v, want) {
			t.Errorf("live pane lacks %q", want)
		}
	}
	s.cursor = menuIndex("system")
	if v := s.View(); !strings.Contains(v, "Machine") || !strings.Contains(v, "Agent") || !strings.Contains(v, "uptime") {
		t.Errorf("system pane incomplete:\n%s", v)
	}
	s.cursor = menuIndex("batch")
	if v := s.View(); !strings.Contains(v, "cpu.usage_pct") {
		t.Error("batch pane lacks samples")
	}
	// Enter on a settings item opens a form; on an overview item it does not
	s.cursor = menuIndex("server")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.form == nil {
		t.Fatal("enter on Server did not open a form")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEsc})
	s.cursor = menuIndex("live")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.form != nil {
		t.Fatal("enter on Live opened a form")
	}
	// update pane: the running version shows before any network answer,
	// and the answer adds the time it arrived
	s.cursor = menuIndex("update")
	if v := s.View(); !strings.Contains(v, "running") || !strings.Contains(v, Version) {
		t.Errorf("update pane does not name the running version:\n%s", v)
	}
	s.Update(updateMsg{})
	if v := s.View(); !strings.Contains(v, "checked ") {
		t.Errorf("update pane did not record when it checked:\n%s", v)
	}
	// service pane: running state, boot state, and one row per thing it
	// can do — no letter shortcuts anywhere
	s.cursor, s.inPane = menuIndex("service"), false
	s.svcState, s.svcEnabled = "inactive", "disabled"
	v = s.View()
	for _, want := range []string{"not enabled at boot", "Start the service", "Restart the service", "Enable at boot"} {
		if !strings.Contains(v, want) {
			t.Errorf("service pane lacks %q:\n%s", want, v)
		}
	}
	// health check: landing on it runs the checks, Enter moves the cursor
	// into the pane, and the first row runs them again
	s.cursor, s.inPane, s.checks, s.checkBusy = menuIndex("check")-1, false, nil, false
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyDown}) // arriving on the section
	if s.cursor != menuIndex("check") {
		t.Fatalf("down landed on %q", menuItems[s.cursor].key)
	}
	if cmd == nil {
		t.Fatal("landing on the check did not start it")
	}
	s.Update(cmd())
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !s.inPane || s.act != 0 {
		t.Fatalf("enter did not move the cursor into the pane (inPane=%v act=%d)", s.inPane, s.act)
	}
	s.checks, s.checkBusy = nil, false
	_, cmd = s.Update(tea.KeyMsg{Type: tea.KeyEnter}) // "Run the checks again"
	if cmd == nil {
		t.Fatal("the first row did not run the checks")
	}
	s.Update(cmd())
	if s.checks == nil {
		t.Fatal("the check never ran")
	}
	v = s.View()
	for _, want := range []string{"config file", "agent binary", "checked ", "Run the checks again", "Compare with the signed release"} {
		if !strings.Contains(v, want) {
			t.Errorf("check pane lacks %q:\n%s", want, v)
		}
	}
	// repair: the cursor moves onto a row and Enter asks before anything
	// touches the disk; the answer is a choice, not a letter
	s.cursor, s.inPane = menuIndex("repair"), false
	if v := s.View(); !strings.Contains(v, "Reinstall the binary") || !strings.Contains(v, "Reset agent.yml to defaults") {
		t.Errorf("repair pane lacks its actions:\n%s", v)
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEnter}) // into the pane
	s.Update(tea.KeyMsg{Type: tea.KeyDown})  // onto "Reset agent.yml"
	if s.act != 1 {
		t.Fatalf("down moved to row %d, want 1", s.act)
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.confirm == nil {
		t.Fatal("reset did not ask first")
	}
	if s.confirm.choice != 1 {
		t.Errorf("the question starts on %d, want the safe answer (1)", s.confirm.choice)
	}
	if v := s.View(); !strings.Contains(v, "Write a fresh agent.yml?") || !strings.Contains(v, "No, leave it alone") {
		t.Errorf("the question is not on screen:\n%s", v)
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEnter}) // No is under the cursor
	if s.confirm != nil {
		t.Error("the question stayed after it was answered")
	}
	if _, err := os.Stat(w.path); !os.IsNotExist(err) {
		t.Error("a declined reset still wrote agent.yml")
	}
	// saying yes writes the file
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.confirm == nil {
		t.Fatal("the question did not come back")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyUp})
	if cmd := func() tea.Cmd { _, c := s.Update(tea.KeyMsg{Type: tea.KeyEnter}); return c }(); cmd != nil {
		cmd()
	}
	if _, err := os.Stat(w.path); err != nil {
		t.Errorf("yes did not write agent.yml: %v", err)
	}
	// a development build has no release to reinstall: the row says so
	// and choosing it explains instead of asking
	s.act = 0
	if v := s.View(); !strings.Contains(v, "development build") {
		t.Errorf("the reinstall row does not say why it cannot run:\n%s", v)
	}
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.confirm != nil || !strings.Contains(s.status, "development build") {
		t.Errorf("reinstall on a dev build: confirm=%v status=%q", s.confirm != nil, s.status)
	}
	if s.statusKind != msgWarn {
		t.Errorf("nothing-to-do was reported as kind %d, want a note", s.statusKind)
	}
}

// Something standing in the way is an error, and the strip says so in
// the same voice as a repair that failed.
func TestBlockedActionIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: nothing is blocked")
	}
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	s.cursor = menuIndex("service")
	s.svcState, s.svcEnabled = "inactive", "disabled"
	s.Update(tea.KeyMsg{Type: tea.KeyEnter}) // into the pane, on "Start the service"
	s.Update(tea.KeyMsg{Type: tea.KeyEnter}) // choose it
	if !strings.Contains(s.status, "needs root") {
		t.Fatalf("no reason was given: %q", s.status)
	}
	if s.statusKind != msgFail {
		t.Errorf("needing root was reported as kind %d, want a failure", s.statusKind)
	}
	if v := s.View(); !strings.Contains(v, "✗ needs root") {
		t.Errorf("the strip does not mark it as a failure:\n%s", v)
	}
}

// A settings pane must read the same in view and edit mode: the same
// title, the same words, the same rows — only the cursor colour differs.
func TestViewAndEditMatch(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 150, Height: 46})
	strip := func(v string) []string {
		var out []string
		for _, l := range strings.Split(regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`).ReplaceAllString(v, ""), "\n") {
			if t := strings.TrimSpace(strings.TrimLeft(l, "❯▌● ○")); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	for _, key := range []string{"server", "host", "interval", "disks", "network", "processes", "cloud", "auto", "log"} {
		s.cursor = menuIndex(key)
		view := strip(s.details(key))
		s.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if s.form == nil {
			t.Fatalf("%s: enter opened no form", key)
		}
		edit := strip(s.paneHeader(key) + s.form.View())
		s.Update(tea.KeyMsg{Type: tea.KeyEsc})
		// view ends with the "Enter to change" hint; the rest must match
		if len(view) == 0 || view[len(view)-1] != "Enter to change" {
			t.Fatalf("%s: view has no hint line: %q", key, view)
		}
		view = view[:len(view)-1]
		if len(view) != len(edit) {
			t.Fatalf("%s: view has %d lines, edit %d\nview: %q\nedit: %q", key, len(view), len(edit), view, edit)
		}
		for i := range view {
			if view[i] != edit[i] {
				t.Errorf("%s line %d differs:\n view: %q\n edit: %q", key, i, view[i], edit[i])
			}
		}
	}
}

// Nothing may run past the frame: every menu name is shown in full when
// the terminal has the room, and no rendered line is wider than the
// terminal at any width.
func TestScreenFitsTheTerminal(t *testing.T) {
	longest := ""
	for _, it := range menuItems {
		if len(it.label) > len(longest) {
			longest = it.label
		}
	}
	for _, width := range []int{200, 150, 120, 100, 80, 60} {
		w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
		w.tty = true
		s := newScreen(w)
		s.Update(tea.WindowSizeMsg{Width: width, Height: 34})
		for _, key := range []string{"system", "file", "check", "repair", "service", "update"} {
			s.cursor = menuIndex(key)
			for n, line := range strings.Split(s.View(), "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("width %d, %s: line %d is %d wide:\n%s", width, key, n+1, got, line)
				}
			}
		}
		if width >= 100 && !strings.Contains(s.View(), longest) {
			t.Errorf("width %d cut the longest menu name (%q)", width, longest)
		}
	}
}

// A message must never move the screen: the strip under the panes is
// reserved whether or not there is anything in it, at every terminal size.
func TestMessageDoesNotMoveTheScreen(t *testing.T) {
	for _, size := range []struct{ w, h int }{{200, 50}, {150, 40}, {120, 30}, {120, 26}, {100, 22}, {80, 18}} {
		w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
		w.tty = true
		s := newScreen(w)
		s.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		s.cursor = menuIndex("check")
		quiet := strings.Split(s.View(), "\n")
		s.say(msgFail, "spool folder: mkdir /var/lib/watchfor-agent: permission denied")
		loud := strings.Split(s.View(), "\n")
		if len(quiet) != len(loud) {
			t.Errorf("%dx%d: the screen went from %d lines to %d when something went wrong",
				size.w, size.h, len(quiet), len(loud))
		}
		if len(loud) > size.h {
			t.Errorf("%dx%d: the screen is %d lines, taller than the terminal", size.w, size.h, len(loud))
		}
		for i, line := range loud {
			if lipgloss.Width(line) > size.w {
				t.Errorf("%dx%d: line %d is %d wide", size.w, size.h, i+1, lipgloss.Width(line))
			}
		}
		// the message itself is on screen, and marked as a failure
		if !strings.Contains(strings.Join(loud, "\n"), "permission denied") {
			t.Errorf("%dx%d: the message is not on the screen:\n%s", size.w, size.h, strings.Join(loud, "\n"))
		}
		// every section stays reachable even when the list is windowed
		for i := range menuItems {
			s.cursor = i
			view := s.View()
			if !strings.Contains(view, menuItems[i].label) {
				t.Fatalf("%dx%d: %q is not shown when the cursor is on it", size.w, size.h, menuItems[i].label)
			}
		}
	}
}

// Resources and disks are read as one table: every bar is the same
// length and every figure starts in the same column, whatever this
// machine's mounts and interfaces are called.
func TestLiveBarsLineUp(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	s.Update(s.sample()())
	var rows []string
	for _, line := range strings.Split(s.details("live"), "\n") {
		if strings.Contains(line, "█") || strings.Contains(line, "░") {
			rows = append(rows, line)
		}
	}
	if len(rows) < 2 {
		t.Skip("this machine reported fewer than two bars")
	}
	width, at := -1, -1
	for _, r := range rows {
		plain := []rune(ansi.Strip(r))
		first, last := -1, -1
		for i, c := range plain {
			if c == '█' || c == '░' {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if width < 0 {
			width, at = last-first+1, first
		}
		if got := last - first + 1; got != width {
			t.Errorf("a bar is %d wide, the others %d:\n%s", got, width, strings.Join(rows, "\n"))
		}
		if first != at {
			t.Errorf("a bar starts at column %d, the others at %d:\n%s", first, at, strings.Join(rows, "\n"))
		}
		pct := -1
		for i, c := range plain { // columns, not bytes: a bar rune is three
			if c == '%' {
				pct = i
				break
			}
		}
		if pct != at+width+4 {
			t.Errorf("the figure is at column %d, the bar ends at %d:\n%q", pct, at+width, r)
		}
	}
}

// underBand walks a rendered line and reports whether every visible
// character sits on the band: a background wrapped around already-styled
// text is cut off by the first reset inside it.
func underBand(line string) bool {
	const band = "48;2;30;42;71"
	on := false
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			end := strings.IndexByte(line[i:], 'm')
			if end < 0 {
				return false
			}
			seq := line[i+2 : i+end]
			switch {
			case strings.Contains(seq, band):
				on = true
			case seq == "" || seq == "0" || strings.HasPrefix(seq, "0;"):
				on = false
			}
			i += end + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if r != ' ' && !on {
			return false
		}
		i += size
	}
	return true
}

// The row the cursor is on is lit from edge to edge — in the menu, in a
// maintenance pane, and in a question — not only under the cursor glyph.
func TestCursorRowIsLitEdgeToEdge(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	s.cursor = menuIndex("update")
	find := func(view, word string) string {
		for _, l := range strings.Split(view, "\n") {
			if strings.Contains(ansi.Strip(l), word) {
				return l
			}
		}
		t.Fatalf("no line with %q:\n%s", word, ansi.Strip(view))
		return ""
	}
	// the menu row under the cursor: the part of the line inside the
	// menu's frame, before the right pane starts
	if row := find(s.View(), "newer agent releases"); !underBand(strings.Split(row, "│")[1]) {
		t.Errorf("the menu's cursor row is not lit edge to edge:\n%q", row)
	}
	// the action row once the keyboard is in the pane
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if row := find(s.details("update"), "Check for a newer release"); !underBand(row) {
		t.Errorf("the pane's cursor row is not lit edge to edge:\n%q", row)
	}
	// and the answer under the cursor in a question
	s.cursor, s.inPane, s.act = menuIndex("repair"), true, 1
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.confirm == nil {
		t.Fatal("no question appeared")
	}
	if row := find(s.details("repair"), "No, leave it alone"); !underBand(row) {
		t.Errorf("the answer under the cursor is not lit edge to edge:\n%q", row)
	}
}
