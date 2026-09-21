//go:build linux

package main

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/watchfor-io/agent/internal/health"
	"github.com/watchfor-io/agent/internal/metric"
)

func (s *screen) paneHeader(key string) string {
	m := sectionMeta[key]
	w := max(24, s.rightWidth()-4)
	head := stTitle.Render(m.title)
	if at := s.paneTime(key); at != "" {
		if pad := w - lipgloss.Width(head) - lipgloss.Width(at); pad > 0 {
			head += strings.Repeat(" ", pad) + stDim.Render(at)
		}
	}
	why := stDim.Width(w).Render(m.why)
	return head + "\n" + stRule.Render(strings.Repeat("─", min(len(m.title)+8, 40))) + "\n\n" + why + "\n\n"
}

// paneTime is the "as of" stamp a pane carries in its title line instead
// of spending a row of its own on it.
func (s *screen) paneTime(key string) string {
	switch key {
	case "check":
		if !s.checkedAt.IsZero() {
			return "checked " + s.checkedAt.Format("15:04:05")
		}
	case "update":
		if !s.upChecked.IsZero() {
			return "checked " + s.upChecked.Format("15:04:05")
		}
	case "live", "batch", "system":
		if !s.sampled.IsZero() {
			return "sampled " + s.sampled.Format("15:04:05")
		}
	}
	return ""
}

// fitLines cuts anything still too wide for the pane. A long path or a
// journal line would otherwise run through the frame and out of the
// terminal; the viewport does not clip for us.
func fitLines(text string, w int) string {
	if w < 8 {
		w = 8
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = ansi.Truncate(l, w, "…")
		}
	}
	return strings.Join(lines, "\n")
}

func (s *screen) systemPane() string {
	sum, o := s.w.sum, s.w.o
	var b strings.Builder
	b.WriteString(s.paneHeader("system"))
	line := func(k, v string) { fmt.Fprintf(&b, "  %s %s\n", stDim.Render(fmt.Sprintf("%-12s", k)), v) }
	b.WriteString(stTitle.Render("Machine") + "\n")
	line("host", sum.Hostname)
	hw := firstOr(sum.Hardware, stDim.Render("unknown"))
	if sum.Virtualization != "" {
		hw += stDim.Render(" · " + sum.Virtualization)
	}
	line("hardware", hw)
	line("cpu", fmt.Sprintf("%s · %d cores", firstOr(sum.CPUModel, "unknown"), sum.Cores))
	line("memory", fmtBytesShort(float64(sum.MemTotal)))
	if up, ok := s.metric("sys.uptime_s"); ok {
		line("uptime", fmtUptime(up))
	}
	addr := firstOr(sum.PrimaryAddress, stDim.Render("—"))
	if sum.PrimaryIface != "" {
		addr += stDim.Render(" on " + sum.PrimaryIface)
	}
	line("address", addr)
	if len(sum.PublicAddresses) > 0 {
		line("public", strings.Join(sum.PublicAddresses, stDim.Render(" · ")))
	}
	b.WriteString("\n" + stTitle.Render("Agent") + "\n")
	line("version", Version+stDim.Render(" · "+runtime.GOOS+"/"+runtime.GOARCH))
	line("service", s.svcStateStyled())
	line("sends to", o.Server)
	line("every", o.Interval)
	tok := stOn.Render("present")
	if o.TokenInline == "" {
		if _, err := os.Stat(o.TokenFile); err != nil {
			tok = stWarn.Render("missing — see Server")
		}
	}
	line("token", tok)
	if s.spoolN > 0 {
		line("unsent", stWarn.Render(fmt.Sprintf("%d batches · %s waiting for WatchFor", s.spoolN, fmtBytesShort(float64(s.spoolB)))))
	} else {
		line("unsent", stDim.Render("nothing waiting"))
	}
	line("auto-update", onOffStyled(o.AutoUpdate))
	line("config", s.w.path)
	if s.modErr != nil {
		b.WriteString("\n  " + stWarn.Render("collectors: "+s.modErr.Error()) + "\n")
	}
	return b.String()
}

