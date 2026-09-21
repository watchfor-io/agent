//go:build linux

// Package detect looks at the machine the way the collectors will and
// describes it — for `config detect` (a human summary) and `config init`
// (a commented agent.yml that lists what was found, so choosing what to
// watch is editing a list, not guessing).
package detect

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/watchfor-io/agent/internal/hostinfo"
	"github.com/watchfor-io/agent/internal/modules/disk"
	"github.com/watchfor-io/agent/internal/modules/network"
	"github.com/watchfor-io/agent/internal/modules/processes"
	"github.com/watchfor-io/agent/internal/procfs"
)

// Summary is what detect found: the machine's identity and everything the
// collectors could watch, so choosing what to monitor is picking from a
// list rather than knowing the names in advance.
type Summary struct {
	Hostname       string
	CPUModel       string
	Cores          int
	MemTotal       uint64
	Hardware       string
	Virtualization string
	PrimaryAddress string
	PrimaryIface   string
	// PublicAddresses: what the cloud assigned from the outside, if known.
	PublicAddresses []string
	Mounts          []disk.Discovered
	Devices         []string
	Disks           []disk.Disk
	Interfaces      []string
	// Running processes grouped by name, biggest first — what the
	// configure screen offers when choosing what to watch.
	Processes []processes.Running
	// Routable addresses per interface and the link state, so a list of
	// interfaces means something to someone who does not know the names.
	Addresses  map[string][]string
	IfaceState map[string]string
	Warnings   []string
}

// IfaceLine is "eth0  192.168.1.10 · up" — how an interface is shown to a person.
func (s Summary) IfaceLine(name string) string {
	parts := []string{}
	if a := s.Addresses[name]; len(a) > 0 {
		parts = append(parts, strings.Join(a, ", "))
	} else {
		parts = append(parts, "no address")
	}
	if st := s.IfaceState[name]; st != "" {
		parts = append(parts, st)
	}
	return strings.Join(parts, " · ")
}

// IfaceRow is one aligned table row: name, IPv4, IPv6, state — widths
// from the whole list, so a column of rows reads as a table.
func (s Summary) IfaceRow(name string) string {
	nw, v4w := 9, 15
	for _, n := range s.Interfaces {
		nw = max(nw, utf8.RuneCountInString(n))
		if v4 := firstAddr(s.Addresses[n], false); utf8.RuneCountInString(v4) > v4w {
			v4w = utf8.RuneCountInString(v4)
		}
	}
	v4 := firstAddr(s.Addresses[name], false)
	v6 := firstAddr(s.Addresses[name], true)
	if v4 == "" {
		v4 = "—"
	}
	state := s.IfaceState[name]
	if state == "" {
		state = "?"
	}
	if v6 != "" {
		return fmt.Sprintf("%-*s  %-*s  %-5s  %s", nw, name, v4w, v4, state, v6)
	}
	return fmt.Sprintf("%-*s  %-*s  %-5s", nw, name, v4w, v4, state)
}

func firstAddr(addrs []string, v6 bool) string {
	for _, a := range addrs {
		if strings.Contains(a, ":") == v6 {
			return a
		}
	}
	return ""
}

// diskOrder lists the devices as they are stacked: a disk, then whatever
// sits on it, in the order a person would draw them.
func (s Summary) diskOrder() []string {
	known := map[string]bool{}
	for _, d := range s.Devices {
		known[d] = true
	}
	parent := map[string]string{}
	for _, d := range s.Disks {
		if d.Parent != "" && known[d.Parent] {
			parent[d.Name] = d.Parent
		}
	}
	var out []string
	var walk func(on string)
	walk = func(on string) {
		for _, d := range s.Devices {
			if parent[d] == on {
				out = append(out, d)
				walk(d)
			}
		}
	}
	walk("")
	if len(out) != len(s.Devices) { // a cycle or something unexpected: leave it alone
		return s.Devices
	}
	return out
}

