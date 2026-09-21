//go:build linux

package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/watchfor-io/agent/internal/detect"
	"github.com/watchfor-io/agent/internal/modules/disk"
	"github.com/watchfor-io/agent/internal/modules/processes"
)

// tickTheme is formTheme without the cursor band: see multi().
func tickTheme() *huh.Theme {
	t := formTheme(0)
	plain := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E5E7EB"}).Bold(true)
	t.Focused.SelectedOption = plain
	t.Blurred.SelectedOption = plain
	return t
}

// formTheme dresses huh's fields as the rest of the screen: the same
// cursor, and the same band across the row it stands on. width is the
// pane the form sits in, so the band runs to its edge; 0 means the
// width is not known yet and the band hugs the text.
func formTheme(width int) *huh.Theme {
	t := huh.ThemeBase()
	accentC := lipgloss.Color("#4F8EF7")
	dimC := lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8B95A7"}
	textC := lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E5E7EB"}
	redC := lipgloss.Color("#EF4444")
	bandC := lipgloss.AdaptiveColor{Light: "#DBEAFE", Dark: "#1E2A47"}
	f := &t.Focused
	f.Base = lipgloss.NewStyle().PaddingBottom(1)
	f.Title = lipgloss.NewStyle().Bold(true).Foreground(textC)
	f.Description = lipgloss.NewStyle().Foreground(dimC).MarginBottom(1)
	f.ErrorIndicator = lipgloss.NewStyle().Foreground(redC).SetString(" ✗")
	f.ErrorMessage = lipgloss.NewStyle().Foreground(redC).SetString(" ✗ ")
	f.SelectSelector = lipgloss.NewStyle().Foreground(accentC).Bold(true).SetString("❯ ")
	f.MultiSelectSelector = lipgloss.NewStyle().Foreground(accentC).Bold(true).SetString("❯ ")
	f.Option = lipgloss.NewStyle().Foreground(textC)
	// The row the cursor stands on wears the band, exactly as a row in
	// the menu or in a maintenance pane does.
	sel := lipgloss.NewStyle().Foreground(textC).Bold(true).Background(bandC)
	if width > 2 {
		sel = sel.Width(width - 2) // the two columns the cursor takes
	}
	f.SelectedOption = sel
	f.UnselectedOption = lipgloss.NewStyle().Foreground(textC)
	// A tick says this row is picked, nothing about whether it is well:
	// green here reads as "all good" next to a process that is not
	// running, so the mark takes the accent and green is left to mean
	// running.
	f.SelectedPrefix = lipgloss.NewStyle().Foreground(accentC).SetString("● ")
	f.UnselectedPrefix = lipgloss.NewStyle().Foreground(dimC).SetString("○ ")
	f.NextIndicator = lipgloss.NewStyle().Foreground(accentC).SetString("›")
	f.PrevIndicator = lipgloss.NewStyle().Foreground(accentC).SetString("‹")
	f.FocusedButton = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(accentC).Padding(0, 2).Bold(true)
	f.BlurredButton = lipgloss.NewStyle().Foreground(dimC).Padding(0, 2)
	f.TextInput.Cursor = lipgloss.NewStyle().Foreground(accentC)
	f.TextInput.Placeholder = lipgloss.NewStyle().Foreground(dimC).Italic(true).Background(bandC)
	f.TextInput.Prompt = lipgloss.NewStyle().Foreground(accentC)
	// the line being typed wears the band too, so the row under the
	// cursor looks the same whether it is a list or a box to type in
	// No width here: the text field renders the value, its own padding
	// and the cursor as separate pieces, and a style with a width would
	// pad every piece to it and spill the row over three lines. The
	// field pads to its own width anyway, so the band reaches the end of
	// the row by itself.
	f.TextInput.Text = lipgloss.NewStyle().Foreground(textC).Background(bandC)
	f.Card = lipgloss.NewStyle()
	f.NoteTitle = f.Title
	t.Blurred = t.Focused
	t.Blurred.Base = lipgloss.NewStyle().PaddingBottom(1)
	t.Blurred.Title = lipgloss.NewStyle().Foreground(dimC)
	// At rest the current choice is still marked, just quietly: the pane
	// has to show what is set, not only what can be picked.
	t.Blurred.SelectSelector = lipgloss.NewStyle().Foreground(dimC).SetString("❯ ")
	// At rest there is no band: the pane shows what is set, and only the
	// pane with the keyboard carries a lit row.
	t.Blurred.SelectedOption = lipgloss.NewStyle().Foreground(textC).Bold(true)
	t.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")
	t.Blurred.TextInput.Prompt = lipgloss.NewStyle().Foreground(dimC)
	t.Blurred.TextInput.Text = lipgloss.NewStyle().Foreground(textC)
	t.Blurred.TextInput.Placeholder = lipgloss.NewStyle().Foreground(dimC).Italic(true)
	t.Group.Title = f.Title
	t.Group.Description = f.Description
	t.Form.Base = lipgloss.NewStyle()
	t.Group.Base = lipgloss.NewStyle()
	return t
}

