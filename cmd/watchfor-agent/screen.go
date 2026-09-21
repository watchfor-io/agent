//go:build linux

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/health"
	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/spool"
)

// The configure screen: a fixed two-pane layout on the alternate screen.
// Left, the sections with their current values and a cursor; right, what
// the highlighted section means and holds — or the form for it, once
// opened. Every confirmed change is written at once (the previous file is
// kept as .bak), so there is nothing to "save"; Esc always goes back.

type menuItem struct {
	key, label string
	group      string // a header drawn above the first item of each group
	hint       string // what the item is, in a few words
	live       bool   // the pane shows collector data and refreshes
}

var menuItems = []menuItem{
	{"system", "System", "Overview", "machine and agent", true},
	{"live", "Live", "", "cpu, memory, disks, network", true},
	{"batch", "The batch", "", "what it would send", true},
	{"server", "Server", "Settings", "address and token", false},
	{"host", "Host", "", "name and tags", false},
	{"interval", "Sending", "", "how often, unsent storage", false},
	{"disks", "Disks", "", "mounts to watch", true},
	{"network", "Network", "", "interfaces to watch", true},
	{"processes", "Processes", "", "busiest and watched", true},
	{"cloud", "Cloud lookup", "", "instance size from the cloud", false},
	{"auto", "Auto-update", "", "daily signed releases", false},
	{"log", "Log level", "", "how much it logs", false},
	{"file", "The file", "", "agent.yml as written", false},
	{"update", "Update", "Maintenance", "newer agent releases", false},
	{"check", "Health check", "", "files, permissions, service", false},
	{"service", "Service & log", "", "systemd and journal", false},
	{"repair", "Reinstall & reset", "", "fresh binary, default config", false},
}

func menuIndex(key string) int {
	for i, it := range menuItems {
		if it.key == key {
			return i
		}
	}
	return 0
}

type tickMsg time.Time

func tickIn(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

type screen struct {
	w       *wizard
	cursor  int
	form    *huh.Form
	apply   func()
	leaving bool // the "leave?" question is up
	fade    tea.Cmd
	// onLeave replaces tea.Quit when embedded; nil quits the program.
	onLeave tea.Cmd
	preview *viewport.Model
	pane    *viewport.Model // long overview panes scroll with PgUp/PgDn
	listPos map[huh.Field]int
	// the settings panes show the section's own form, unfocused: the
	// same layout and words as edit mode, values in place of a cursor
	viewForms  map[string]*huh.Form
	status     string
	statusKind msgKind
	statusAt   time.Time
	width      int
	height     int

	// the machine as the collectors see it, refreshed every 2 s
	mods       []modules.Module
	modErr     error
	latest     *metric.Batch
	prev       *metric.Batch
	sampled    time.Time
	spoolN     int
	spoolB     int64
	svcState   string
	svcEnabled string

	// maintenance
	upd       *updateMsg
	upBusy    bool
	upChecked time.Time
	inst      *installMsg
	svc       *serviceMsg
	checks    []health.Check
	checkedAt time.Time
	deepBusy  bool // the release comparison is downloading
	deepDone  bool // the last run included it
	checkBusy bool
	confirm   *confirmAction

	// inPane means the keyboard belongs to the pane on the right: ↑↓
	// move between its actions instead of between menu sections.
	inPane bool
	act    int

	// menuTop is the first section drawn when the terminal is too short
	// for the whole menu.
	menuTop int

	// sampling is true while a sample is being collected: the modules keep
	// state between calls and are not safe to run twice at once.
	sampling bool
}

func newScreen(w *wizard) *screen {
	s := &screen{w: w, width: 100, height: 30}
	s.reloadCollectors()
	return s
}

// reloadCollectors builds the modules from the file on disk (the running
// agent's view), or from the defaults when there is no file yet.
func (s *screen) reloadCollectors() {
	cfg, err := config.ParseFile(s.w.path)
	if err != nil {
		cfg, _ = config.ParseText("server:\n  url: https://ingest.watchfor.io\n  token: none\n")
	}
	s.mods, s.modErr = buildModules(cfg)
}

type sampleTick struct{}

// viewRefresh makes a form rebuild its view without changing anything.
type viewRefresh struct{}

// Init starts the screen's background work: a first sample, a second one
// soon after so rates exist on the first paint, and a look at the service.
func (s *screen) Init() tea.Cmd {
	// two quick samples so rates exist on the first paint, then every 2 s
	return tea.Batch(s.sampleNow(), tea.Tick(firstSampleAfter, func(time.Time) tea.Msg { return sampleTick{} }), s.serviceState())
}

// sampleNow collects once unless a collection is already under way: a
// slow machine must not end up with two samples racing over the modules'
// state.
func (s *screen) sampleNow() tea.Cmd {
	if s.sampling {
		return nil
	}
	s.sampling = true
	return s.sample()
}

func (s *screen) sample() tea.Cmd {
	mods := s.mods
	return func() tea.Msg {
		b := metric.NewBatch(time.Now())
		var firstErr error
		for _, m := range mods {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := m.Collect(ctx, b); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", m.Name(), err)
			}
			cancel()
		}
		return sampleMsg{batch: b, err: firstErr}
	}
}

func (s *screen) serviceState() tea.Cmd {
	return func() tea.Msg {
		if !haveSystemctl() {
			return svcStateMsg{}
		}
		active, _ := execCmdCombined(context.Background(), "systemctl", "is-active", "watchfor-agent")
		enabled, _ := execCmdCombined(context.Background(), "systemctl", "is-enabled", "watchfor-agent")
		return svcStateMsg{active: strings.TrimSpace(string(active)), enabled: strings.TrimSpace(string(enabled))}
	}
}

