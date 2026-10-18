//go:build linux

package processes

import (
	"path"
	"sort"
	"strings"

	"github.com/watchfor-io/agent/internal/procfs"
)

// Running is a group of processes on this machine that share a name —
// what a person picks from when choosing what to watch.
type Running struct {
	Name    string // comm, as ps shows it
	Count   int
	RSS     uint64 // memory of the whole group
	User    string // the user of the first one
	Cmdline string // a representative command line
}

// Discover lists the running processes grouped by name, biggest first.
// Kernel threads (no command line) are left out: nobody watches those.
func Discover() ([]Running, error) {
	pids, err := procfs.Pids()
	if err != nil {
		return nil, err
	}
	own := newOwners()
	byName := map[string]*Running{}
	for _, pid := range pids {
		p, err := procfs.ReadProcess(pid)
		if err != nil {
			continue
		}
		cmd := procfs.Cmdline(pid, cmdlineCap)
		if strings.TrimSpace(cmd) == "" {
			continue // kernel thread
		}
		g, ok := byName[p.Comm]
		if !ok {
			g = &Running{Name: p.Comm, User: own.user(p), Cmdline: cmd}
			byName[p.Comm] = g
		}
		g.Count++
		g.RSS += p.RSSBytes
	}
	out := make([]Running, 0, len(byName))
	for _, g := range byName {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RSS != out[j].RSS {
			return out[i].RSS > out[j].RSS
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Suggest picks the way to watch a group that will keep matching: the
// name when it is a whole story by itself, the command line when several
// unrelated things share the name (python, java, node, sh…).
func Suggest(g Running) Watch {
	switch g.Name {
	case "python", "python3", "java", "node", "ruby", "perl", "sh", "bash", "docker-proxy", "runc":
		if arg := firstArgument(g.Cmdline); arg != "" {
			return Watch{Cmdline: arg}
		}
	}
	if full := FullName(g); full != g.Name {
		return Watch{Cmdline: full}
	}
	return Watch{Name: g.Name}
}

// commLimit is where the kernel cuts a process name: "gnome-terminal-server"
// is "gnome-terminal-" to ps and to /proc.
const commLimit = 15

// FullName is the name a person knows the process by. The kernel keeps
// only the first 15 characters, so a longer name is taken from the
// program the command line starts with, when that program is the same one.
func FullName(g Running) string {
	if len(g.Name) < commLimit {
		return g.Name
	}
	fields := strings.Fields(g.Cmdline)
	if len(fields) == 0 {
		return g.Name
	}
	base := path.Base(fields[0])
	if strings.HasPrefix(base, g.Name) && base != g.Name {
		return base
	}
	return g.Name
}

// firstArgument is the part of a command line that says what it runs: the
// script or jar, not the interpreter.
func firstArgument(cmdline string) string {
	fields := strings.Fields(cmdline)
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			continue
		}
		return f
	}
	if len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// Matches counts the processes a watch matches right now — the answer to
// "will this actually watch anything?".
func Matches(w Watch) int {
	pids, err := procfs.Pids()
	if err != nil {
		return 0
	}
	own := newOwners()
	n := 0
	buf := make([]byte, cmdlineMatchCap)
	for _, pid := range pids {
		p, err := procfs.ReadProcess(pid)
		if err != nil {
			continue
		}
		if w.Name != "" && p.Comm != w.Name {
			continue
		}
		if w.Cmdline != "" && !strings.Contains(procfs.CmdlineRaw(pid, buf), w.Cmdline) {
			continue
		}
		if w.User != "" && own.user(p) != w.User {
			continue
		}
		n++
	}
	return n
}
