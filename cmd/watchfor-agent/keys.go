//go:build linux

package main

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// fieldAt is where the cursor is among the section's fields, -1 when the
// order is not known.
func (s *screen) fieldAt() int {
	f := s.form.GetFocusedField()
	for i, have := range s.w.fields {
		if have == f {
			return i
		}
	}
	return -1
}

// stepField moves to the next or previous field and comes round to the
// first one after the last: a section is a ring, not a dead end. It
// never steps past the ends of the form itself, so tabbing can never
// finish the form by accident.
func (s *screen) stepField(delta int) tea.Cmd {
	n := len(s.w.fields)
	at := s.fieldAt()
	if n < 2 || at < 0 {
		return nil
	}
	want := ((at+delta)%n + n) % n
	steps := want - at
	var cmds []tea.Cmd
	for ; steps > 0; steps-- {
		cmds = append(cmds, s.form.NextField())
	}
	for ; steps < 0; steps++ {
		cmds = append(cmds, s.form.PrevField())
	}
	return tea.Batch(cmds...)
}

// atListEnd says whether a move would take the cursor past the end of
// the list it is in — or that the field is not a list at all, where Up
// and Down have nothing of their own to do.
func (s *screen) atListEnd(up bool) bool {
	f := s.form.GetFocusedField()
	n := s.w.optionCount[f]
	if n == 0 {
		return true
	}
	pos, seen := s.listPos[f]
	if !seen {
		pos = s.w.startPos[f]
	}
	if up {
		return pos <= 0
	}
	return pos >= n-1
}

// clampList tracks where the cursor stands in each list field, so Up at
// the top and Down at the bottom can be swallowed instead of wrapping.
func (s *screen) clampList(msg tea.KeyMsg) bool {
	f := s.form.GetFocusedField()
	n := s.w.optionCount[f]
	if n == 0 {
		return false
	}
	pos, seen := s.listPos[f]
	if !seen {
		pos = s.w.startPos[f]
		if s.listPos == nil {
			s.listPos = map[huh.Field]int{}
		}
		s.listPos[f] = pos
	}
	switch msg.String() {
	case "up", "k", "ctrl+k", "ctrl+p", "left", "h":
		if pos <= 0 {
			return true
		}
		s.listPos[f] = pos - 1
	case "down", "j", "ctrl+j", "ctrl+n", "right", "l":
		if pos >= n-1 {
			return true
		}
		s.listPos[f] = pos + 1
	case "home", "g":
		s.listPos[f] = 0
	case "end", "G":
		s.listPos[f] = n - 1
	}
	return false
}

func (s *screen) open(key string) tea.Cmd {
	switch key {
	case "system", "live", "batch":
		return s.sampleNow() // Enter refreshes now
	case "update":
		if !s.upBusy {
			s.upBusy = true
			s.upd, s.inst = nil, nil
			return s.checkUpdate()
		}
		return nil
	case "check":
		if !s.checkBusy {
			s.checkBusy = true
			return s.runChecks(nil, "", false)
		}
		return nil
	case "repair":
		return nil
	case "service":
		s.svc = nil
		return tea.Batch(s.serviceState(), s.serviceInfo())
	}
	if key == "file" {
		vp := viewport.New(s.rightWidth()-4, s.bodyHeight()-2)
		vp.SetContent(maskTokens(s.w.sum.YAML(s.w.o)))
		s.preview = &vp
		return nil
	}
	s.w.listHeight = max(4, s.bodyHeight()-12)
	s.w.optionCount, s.w.startPos = map[huh.Field]int{}, map[huh.Field]int{}
	s.w.paneW = s.rightWidth() - 4
	s.listPos = nil
	sec := s.w.section(key)
	s.form = sec.form.WithWidth(s.w.paneW)
	s.apply = sec.apply
	return s.form.Init()
}

// key routes a keypress to whatever holds the keyboard: a question, the
// offer to quit, the file preview, an open form, or the menu itself.
func (s *screen) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case s.confirm != nil:
		return s.keyConfirm(msg)
	case s.leaving:
		return s.keyLeaving(msg)
	case s.preview != nil:
		return s.keyPreview(msg)
	case s.form != nil:
		return s.keyForm(msg)
	}
	return s.keyMenu(msg)
}

// keyConfirm answers a question: the cursor picks, Enter confirms, and
// only a yes runs anything.
func (s *screen) keyConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		s.confirm.choice = 0
	case "down", "j":
		s.confirm.choice = 1
	case "tab":
		s.confirm.choice = 1 - s.confirm.choice
	case "enter", "right", "l":
		yes := s.confirm.choice == 0
		run := s.confirm.run
		s.confirm = nil
		if yes {
			return s, run()
		}
	case "y", "Y":
		run := s.confirm.run
		s.confirm = nil
		return s, run()
	case "esc", "n", "N", "q", "Q", "left", "h":
		s.confirm = nil
	}
	return s, nil
}

// keyLeaving is the offer to quit: y or q take it, anything else stays.
func (s *screen) keyLeaving(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter", "q", "Q":
		return s, s.quit()
	default:
		s.leaving = false
	}
	return s, nil
}

// keyPreview scrolls the file preview until a key closes it.
func (s *screen) keyPreview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "Q", "enter", "p", "P":
		s.preview = nil
		return s, nil
	}
	vp, cmd := s.preview.Update(msg)
	s.preview = &vp
	return s, cmd
}

