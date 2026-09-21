//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"
	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/detect"
	"github.com/watchfor-io/agent/internal/health"
	"github.com/watchfor-io/agent/internal/hostinfo"
	"github.com/watchfor-io/agent/internal/modules"
	"github.com/watchfor-io/agent/internal/modules/disk"
	"github.com/watchfor-io/agent/internal/modules/network"
	"github.com/watchfor-io/agent/internal/modules/processes"
	"github.com/watchfor-io/agent/internal/modules/system"
	"github.com/watchfor-io/agent/internal/update"
)

// runConfigure is `watchfor-agent configure`: arrow-key forms over
// agent.yml. It shows what the machine has (mounts, disks, interfaces) as
// lists to tick, then writes the same commented file the installer does
// (the previous one is kept as agent.yml.bak). Works over any ssh session;
// without a terminal it falls back to numbered prompts. Root to write
// /etc/watchfor-agent.
func runConfigure(args []string) int {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if !isTerminal(os.Stdin) {
		fmt.Fprintln(os.Stderr, "configure needs a terminal; from a script use: watchfor-agent config set <key> <value>")
		return exitUsage
	}
	w := newWizard(*configPath)
	if w.tty {
		sc := newScreen(w)
		sc.cursor = menuIndex("server")
		if _, err := tea.NewProgram(sc, tea.WithAltScreen()).Run(); err != nil {
			fmt.Fprintln(os.Stderr, "configure:", err)
			return exitError
		}
		// Back on the normal screen: one line on what happens now.
		if w.wrote {
			fmt.Println(w.afterSave())
		}
		return exitOK
	}
	return w.runAccessible()
}

type wizard struct {
	path         string
	sum          detect.Summary
	o            detect.Options
	orig         detect.Options
	existed      bool
	autoBefore   bool
	tty          bool
	needsRestart bool
	wrote        bool
	note         string
	listHeight   int         // rows a tick list may use before it scrolls; the screen sets it
	paneW        int         // the pane a form is drawn in; the screen sets it
	fields       []huh.Field // the section's fields in the order they are drawn
	// per list field: how many options it has and where its cursor starts,
	// so the screen can stop Up/Down at the ends instead of wrapping
	optionCount map[huh.Field]int
	startPos    map[huh.Field]int
}

// newWizard reads agent.yml and looks the machine over. newWizardWith is
// the same with a summary already in hand — what tests use, so that no
// test asks the kernel, the cloud or the resolver anything.
func newWizard(path string) *wizard {
	w, cloud := prepareWizard(path)
	return w.with(detect.Collect(hostinfo.Options{Target: w.o.Server, CloudMetadata: cloud}))
}

func newWizardWith(path string, sum detect.Summary) *wizard {
	w, _ := prepareWizard(path)
	return w.with(sum)
}

func prepareWizard(path string) (w *wizard, cloud bool) {
	w = &wizard{path: path, listHeight: 12}
	w.tty = isTerminal(os.Stdin)
	w.o = detect.DefaultOptions()
	w.o.WrittenBy = "watchfor-agent configure"
	cloud = true
	if cfg, err := config.ParseFile(path); err == nil {
		w.existed = true
		w.load(cfg)
		cloud = cfg.Facts.CloudMetadataEnabled() // the file's word, before anything is looked up
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "configure: %s: %v\n  starting from the defaults; the file is replaced on save (a .bak is kept)\n", path, err)
	}
	return w, cloud
}

func (w *wizard) with(sum detect.Summary) *wizard {
	w.sum = sum
	w.orig = w.o
	w.autoBefore = w.o.AutoUpdate
	return w
}

