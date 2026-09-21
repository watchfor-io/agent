//go:build linux

package main

import (
	"github.com/watchfor-io/agent/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/watchfor-io/agent/internal/modules/disk"
	"github.com/watchfor-io/agent/internal/modules/processes"
)

// col is the reason every picker row lines up: one width, whatever the
// name is, with the part that identifies the process kept.
func TestColumnWidth(t *testing.T) {
	long := "/home/someone/work/project/node_modules/.bin/next"
	for _, in := range []string{"brave", long, "a-very-long-service-name-indeed", ""} {
		if got := lipgloss.Width(col(in, 26)); got != 26 {
			t.Errorf("col(%q) is %d wide, want 26", in, got)
		}
	}
	if got := col(long, 26); !strings.HasSuffix(strings.TrimSpace(got), ".bin/next") {
		t.Errorf("a path lost its last part: %q", got)
	}
	if got := col("a-very-long-service-name-indeed", 26); !strings.HasSuffix(strings.TrimSpace(got), "…") {
		t.Errorf("a long name was not marked as cut: %q", got)
	}
}

// Every process row puts its memory in the same column, however long the
// command line is, and no more than maxWatches can be ticked.
func TestProcessPickerLinesUp(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	w.sum.Processes = []processes.Running{
		{Name: "brave", Count: 51, RSS: 9 << 30, User: "root"},
		{Name: "node", Cmdline: "/home/someone/work/project/node_modules/.bin/next", Count: 2, RSS: 128 << 20, User: "someone"},
		{Name: "dockerd", Count: 1, RSS: 1 << 30, User: "root"},
		{Name: "a-very-long-service-name-indeed", Count: 1, RSS: 23 << 20, User: "someone"},
	}
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	view := s.viewForm("processes").View()
	at := -1
	rows := 0
	for _, line := range strings.Split(view, "\n") {
		b := strings.Index(line, " MB ")
		if b < 0 {
			b = strings.Index(line, " GB ")
		}
		if b < 0 {
			continue
		}
		i := lipgloss.Width(line[:b]) // columns, not bytes: × and … are wider
		rows++
		if at < 0 {
			at = i
			continue
		}
		if i != at {
			t.Errorf("a row puts its memory at column %d, the others at %d:\n%s", i, at, line)
		}
	}
	if rows != len(w.sum.Processes) {
		t.Fatalf("the picker offered %d rows, want %d:\n%s", rows, len(w.sum.Processes), view)
	}
}

// A tick list must not wear the cursor band: huh calls a ticked option
// "selected", so a band meant for the row under the cursor would pad
// every ticked row to the full pane and wrap it in two.
func TestTickListsAreNotBanded(t *testing.T) {
	if w := tickTheme().Focused.SelectedOption.GetWidth(); w != 0 {
		t.Errorf("a ticked row is padded to %d columns; it must take only its own width", w)
	}
	// the band is a background colour, so ask the renderer for colours
	// and look for it on a row that is ticked but not under the cursor
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.paneW = 60
	w.optionCount, w.startPos = map[huh.Field]int{}, map[huh.Field]int{}
	field, _ := multi(w, "Mounts", "", []string{"/", "/boot"}, func(s string) string { return s }, nil, 12)
	f := w.newForm(group(field)).WithWidth(w.paneW)
	f.Init()
	const band = "48;2;30;42;71" // the cursor band, as a background
	for _, line := range strings.Split(f.View(), "\n") {
		i := strings.Index(line, "●")
		if i < 0 {
			continue
		}
		// the tick and the name it marks belong on one line: a padded
		// row pushes the name onto the next one
		if rest := strings.TrimSpace(ansi.Strip(line[i:])); rest == "●" {
			t.Errorf("a ticked row wrapped: the tick is alone on its line:\n%q", line)
		}
		if strings.Contains(line, band) {
			t.Errorf("a ticked row wears the cursor band:\n%q", line)
		}
	}
	// the single-choice list still has it, so this is not a theme that
	// simply lost its band
	sel, _ := "on", ""
	pickField := pick(w, sel, "Disk activity", "", huh.NewOption("on", "on"), huh.NewOption("off", "off"))
	g := w.newForm(group(pickField)).WithWidth(w.paneW)
	g.Init()
	if !strings.Contains(g.View(), band) {
		t.Errorf("the cursor row of a single-choice list lost its band:\n%s", g.View())
	}
}

