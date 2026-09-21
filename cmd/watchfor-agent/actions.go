//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/watchfor-io/agent/internal/detect"
	"github.com/watchfor-io/agent/internal/health"
	"github.com/watchfor-io/agent/internal/update"
)

// An action is something a pane can do. There are no letter shortcuts
// anywhere on this screen: an action is a row you move onto with ↑↓ and
// choose with Enter, exactly like the menu on the left. Anything that
// replaces a file, stops the agent or reaches the network asks first.
type action struct {
	label string
	why   string
	// ready says whether the action can run now. The reason takes the
	// place of the explanation on the row when it cannot, and its kind
	// decides how loudly the strip says it when someone chooses the row
	// anyway: something in the way is a failure, something already done
	// or under way is only worth a note.
	ready func() (ok bool, why string, kind msgKind)
	// ask is the question to put before running; nil runs at once.
	ask func() (title, body, yes string)
	run func() tea.Cmd
}

// needRoot is the usual reason an action cannot run, with the command to
// run by hand instead.
func needRoot(byHand string) (bool, string, msgKind) {
	if os.Geteuid() == 0 {
		return true, "", msgInfo
	}
	return false, "needs root — run: " + byHand, msgFail
}

func (s *screen) actions(key string) []action {
	switch key {
	case "update":
		return s.updateActions()
	case "service":
		return s.serviceActions()
	case "check":
		return s.checkActions()
	case "repair":
		return s.repairActions()
	}
	return nil
}

func (s *screen) busy() (bool, string, msgKind) {
	if s.upBusy || s.checkBusy {
		return false, "working…", msgWarn
	}
	return true, "", msgInfo
}

func (s *screen) updateActions() []action {
	return []action{{
		label: "Check for a newer release",
		why:   "asks watchfor.io which release is current",
		ready: s.busy,
		run: func() tea.Cmd {
			s.upBusy = true
			s.upd, s.inst = nil, nil
			return s.checkUpdate()
		},
	}, {
		label: "Install it",
		why:   "verifies the signature, swaps the binary in, restarts the service",
		ready: func() (bool, string, msgKind) {
			if ok, why, kind := s.busy(); !ok {
				return ok, why, kind
			}
			switch {
			case s.upd == nil || s.upd.st.Latest == "":
				return false, "check first — nothing is known yet", msgWarn
			case !s.upd.st.Available:
				return false, "this is already the newest release", msgWarn
			}
			return needRoot("sudo watchfor-agent upgrade")
		},
		ask: func() (string, string, string) {
			return "Install watchfor-agent " + s.upd.st.Latest + "?",
				"The release is downloaded, its signature and checksum are verified, and the binary on disk is replaced. The service restarts; agent.yml and the token are untouched.",
				"install it"
		},
		run: func() tea.Cmd { s.upBusy = true; return s.install() },
	}}
}

func (s *screen) serviceActions() []action {
	if !haveSystemctl() {
		return nil
	}
	running := s.svcState == "active"
	enabled := s.svcEnabled == "enabled"
	rootFor := func(args ...string) func() (bool, string, msgKind) {
		return func() (bool, string, msgKind) { return needRoot("sudo systemctl " + strings.Join(args, " ")) }
	}
	unless := func(cond bool, why string, next func() (bool, string, msgKind)) func() (bool, string, msgKind) {
		return func() (bool, string, msgKind) {
			if cond {
				return false, why, msgWarn // nothing in the way, nothing to do
			}
			return next()
		}
	}
	return []action{{
		label: "Start the service",
		why:   "the agent begins sending again",
		ready: unless(running, "it is already running", rootFor("start", "watchfor-agent")),
		run:   func() tea.Cmd { return s.runUnit("start", "watchfor-agent") },
	}, {
		label: "Restart the service",
		why:   "picks up a changed agent.yml at once",
		ready: rootFor("restart", "watchfor-agent"),
		run:   func() tea.Cmd { return s.runUnit("restart", "watchfor-agent") },
	}, {
		label: "Stop the service",
		why:   "this host stops reporting until it is started again",
		ready: unless(!running, "it is not running", rootFor("stop", "watchfor-agent")),
		ask: func() (string, string, string) {
			return "Stop watchfor-agent?",
				"The agent stops sending. WatchFor sees the host go quiet and, after the grace period, treats it as down — if you are working on this machine, silence it in the dashboard first.",
				"stop it"
		},
		run: func() tea.Cmd { return s.runUnit("stop", "watchfor-agent") },
	}, {
		label: "Enable at boot",
		why:   "the agent comes back after a reboot",
		ready: unless(enabled, "it already starts at boot", rootFor("enable", "--now", "watchfor-agent")),
		run:   func() tea.Cmd { return s.runUnit("enable", "--now", "watchfor-agent") },
	}, {
		label: "Disable at boot",
		why:   "it keeps running now, but not after a reboot",
		ready: unless(!enabled, "it does not start at boot", rootFor("disable", "watchfor-agent")),
		ask: func() (string, string, string) {
			return "Stop watchfor-agent starting at boot?",
				"The service keeps running until this machine reboots, and does not come back after it. The host then goes quiet without anyone touching it.",
				"disable it"
		},
		run: func() tea.Cmd { return s.runUnit("disable", "watchfor-agent") },
	}, {
		label: "Refresh the log",
		why:   "reads the state and the last lines again",
		run:   func() tea.Cmd { s.svc = nil; return tea.Batch(s.serviceState(), s.serviceInfo()) },
	}}
}