// load carries every setting of an existing file into the options.
func (w *wizard) load(cfg *config.Config) {
	o := &w.o
	o.Server, o.TokenFile, o.TokenInline, o.CAFile = cfg.Server.URL, cfg.Server.TokenFile, cfg.Server.Token, cfg.Server.CAFile
	if cfg.Server.Timeout > 0 && cfg.Server.Timeout != 15*time.Second {
		o.Timeout = fmtDur(cfg.Server.Timeout)
	}
	o.HostName, o.Tags = cfg.Host.Name, cfg.Host.Tags
	if h, _ := os.Hostname(); o.HostName == h || o.HostName == strings.SplitN(h, ".", 2)[0] {
		o.HostName = "" // the default, not a choice
	}
	o.Interval = fmtDur(cfg.Interval)
	o.SpoolDir, o.MaxMB = cfg.Spool.Dir, cfg.Spool.Max()
	o.LogLevel = cfg.Log.Level
	o.CloudMetadata = cfg.Facts.CloudMetadataEnabled()
	o.AutoUpdate = cfg.Updates.Auto != nil && *cfg.Updates.Auto
	if n, ok := cfg.Modules["system"]; ok {
		var c system.Config
		if modules.Decode(&n, &c) == nil {
			o.PerCore = c.PerCore
		}
	}
	if n, ok := cfg.Modules["disk"]; ok {
		var c disk.Config
		if modules.Decode(&n, &c) == nil {
			o.Mounts, o.IgnoreFS, o.Devices = c.Mounts, c.IgnoreFS, c.Devices
			o.IO = c.IO == nil || *c.IO
		}
	}
	if n, ok := cfg.Modules["network"]; ok {
		var c network.Config
		if modules.Decode(&n, &c) == nil {
			o.Interfaces, o.IgnoreIfaces = c.Interfaces, c.Ignore
		}
	}
	if n, ok := cfg.Modules["processes"]; ok {
		var c processes.Config
		if modules.Decode(&n, &c) == nil {
			if c.Top != nil {
				o.Top = *c.Top
			}
			o.Watches = c.Watch
		}
	}
}

func fmtDur(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

// section is one editable part of the config: a form to fill in and what
// to do with the answers. The screen embeds the form; the accessible path
// runs it.
type section struct {
	form  *huh.Form
	apply func()
}

func (w *wizard) form(groups ...*huh.Group) error {
	return newForm(groups...).WithAccessible(!w.tty).Run()
}

func (w *wizard) menuOptions() []huh.Option[string] {
	o := w.o
	token := "token file " + o.TokenFile
	switch {
	case o.TokenInline != "":
		token = "token in the config"
	default:
		if _, err := os.Stat(o.TokenFile); err == nil {
			token += " (present)"
		} else {
			token += " (missing)"
		}
	}
	host := firstOr(o.HostName, w.sum.Hostname+" (default)")
	if len(o.Tags) > 0 {
		host += " · " + tagLine(o.Tags)
	}
	disks := fmt.Sprintf("all %d mounts", len(w.sum.Mounts))
	if o.Mounts != nil {
		disks = fmt.Sprintf("%d of %d mounts", len(o.Mounts), len(w.sum.Mounts))
	}
	switch {
	case !o.IO:
		disks += " · I/O off"
	case o.Devices != nil:
		disks += fmt.Sprintf(" · I/O on %d disks", len(o.Devices))
	default:
		disks += fmt.Sprintf(" · I/O on %d disks", len(w.sum.Devices))
	}
	net := fmt.Sprintf("all %d interfaces", len(w.sum.Interfaces))
	if o.Interfaces != nil {
		net = fmt.Sprintf("%d of %d interfaces", len(o.Interfaces), len(w.sum.Interfaces))
	}
	row := func(label, value string) string { return fmt.Sprintf("%-13s %s", label, value) }
	return []huh.Option[string]{
		huh.NewOption(row("Server", o.Server+" · "+token), "server"),
		huh.NewOption(row("Host", host), "host"),
		huh.NewOption(row("Sending", "every "+o.Interval), "interval"),
		huh.NewOption(row("Disks", disks), "disks"),
		huh.NewOption(row("Network", net), "network"),
		huh.NewOption(row("Processes", fmt.Sprintf("top %d · %d watches", o.Top, len(o.Watches))), "processes"),
		huh.NewOption(row("Cloud lookup", onOff(o.CloudMetadata)), "cloud"),
		huh.NewOption(row("Auto-update", onOff(o.AutoUpdate)), "auto"),
		huh.NewOption(row("Log level", o.LogLevel), "log"),
		huh.NewOption(row("Show file", "the agent.yml this would write"), "show"),
		huh.NewOption(row("Save", "write "+w.path+" and restart"), "save"),
		huh.NewOption(row("Quit", "leave without saving"), "quit"),
	}
}

func tagLine(tags map[string]string) string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + tags[k]
	}
	return strings.Join(parts, " ")
}