// keyForm edits the open section. One rule everywhere: Space changes,
// Enter confirms, Tab and the arrows walk the fields in a ring.
func (s *screen) keyForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return s, s.quit()
	}
	// One rule everywhere: Space changes, Enter confirms. huh's
	// single-choice list ignores Space, so here it steps to the
	// next option — a two-way switch flips, a level list cycles.
	if msg.String() == " " {
		if sel, ok := s.form.GetFocusedField().(*huh.Select[string]); ok {
			return s, stepSelect(s, sel)
		}
	}
	// Tab walks the section's fields, round and round.
	switch msg.String() {
	case "tab":
		return s, s.stepField(1)
	case "shift+tab":
		return s, s.stepField(-1)
	}
	// Up and Down walk the list they are in; at its end they
	// carry on to the next field rather than stopping dead or
	// wrapping the list back on itself.
	switch msg.String() {
	case "up", "k":
		if s.atListEnd(true) {
			return s, s.stepField(-1)
		}
	case "down", "j":
		if s.atListEnd(false) {
			return s, s.stepField(1)
		}
	}
	if dropped := s.clampList(msg); dropped {
		return s, nil
	}
	return s, s.updateForm(msg)
}

// keyMenu moves between sections and into a pane's actions; landing on
// a maintenance section fetches what it shows.
func (s *screen) keyMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return s, s.quit()
	case "esc":
		if s.inPane {
			s.inPane = false
			return s, nil
		}
		return s.offerToLeave()
	case "q", "Q": // Caps Lock must not lock anyone in
		// one keypress too many should not throw someone out
		return s.offerToLeave()
	case "up", "k":
		if s.inPane {
			if s.act > 0 {
				s.act--
			}
			return s, nil
		}
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.inPane {
			if s.act < len(s.actions(menuItems[s.cursor].key))-1 {
				s.act++
			}
			return s, nil
		}
		if s.cursor < len(menuItems)-1 {
			s.cursor++
		}
	case "home", "g":
		if s.inPane {
			s.act = 0
			return s, nil
		}
		s.cursor = 0
	case "end", "G":
		if s.inPane {
			s.act = max(0, len(s.actions(menuItems[s.cursor].key))-1)
			return s, nil
		}
		s.cursor = len(menuItems) - 1
	case "left", "h":
		if s.inPane {
			s.inPane = false
		}
		return s, nil
	case "enter", "right", "l":
		key := menuItems[s.cursor].key
		if acts := s.actions(key); len(acts) > 0 {
			if !s.inPane {
				s.inPane, s.act = true, 0
				return s, nil
			}
			return s, s.choose(acts[s.act])
		}
		return s, s.open(key)
	case "p", "P":
		s.cursor = menuIndex("file")
		s.inPane = false
		return s, s.open("file")
	case "pgdown", "pgup":
		if s.pane != nil {
			vp, cmd := s.pane.Update(msg)
			s.pane = &vp
			return s, cmd
		}
	}
	s.inPane = false // any move leaves the pane's actions
	// landing on a maintenance item fetches what it shows
	switch menuItems[s.cursor].key {
	case "service":
		if s.svc == nil {
			return s, tea.Batch(s.serviceState(), s.serviceInfo())
		}
	case "update":
		if s.upd == nil && s.inst == nil && !s.upBusy {
			s.upBusy = true
			return s, s.checkUpdate()
		}
	case "check":
		if s.checks == nil && !s.checkBusy {
			s.checkBusy = true
			return s, s.runChecks(nil, "", false)
		}
	}
	return s, nil
}

// updateForm feeds a message to the open form and reacts the moment it
// completes or is abandoned. huh finishes a form on its own follow-up
// message (nextGroupMsg), not on the Enter key itself, so this has to run
// for every message — otherwise the form is done and blank until the next
// keypress. SubmitCmd/CancelCmd are nil (set in newForm), so completing a
// form never quits the program.
func (s *screen) updateForm(msg tea.Msg) tea.Cmd {
	f, cmd := s.form.Update(msg)
	s.form = f.(*huh.Form)
	switch s.form.State {
	case huh.StateCompleted:
		apply := s.apply
		s.form, s.apply = nil, nil
		before := s.w.o
		apply()
		if err := s.saveNow(); err != nil {
			s.w.o = before // the agent refused it; the screen keeps what is on disk
		}
		s.reloadCollectors()
		s.forgetViewForms()
		return tea.Batch(cmd, s.takeFade(), s.sampleNow())
	case huh.StateAborted:
		s.form, s.apply = nil, nil
		return nil
	}
	return cmd
}

// stepSelect moves a single-choice list to the next option, wrapping
// from the last back to the first: Down, and Home when Down changed
// nothing.
func stepSelect[T comparable](s *screen, sel *huh.Select[T]) tea.Cmd {
	n := s.w.optionCount[sel]
	pos := s.w.startPos[sel]
	if p, ok := s.listPos[sel]; ok {
		pos = p
	}
	if s.listPos == nil {
		s.listPos = map[huh.Field]int{}
	}
	if n > 0 && pos >= n-1 {
		s.listPos[sel] = 0
		return s.updateForm(tea.KeyMsg{Type: tea.KeyHome})
	}
	s.listPos[sel] = pos + 1
	return s.updateForm(tea.KeyMsg{Type: tea.KeyDown})
}

func (s *screen) quit() tea.Cmd {
	if s.onLeave != nil {
		return s.onLeave
	}
	return tea.Quit
}

// offerToLeave asks before quitting — unless an install is half done, in
// which case leaving now would abandon a temp binary next to the real one.
func (s *screen) offerToLeave() (tea.Model, tea.Cmd) {
	if s.upBusy || s.deepBusy {
		s.say(msgWarn, "an install or a download is running — let it finish first")
		return s, s.takeFade()
	}
	s.leaving = true
	return s, nil
}