type svcStateMsg struct{ active, enabled string }

func (s *screen) refreshSpool() {
	dir := s.w.o.SpoolDir
	if dir == "" {
		return
	}
	s.spoolN, s.spoolB = spool.Stats(dir) // read-only: the daemon owns that directory
}

func (s *screen) svcStateStyled() string {
	switch s.svcState {
	case "active":
		return stOn.Render("running")
	case "inactive":
		return stWarn.Render("stopped")
	case "failed":
		return stBarBad.Render("failed")
	case "":
		return stDim.Render("no systemd")
	}
	return stDim.Render(s.svcState)
}

func onOffStyled(b bool) string {
	if b {
		return stOn.Render("on")
	}
	return stOff.Render("off")
}

// viewForm is the section's form rendered without focus — what the pane
// shows before Enter. Cached until something changes.
func (s *screen) viewForm(key string) *huh.Form {
	if f, ok := s.viewForms[key]; ok {
		return f
	}
	if s.viewForms == nil {
		s.viewForms = map[string]*huh.Form{}
	}
	s.w.paneW = s.rightWidth() - 4
	f := s.w.section(key).form.WithWidth(s.w.paneW)
	// A form draws nothing until it is initialised; once it is, the
	// focused field is blurred so the pane shows the same layout at rest.
	f.Init()
	if fld := f.GetFocusedField(); fld != nil {
		fld.Blur()
	}
	f.Update(viewRefresh{})
	s.viewForms[key] = f
	return f
}

func (s *screen) forgetViewForms() { s.viewForms = nil }

func isSetting(key string) bool {
	switch key {
	case "server", "host", "interval", "disks", "network", "processes", "cloud", "auto", "log":
		return true
	}
	return false
}

// details is the right pane for the highlighted section: what it is in
// one plain sentence, then what is set now or what can be done.
func (s *screen) details(key string) string {
	// a question waits for its answer and nothing else is shown
	if s.confirm != nil {
		return s.paneHeader(key) + s.confirmView()
	}
	// Settings show their own form, unfocused: one layout and one set of
	// words in both modes. Everything else has a pane of its own.
	if isSetting(key) {
		return s.paneHeader(key) + s.viewForm(key).View() + "\n" + stDim.Render("Enter to change")
	}
	switch key {
	case "system":
		return s.systemPane()
	case "live":
		return s.livePane()
	case "batch":
		return s.batchPane()
	case "update":
		return s.updatePane()
	case "service":
		return s.servicePane()
	case "check":
		return s.checkPane()
	case "repair":
		return s.repairPane()
	case "file":
		return s.paneHeader(key) +
			fmt.Sprintf("  %s %s\n", stDim.Render(fmt.Sprintf("%-12s", "path")), s.w.path) +
			fmt.Sprintf("  %s %s\n", stDim.Render(fmt.Sprintf("%-12s", "backup")), s.w.path+".bak"+stDim.Render("  the previous version")) +
			"\n" + stDim.Render("Enter to see it")
	}
	return ""
}

// Update is the message loop: sizes, ticks and results of background
// work land in the model directly, keys go through key, and whatever is
// left belongs to the open form.
func (s *screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		if s.form != nil {
			s.form = s.form.WithWidth(s.rightWidth() - 4)
		}
		s.forgetViewForms()
		if s.preview != nil {
			s.preview.Width, s.preview.Height = s.rightWidth()-4, s.bodyHeight()-2
		}
		return s, nil
	case tickMsg:
		return s, nil // a re-render, so a status line can fade
	case sampleTick:
		if s.form != nil {
			// a form is open: keep the clock going, skip the sample
			return s, tea.Tick(sampleEvery, func(time.Time) tea.Msg { return sampleTick{} })
		}
		return s, tea.Batch(s.sampleNow(), s.serviceState(), tea.Tick(sampleEvery, func(time.Time) tea.Msg { return sampleTick{} }))
	case sampleMsg:
		s.sampling = false
		if msg.batch != nil {
			s.prev, s.latest = s.latest, msg.batch
			s.sampled = time.Now()
		}
		s.modErr = msg.err // sticky only while it keeps happening
		s.refreshSpool()
		return s, nil
	case svcStateMsg:
		s.svcState, s.svcEnabled = msg.active, msg.enabled
		return s, nil
	case updateMsg:
		s.upBusy = false
		s.upd = &msg
		s.upChecked = time.Now()
		return s, nil
	case checksMsg:
		s.checkBusy, s.deepBusy = false, false
		s.checks = msg.checks
		s.checkedAt = time.Now()
		s.deepDone = msg.deep
		if msg.fixed != "" {
			s.say(msg.kind, msg.fixed)
			return s, tea.Batch(s.serviceState(), s.takeFade())
		}
		return s, s.serviceState()
	case installMsg:
		s.upBusy = false
		s.inst = &msg
		return s, s.serviceState()
	case serviceMsg:
		s.svc = &msg
		return s, nil
	case unitDoneMsg:
		if msg.err != nil {
			s.say(msgFail, firstOr(msg.out, msg.err.Error()))
		} else {
			s.say(msgOK, "systemctl "+strings.Join(msg.args, " "))
		}
		return s, tea.Batch(s.serviceState(), s.serviceInfo(), s.takeFade())
	case tea.KeyMsg:
		return s.key(msg)
	}
	if s.form != nil {
		return s, s.updateForm(msg)
	}
	return s, nil
}