func (w *wizard) runAccessible() int {
	machine := fmt.Sprintf("%s · %s · %d cores · %.1f GB RAM", firstOr(w.sum.Hardware, "unknown hardware"), firstOr(w.sum.CPUModel, "unknown cpu"), w.sum.Cores, float64(w.sum.MemTotal)/(1<<30))
	if !w.existed {
		machine += "\nno config yet — starting from the defaults"
	}
	for {
		var choice string
		err := w.form(huh.NewGroup(
			huh.NewSelect[string]().
				Title("watchfor-agent configure  ·  " + w.path).
				Description(machine).
				Options(w.menuOptions()...).
				Value(&choice),
		))
		if err != nil {
			return w.abort()
		}
		switch choice {
		case "server", "host", "interval", "disks", "network", "processes", "cloud", "auto", "log":
			sec := w.section(choice)
			if err = sec.form.WithAccessible(true).Run(); err == nil {
				sec.apply()
			}
		case "show":
			fmt.Print("\n" + w.sum.YAML(w.o) + "\n")
			var back bool
			err = w.form(huh.NewGroup(huh.NewConfirm().Title("That is the file it would write.").Affirmative("Back").Negative("Back").Value(&back)))
		case "save":
			status, err := w.save()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				continue
			}
			fmt.Println("✓ " + status)
			fmt.Println(w.afterSave())
			return exitOK
		case "quit":
			if !w.dirty() {
				return exitOK
			}
			var quit bool
			if err := w.form(huh.NewGroup(huh.NewConfirm().Title("Changes are not saved — quit anyway?").Affirmative("Quit").Negative("Back").Value(&quit))); err != nil {
				return w.abort()
			}
			if quit {
				return exitOK
			}
		}
		if err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				continue // Esc in a section goes back to the menu
			}
			fmt.Fprintln(os.Stderr, "configure:", err)
			return exitError
		}
	}
}

func (w *wizard) abort() int {
	if w.dirty() {
		fmt.Println("left without saving")
	}
	return exitOK
}

func (w *wizard) dirty() bool { return fmt.Sprintf("%+v", w.o) != fmt.Sprintf("%+v", w.orig) }

// section builds the form for one menu key. Every form reads the same
// way — a title that says what it is, one plain sentence on why it
// matters, then the control — and Esc always leaves it untouched.
func (w *wizard) section(key string) section {
	w.fields = nil // this section's fields, in order, are collected as it builds
	switch key {
	case "server":
		return w.sectionServer()
	case "host":
		return w.sectionHost()
	case "interval":
		return w.sectionInterval()
	case "disks":
		return w.sectionDisks()
	case "network":
		return w.sectionNetwork()
	case "processes":
		return w.sectionProcesses()
	case "cloud":
		return w.sectionOnOff("cloud",
			"on      ask the cloud for the instance size", "off     never contact the cloud metadata service",
			w.o.CloudMetadata, func(v bool) { w.o.CloudMetadata = v })
	case "auto":
		return w.sectionOnOff("auto",
			"on      install newer signed releases daily", "off     update by hand: sudo watchfor-agent upgrade",
			w.o.AutoUpdate, func(v bool) { w.o.AutoUpdate = v })
	default:
		return w.sectionLogLevel()
	}
}

// sectionOnOff is a two-way switch. The options are strings, not bools:
// huh renders a Select[bool] with only one of its two options.
func (w *wizard) sectionOnOff(key, onLabel, offLabel string, current bool, set func(bool)) section {
	v := onOff(current)
	return section{w.newForm(group(pick(w, v, "", "",
		huh.NewOption(onLabel, "on"), huh.NewOption(offLabel, "off")).Value(&v))), func() { set(v == "on") }}
}

