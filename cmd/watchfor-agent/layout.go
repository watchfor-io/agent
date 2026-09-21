//go:build linux

package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The menu is measured once: the label column is as wide as the
// longest name, the hint column as wide as the longest hint. Nothing in
// the menu is ever cut when the terminal has the room for it.
var menuLabelW, menuHintW = func() (int, int) {
	l, h := 0, 0
	for _, it := range menuItems {
		l = max(l, lipgloss.Width(it.label))
		h = max(h, lipgloss.Width(it.hint))
	}
	return l, h
}()

// gutter is the air between a name and the words that explain it,
// everywhere on this screen. One space reads as cramped; three lets the
// eye find the second column without a rule between them.
const gutter = "   "

// menuChrome is what a menu row needs besides its two columns: the
// cursor, the gutter, and the pane's border and padding.
const menuChrome = 2 + len(gutter) + 4

// msgKind decides how the strip is drawn: plain, worth noticing, done,
// or went wrong.
type msgKind int

const (
	msgInfo msgKind = iota
	msgWarn
	msgOK
	msgFail
)

// messageRows is the height the strip takes on a terminal with room for
// it. Whatever it is, it is reserved message or not, so nothing above it
// ever moves; a short terminal spends one line instead of three.
const messageRows = 3

// stripRows is the room this terminal can spare for the message strip: a
// framed box when the menu fits with it, one plain line otherwise.
func (s *screen) stripRows() int {
	if s.height >= menuRows()+2+messageRows+2 {
		return messageRows
	}
	return 1
}

// menuRows is the menu at its fullest: every section, its group names and
// the blank line between groups.
func menuRows() int {
	groups := 0
	for _, it := range menuItems {
		if it.group != "" {
			groups++
		}
	}
	return len(menuItems) + groups + groups - 1
}