// runUnit runs one systemctl command and reads the service back.
func (s *screen) runUnit(args ...string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // `stop` may wait for TimeoutStopSec
		defer cancel()
		out, err := execCmdCombined(ctx, "systemctl", args...)
		return unitDoneMsg{args: args, out: strings.TrimSpace(string(out)), err: err}
	}
}

// unitDoneMsg is systemctl's answer, back on the screen's own goroutine.
type unitDoneMsg struct {
	args []string
	out  string
	err  error
}

func (s *screen) fixable() []health.Check {
	var out []health.Check
	for _, c := range s.checks {
		if c.Fix != nil {
			out = append(out, c)
		}
	}
	return out
}

// repairWhy says how much there is to repair, so the pane needs no line
// of its own for counting.
func (s *screen) repairWhy() string {
	n := len(s.fixable())
	if n == 0 {
		return "nothing here can be fixed automatically"
	}
	bad := 0
	for _, c := range s.checks {
		if c.State == health.Fail || c.State == health.Warn {
			bad++
		}
	}
	return fmt.Sprintf("%d of the %d problems above, in one go", n, bad)
}

func (s *screen) checkActions() []action {
	return []action{{
		label: "Run the checks again",
		why:   "reads the files, the service and the way out to WatchFor",
		ready: s.busy,
		run: func() tea.Cmd {
			s.checkBusy = true
			return s.runChecks(nil, "", false)
		},
	}, {
		label: "Repair what can be repaired",
		why:   s.repairWhy(),
		ready: func() (bool, string, msgKind) {
			if ok, why, kind := s.busy(); !ok {
				return ok, why, kind
			}
			if len(s.fixable()) == 0 {
				return false, "nothing here can be fixed automatically", msgWarn
			}
			return true, "", msgInfo
		},
		ask: func() (string, string, string) {
			var what []string
			for _, c := range s.fixable() {
				what = append(what, "· "+c.Name+": "+c.FixMsg)
			}
			return "Repair the installation?",
				"This changes the machine:\n" + strings.Join(what, "\n"),
				"repair it"
		},
		run: func() tea.Cmd {
			fixes := s.fixable()
			s.checkBusy = true
			names := make([]string, 0, len(fixes))
			for _, c := range fixes {
				names = append(names, c.Name)
			}
			all := func(ctx context.Context) error {
				for _, c := range fixes {
					if err := c.Fix(ctx); err != nil {
						return fmt.Errorf("%s: %w", c.Name, err)
					}
				}
				return nil
			}
			return s.runChecks(all, strings.Join(names, ", "), s.deepDone)
		},
	}, {
		label: "Compare with the signed release",
		why:   "downloads the release WatchFor signed and hashes the binary on disk",
		ready: s.busy,
		ask: func() (string, string, string) {
			return "Download the signed release?",
				"A few megabytes over HTTPS from the release host. Nothing is installed: the archive is verified against the key built into this binary and its watchfor-agent is compared with the one on disk.",
				"download and compare"
		},
		run: func() tea.Cmd {
			s.checkBusy, s.deepBusy = true, true
			return s.runChecks(nil, "", true)
		},
	}}
}