func (w *wizard) sectionServer() section {
	url, tokenFile, token := w.o.Server, w.o.TokenFile, ""
	fields := []huh.Field{
		newInput().Title("Address").
			Description("Keep it unless WatchFor gave you another one.").
			Value(&url).
			Validate(func(s string) error {
				if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
					return errors.New("must start with https://")
				}
				return nil
			}),
	}
	if w.o.TokenInline != "" {
		fields = append(fields, huh.NewNote().Title("Token").Description("The token is set inside the config ("+maskToken(w.o.TokenInline)+"). Fill in a token file below to switch to a file, or leave it."))
	}
	fields = append(fields,
		newInput().Title("Token file").
			Description("The host token from the dashboard (Hosts → Add host) lives in this file, readable only by the agent.").
			Value(&tokenFile),
		newInput().Title("Paste the token").
			Description("Only to write that file now; empty keeps what is there. Nothing is shown while you type.").
			EchoMode(huh.EchoModePassword).Value(&token),
	)
	return section{w.newForm(w.group(fields...)), func() {
		w.o.Server = strings.TrimRight(url, "/")
		if tokenFile != "" {
			w.o.TokenFile = tokenFile
		}
		switch {
		case token != "":
			if err := health.WritePrivate(w.o.TokenFile, []byte(strings.TrimSpace(token)+"\n"), health.ServiceUser); err != nil {
				w.note = "could not write " + w.o.TokenFile + ": " + err.Error()
			} else {
				w.o.TokenInline = "" // a real token is in the file now
			}
		case w.o.TokenInline != "" && tokenFile != "":
			// switching an inline token to a file, but no token was pasted:
			// keep the inline token unless that file already holds one
			if st, err := os.Stat(w.o.TokenFile); err == nil && st.Size() > 0 {
				w.o.TokenInline = ""
			} else {
				w.note = "kept the token in agent.yml — paste it above to move it into " + w.o.TokenFile
			}
		}
	}}
}

func (w *wizard) sectionHost() section {
	name, tags := w.o.HostName, tagLine(w.o.Tags)
	return section{w.newForm(w.group(newInput().Title("Name").
		Description("Empty means the system hostname: "+w.sum.Hostname+".").
		Placeholder("empty — the hostname is used: "+w.sum.Hostname).Value(&name),
		newInput().Title("Tags").
			Description("key=value with spaces between: env=prod role=web. Empty clears them.").
			Placeholder("none — for example env=prod role=web").Value(&tags).
			Validate(func(s string) error {
				for _, kv := range strings.Fields(s) {
					if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
						return fmt.Errorf("%q should look like key=value", kv)
					}
				}
				return nil
			}),
	)), func() {
		w.o.HostName = strings.TrimSpace(name)
		w.o.Tags = nil
		if fs := strings.Fields(tags); len(fs) > 0 {
			w.o.Tags = map[string]string{}
			for _, kv := range fs {
				k, v, _ := strings.Cut(kv, "=")
				w.o.Tags[k] = v
			}
		}
	}}
}

func (w *wizard) sectionInterval() section {
	v := w.o.Interval
	opts := make([]huh.Option[string], 0, len(intervalChoices)+1)
	known := false
	cur, _ := time.ParseDuration(v)
	for _, c := range intervalChoices {
		opts = append(opts, huh.NewOption(c.label, c.value))
		// 60s in the file and 1m in the list are the same choice
		if d, err := time.ParseDuration(c.value); err == nil && d == cur && cur > 0 {
			v, known = c.value, true
		}
	}
	if !known && v != "" {
		// a hand-edited value outside the list stays selectable, so
		// opening this section does not silently change it
		opts = append([]huh.Option[string]{huh.NewOption(fmt.Sprintf("%-20s set by hand in agent.yml", "every "+v), v)}, opts...)
	}
	dir, maxMB := w.o.SpoolDir, strconv.Itoa(w.o.MaxMB)
	return section{w.newForm(w.group(pick(w, v, "Send every", "Shorter means fresher charts and more data. Your plan sets the fastest allowed; a faster choice is rounded up by WatchFor.", opts...).Value(&v),
		newInput().Title("Keep unsent measurements in").
			Description("A folder the agent can write. Measurements wait here while WatchFor cannot be reached and are sent later.").
			Value(&dir).
			Validate(func(s string) error {
				if !strings.HasPrefix(s, "/") {
					return errors.New("an absolute path, like /var/lib/watchfor-agent")
				}
				return nil
			}),
		newInput().Title("Keep at most (MB)").
			Description("The oldest are dropped first when it fills. 0 keeps nothing.").
			Value(&maxMB).
			Validate(func(s string) error {
				n, err := strconv.Atoi(s)
				if err != nil || n < 0 || n > 100000 {
					return errors.New("a number of megabytes, like 64")
				}
				return nil
			}),
	)), func() {
		if d, err := time.ParseDuration(v); err == nil {
			w.o.Interval = fmtDur(d)
		}
		w.o.SpoolDir = strings.TrimRight(strings.TrimSpace(dir), "/")
		if w.o.SpoolDir == "" {
			w.o.SpoolDir = "/"
		}
		w.o.MaxMB, _ = strconv.Atoi(maxMB)
	}}
}

