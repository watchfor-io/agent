//go:build linux

// Package procfs reads the handful of /proc and /sys files the collectors
// need. Parsers take the file's exact format as documented in proc(5) and
// Documentation/admin-guide/iostats.rst; anything else is an error, not a
// guess. Root is overridable so tests run against captured fixtures.
package procfs

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	Root    = "/proc"
	SysRoot = "/sys"
)

// ClockTicks is USER_HZ, the unit of the CPU time fields in /proc. It is 100
// on every Linux architecture regardless of the kernel's HZ.
const ClockTicks = 100

func path(parts ...string) string {
	return filepath.Join(append([]string{Root}, parts...)...)
}

func readLines(name string, fn func(fields []string) error) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if err := fn(fields); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return sc.Err()
}

func parseUints(fields []string) ([]uint64, error) {
	out := make([]uint64, len(fields))
	for i, f := range fields {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("field %d %q: not a number", i, f)
		}
		out[i] = v
	}
	return out, nil
}

// ─── /proc/stat ──────────────────────────────────────────────────────────

type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// Total excludes guest time: the kernel already accounts it inside user/nice.
func (c CPUTimes) Total() uint64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

func (c CPUTimes) Sub(p CPUTimes) CPUTimes {
	return CPUTimes{
		User: c.User - p.User, Nice: c.Nice - p.Nice, System: c.System - p.System,
		Idle: c.Idle - p.Idle, IOWait: c.IOWait - p.IOWait, IRQ: c.IRQ - p.IRQ,
		SoftIRQ: c.SoftIRQ - p.SoftIRQ, Steal: c.Steal - p.Steal,
	}
}

type Stat struct {
	CPU             CPUTimes
	PerCPU          []CPUTimes
	ContextSwitches uint64
	Forks           uint64
	ProcsRunning    uint64
	ProcsBlocked    uint64
	BootTime        int64
}

func ReadStat() (Stat, error) {
	var st Stat
	err := readLines(path("stat"), func(f []string) error {
		switch {
		case f[0] == "cpu":
			t, err := cpuTimes(f[1:])
			if err != nil {
				return err
			}
			st.CPU = t
		case strings.HasPrefix(f[0], "cpu"):
			t, err := cpuTimes(f[1:])
			if err != nil {
				return err
			}
			st.PerCPU = append(st.PerCPU, t)
		case f[0] == "ctxt" && len(f) > 1:
			st.ContextSwitches, _ = strconv.ParseUint(f[1], 10, 64)
		case f[0] == "processes" && len(f) > 1:
			st.Forks, _ = strconv.ParseUint(f[1], 10, 64)
		case f[0] == "procs_running" && len(f) > 1:
			st.ProcsRunning, _ = strconv.ParseUint(f[1], 10, 64)
		case f[0] == "procs_blocked" && len(f) > 1:
			st.ProcsBlocked, _ = strconv.ParseUint(f[1], 10, 64)
		case f[0] == "btime" && len(f) > 1:
			st.BootTime, _ = strconv.ParseInt(f[1], 10, 64)
		}
		return nil
	})
	return st, err
}

func cpuTimes(f []string) (CPUTimes, error) {
	if len(f) < 8 {
		return CPUTimes{}, fmt.Errorf("cpu line has %d fields, want at least 8", len(f))
	}
	v, err := parseUints(f[:8])
	if err != nil {
		return CPUTimes{}, err
	}
	return CPUTimes{User: v[0], Nice: v[1], System: v[2], Idle: v[3], IOWait: v[4], IRQ: v[5], SoftIRQ: v[6], Steal: v[7]}, nil
}

// ─── /proc/meminfo ───────────────────────────────────────────────────────

// Meminfo maps field name to bytes (the file reports kB).
type Meminfo map[string]uint64

func ReadMeminfo() (Meminfo, error) {
	m := make(Meminfo, 64)
	err := readLines(path("meminfo"), func(f []string) error {
		if len(f) < 2 {
			return nil
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return nil
		}
		if len(f) > 2 && f[2] == "kB" {
			v *= 1024
		}
		m[strings.TrimSuffix(f[0], ":")] = v
		return nil
	})
	if err == nil && m["MemTotal"] == 0 {
		err = errors.New("meminfo: MemTotal missing")
	}
	return m, err
}

// ─── /proc/loadavg, /proc/uptime ─────────────────────────────────────────

type LoadAvg struct {
	Load1, Load5, Load15 float64
}