// diskDepth is how far a device is from the hardware: 0 for a disk, 1
// for the volume on it, and so on.
func (s Summary) diskDepth(name string) int {
	by := map[string]disk.Disk{}
	for _, d := range s.Disks {
		by[d.Name] = d
	}
	depth := 0
	for at, ok := by[name]; ok && at.Parent != ""; at, ok = by[at.Parent] {
		depth++
		if depth > 8 { // stacked that deep is a loop, not a machine
			break
		}
	}
	return depth
}

// diskName is what to call a device in a list: its own name, and the
// name the system gave the volume when there is one.
func (s Summary) diskName(d disk.Disk) string {
	name := d.Name
	if d.Label != "" && d.Label != d.Name {
		name += " · " + d.Label
	}
	return name
}

// DiskRow is one line of the disk list, drawn as the tree it is:
//
//	nvme0n1                      238 GB  SSD
//	└─ dm-0 · dm_crypt-0         235 GB  encrypted
//	   └─ dm-1 · ubuntu--vg-...  235 GB  LVM
func (s Summary) DiskRow(name string) string {
	by := map[string]disk.Disk{}
	for _, d := range s.Disks {
		by[d.Name] = d
	}
	nw := 8
	for _, n := range s.Devices {
		nw = max(nw, 3*s.diskDepth(n)+len(s.diskName(by[n])))
	}
	d, ok := by[name]
	if !ok {
		return fmt.Sprintf("%-*s", nw, name)
	}
	lead := strings.Repeat("   ", max(0, s.diskDepth(name)-1))
	if s.diskDepth(name) > 0 {
		lead += "└─ "
	}
	kind := "HDD"
	switch {
	case d.Kind != "":
		kind = d.Kind
	case d.SSD:
		kind = "SSD"
	}
	if len(d.Spans) > 0 {
		kind += " over " + strings.Join(d.Spans, ", ")
	}
	return fmt.Sprintf("%-*s  %8s  %s", nw, lead+s.diskName(d), bytes(d.SizeBytes), kind)
}

// Collect gathers everything; a collector that fails leaves a warning,
// not an error — a half-known machine is still worth a config.
func Collect(o hostinfo.Options) Summary {
	var s Summary
	s.Hostname, _ = os.Hostname()
	s.Hostname = procfs.Clean(s.Hostname)
	f := hostinfo.Collect(o)
	s.CPUModel, s.Cores, s.MemTotal = f.CPUModel, f.CPUCores, f.MemTotal
	s.Hardware, s.Virtualization = f.Hardware, f.Virtualization
	s.PrimaryAddress, s.PrimaryIface = f.PrimaryAddress, f.PrimaryIface
	s.PublicAddresses = f.PublicAddresses
	s.Addresses = f.Addresses
	var err error
	if s.Mounts, s.Devices, err = disk.Discover(); err != nil {
		s.Warnings = append(s.Warnings, "disks: "+err.Error())
	}
	s.Disks = disk.Disks(s.Devices)
	s.Devices = s.diskOrder() // a child right under what it sits on
	if s.Interfaces, err = network.Discover(); err != nil {
		s.Warnings = append(s.Warnings, "network: "+err.Error())
	}
	if ps, err := processes.Discover(); err == nil {
		s.Processes = ps
	} else {
		s.Warnings = append(s.Warnings, "processes: "+err.Error())
	}
	s.IfaceState = map[string]string{}
	for _, i := range s.Interfaces {
		s.IfaceState[i] = network.State(i)
	}
	return s
}

// Bytes formats a size the way the summary does.
func Bytes(n uint64) string { return bytes(n) }