func (s *screen) livePane() string {
	var b strings.Builder
	b.WriteString(s.paneHeader("live"))
	if s.latest == nil {
		b.WriteString("  " + stDim.Render("collecting…") + "\n")
		return b.String()
	}
	// One label column and one bar width for the whole pane: resources,
	// disks and interfaces are read as one table, so the bars end and the
	// figures start in the same place whatever this machine is called.
	ds := s.samples("disk.used_pct")
	sort.Slice(ds, func(i, j int) bool { return ds[i].V > ds[j].V })
	rx := s.samples("net.rx_bytes_per_s")
	sort.Slice(rx, func(i, j int) bool { return rx[i].V > rx[j].V })
	labW := 8
	for _, d := range ds {
		labW = max(labW, lipgloss.Width(d.L["mount"]))
	}
	for _, r := range rx {
		labW = max(labW, lipgloss.Width(r.L["if"]))
	}
	labW = min(labW, 20)
	barW := max(10, min(40, s.rightWidth()-labW-38))
	line := func(k, v string) { fmt.Fprintf(&b, "  %s  %s\n", stDim.Render(fmt.Sprintf("%-*s", labW, k)), v) }
	b.WriteString(stTitle.Render("Resources") + "\n")
	if v, ok := s.metric("cpu.usage_pct"); ok {
		user, _ := s.metric("cpu.user_pct")
		sys, _ := s.metric("cpu.system_pct")
		io, _ := s.metric("cpu.iowait_pct")
		steal, _ := s.metric("cpu.steal_pct")
		line("cpu", bar(v, barW)+stDim.Render(fmt.Sprintf("  user %.0f · system %.0f · i/o wait %.0f · steal %.0f", user, sys, io, steal)))
	} else {
		line("cpu", stDim.Render("one more sample…"))
	}
	if v, ok := s.metric("mem.used_pct"); ok {
		used, _ := s.metric("mem.used_bytes")
		total, _ := s.metric("mem.total_bytes")
		cache, _ := s.metric("mem.cached_bytes")
		line("memory", bar(v, barW)+stDim.Render(fmt.Sprintf("  %s of %s · cache %s", fmtBytesShort(used), fmtBytesShort(total), fmtBytesShort(cache))))
	}
	if total, ok := s.metric("swap.total_bytes"); ok && total > 0 {
		v, _ := s.metric("swap.used_pct")
		used, _ := s.metric("swap.used_bytes")
		line("swap", bar(v, barW)+stDim.Render(fmt.Sprintf("  %s of %s", fmtBytesShort(used), fmtBytesShort(total))))
	}
	l1, _ := s.metric("load.1")
	l5, _ := s.metric("load.5")
	l15, _ := s.metric("load.15")
	line("load", fmt.Sprintf("%.2f  %.2f  %.2f%s", l1, l5, l15, stDim.Render(fmt.Sprintf("   1 · 5 · 15 min, %d cores", s.w.sum.Cores))))

	b.WriteString("\n" + stTitle.Render("Disks") + "\n")
	for _, d := range ds {
		total, _ := s.metric("disk.total_bytes", metric.Labels{"mount": d.L["mount"]})
		line(ansi.Truncate(d.L["mount"], labW, "…"),
			bar(d.V, barW)+stDim.Render("  "+fmtBytesShort(total)+" · "+d.L["fs"]))
	}
	if len(ds) == 0 {
		b.WriteString("  " + stDim.Render("—") + "\n")
	}

	b.WriteString("\n" + stTitle.Render("Network") + "\n")
	for _, r := range rx {
		tx, _ := s.metric("net.tx_bytes_per_s", metric.Labels{"if": r.L["if"]})
		line(ansi.Truncate(r.L["if"], labW, "…"),
			fmt.Sprintf("%s %10s   %s %10s", stDim.Render("↓"), fmtRate(r.V), stDim.Render("↑"), fmtRate(tx)))
	}
	if len(rx) == 0 {
		b.WriteString("  " + stDim.Render("one more sample…") + "\n")
	}

	b.WriteString("\n" + stTitle.Render("Busiest processes") + "\n")
	ps := s.samples("proc.cpu_pct")
	sort.Slice(ps, func(i, j int) bool { return ps[i].V > ps[j].V })
	for i, p := range ps {
		if i >= 8 {
			break
		}
		rss, _ := s.metric("proc.rss_bytes", metric.Labels{"pid": p.L["pid"]})
		fmt.Fprintf(&b, "  %-18s %5.1f%%  %9s  %s\n", ansi.Truncate(p.L["name"], 18, "…"), p.V, fmtBytesShort(rss), stDim.Render(p.L["user"]))
	}
	if total, ok := s.metric("proc.total"); ok {
		running, _ := s.metric("proc.running")
		b.WriteString("  " + stDim.Render(fmt.Sprintf("%.0f processes · %.0f running", total, running)) + "\n")
	}
	return b.String()
}