// newForm is the wizard's, not a free function, because the theme has to
// know how wide the pane is: huh keeps the first theme a field is given
// and ignores every later one, so the width must be right at build time.
func (w *wizard) newForm(groups ...*huh.Group) *huh.Form {
	return newFormWidth(w.paneW, groups...)
}

func newForm(groups ...*huh.Group) *huh.Form { return newFormWidth(0, groups...) }

func newFormWidth(width int, groups ...*huh.Group) *huh.Form {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "back"))
	// Stacked: every group of a section is on screen at once, so the pane
	// shows the whole section in view mode and the cursor simply walks it
	// in edit mode — no hidden second step.
	f := huh.NewForm(groups...).WithTheme(formTheme(width)).WithShowHelp(false).WithKeyMap(km).WithLayout(huh.LayoutStack)
	// Embedded in the screen: finishing or leaving a form must not quit
	// the program (huh's defaults are tea.Quit / tea.Interrupt). Run()
	// in the accessible path sets its own.
	f.SubmitCmd, f.CancelCmd = nil, nil
	return f
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func firstOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// Every section has one title and one plain explanation, shown the same
// way in the overview pane and at the top of its form. The "Now" block
// in the overview lists exactly what the form can change.
var sectionMeta = map[string]struct{ title, why string }{
	"server":    {"Server", "Where measurements go, and the token that proves this server is yours."},
	"host":      {"Host", "How this server appears in the dashboard, and tags to filter and route by."},
	"interval":  {"Sending", "How often the agent sends, and where batches wait when it cannot."},
	"disks":     {"Disks", "Space used and how busy each disk is. Removable media, /tmp and app images are left out unless you name them."},
	"network":   {"Network", "Traffic, errors and drops per interface. Loopback and container links are left out."},
	"processes": {"Processes", "The busiest processes, and the ones you want an alert about when they stop."},
	"cloud":     {"Cloud lookup", "Ask the cloud for this machine\u2019s instance size, like t4g.small. Once an hour, nothing else."},
	"auto":      {"Auto-update", "Install signed releases by itself, once a day. Off means you upgrade by hand."},
	"log":       {"Log level", "How much goes to the journal. info day to day, debug for a closer look."},
	"file":      {"The file", "The agent.yml this screen writes, with every setting explained in a comment."},
	"system":    {"System", "This machine and the agent on it, right now."},
	"live":      {"Live", "What the collectors measure, refreshed every two seconds."},
	"batch":     {"The batch", "Every value the agent would send to WatchFor right now, by name."},
	"update":    {"Update", "Newer releases, as WatchFor reports them. Installing verifies the signature first."},
	"service":   {"Service & log", "The service that runs the agent, and the last lines of its log."},
	"check":     {"Health check", "Everything that has to be right for the agent to work, and what can be put right from here."},
	"repair":    {"Reinstall & reset", "Two ways back to a known state when something is beyond repair."},
}

// newInput is huh's text input wearing the same cursor as the lists.
func newInput() *huh.Input { return huh.NewInput().Prompt("❯ ") }

// group is a form group of one section. The section's title and
// explanation are drawn by the screen above the form, the same way in
// view and edit mode, so the group itself carries no header.
func group(fields ...huh.Field) *huh.Group {
	return huh.NewGroup(fields...)
}

// group is the wizard's, so it can remember the order of the fields it
// builds: huh does not say which field is where, and the screen needs to
// know to walk a section with Tab and the arrow keys.
func (w *wizard) group(fields ...huh.Field) *huh.Group {
	w.fields = append(w.fields, fields...)
	return group(fields...)
}

// intervalChoices are the send intervals offered; anything between
// them the server would only round anyway, and slower than an hour is
// not monitoring.
var intervalChoices = []struct{ value, label string }{
	{"30s", "every 30 seconds     fine-grained · Pro plan and up"},
	{"1m", "every minute         the usual choice"},
	{"3m", "every 3 minutes"},
	{"5m", "every 5 minutes      quiet servers"},
	{"10m", "every 10 minutes"},
	{"15m", "every 15 minutes"},
	{"30m", "every 30 minutes"},
	{"1h", "every hour           the slowest that still tells you something"},
}

// pick builds a single-choice list that shows every option and records
// its size and the position of the current value for the screen.
func pick[T comparable](w *wizard, current T, title, desc string, opts ...huh.Option[T]) *huh.Select[T] {
	// Height(0): huh sizes the list to its options and leaves the scroll
	// offset alone, so the list stands still and only the cursor moves.
	// Any explicit height makes huh start the window at the current value
	// and scroll — the effect to avoid.
	sel := huh.NewSelect[T]().Title(title).Description(desc).Options(opts...).Height(0)
	if w.optionCount != nil {
		w.optionCount[sel] = len(opts)
		for i, o := range opts {
			if o.Value == current {
				w.startPos[sel] = i
			}
		}
	}
	return sel
}