func bytes(n uint64) string {
	const g = 1 << 30
	switch {
	case n >= 10*g:
		return fmt.Sprintf("%.0f GB", float64(n)/g)
	case n >= g:
		return fmt.Sprintf("%.1f GB", float64(n)/g)
	default:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Print writes the human summary.
func (s Summary) Print(w io.Writer) {
	row := func(k, v string) { fmt.Fprintf(w, "%-12s %s\n", k, v) }
	row("host", s.Hostname)
	cpu := orDash(s.CPUModel)
	if s.Cores > 0 {
		cpu += fmt.Sprintf(" · %d cores", s.Cores)
	}
	row("cpu", cpu)
	row("memory", bytes(s.MemTotal))
	hw := orDash(s.Hardware)
	if s.Virtualization != "" {
		hw += " · " + s.Virtualization
	}
	row("hardware", hw)
	addr := orDash(s.PrimaryAddress)
	if s.PrimaryIface != "" {
		addr += " on " + s.PrimaryIface
	}
	row("address", addr)
	if len(s.PublicAddresses) > 0 {
		row("public", strings.Join(s.PublicAddresses, " · "))
	}
	for i, m := range s.Mounts {
		k := ""
		if i == 0 {
			k = "mounts"
		}
		row(k, fmt.Sprintf("%-14s %8s  %3.0f%% used  %-6s %s", m.Point, bytes(m.TotalBytes), m.UsedPct, m.FSType, m.Device))
	}
	if len(s.Mounts) == 0 {
		row("mounts", "—")
	}
	for i, d := range s.Devices {
		k := ""
		if i == 0 {
			k = "disks"
		}
		row(k, s.DiskRow(d))
	}
	if len(s.Devices) == 0 {
		row("disks", "—")
	}
	for i, n := range s.Interfaces {
		k := ""
		if i == 0 {
			k = "interfaces"
		}
		row(k, fmt.Sprintf("%-14s %s", n, s.IfaceLine(n)))
	}
	if len(s.Interfaces) == 0 {
		row("interfaces", "—")
	}
	for _, w := range s.Warnings {
		row("warning", w)
	}
}

// Options is everything the generated agent.yml says. Zero values mean
// "the default, shown as a comment"; a chosen value is written as a live
// line, so the wizard and the installer render the same file.
type Options struct {
	Server, TokenFile, TokenInline string
	CAFile                         string
	Timeout                        string
	HostName                       string
	Tags                           map[string]string
	Interval                       string
	SpoolDir                       string
	MaxMB                          int
	// Path is where the file is written, for its own header.
	Path          string
	LogLevel      string
	CloudMetadata bool
	AutoUpdate    bool
	PerCore       bool
	Mounts        []string // nil: every real filesystem
	IgnoreFS      []string
	Devices       []string // nil: every whole disk
	IO            bool
	Interfaces    []string // nil: all but the ignore list
	IgnoreIfaces  []string // nil: the built-in list
	Top           int
	Watches       []processes.Watch
	WrittenBy     string
}

// DefaultOptions is what a fresh install gets.
func DefaultOptions() Options {
	return Options{ // #nosec G101 -- a path to the token file, not a credential
		Server: "https://ingest.watchfor.io", TokenFile: "/etc/watchfor-agent/token",
		Interval: "1m", SpoolDir: "/var/lib/watchfor-agent", MaxMB: 64, LogLevel: "info",
		CloudMetadata: true, IO: true, Top: 10, WrittenBy: "the installer",
	}
}

var plain = regexp.MustCompile(`^[A-Za-z0-9_./:@+=-]+$`)

// yq renders a scalar for YAML: plain when it can be, double-quoted otherwise.
func yq(s string) string {
	if plain.MatchString(s) {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func yqList(items []string) string {
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = yq(it)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// YAML renders the commented agent.yml with the detected lists in place.
func (s Summary) YAML(o Options) string {
	d := DefaultOptions()
	if o.Interval == "" {
		o.Interval = d.Interval
	}
	if o.SpoolDir == "" {
		o.SpoolDir = d.SpoolDir
	}
	if o.MaxMB == 0 {
		o.MaxMB = d.MaxMB
	}
	if o.LogLevel == "" {
		o.LogLevel = d.LogLevel
	}

	if o.WrittenBy == "" {
		o.WrittenBy = d.WrittenBy
	}
	// detected lists, for the comments
	var points, notes []string
	for _, m := range s.Mounts {
		points = append(points, m.Point)
		notes = append(notes, fmt.Sprintf("%s (%s, %s)", m.Point, m.FSType, bytes(m.TotalBytes)))
	}
	found := func(list []string) string {
		if len(list) == 0 {
			return "nothing found"
		}
		return strings.Join(list, ", ")
	}
	// live-or-comment line helpers
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	// key line with an aligned trailing comment; live when active
	kv := func(indent int, active bool, key, val, comment string) {
		prefix := strings.Repeat(" ", indent)
		if !active {
			prefix += "# "
		}
		line := prefix + key + ": " + val
		if comment != "" {
			if len(line) < 42 {
				line += strings.Repeat(" ", 42-len(line))
			} else {
				line += " "
			}
			line += "# " + comment
		}
		w("%s\n", line)
	}
	note := func(indent int, text string) { w("%s# %s\n", strings.Repeat(" ", indent), text) }

	w("# %s — written by %s on %s\n", firstOr(o.Path, "/etc/watchfor-agent/agent.yml"), o.WrittenBy, time.Now().Format("2006-01-02"))
	w("# for %s (%s, %d cores, %s RAM).\n#\n", orDash(s.Hostname), orDash(s.CPUModel), s.Cores, bytes(s.MemTotal))
	w("# Every key except server is optional; unknown keys are an error.\n")
	w("#   see what it would send:   watchfor-agent check\n")
	w("#   change it in a menu:      sudo watchfor-agent configure\n")
	w("#   change one value:         sudo watchfor-agent config set interval 30s\n")
	w("#   list what this box has:   watchfor-agent config detect\n")
	w("# Restart after editing:      sudo systemctl restart watchfor-agent\n\n")

	w("server:\n")
	kv(2, true, "url", o.Server, "")
	if o.TokenInline != "" {
		kv(2, true, "token", yq(o.TokenInline), "or token_file: a 0600 file with the token")
	} else {
		tf := o.TokenFile
		if tf == "" {
			tf = d.TokenFile
		}
		kv(2, true, "token_file", tf, "0600, owned by watchfor-agent; never put the token in this file")
	}
	kv(2, o.CAFile != "", "ca_file", firstOr(o.CAFile, "/etc/watchfor-agent/ca.pem"), "pin a CA instead of the system store")
	kv(2, o.Timeout != "", "timeout", firstOr(o.Timeout, "15s"), "")
	w("\nhost:\n")
	kv(2, o.HostName != "", "name", firstOr(o.HostName, orDash(s.Hostname)), "default: short hostname; what the dashboard shows")
	if len(o.Tags) > 0 {
		keys := make([]string, 0, len(o.Tags))
		for k := range o.Tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + ": " + yq(o.Tags[k])
		}
		kv(2, true, "tags", "{ "+strings.Join(parts, ", ")+" }", "at most 32; keys [a-z0-9_.-]")
	} else {
		kv(2, false, "tags", "{ env: prod, role: web }", "at most 32; keys [a-z0-9_.-]")
	}
	w("\n")
	kv(0, true, "interval", o.Interval, "how often a batch is pushed; the plan sets the floor and the")
	note(42, "server adjusts a running agent (this is the value it starts from)")
	w("\nspool:                                    # batches kept on disk while WatchFor is unreachable, replayed later\n")
	kv(2, true, "dir", o.SpoolDir, "")
	kv(2, true, "max_mb", fmt.Sprint(o.MaxMB), "oldest dropped first; 0 = no spool (batches are lost while the server is unreachable)")
	w("\nlog:\n")
	kv(2, true, "level", o.LogLevel, "debug | info | warn | error — debug adds every push")
	note(42, "(samples, bytes), spooled batches, facts and cloud lookups")
	w("\nfacts:\n")
	kv(2, true, "cloud_metadata", fmt.Sprint(o.CloudMetadata), "ask the recognised cloud's metadata service for the instance")
	note(42, "size when the firmware does not carry it (GCE, Azure, OCI…);")
	note(42, "false: never contact a metadata service")
	w("\nupdates:\n")
	kv(2, true, "auto", fmt.Sprint(o.AutoUpdate), "daily signed updates; kept in sync by: sudo watchfor-agent auto-update on|off")
	w("\nmodules:\n")
	if o.PerCore {
		w("  system:                                 # cpu, load, memory, swap, uptime\n")
		kv(4, true, "per_core", "true", "one cpu.usage_pct series per core")
	} else {
		w("  system: {}                              # cpu, load, memory, swap, uptime\n")
		kv(4, false, "per_core", "true", "one cpu.usage_pct series per core")
	}
	w("\n")
	diskLive := o.Mounts != nil || o.Devices != nil || len(o.IgnoreFS) > 0 || !o.IO
	if diskLive {
		w("  disk:                                   # usage per mount, I/O per whole disk\n")
	} else {
		w("  disk: {}                                # usage per mount, I/O per whole disk\n")
	}
	note(2, "Found on this machine: "+found(notes))
	note(2, "Watch only some of them by listing the mount points (default: every real filesystem;")
	note(2, "USB sticks, /media, /tmp, app images, snaps and other FUSE mounts are left out unless listed here):")
	kv(4, o.Mounts != nil, "mounts", yqList(firstList(o.Mounts, points, []string{"/"})), "")
	note(2, "Whole disks for I/O rates (default: every whole disk, partitions folded in): "+found(s.Devices))
	kv(4, o.Devices != nil, "devices", yqList(firstList(o.Devices, s.Devices, nil)), "")
	kv(4, len(o.IgnoreFS) > 0, "ignore_fs", yqList(firstList(o.IgnoreFS, []string{"nfs"}, nil)), "skip a filesystem type on top of the built-in pseudo-fs list")
	kv(4, !o.IO, "io", "false", "turn the I/O rates off")
	w("\n")
	netLive := o.Interfaces != nil || o.IgnoreIfaces != nil
	if netLive {
		w("  network:                                # rates per interface, socket counts\n")
	} else {
		w("  network: {}                             # rates per interface, socket counts\n")
	}
	ifaces := make([]string, len(s.Interfaces))
	for i, n := range s.Interfaces {
		ifaces[i] = n
		if a := s.Addresses[n]; len(a) > 0 {
			ifaces[i] += " (" + a[0] + ")"
		}
	}
	note(2, "Found on this machine (loopback, docker*, veth*, br-*, virbr* already ignored): "+found(ifaces))
	kv(4, o.Interfaces != nil, "interfaces", yqList(firstList(o.Interfaces, s.Interfaces, nil)), "")
	kv(4, o.IgnoreIfaces != nil, "ignore", yqList(firstList(o.IgnoreIfaces, []string{"lo", "veth*", "docker*", "br-*", "virbr*"}, nil)), "")
	w("\n  processes:                              # state counts, top consumers, watches\n")
	kv(4, true, "top", fmt.Sprint(o.Top), "top CPU and memory consumers reported each batch")
	if len(o.Watches) > 0 {
		w("    watch:                                # \"is it running, what does it use\" — alertable\n")
		for _, wt := range o.Watches {
			first := true
			put := func(k, v string) {
				if v == "" {
					return
				}
				if first {
					w("      - %s: %s\n", k, yq(v))
					first = false
				} else {
					w("        %s: %s\n", k, yq(v))
				}
			}
			put("name", wt.Name)
			put("cmdline", wt.Cmdline)
			put("user", wt.User)
		}
	} else {
		note(4, "watch:                              # \"is it running, what does it use\" — alertable")
		note(4, "  - name: nginx                     # comm, the 15-char kernel name")
		note(4, "  - cmdline: postgres -D            # substring of the command line")
		note(4, "    user: postgres")
	}
	return b.String()
}

func firstOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// firstList: the chosen list, else what was detected, else a fallback.
func firstList(chosen, detected, fallback []string) []string {
	switch {
	case chosen != nil:
		return chosen
	case len(detected) > 0:
		return detected
	}
	return fallback
}
