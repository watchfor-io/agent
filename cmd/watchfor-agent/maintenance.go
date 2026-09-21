//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/health"
	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/update"
)

type updateMsg struct {
	st  update.Status
	err error
}

type installMsg struct {
	res update.Result
	out string
	err error
}

type serviceMsg struct {
	journal string
}

type sampleMsg struct {
	batch *metric.Batch
	err   error
}

type checksMsg struct {
	checks []health.Check
	fixed  string
	kind   msgKind
	deep   bool
}

// confirmAction is a dangerous thing waiting for a yes: reinstalling the
// binary, or writing a fresh config over the current one.
type confirmAction struct {
	title, body, yes string
	choice           int // 0 yes, 1 no — the cursor starts on no
	run              func() tea.Cmd
}

func (s *screen) checkUpdate() tea.Cmd {
	stateDir := s.w.o.SpoolDir
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		st, err := update.Check(ctx, update.Options{Current: Version, StateDir: stateDir})
		return updateMsg{st: st, err: err}
	}
}

func (s *screen) install() tea.Cmd {
	stateDir := s.w.o.SpoolDir
	return func() tea.Msg {
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		res, err := update.Run(ctx, update.Options{Current: Version, StateDir: stateDir, Restart: true, Out: &out})
		return installMsg{res: res, out: out.String(), err: err}
	}
}

// runChecks looks the installation over; fix, when set, is applied first.
// deep also compares the binary on disk with the signed release, which
// needs the network and a minute.
func (s *screen) runChecks(fix func(context.Context) error, fixName string, deep bool) tea.Cmd {
	path, cfg := s.w.path, s.cfgOnDisk()
	timeout := 30 * time.Second
	if deep {
		timeout = 3 * time.Minute
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		msg := checksMsg{}
		if fix != nil {
			if err := fix(ctx); err != nil {
				msg.fixed, msg.kind = err.Error(), msgFail
			} else {
				msg.fixed, msg.kind = "repaired "+fixName, msgOK
			}
		}
		cfgv, cfgErr := config.ParseFile(path)
		if cfgv == nil {
			cfgv = cfg
		}
		o := healthProbe
		o.ConfigPath, o.Config, o.ConfigErr, o.Version = path, cfgv, cfgErr, Version
		o.Root, o.Exec, o.Deep = os.Geteuid() == 0, health.Exec(execCmdCombined), deep
		msg.checks = health.Run(ctx, o)
		msg.deep = deep
		return msg
	}
}

func (s *screen) cfgOnDisk() *config.Config {
	cfg, err := config.ParseFile(s.w.path)
	if err != nil {
		return nil
	}
	return cfg
}

// reinstall fetches the running version again and swaps the binary in.
func (s *screen) reinstall() tea.Cmd {
	stateDir := s.w.o.SpoolDir
	return func() tea.Msg {
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		res, err := update.Run(ctx, update.Options{
			Current: Version, Target: Version, Force: true, AllowDowngrade: true,
			StateDir: stateDir, Restart: true, Out: &out,
		})
		return installMsg{res: res, out: out.String(), err: err}
	}
}

func (s *screen) serviceInfo() tea.Cmd {
	return func() tea.Msg {
		journal := "journal not readable — try: sudo journalctl -u watchfor-agent -n 50"
		if out, err := execCmdCombined(context.Background(), "journalctl", "-u", "watchfor-agent", "-n", "40", "--no-pager", "-o", "short-iso"); err == nil && strings.TrimSpace(string(out)) != "" {
			journal = strings.TrimSpace(string(out))
		}
		return serviceMsg{journal: journal}
	}
}

// say puts one line in the strip at the foot of the screen. The strip is
// always there — an error must not push the rest of the screen around —
// and it clears itself after a while.
func (s *screen) say(kind msgKind, text string) {
	s.status, s.statusKind, s.statusAt = text, kind, time.Now()
	s.fade = tickIn(fadeAfter)
}

func (s *screen) saveNow() error {
	if _, err := s.w.save(); err != nil {
		s.say(msgFail, err.Error())
		return err
	}
	if n := noteSuffix(s.w); n != "" {
		s.say(msgWarn, n)
	}
	return nil
}

func (s *screen) takeFade() tea.Cmd {
	c := s.fade
	s.fade = nil
	return c
}

func noteSuffix(w *wizard) string {
	if w.note == "" {
		return ""
	}
	n := w.note
	w.note = ""
	return n
}

// healthProbe is what the health check asks of the machine; the zero
// value is the real machine, tests put stand-ins in.
var healthProbe health.Options

// sampleEvery is how often the live panes are refreshed; the first sample
// comes sooner so the screen does not open empty.
var (
	sampleEvery      = 2 * time.Second
	firstSampleAfter = 1200 * time.Millisecond
)

// fadeAfter is how long a message stays in the strip before it clears.
var fadeAfter = 4 * time.Second