func (w *wizard) sectionDisks() section {
	points := mountPoints(w.sum.Mounts)
	byPoint := map[string]disk.Discovered{}
	for _, m := range w.sum.Mounts {
		byPoint[m.Point] = m
	}
	if len(points) == 0 {
		return section{w.newForm(w.group(huh.NewNote().Description("No filesystems were found on this machine. The agent will report whatever appears."))), func() {}}
	}
	mountsField, mounts := multi(w, "Mounts to watch",
		"Space ticks or unticks, Enter confirms. All ticked = every real filesystem, also disks mounted later.",
		points, func(p string) string { return mountLine(byPoint[p]) }, w.o.Mounts, w.listHeight)
	io := w.o.IO
	ioChoice := onOff(io)
	groups := []*huh.Group{
		w.group(mountsField),
		w.group(pick(w, ioChoice, "Disk activity", "Reads and writes per second, and how busy each disk is — shows when a slow server is waiting on its disks.",
			huh.NewOption("on      measure disk activity", "on"), huh.NewOption("off     space usage only", "off")).Value(&ioChoice)),
	}
	var devs *[]string
	if len(w.sum.Devices) > 0 {
		var devField *huh.MultiSelect[string]
		devField, devs = multi(w, "Disks to measure",
			"Used when disk activity is on. Partitions count towards their disk; anything stacked on one (encryption, LVM) is listed under it. All ticked = every disk.",
			w.sum.Devices, func(d string) string { return w.sum.DiskRow(d) }, w.o.Devices, w.listHeight)
		// one group, nothing hidden: the pane shows the whole section
		groups = append(groups, w.group(devField))
	}
	return section{w.newForm(groups...), func() {
		w.o.Mounts = asChoice(*mounts, points)
		w.o.IO = ioChoice == "on"
		if devs != nil && w.o.IO {
			w.o.Devices = asChoice(*devs, w.sum.Devices)
		}
	}}
}

func (w *wizard) sectionNetwork() section {
	if len(w.sum.Interfaces) == 0 {
		return section{w.newForm(w.group(huh.NewNote().Description("No network interfaces were found beyond loopback and container links."))), func() {}}
	}
	field, chosen := multi(w, "Interfaces to watch",
		"Space ticks or unticks, Enter confirms. All ticked = every interface, also ones that appear later.",
		w.sum.Interfaces, func(n string) string { return w.sum.IfaceRow(n) }, w.o.Interfaces, w.listHeight)
	return section{w.newForm(w.group(field)), func() { w.o.Interfaces = asChoice(*chosen, w.sum.Interfaces) }}
}