func (s *screen) batchPane() string {
	var b strings.Builder
	b.WriteString(s.paneHeader("batch"))
	if s.latest == nil {
		b.WriteString("  " + stDim.Render("collecting…") + "\n")
		return b.String()
	}
	samples := append([]metric.Sample(nil), s.latest.Samples()...)
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].M != samples[j].M {
			return samples[i].M < samples[j].M
		}
		return fmt.Sprint(samples[i].L) < fmt.Sprint(samples[j].L)
	})
	b.WriteString(stDim.Render(fmt.Sprintf("  %d values as of %s", len(samples), s.sampled.Format("15:04:05"))) + "\n\n")
	for _, sm := range samples {
		labels := ""
		if len(sm.L) > 0 {
			keys := make([]string, 0, len(sm.L))
			for k := range sm.L {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = k + "=" + sm.L[k]
			}
			labels = stDim.Render("{" + strings.Join(parts, ", ") + "}")
		}
		fmt.Fprintf(&b, "  %-28s %14.2f  %s\n", sm.M, sm.V, labels)
	}
	return b.String()
}

func (s *screen) updatePane() string {
	var b strings.Builder
	b.WriteString(s.paneHeader("update"))
	fmt.Fprintf(&b, "  %s %s%s\n\n", stDim.Render(fmt.Sprintf("%-12s", "running")), stBig.Render(Version), stDim.Render(" · "+runtime.GOOS+"/"+runtime.GOARCH))
	switch {
	case s.inst != nil:
		if s.inst.err != nil {
			b.WriteString("  " + stWarn.Render("install failed: "+s.inst.err.Error()) + "\n")
		} else if s.inst.res.Updated {
			fmt.Fprintf(&b, "  %s installed %s → %s", stOn.Render("✓"), s.inst.res.From, s.inst.res.To)
			if s.inst.res.Restarted {
				b.WriteString(" and restarted the service")
			}
			b.WriteString("\n")
		} else {
			b.WriteString("  nothing to install\n")
		}
		if strings.TrimSpace(s.inst.out) != "" {
			b.WriteString("\n" + stDim.Render(strings.TrimSpace(s.inst.out)) + "\n")
		}
	case s.upBusy:
		b.WriteString("  " + stDim.Render("asking watchfor.io for the newest release…") + "\n")
	case s.upd == nil:
		b.WriteString("  " + stDim.Render("checking…") + "\n")
	case s.upd.err != nil:
		b.WriteString("  " + stWarn.Render("could not check: "+s.upd.err.Error()) + "\n\n  " + stDim.Render("Enter tries again") + "\n")
	case s.upd.st.Available:
		fmt.Fprintf(&b, "  %s %s %s\n", stDim.Render(fmt.Sprintf("%-12s", "available")), stOn.Render(s.upd.st.Latest), stDim.Render("reported by "+s.upd.st.Origin))
	case s.upd.st.Latest == "":
		fmt.Fprintf(&b, "  %s %s\n", stDim.Render(fmt.Sprintf("%-12s", "newest")), stDim.Render("no release reported yet"))
	default:
		fmt.Fprintf(&b, "  %s %s\n", stDim.Render(fmt.Sprintf("%-12s", "newest")), stOn.Render("up to date"))
	}
	b.WriteString(s.actionList("update"))
	return b.String()
}

