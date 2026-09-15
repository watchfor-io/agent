//go:build linux

package procfs

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Process struct {
	PID        int
	Comm       string
	State      byte
	PPID       int
	UTime      uint64 // clock ticks
	STime      uint64
	Threads    int
	StartTicks uint64 // ticks after boot
	RSSBytes   uint64
	UID        int
}

func (p Process) CPUTicks() uint64 { return p.UTime + p.STime }

// Pids lists the numeric entries of /proc. Anything that disappears between
// listing and reading is a process that exited — callers skip those.
func Pids() ([]int, error) {
	entries, err := os.ReadDir(Root)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func ReadProcess(pid int) (Process, error) {
	dir := path(strconv.Itoa(pid))
	stat, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return Process{}, err
	}
	p, err := parseStat(stat)
	if err != nil {
		return Process{}, fmt.Errorf("pid %d: %w", pid, err)
	}
	p.PID = pid

	if statm, err := os.ReadFile(dir + "/statm"); err == nil {
		f := strings.Fields(string(statm))
		if len(f) > 1 {
			pages, _ := strconv.ParseUint(f[1], 10, 64)
			p.RSSBytes = pages * uint64(os.Getpagesize())
		}
	}
	p.UID = -1
	if status, err := os.ReadFile(dir + "/status"); err == nil {
		if i := bytes.Index(status, []byte("\nUid:")); i >= 0 {
			f := strings.Fields(string(status[i+5:]))
			if len(f) > 0 {
				p.UID, _ = strconv.Atoi(f[0])
			}
		}
	}
	return p, nil
}

// Cmdline returns the process command line with NULs as spaces, capped so a
// pathological argv cannot bloat a batch.
func Cmdline(pid int, max int) string {
	b, err := os.ReadFile(path(strconv.Itoa(pid), "cmdline"))
	if err != nil || len(b) == 0 {
		return ""
	}
	if len(b) > max {
		b = b[:max]
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}

// parseStat handles the one awkward thing about /proc/<pid>/stat: comm is
// in parentheses and may itself contain spaces or parentheses, so the fixed
// fields are counted from the LAST ')' rather than split naively.
func parseStat(b []byte) (Process, error) {
	open := bytes.IndexByte(b, '(')
	close := bytes.LastIndexByte(b, ')')
	if open < 0 || close < open {
		return Process{}, fmt.Errorf("stat: comm not delimited")
	}
	f := strings.Fields(string(b[close+1:]))
	// f[0] is field 3 (state); field N is f[N-3].
	if len(f) < 22 {
		return Process{}, fmt.Errorf("stat: %d fields after comm, want at least 22", len(f))
	}
	p := Process{Comm: string(b[open+1 : close]), State: f[0][0]}
	p.PPID, _ = strconv.Atoi(f[1])
	p.UTime, _ = strconv.ParseUint(f[11], 10, 64)
	p.STime, _ = strconv.ParseUint(f[12], 10, 64)
	p.Threads, _ = strconv.Atoi(f[17])
	p.StartTicks, _ = strconv.ParseUint(f[19], 10, 64)
	return p, nil
}