// sectionProcesses: the top list, then the processes to watch picked from
// what is running right now. The agent works out how to match each one
// (by name, or by the script it runs), so nobody has to write patterns.
func (w *wizard) sectionProcesses() section {
	top := strconv.Itoa(w.o.Top)
	known := false
	for _, o := range topChoices {
		known = known || o.Value == top
	}
	opts := topChoices
	if !known {
		opts = append([]huh.Option[string]{huh.NewOption(fmt.Sprintf("top %-5s set by hand in agent.yml", top), top)}, topChoices...)
	}

	// One option per running process group, plus anything already watched
	// that is not running now (a stopped service is exactly what you want
	// to be told about).
	type item struct {
		watch processes.Watch
		label string
	}
	// the name column takes what the pane can spare: the count, memory and
	// owner columns come first, and a row that does not fit is cut, never
	// wrapped in two
	nameW := watchNameW
	if w.paneW > 0 {
		nameW = max(12, min(watchNameW, w.paneW-4-(4+1+9+2+15)))
	}
	var items []item
	seen := map[string]bool{}
	for _, wt := range w.o.Watches {
		key := watchText(wt)
		seen[key] = true
		n := processes.Matches(wt)
		state := stateNow(n)
		count := ""
		if n > 1 {
			count = fmt.Sprintf("×%d", n)
		}
		items = append(items, item{wt, fmt.Sprintf("%s %-4s %9s  %s", col(watchLabel(wt), nameW), count, "", state)})
	}
	// the biggest handful: a full process table is a list nobody reads,
	// and anything missing can still be watched by editing agent.yml
	shown := 0
	for _, g := range w.sum.Processes {
		if shown >= processPickLimit {
			break
		}
		wt := processes.Suggest(g)
		if seen[watchText(wt)] {
			continue
		}
		shown++
		count := ""
		if g.Count > 1 {
			count = fmt.Sprintf("×%d", g.Count)
		}
		items = append(items, item{wt, fmt.Sprintf("%s %-4s %9s  %s", col(watchLabel(wt), nameW), count, detect.Bytes(g.RSS), g.User)})
	}
	chosen := make([]string, 0, len(w.o.Watches))
	watchOpts := make([]huh.Option[string], len(items))
	for i, it := range items {
		key := watchText(it.watch)
		on := seen[key]
		watchOpts[i] = huh.NewOption(w.fitRow(it.label), strconv.Itoa(i)).Selected(on) // by index: a process may name itself to look like a watch line
		if on {
			chosen = append(chosen, key)
		}
	}
	watchField := huh.NewMultiSelect[string]().
		Title(fmt.Sprintf("Processes to watch (up to %d)", maxWatches)).
		Description("The biggest running now. A watched process is reported whether it runs or not, with its CPU and memory, so an alert can fire the moment it stops. Space ticks, Enter confirms.").
		Options(watchOpts...).Limit(maxWatches).Height(0).Value(&chosen).Filterable(true)
	watchField.WithTheme(tickTheme()) // a tick list, like the others: no cursor band on ticked rows
	if w.optionCount != nil {
		w.optionCount[watchField] = len(watchOpts)
		w.startPos[watchField] = 0
	}

	// Anything the list does not offer — a small daemon, a service that is
	// down right now — is typed in by hand, the way it would be written
	// in agent.yml.
	byHand := ""
	byHandField := newInput().Title("Add by hand").
		Description("Names or command-line parts the list above does not offer, separated by commas: sshd, cmdline:gunicorn user:www. A process that is down right now can be added too — being told when it comes back is the point.").
		Placeholder("none — for example sshd, cron").
		Validate(func(s string) error {
			_, bad := parseWatches(s)
			if bad != "" {
				return fmt.Errorf("%q: a name is one word — start a longer match with cmdline", bad)
			}
			return nil
		}).Value(&byHand)

	return section{w.newForm(w.group(pick(w, top, "Busiest processes to report", "The top ones by CPU and by memory ride along in every batch.", opts...).Value(&top),
		watchField,
		byHandField,
	)), func() {
		w.o.Top, _ = strconv.Atoi(top)
		w.o.Watches = nil
		have := map[string]bool{}
		add := func(wt processes.Watch) {
			if key := watchText(wt); !have[key] {
				have[key] = true
				w.o.Watches = append(w.o.Watches, wt)
			}
		}
		for _, key := range chosen {
			if i, err := strconv.Atoi(key); err == nil && i >= 0 && i < len(items) {
				add(items[i].watch)
			}
		}
		extra, _ := parseWatches(byHand)
		for _, wt := range extra {
			add(wt)
		}
		if len(w.o.Watches) > maxWatches {
			dropped := len(w.o.Watches) - maxWatches
			w.o.Watches = w.o.Watches[:maxWatches]
			w.note = fmt.Sprintf("only %d processes can be watched — the last %d you added were left out", maxWatches, dropped)
		}
	}}
}

func (w *wizard) sectionLogLevel() section {
	level := w.o.LogLevel
	return section{w.newForm(w.group(pick(w, level, "", "",
		huh.NewOption("info     the usual: starts, changes, failures, updates", "info"),
		huh.NewOption("debug    everything: each send, what was kept on disk, cloud lookups", "debug"),
		huh.NewOption("warn     problems only", "warn"),
		huh.NewOption("error    only what stopped the agent from working", "error"),
	).Value(&level))), func() { w.o.LogLevel = level }}
}