func (s *screen) checkPane() string {
	var b strings.Builder
	b.WriteString(s.paneHeader("check"))
	if s.checks == nil {
		if s.checkBusy {
			b.WriteString("  " + stDim.Render("looking…") + "\n")
		}
		b.WriteString(s.actionList("check"))
		return b.String()
	}
	mark := map[health.State]string{
		health.OK:      stOn.Render("●"),
		health.Warn:    stWarn.Render("●"),
		health.Fail:    stBarBad.Render("●"),
		health.Skipped: stDim.Render("○"),
	}
	nameW := 0
	for _, c := range s.checks {
		nameW = max(nameW, lipgloss.Width(c.Name))
	}
	bad := 0
	for _, c := range s.checks {
		if c.State == health.Fail || c.State == health.Warn {
			bad++
		}
		fmt.Fprintf(&b, "  %s %-*s%s%s\n", mark[c.State], nameW, c.Name, gutter, c.Detail)
	}
	if bad == 0 {
		b.WriteString("\n  " + stOn.Render("everything checks out") + "\n")
	}
	if s.deepBusy {
		b.WriteString("\n  " + stDim.Render("downloading the signed release…") + "\n")
	}
	b.WriteString(s.actionList("check"))
	return b.String()
}

func (s *screen) repairPane() string {
	var b strings.Builder
	b.WriteString(s.paneHeader("repair"))
	if s.inst != nil {
		if s.inst.err != nil {
			b.WriteString("  " + stWarn.Render("reinstall failed: "+s.inst.err.Error()) + "\n\n")
		} else if s.inst.res.Updated {
			fmt.Fprintf(&b, "  %s reinstalled %s", stOn.Render("✓"), s.inst.res.To)
			if s.inst.res.Restarted {
				b.WriteString(" and restarted the service")
			}
			b.WriteString("\n\n")
		}
	}
	b.WriteString(s.actionList("repair"))
	return b.String()
}

func (s *screen) servicePane() string {
	var b strings.Builder
	b.WriteString(s.paneHeader("service"))
	boot := stDim.Render("not enabled at boot")
	switch s.svcEnabled {
	case "enabled":
		boot = stOn.Render("enabled at boot")
	case "disabled":
		boot = stWarn.Render("not enabled at boot")
	case "":
		boot = stDim.Render("no unit installed")
	}
	fmt.Fprintf(&b, "  %s   %s · %s\n", stTitle.Render("watchfor-agent.service"), s.svcStateStyled(), boot)
	if !haveSystemctl() {
		b.WriteString("  " + stDim.Render("no systemd here — the agent runs from cron or by hand") + "\n")
	}
	b.WriteString(s.actionList("service"))
	b.WriteString("\n" + stTitle.Render("Log") + stDim.Render("  journalctl -u watchfor-agent") + "\n")
	if s.svc == nil {
		b.WriteString("  " + stDim.Render("reading…") + "\n")
	} else {
		for _, l := range strings.Split(s.svc.journal, "\n") {
			b.WriteString("  " + l + "\n")
		}
	}
	return b.String()
}

func (s *screen) metric(name string, labels ...metric.Labels) (float64, bool) {
	if s.latest == nil {
		return 0, false
	}
	for _, sm := range s.latest.Samples() {
		if sm.M != name {
			continue
		}
		if len(labels) == 0 {
			if len(sm.L) == 0 {
				return sm.V, true
			}
			continue
		}
		match := true
		for k, v := range labels[0] {
			if sm.L[k] != v {
				match = false
				break
			}
		}
		if match {
			return sm.V, true
		}
	}
	return 0, false
}

func (s *screen) samples(name string) []metric.Sample {
	if s.latest == nil {
		return nil
	}
	var out []metric.Sample
	for _, sm := range s.latest.Samples() {
		if sm.M == name {
			out = append(out, sm)
		}
	}
	return out
}

func fmtBytesShort(v float64) string {
	switch {
	case v >= 1<<30:
		return fmt.Sprintf("%.1f GB", v/(1<<30))
	case v >= 1<<20:
		return fmt.Sprintf("%.0f MB", v/(1<<20))
	case v >= 1<<10:
		return fmt.Sprintf("%.0f KB", v/(1<<10))
	}
	return fmt.Sprintf("%.0f B", v)
}

func fmtRate(v float64) string { return fmtBytesShort(v) + "/s" }

func fmtUptime(sec float64) string {
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
