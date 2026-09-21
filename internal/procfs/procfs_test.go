//go:build linux

package procfs

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	Root = "testdata/proc"
	os.Exit(m.Run())
}

func TestStat(t *testing.T) {
	st, err := ReadStat()
	if err != nil {
		t.Fatal(err)
	}
	if st.CPU.Total() == 0 || len(st.PerCPU) == 0 {
		t.Errorf("cpu times not parsed: %+v", st.CPU)
	}
	if st.BootTime == 0 || st.ContextSwitches == 0 {
		t.Errorf("btime/ctxt not parsed: %+v", st)
	}
	d := st.CPU.Sub(CPUTimes{})
	if d != st.CPU {
		t.Error("Sub of zero should be identity")
	}
}

func TestMeminfo(t *testing.T) {
	m, err := ReadMeminfo()
	if err != nil {
		t.Fatal(err)
	}
	if m["MemTotal"] < 1<<20 || m["MemAvailable"] == 0 {
		t.Errorf("meminfo in bytes expected, got MemTotal=%d MemAvailable=%d", m["MemTotal"], m["MemAvailable"])
	}
}

func TestLoadAndUptime(t *testing.T) {
	if la, err := ReadLoadAvg(); err != nil || la.Load1 < 0 {
		t.Errorf("loadavg: %v %+v", err, la)
	}
	if up, err := ReadUptime(); err != nil || up <= 0 {
		t.Errorf("uptime: %v %s", err, up)
	}
}

func TestDiskStats(t *testing.T) {
	ds, err := ReadDiskStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) == 0 {
		t.Fatal("no devices")
	}
	for _, d := range ds {
		if d.Name == "" {
			t.Errorf("unnamed device: %+v", d)
		}
	}
}

func TestMounts(t *testing.T) {
	ms, err := ReadMounts()
	if err != nil {
		t.Fatal(err)
	}
	var root bool
	for _, m := range ms {
		if m.Point == "/" {
			root = true
		}
	}
	if !root {
		t.Error("root mount not found")
	}
	if got := unescape(`/mnt/my\040disk`); got != "/mnt/my disk" {
		t.Errorf("unescape = %q", got)
	}
}

func TestNetDev(t *testing.T) {
	nd, err := ReadNetDev()
	if err != nil {
		t.Fatal(err)
	}
	var lo bool
	for _, d := range nd {
		if d.Name == "lo" {
			lo = true
		}
	}
	if !lo {
		t.Errorf("lo missing in %+v", nd)
	}
}

func TestSockStat(t *testing.T) {
	s, err := ReadSockStat()
	if err != nil {
		t.Fatal(err)
	}
	if s.SocketsUsed == 0 {
		t.Errorf("sockstat not parsed: %+v", s)
	}
}

func TestProcessCommWithSpacesAndParens(t *testing.T) {
	p, err := ReadProcess(4242)
	if err != nil {
		t.Fatal(err)
	}
	if p.Comm != "Web Content (2)" || p.State != 'S' || p.PPID != 1 {
		t.Errorf("stat parse: %+v", p)
	}
	if p.UTime != 1500 || p.STime != 250 || p.Threads != 7 || p.StartTicks != 98765 {
		t.Errorf("fields after comm off by one: %+v", p)
	}
	if p.RSSBytes != 45678*uint64(os.Getpagesize()) {
		t.Errorf("rss = %d", p.RSSBytes)
	}
	if p.UID != 1000 {
		t.Errorf("uid = %d", p.UID)
	}
	if got := Cmdline(4242, 256); got != "firefox -contentproc -childID 2" {
		t.Errorf("cmdline = %q", got)
	}
	if got := Cmdline(4242, 7); got != "firefox" {
		t.Errorf("capped cmdline = %q", got)
	}
}

func TestPids(t *testing.T) {
	pids, err := Pids()
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 1 || pids[0] != 4242 {
		t.Errorf("pids = %v", pids)
	}
}