func (s *screen) repairActions() []action {
	return []action{{
		label: "Reinstall the binary",
		why:   "downloads " + Version + " again, verifies it, swaps it in",
		ready: func() (bool, string, msgKind) {
			if ok, why, kind := s.busy(); !ok {
				return ok, why, kind
			}
			if _, err := update.Parse(Version); err != nil {
				return false, "a development build (" + Version + ") has no release to reinstall", msgWarn
			}
			return needRoot("sudo watchfor-agent upgrade -version " + Version)
		},
		ask: func() (string, string, string) {
			return "Reinstall watchfor-agent " + Version + "?",
				"The release is downloaded again, its signature and checksum are verified, and the binary on disk is replaced. The service restarts; agent.yml and the token are untouched.",
				"reinstall it"
		},
		run: func() tea.Cmd { s.upBusy = true; return s.reinstall() },
	}, {
		label: "Reset agent.yml to defaults",
		why:   "keeps the server address and token file, nothing else",
		ready: s.busy,
		ask: func() (string, string, string) {
			return "Write a fresh agent.yml?",
				"Everything in " + s.w.path + " goes back to the defaults, with this machine's mounts and interfaces listed again. The server address and token file are kept, and the file you have now stays as agent.yml.bak.",
				"reset the config"
		},
		run: func() tea.Cmd {
			o := detect.DefaultOptions()
			o.Server, o.TokenFile, o.WrittenBy = s.w.o.Server, s.w.o.TokenFile, "watchfor-agent configure"
			s.w.o = o
			_ = s.saveNow()
			s.reloadCollectors()
			s.forgetViewForms()
			s.say(msgOK, "agent.yml written with the defaults")
			return s.takeFade()
		},
	}}
}

// choose runs what the cursor is on: the reason when it cannot run, the
// question when it is worth asking, otherwise the thing itself.
func (s *screen) choose(a action) tea.Cmd {
	if a.ready != nil {
		if ok, reason, kind := a.ready(); !ok {
			s.say(kind, reason)
			return s.takeFade()
		}
	}
	if a.ask != nil {
		title, body, yes := a.ask()
		s.confirm = &confirmAction{title: title, body: body, yes: yes, choice: 1, run: a.run}
		return nil
	}
	return a.run()
}

// actionRow draws one choosable line the way the menu draws its own.
// selected is the row this pane's cursor is on — it is marked whether or
// not the pane has the keyboard, so both sides always show where their
// cursor stands; focused adds the band that says the keys land here.
func (s *screen) actionRow(label, why string, selected, focused bool, nameW int) string {
	w := max(24, s.rightWidth()-4)
	nameW = min(nameW, max(12, w-16)) // a very narrow pane keeps room for the reason
	name := fmt.Sprintf("%-*s", nameW, ansi.Truncate(label, nameW, "…"))
	switch {
	case selected && focused:
		return bandRow(w, piece{stCursor, "❯ "}, piece{stRowSel, name}, piece{stBand, gutter}, piece{stDim, why})
	case selected:
		return stDim.Render("❯ ") + stRow.Render(name) + gutter + stDim.Render(why)
	}
	return "  " + stRow.Render(name) + gutter + stDim.Render(why)
}

// actionList is the block of rows at the foot of a maintenance pane.
func (s *screen) actionList(key string) string {
	acts := s.actions(key)
	if len(acts) == 0 {
		return ""
	}
	nameW := 0
	for _, a := range acts {
		nameW = max(nameW, lipgloss.Width(a.label))
	}
	var b strings.Builder
	b.WriteString("\n")
	for i, a := range acts {
		ok, reason := true, ""
		if a.ready != nil {
			ok, reason, _ = a.ready()
		}
		why := a.why
		if !ok && reason != "" {
			why = reason
		}
		b.WriteString(s.actionRow(a.label, why, i == s.act, s.inPane, nameW) + "\n")
	}
	return b.String()
}

// confirmView is the question itself: two rows, the safe one first under
// the cursor, chosen the same way as everything else.
func (s *screen) confirmView() string {
	c := s.confirm
	w := max(24, s.rightWidth()-4)
	var b strings.Builder
	b.WriteString(stTitle.Render(c.title) + "\n\n" + stDim.Width(w).Render(c.body) + "\n\n")
	yes, no := "Yes, "+c.yes, "No, leave it alone"
	nameW := max(lipgloss.Width(yes), lipgloss.Width(no))
	b.WriteString(s.actionRow(yes, "", c.choice == 0, true, nameW) + "\n")
	b.WriteString(s.actionRow(no, "", c.choice == 1, true, nameW) + "\n")
	return b.String()
}