// save writes the file and keeps the auto-update timer in step; it
// returns what happened, for the status line. Root and systemd decide
// whether a restart can be offered.
func (w *wizard) save() (status string, err error) {
	if _, err := config.WriteConfig(w.path, w.sum.YAML(w.o)); err != nil {
		return "", err
	}
	w.existed = true
	status = "saved " + w.path
	if w.o.AutoUpdate != w.autoBefore {
		if os.Geteuid() == 0 {
			ctx := context.Background()
			var terr error
			if w.o.AutoUpdate {
				terr = update.EnableTimer(ctx, "", execCmdCombined)
			} else {
				terr = update.DisableTimer(ctx, "", execCmdCombined)
			}
			if terr != nil {
				status += " · auto-update timer: " + terr.Error()
			} else {
				status += " · auto-update timer " + map[bool]string{true: "enabled", false: "removed"}[w.o.AutoUpdate]
				w.autoBefore = w.o.AutoUpdate
			}
		} else {
			status += " · timer needs root: sudo watchfor-agent auto-update " + onOff(w.o.AutoUpdate)
		}
	}
	w.orig = w.o
	w.needsRestart = true
	w.wrote = true
	return status, nil
}

func (w *wizard) canRestart() bool { return os.Geteuid() == 0 && haveSystemctl() }

// afterSave is the one line printed once the file is written: a running
// agent (0.7.0+) notices the change itself within seconds; an older one,
// or an agent that is not running, needs a restart.
func (w *wizard) afterSave() string {
	if serviceActive() {
		return fmt.Sprintf("Saved to %s — the running agent picks it up by itself within half a minute.", w.path)
	}
	if w.canRestart() {
		return fmt.Sprintf("Saved to %s. Start the agent with: sudo systemctl start watchfor-agent", w.path)
	}
	return fmt.Sprintf("Saved to %s.", w.path)
}

func serviceActive() bool {
	if !haveSystemctl() {
		return false
	}
	out, err := execCmdCombined(context.Background(), "systemctl", "is-active", "watchfor-agent")
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

func haveSystemctl() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func execCmdCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// runHome is `watchfor-agent` with no arguments on a terminal: the
// overview screen with configure, the batch, update and the service.
func runHome(args []string) int {
	fs := flag.NewFlagSet("home", flag.ContinueOnError)
	configPath := fs.String("config", envOr("WATCHFOR_AGENT_CONFIG", defaultConfig), "path to agent.yml")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if !isTerminal(os.Stdin) {
		fmt.Fprintln(os.Stderr, "the overview needs a terminal; try: watchfor-agent check")
		return exitUsage
	}
	w := newWizard(*configPath)
	w.tty = true
	if _, err := tea.NewProgram(newScreen(w), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "overview:", err)
		return exitError
	}
	if w.wrote {
		fmt.Println(w.afterSave())
	}
	return exitOK
}

// fitRow cuts a list row at the pane's edge. huh would wrap it onto a
// second line instead, which reads as two rows; the tail of a row is
// the least important part (the owner, the filesystem type).
func (w *wizard) fitRow(label string) string {
	if w.paneW <= 0 {
		return label
	}
	return ansi.Truncate(label, max(12, w.paneW-4), "…") // 4: the cursor and the tick
}

// maskToken shows a token the way a screen may: an env: reference as it
// is, a literal only by its fingerprint.
func maskToken(tok string) string {
	if tok == "" || strings.HasPrefix(tok, "env:") {
		return tok
	}
	sum := sha256.Sum256([]byte(tok))
	return "•••• " + hex.EncodeToString(sum[:4])
}

// maskTokens hides the value of any token: line in a config text before
// it is shown, keeping env: references, which are not secrets.
func maskTokens(text string) string {
	return tokenLine.ReplaceAllStringFunc(text, func(line string) string {
		m := tokenLine.FindStringSubmatch(line)
		if strings.HasPrefix(strings.Trim(m[2], `"'`), "env:") {
			return line
		}
		return m[1] + "••••"
	})
}

var tokenLine = regexp.MustCompile(`(?m)^(\s*token:\s*)(\S.*)$`)