func ReadLoadAvg() (LoadAvg, error) {
	b, err := os.ReadFile(path("loadavg"))
	if err != nil {
		return LoadAvg{}, err
	}
	var la LoadAvg
	if _, err := fmt.Sscanf(string(b), "%f %f %f", &la.Load1, &la.Load5, &la.Load15); err != nil {
		return LoadAvg{}, fmt.Errorf("loadavg: %w", err)
	}
	return la, nil
}

func ReadUptime() (time.Duration, error) {
	b, err := os.ReadFile(path("uptime"))
	if err != nil {
		return 0, err
	}
	var up float64
	if _, err := fmt.Sscanf(string(b), "%f", &up); err != nil {
		return 0, fmt.Errorf("uptime: %w", err)
	}
	return time.Duration(up * float64(time.Second)), nil
}

// ─── /proc/diskstats ─────────────────────────────────────────────────────

type DiskStat struct {
	Major, Minor    int
	Name            string
	ReadsCompleted  uint64
	SectorsRead     uint64
	ReadTicksMs     uint64
	WritesCompleted uint64
	SectorsWritten  uint64
	WriteTicksMs    uint64
	IOInProgress    uint64
	IOTicksMs       uint64
}

// SectorSize is fixed at 512 in diskstats regardless of the device's block size.
const SectorSize = 512

func ReadDiskStats() ([]DiskStat, error) {
	var out []DiskStat
	err := readLines(path("diskstats"), func(f []string) error {
		if len(f) < 14 {
			return fmt.Errorf("diskstats line has %d fields, want at least 14", len(f))
		}
		v, err := parseUints(f[3:14])
		if err != nil {
			return err
		}
		major, _ := strconv.Atoi(f[0])
		minor, _ := strconv.Atoi(f[1])
		out = append(out, DiskStat{
			Major: major, Minor: minor, Name: f[2],
			ReadsCompleted: v[0], SectorsRead: v[2], ReadTicksMs: v[3],
			WritesCompleted: v[4], SectorsWritten: v[6], WriteTicksMs: v[7],
			IOInProgress: v[8], IOTicksMs: v[9],
		})
		return nil
	})
	return out, err
}

// ─── /proc/self/mounts ───────────────────────────────────────────────────

type Mount struct {
	Device, Point, FSType string
}

func ReadMounts() ([]Mount, error) {
	var out []Mount
	err := readLines(path("self", "mounts"), func(f []string) error {
		if len(f) < 3 {
			return nil
		}
		out = append(out, Mount{Device: unescape(f[0]), Point: unescape(f[1]), FSType: f[2]})
		return nil
	})
	return out, err
}

// unescape decodes the octal escapes mounts uses for space, tab, newline and
// backslash in paths ("\040" is a space).
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ─── /proc/net/dev, /proc/net/sockstat ───────────────────────────────────

type NetDev struct {
	Name                                    string
	RxBytes, RxPackets, RxErrors, RxDropped uint64
	TxBytes, TxPackets, TxErrors, TxDropped uint64
}

func ReadNetDev() ([]NetDev, error) {
	var out []NetDev
	err := readLines(path("net", "dev"), func(f []string) error {
		// Header lines; data lines are "eth0: rx... tx...", sometimes with
		// the counters glued to the colon.
		name, rest, ok := strings.Cut(strings.Join(f, " "), ":")
		if !ok || strings.Contains(name, "|") {
			return nil
		}
		cols := strings.Fields(rest)
		if len(cols) < 16 {
			return fmt.Errorf("net/dev line for %s has %d columns", name, len(cols))
		}
		v, err := parseUints(cols[:16])
		if err != nil {
			return err
		}
		out = append(out, NetDev{
			Name:    strings.TrimSpace(name),
			RxBytes: v[0], RxPackets: v[1], RxErrors: v[2], RxDropped: v[3],
			TxBytes: v[8], TxPackets: v[9], TxErrors: v[10], TxDropped: v[11],
		})
		return nil
	})
	return out, err
}

type SockStat struct {
	SocketsUsed uint64
	TCPInUse    uint64
	TCPOrphan   uint64
	TCPTimeWait uint64
	UDPInUse    uint64
}

func ReadSockStat() (SockStat, error) {
	var s SockStat
	err := readLines(path("net", "sockstat"), func(f []string) error {
		kv := map[string]uint64{}
		for i := 1; i+1 < len(f); i += 2 {
			kv[f[i]], _ = strconv.ParseUint(f[i+1], 10, 64)
		}
		switch f[0] {
		case "sockets:":
			s.SocketsUsed = kv["used"]
		case "TCP:":
			s.TCPInUse, s.TCPOrphan, s.TCPTimeWait = kv["inuse"], kv["orphan"], kv["tw"]
		case "UDP:":
			s.UDPInUse = kv["inuse"]
		}
		return nil
	})
	return s, err
}