// messageStrip is the framed line under the panes. Empty space when
// there is nothing to say, a coloured box the moment there is.
func (s *screen) messageStrip() string {
	rows := s.stripRows()
	blank := strings.Repeat("\n", rows-1)
	text := s.status
	if text == "" || time.Since(s.statusAt) > fadeAfter+2*time.Second {
		return blank
	}
	var mark string
	var colour lipgloss.TerminalColor
	switch s.statusKind {
	case msgFail:
		mark, colour = "✗", lipgloss.Color("#EF4444")
	case msgWarn:
		mark, colour = "!", amber
	case msgOK:
		mark, colour = "✓", green
	default:
		mark, colour = "·", border
	}
	w := max(20, s.width-2)
	line := lipgloss.NewStyle().Foreground(colour).Render(mark) + " " +
		lipgloss.NewStyle().Foreground(fgText).Render(text)
	if rows < messageRows { // no room for a frame: one plain line
		return " " + ansi.Truncate(line, w-2, "…")
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(colour).
		Padding(0, 1).Width(w - 4).MaxHeight(messageRows).
		Render(ansi.Truncate(line, w-8, "…"))
	return " " + strings.ReplaceAll(box, "\n", "\n ")
}

// menuShape decides how much of the menu's furniture fits: the blank
// line between groups first, then the group names themselves. The rows
// are never dropped and the list never scrolls.
func (s *screen) menuShape() (air, heads bool) {
	groups := 0
	for _, it := range menuItems {
		if it.group != "" {
			groups++
		}
	}
	room := s.bodyHeight() - 2
	switch {
	case room >= len(menuItems)+groups+groups-1:
		return true, true
	case room >= len(menuItems)+groups:
		return false, true
	}
	return false, false
}

// menuWindow is every section, unless the terminal is too short for the
// bare list — then it is the part of it around the cursor, moving only
// when the cursor reaches an edge.
func (s *screen) menuWindow() (first, last int) {
	room := s.bodyHeight() - 2
	if room >= len(menuItems) {
		s.menuTop = 0
		return 0, len(menuItems) - 1
	}
	if s.cursor < s.menuTop {
		s.menuTop = s.cursor
	}
	if s.cursor >= s.menuTop+room {
		s.menuTop = s.cursor - room + 1
	}
	if s.menuTop > len(menuItems)-room {
		s.menuTop = len(menuItems) - room
	}
	return s.menuTop, s.menuTop + room - 1
}

// piece is one styled run of a row.
type piece struct {
	st   lipgloss.Style
	text string
}

// bandRow paints one row on the band, to the full width. Every piece
// carries the background itself: a background wrapped around text that
// is already styled stops at the first reset inside it, and only the
// cursor glyph ends up lit — which is exactly what the screen did before.
func bandRow(width int, parts ...piece) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.st.Background(bandBg).Render(p.text))
	}
	if pad := width - lipgloss.Width(b.String()); pad > 0 {
		b.WriteString(stBand.Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

func (s *screen) leftWidth() int {
	want := menuLabelW + menuHintW + menuChrome
	if s.width < 70 { // a small terminal: split it and cut the hints
		return max(18, s.width*45/100)
	}
	// otherwise the menu gets what it needs as long as the right pane
	// keeps 46 columns; the hints give way before the names do.
	return min(want, max(34, s.width-46))
}

func (s *screen) rightWidth() int { return max(24, s.width-s.leftWidth()-1) }

func (s *screen) bodyHeight() int { return max(12, s.height-2-s.stripRows()) }

// View draws the whole screen from the last known size: the header, the
// menu and the right pane side by side in one frame, and the strip.
func (s *screen) View() string {
	leftW := s.leftWidth()
	rightW := s.rightWidth()
	labelW := min(menuLabelW, leftW-menuChrome) // the name always wins the room
	hintW := leftW - menuChrome - labelW        // whatever is left over
	bodyH := s.bodyHeight()

	// header
	head := " " + brand("▲ WatchFor") + " " + stTitle.Render("agent") + stDim.Render("  "+Version+" · "+runtime.GOOS+"/"+runtime.GOARCH+" · "+runtime.Version())
	var right0 string
	if !s.sampled.IsZero() {
		right0 = stDim.Render("live " + s.sampled.Format("15:04:05") + " ")
	}
	gap := s.width - lipgloss.Width(head) - lipgloss.Width(right0)
	if gap < 1 {
		gap = 1
	}
	header := head + strings.Repeat(" ", gap) + right0

	// left pane: menu. It never scrolls: when the terminal is too short
	// for the full list the air goes first, then the group names, and
	// every section stays reachable with one keypress.
	var menu strings.Builder
	innerW := leftW - 4 // inside the border and padding
	air, heads := s.menuShape()
	first, last := s.menuWindow()
	for i, it := range menuItems {
		if i < first || i > last {
			continue
		}
		if it.group != "" && heads {
			if i > 0 && air {
				menu.WriteString("\n")
			}
			menu.WriteString(stGroup.Render(it.group) + "\n")
		}
		label := fmt.Sprintf("%-*s", labelW, ansi.Truncate(it.label, labelW, "…")) + gutter
		hint := ""
		if hintW >= 8 { // below that a hint is more ellipsis than words
			hint = ansi.Truncate(it.hint, hintW, "…")
		}
		if i == s.cursor {
			if s.inPane {
				// the keyboard is in the pane: the menu keeps its place
				// marked, but only one row anywhere carries the cursor
				menu.WriteString(stDim.Render("❯ ") + stRow.Render(label) + stDim.Render(hint) + "\n")
				continue
			}
			menu.WriteString(bandRow(innerW, piece{stCursor, "❯ "}, piece{stRowSel, label}, piece{stDim, hint}) + "\n")
			continue
		}
		menu.WriteString("  " + stRow.Render(label) + stDim.Render(hint) + "\n")
	}
	// both panes share one frame — the cursor band says where you are
	leftStyle, rightStyle := stPane, stPane
	left := leftStyle.Width(leftW - 2).Height(bodyH - 2).MaxHeight(bodyH).Render(strings.TrimRight(menu.String(), "\n"))

	// right pane: form, preview or details
	var right string
	switch {
	case s.leaving:
		right = stTitle.Render("Quit?") + "\n" + stRule.Render("────────────────") + "\n\n" +
			stDim.Render("Everything you changed is already in the file.") + "\n\n" +
			"  " + stKey.Render("Y") + " " + stRow.Render("leave") + "      " + stKey.Render("N") + " " + stRow.Render("stay")
	case s.form != nil:
		// The form can be taller than the pane (the process list alone is
		// twenty rows). Draw it into the pane's own viewport and keep the
		// field being edited in view, so nothing is edited off-screen.
		content := s.paneHeader(menuItems[s.cursor].key) + s.form.View()
		if s.pane == nil || s.pane.Width != rightW-4 || s.pane.Height != bodyH-2 {
			vp := viewport.New(rightW-4, bodyH-2)
			s.pane = &vp
		}
		s.pane.SetContent(content)
		s.pane.YOffset = focusOffset(content, s.pane.Height, s.pane.TotalLineCount())
		right = s.pane.View()
	case s.preview != nil:
		right = stTitle.Render(s.w.path) + "  " + stDim.Render("↑↓ scroll · esc back") + "\n" + s.preview.View()
	default:
		content := s.details(menuItems[s.cursor].key)
		// long panes (the batch, the log) scroll with PgUp/PgDn
		if s.pane == nil || s.pane.Width != rightW-4 || s.pane.Height != bodyH-2 {
			vp := viewport.New(rightW-4, bodyH-2)
			s.pane = &vp
		}
		s.pane.SetContent(fitLines(content, rightW-4))
		right = s.pane.View()
	}
	rightBox := rightStyle.Width(rightW - 2).Height(bodyH - 2).MaxHeight(bodyH).Render(right)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", rightBox)

	// footer: keys + status
	var keys string
	key := func(k, what string) string { return stKey.Render(k) + stDim.Render(" "+what+"  ") }
	switch {
	case s.leaving:
		keys = key("y", "leave") + key("n", "stay")
	case s.confirm != nil:
		keys = key("↑↓", "choose") + key("enter", "confirm") + key("esc", "cancel, nothing happens")
	case s.form != nil:
		keys = key("↑↓", "move") + key("space", "change")
		if _, ok := s.form.GetFocusedField().(*huh.MultiSelect[string]); ok {
			keys += key("/", "filter")
		}
		keys += key("tab", "next field") + key("enter", "confirm") + key("esc", "back, nothing changed")
	case s.preview != nil:
		keys = key("↑↓", "scroll") + key("esc", "back")
	case s.inPane:
		keys = key("↑↓", "choose") + key("enter", "run it") + key("esc", "back to the menu") + key("q", "quit")
	default:
		keys = key("↑↓", "move")
		switch menuItems[s.cursor].key {
		case "system", "live", "batch":
			keys += key("enter", "refresh now") + key("pgup/pgdn", "scroll")
		case "update", "check", "service", "repair":
			keys += key("enter", "what this can do") + key("pgup/pgdn", "scroll")
		case "file":
			keys += key("enter", "see the file")
		default:
			keys += key("enter", "change") + key("p", "see the file")
		}
		keys += key("q", "quit")
	}

	// the keys matter more than the link: the link goes first when the
	// terminal is narrow, and the keys are cut only if they still do not fit
	docs := stDim.Render("watchfor.io/docs/hosts ")
	gap = s.width - lipgloss.Width(keys) - lipgloss.Width(docs) - 1
	if gap < 1 {
		docs, gap = "", s.width-lipgloss.Width(keys)-1
	}
	if gap < 0 {
		keys = ansi.Truncate(keys, max(8, s.width-2), "…")
		gap = 0
	}
	footer := " " + keys + strings.Repeat(" ", gap) + docs
	return header + "\n" + body + "\n" + s.messageStrip() + "\n" + footer
}

// focusOffset is the scroll position that keeps the field being edited in
// view. huh draws the focused field's cursor in the accent colour and
// every other field's in dim, so the accent line is the one to follow;
// it is held a little below the top so its label and the line above show.
func focusOffset(content string, height, total int) int {
	if total <= height {
		return 0
	}
	// huh draws the focused field's cursor in the accent colour and, in an
	// input, as a reverse-video block. Either marks the active line.
	const accent = "38;2;79;142;247"
	lines := strings.Split(content, "\n")
	focus := -1
	for i, l := range lines {
		if strings.Contains(l, accent) || strings.Contains(l, "\x1b[7m") || strings.Contains(l, ";7m") {
			focus = i
		}
	}
	if focus < 0 {
		return 0
	}
	off := focus - height/3 // keep the field's title and some context above it
	if off < 0 {
		off = 0
	}
	if max := total - height; off > max {
		off = max
	}
	return off
}