// multi builds a tick list over items; current nil means all ticked.
func multi[T comparable](w *wizard, title, desc string, items []T, label func(T) string, current []T, height int) (*huh.MultiSelect[T], *[]T) {
	sel := map[T]bool{}
	for _, c := range current {
		sel[c] = true
	}
	opts := make([]huh.Option[T], len(items))
	var chosen []T
	for i, it := range items {
		on := current == nil || sel[it]
		opts[i] = huh.NewOption(w.fitRow(label(it)), it).Selected(on)
		if on {
			chosen = append(chosen, it)
		}
	}
	if height < 4 {
		height = 4
	}
	// Height(0) sizes the list to its rows and never scrolls; only a list
	// taller than the pane gets a window (the screen passes the room).
	extra := 0
	if title != "" {
		extra += 2
	}
	if desc != "" {
		extra += 2
	}
	rows := 0
	if len(items)+extra > height {
		rows = max(3, height-extra) + extra
	}
	ms := huh.NewMultiSelect[T]().Title(title).Description(desc).Options(opts...).Height(rows).Value(&chosen).Filterable(true)
	// A tick list wears its own theme: in huh "selected" means ticked,
	// not "the cursor is here", so the band that marks the cursor row in
	// every other list would light up every ticked row instead. huh keeps
	// the first theme a field is given, so setting it here means the
	// form's theme leaves this field alone.
	ms.WithTheme(tickTheme())
	if w.optionCount != nil {
		w.optionCount[ms] = len(items)
		w.startPos[ms] = 0
	}
	return ms, &chosen
}

// asChoice: everything ticked (or nothing) means the default, nil.
func asChoice[T comparable](chosen []T, all []T) []T {
	if len(chosen) == 0 || len(chosen) == len(all) {
		return nil
	}
	return chosen
}

// mountLine is how a mount is shown wherever a person picks from a list.
func mountLine(m disk.Discovered) string {
	return fmt.Sprintf("%-16s %8s  %3.0f%% used  %s", m.Point, detect.Bytes(m.TotalBytes), m.UsedPct, m.FSType)
}

func mountPoints(ms []disk.Discovered) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Point
	}
	return out
}

// topChoices: how many of the busiest processes ride along in a batch.
var topChoices = []huh.Option[string]{
	huh.NewOption("off      no process list", "0"),
	huh.NewOption("top 5    the usual choice", "5"),
	huh.NewOption("top 10", "10"),
	huh.NewOption("top 20   busy application servers", "20"),
}

// maxWatches is what the plan allows per host; more than a handful of
// watched processes is a sign the top list is the better tool.
const maxWatches = 5

// processPickLimit is how many running processes the picker offers.
const processPickLimit = 20

// watchNameW is the name column of the picker: wide enough for the full
// name of anything the kernel would have cut at 15 characters.
const watchNameW = 32

// parseWatches reads a comma-separated list of watches as typed by hand.
// bad is the first entry that could not be read, empty when all could.
func parseWatches(text string) (out []processes.Watch, bad string) {
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		wt := parseWatch(part)
		if wt == nil {
			return nil, part
		}
		out = append(out, *wt)
	}
	return out, ""
}

// stateNow says whether a watched process is running right now, in the
// colour that means it: a stopped one is the whole point of watching, so
// it is marked, not hidden.
func stateNow(n int) string {
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	amber := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	if n == 0 {
		return amber.Render("not running now")
	}
	return green.Render("running")
}

// col is one column of a picker row: always exactly w wide, so a long
// command line cannot push the numbers on its row out of line with the
// rest. A path is cut at the front — its last part is what names it.
func col(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return fmt.Sprintf("%-*s", w, s)
	}
	if strings.Contains(s, "/") {
		return "…" + ansi.TruncateLeft(s, lipgloss.Width(s)-w+1, "")
	}
	return ansi.Truncate(s, w, "…")
}

// watchLabel is how a watch reads in a list.
func watchLabel(wt processes.Watch) string {
	s := wt.Name
	if s == "" {
		s = wt.Cmdline
	}
	if wt.User != "" {
		s += " (" + wt.User + ")"
	}
	return s
}

// watchText renders a watch on one line; parseWatch reads it back.
func watchText(wt processes.Watch) string {
	s := wt.Name
	if s == "" {
		s = "cmdline:" + wt.Cmdline
	}
	if wt.User != "" {
		s += " user:" + wt.User
	}
	return s
}

func parseWatch(line string) *processes.Watch {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var wt processes.Watch
	if i := strings.LastIndex(line, " user:"); i >= 0 {
		wt.User = strings.TrimSpace(line[i+len(" user:"):])
		line = strings.TrimSpace(line[:i])
	}
	switch {
	case strings.HasPrefix(line, "cmdline:"):
		wt.Cmdline = strings.TrimSpace(strings.TrimPrefix(line, "cmdline:"))
		if wt.Cmdline == "" {
			return nil
		}
	case strings.ContainsAny(line, " \t"):
		return nil // a name is one word; anything longer wants cmdline:
	default:
		wt.Name = line
	}
	return &wt
}