// Every settings pane must fit its side of the screen. A style that pads
// a row to the full width lands one column too wide once the tick and
// the cursor are in front of it, and every row silently wraps in two.
func TestSettingsPanesDoNotWrap(t *testing.T) {
	// with colours on, a padded row is measured as the terminal measures
	// it, which is where the wrapping shows
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	for _, width := range []int{200, 150, 120, 100} {
		w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
		w.tty = true
		w.sum.Mounts = []disk.Discovered{{Point: "/", FSType: "ext4", TotalBytes: 231 << 30, UsedPct: 54}}
		w.sum.Devices = []string{"nvme0n1", "dm-0"}
		w.sum.Disks = []disk.Disk{
			{Name: "nvme0n1", SizeBytes: 238 << 30, SSD: true},
			{Name: "dm-0", SizeBytes: 235 << 30, Parent: "nvme0n1", Label: "dm_crypt-0", Kind: "encrypted"},
		}
		w.sum.Interfaces = []string{"enp2s0", "wlp3s0"}
		s := newScreen(w)
		s.Update(tea.WindowSizeMsg{Width: width, Height: 44})
		for _, key := range []string{"server", "host", "interval", "disks", "network", "processes", "cloud", "auto", "log"} {
			s.cursor = menuIndex(key)
			s.inPane = false
			s.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if s.form == nil {
				t.Fatalf("%s did not open", key)
			}
			pane := s.rightWidth() - 4
			// every field takes the keyboard in turn: a list is drawn
			// differently once it is focused, and that is where it wrapped
			for step := 0; step < max(1, len(s.w.fields)); step++ {
				lines := strings.Split(s.form.View(), "\n")
				for n, line := range lines {
					if got := lipgloss.Width(line); got > pane {
						t.Errorf("width %d, %s, field %d: line %d is %d wide, the pane is %d:\n%s",
							width, key, step, n+1, got, pane, line)
					}
					plain := ansi.Strip(line)
					tick := strings.IndexAny(plain, "●○")
					if tick < 0 {
						continue
					}
					if rest := strings.TrimSpace(plain[tick+len("●"):]); rest == "" {
						t.Errorf("width %d, %s, field %d: line %d has a tick and nothing else — the row wrapped:\n%q",
							width, key, step, n+1, line)
					}
					// the line after a row is another row or the gap under the list
					if n+1 < len(lines) {
						next := ansi.Strip(lines[n+1])
						if strings.TrimSpace(next) != "" && !strings.ContainsAny(next, "●○") {
							t.Errorf("width %d, %s, field %d: the row on line %d spilled onto line %d:\n%q\n%q",
								width, key, step, n+1, n+2, plain, next)
						}
					}
				}
				s.Update(tea.KeyMsg{Type: tea.KeyTab})
			}
			s.Update(tea.KeyMsg{Type: tea.KeyEsc})
		}
	}
}

// A section is a ring: Tab walks its fields and comes round, and the
// arrow keys carry on to the next field once a list has no more options
// in that direction.
func TestFieldsAreARing(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	s.cursor = menuIndex("interval")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	n := len(s.w.fields)
	if n < 3 {
		t.Fatalf("the sending section has %d fields, expected the interval, the spool and its size", n)
	}
	// the legend at the foot says so, like every other key
	if v := s.View(); !strings.Contains(v, "tab") || !strings.Contains(v, "next field") {
		t.Errorf("the footer does not mention Tab while editing:\n%s", strings.Split(v, "\n")[len(strings.Split(v, "\n"))-1])
	}
	for i := 1; i <= n; i++ {
		s.Update(tea.KeyMsg{Type: tea.KeyTab})
		if got, want := s.fieldAt(), i%n; got != want {
			t.Fatalf("tab %d moved to field %d, want %d", i, got, want)
		}
	}
	// shift+tab the other way, from the first field round to the last
	s.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := s.fieldAt(); got != n-1 {
		t.Errorf("shift+tab from the first field went to %d, want the last (%d)", got, n-1)
	}
	// and the arrows: at the top of the first list, Up leaves the field
	for s.fieldAt() != 0 {
		s.Update(tea.KeyMsg{Type: tea.KeyTab})
	}
	s.Update(tea.KeyMsg{Type: tea.KeyHome}) // to the top of the interval list
	if got := s.fieldAt(); got != 0 {
		t.Fatalf("home left the field (now %d)", got)
	}
	s.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := s.fieldAt(); got == 0 {
		t.Errorf("up at the top of the list stayed on field 0; it should carry on to another field")
	}
	// and down at the bottom of the last list comes round to the first
	for s.fieldAt() != n-1 {
		s.Update(tea.KeyMsg{Type: tea.KeyTab})
	}
	s.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := s.fieldAt(); got != 0 {
		t.Errorf("down on the last field went to %d, want the first", got)
	}
}

// The watched processes and the ones offered below them are one table:
// whether a process is running belongs in the same column as the user
// that owns the others.
func TestWatchedStateLinesUpWithTheUsers(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	w.o.Watches = []processes.Watch{{Name: "a-watched-thing"}}
	w.sum.Processes = []processes.Running{{Name: "dockerd", Count: 1, RSS: 1 << 30, User: "root"}}
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	var state, user int
	for _, line := range strings.Split(s.details("processes"), "\n") {
		plain := []rune(ansi.Strip(line))
		at := func(word string) int {
			i := strings.Index(string(plain), word)
			if i < 0 {
				return -1
			}
			return len([]rune(string(plain)[:i]))
		}
		if i := at("not running now"); i > 0 {
			state = i
		}
		if i := at("root"); i > 0 {
			user = i
		}
	}
	if state == 0 || user == 0 {
		t.Fatalf("the rows are not both there (state %d, user %d):\n%s", state, user, s.details("processes"))
	}
	if state != user {
		t.Errorf("the state is at column %d and the owner at %d:\n%s", state, user, s.details("processes"))
	}
}

// sgrHas reports whether any escape sequence in the line carries the
// given parameter on its own ("3" is italic; "38;2;r;g;b" a colour).
func sgrHas(line, param string) bool {
	for i := 0; i < len(line); {
		j := strings.Index(line[i:], "\x1b[")
		if j < 0 {
			return false
		}
		end := strings.IndexByte(line[i+j:], 'm')
		if end < 0 {
			return false
		}
		seq := line[i+j+2 : i+j+end]
		if param == "3" {
			for _, p := range strings.Split(seq, ";") {
				if p == "3" {
					return true
				}
			}
		} else if strings.Contains(seq, param) {
			return true
		}
		i += j + end + 1
	}
	return false
}

// A value that is set reads in the text colour; an empty field shows a
// hint, and a hint must never pass for a value — dim and slanted, in
// view mode as in edit mode.
func TestEmptyFieldsShowHintsNotValues(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	const text, dim = "229;231;235", "139;149;167"
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	w.o.Tags = map[string]string{"env": "prod"} // set; the name is left empty
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	s.cursor = menuIndex("host")
	check := func(view, mode string) {
		for _, l := range strings.Split(view, "\n") {
			plain := ansi.Strip(l)
			switch {
			case strings.Contains(plain, "the hostname is used"):
				if !sgrHas(l, dim) || !sgrHas(l, "3") {
					t.Errorf("%s: the empty name's hint is not dim and slanted:\n%q", mode, l)
				}
			case strings.Contains(plain, "env=prod") && strings.Contains(plain, "❯"):
				if !sgrHas(l, text) || sgrHas(l, "3") {
					t.Errorf("%s: the tags that are set do not read as a value:\n%q", mode, l)
				}
			}
		}
	}
	check(s.details("host"), "at rest")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	check(s.form.View(), "editing")
}

// A process the list does not offer is typed in by hand and lands in the
// config like a ticked one; the five-process limit still holds and says so.
func TestWatchByHand(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	w.tty = true
	w.sum.Processes = []processes.Running{
		{Name: "brave", Count: 51, RSS: 9 << 30, User: "someone"},
		{Name: "dockerd", Count: 1, RSS: 1 << 30, User: "root"},
		{Name: "gnome-terminal-", Cmdline: "/usr/libexec/gnome-terminal-server", Count: 1, RSS: 90 << 20, User: "someone"},
	}
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 150, Height: 50})
	s.cursor = menuIndex("processes")
	view := s.details("processes")
	for _, want := range []string{"Add by hand", "gnome-terminal-server"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the pane lacks %q:\n%s", want, view)
		}
	}
	// into the section, past the top list and the tick list, onto the box
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	s.Update(tea.KeyMsg{Type: tea.KeyTab})
	s.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := s.fieldAt(); got != 2 {
		t.Fatalf("the box is field %d, want 2", got)
	}
	for _, r := range "sshd, cmdline:gunicorn user:www" {
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter}) // the last field: Enter confirms the section
	drain(s, cmd)
	if s.form != nil {
		t.Fatal("the section did not close on Enter")
	}
	got := map[string]bool{}
	for _, wt := range w.o.Watches {
		got[watchText(wt)] = true
	}
	for _, want := range []string{"sshd", "cmdline:gunicorn user:www"} {
		if !got[want] {
			t.Errorf("%q is not watched; have %v", want, got)
		}
	}
	if w.note != "" {
		t.Errorf("two watches drew a note: %q", w.note)
	}
	// the limit: four ticked plus two by hand keeps five and explains
	w.o.Watches = []processes.Watch{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}
	w.note = ""
	s.forgetViewForms()
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	s.Update(tea.KeyMsg{Type: tea.KeyTab})
	s.Update(tea.KeyMsg{Type: tea.KeyTab})
	for _, r := range "e, f" {
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd = s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	drain(s, cmd)
	// the note is handed to the strip at the foot of the screen on save
	if len(w.o.Watches) != maxWatches || !strings.Contains(s.status, "left out") {
		t.Errorf("got %d watches and the strip says %q", len(w.o.Watches), s.status)
	}
}

// A save the agent refuses leaves the screen showing what is on disk, not
// the rejected draft: the config, and every pane that reads it, stay put.
func TestRefusedSaveKeepsDiskValue(t *testing.T) {
	w := newWizardWith(filepath.Join(t.TempDir(), "agent.yml"), testSummary())
	if err := os.WriteFile(w.path, []byte("server:\n  url: https://ingest.watchfor.io\n  token_file: /etc/watchfor-agent/token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.load(mustParse(t, w.path))
	w.tty = true
	s := newScreen(w)
	s.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	s.cursor = menuIndex("server")
	s.Update(tea.KeyMsg{Type: tea.KeyEnter}) // edit Address
	for i := 0; i < 40; i++ {
		s.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "http://x" { // http is refused
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	// Tab off the field and confirm the section
	s.Update(tea.KeyMsg{Type: tea.KeyTab})
	s.Update(tea.KeyMsg{Type: tea.KeyTab})
	s.Update(tea.KeyMsg{Type: tea.KeyTab})
	drain(s, func() tea.Msg { _, c := s.Update(tea.KeyMsg{Type: tea.KeyEnter}); return c })
	if got := w.o.Server; got != "https://ingest.watchfor.io" {
		t.Errorf("the refused draft stuck in memory: server = %q", got)
	}
	on := mustReadFile(t, w.path)
	if !strings.Contains(on, "https://ingest.watchfor.io") || strings.Contains(on, "http://x") {
		t.Errorf("agent.yml was changed by a refused save:\n%s", on)
	}
}

func mustParse(t *testing.T, path string) *config.Config {
	t.Helper()
	c, err := config.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
