//go:build linux

package health

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/watchfor-io/agent/internal/agent"
	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/push"
	"github.com/watchfor-io/agent/internal/update"
)

// The service account's uid comes from a band nothing else allocates
// from: systemd documents 60578–61183 as unused — above login.defs'
// UID_MAX (60000), so not a person; between systemd-homed / nspawn's
// --bind-user range and DynamicUser's 61184+; below the 100000+ that
// subuid ranges (rootless containers) start at. Container images take
// their uids from the other end — 999, 998 from `useradd --system`,
// 100–110 from `adduser --system`, 1000/1001 for the app user, 65532/65534
// — and a container without a user namespace runs those as the same
// numbers on the host. `useradd --system` on the host counts down from
// 999 too, which is how the agent's account came to share a uid with
// mailcow's SOGo.
const (
	QuietUIDMin = 60578
	QuietUIDMax = 61183
)

// Proc is a process as the uid checks see it.
type Proc struct {
	PID       int
	UID       int
	Comm      string
	Container string // procfs.Container.String(): "" on the host
}

// agentProc says whether p is this agent: its binary's name, on the host.
// A process in a container that happens to be called watchfor-agent is
// not ours.
func agentProc(p Proc) bool { return p.Comm == "watchfor-agent" && p.Container == "" }

// readProcUIDs is every uid a running process has, from /proc.
func readProcUIDs() (map[int]bool, error) {
	pids, err := procfs.Pids()
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, pid := range pids {
		if p, err := procfs.ReadProcess(pid); err == nil && p.UID >= 0 {
			out[p.UID] = true
		}
	}
	return out, nil
}

// readProcsOf lists the processes running as uid, with the container each
// lives in (read only for these: a cgroup per process on the whole host
// would be thousands of opens for a one-off check).
func readProcsOf(uid int) ([]Proc, error) {
	pids, err := procfs.Pids()
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, pid := range pids {
		p, err := procfs.ReadProcess(pid)
		if err != nil || p.UID != uid {
			continue // gone mid-scan, or someone else's
		}
		c, _ := procfs.ReadContainerPID(pid)
		out = append(out, Proc{PID: pid, UID: p.UID, Comm: p.Comm, Container: c.String()})
	}
	return out, nil
}

// passwdNames is every account in /etc/passwd with that uid — os/user
// returns only the first.
func passwdNames(uid int) []string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) > 2 && fields[2] == strconv.Itoa(uid) {
			out = append(out, fields[0])
		}
	}
	return out
}

// idTaken is a uid some account has, or a gid some group has.
func idTaken(id int) bool {
	s := strconv.Itoa(id)
	if _, err := user.LookupId(s); err == nil {
		return true
	}
	_, err := user.LookupGroupId(s)
	return err == nil
}

// PickUID is the uid (and gid) a fresh or moved service account gets: the
// first in the quiet band that no account and no group has and no running
// process uses. ok is false when the band is full.
func PickUID(inUse map[int]bool, taken func(int) bool) (int, bool) {
	for id := QuietUIDMin; id <= QuietUIDMax; id++ {
		if !inUse[id] && !taken(id) {
			return id, true
		}
	}
	return 0, false
}

// uidShare is the check behind the SOGo report: processes that are not the
// agent but run with its uid. In a container without a user namespace a
// process's uid is the host's, so the container's "sogo" (999) and the
// host's watchfor-agent (999) are one identity to the kernel: ps shows the
// container's processes as watchfor-agent, they may read and write what
// the agent owns (the token, the spool), the agent could signal them and
// read their /proc/<pid>/environ — and the unit's RemoveIPC= deletes that
// uid's System V and POSIX IPC in the host namespace every time the agent
// stops, which breaks a container started with --ipc=host.
func uidShare(o Options) Check {
	c := Check{Name: "service account uid"}
	u, err := o.LookupUser(ServiceUser)
	if err != nil {
		c.State, c.Detail = Skipped, "no "+ServiceUser+" account"
		return c
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	procs, err := o.ProcsOf(uid)
	if err != nil {
		c.State, c.Detail = Skipped, "cannot list processes: "+err.Error()
		return c
	}
	var foreign []Proc
	for _, p := range procs {
		if !agentProc(p) {
			foreign = append(foreign, p)
		}
	}
	var others []string
	for _, n := range o.AccountsWithUID(uid) {
		if n != ServiceUser {
			others = append(others, n)
		}
	}
	if len(foreign) == 0 && len(others) == 0 {
		c.State, c.Detail = OK, fmt.Sprintf("uid %d · no other process or account uses it", uid)
		return c
	}
	c.State = Warn
	var why []string
	if len(foreign) > 0 {
		why = append(why, fmt.Sprintf("%d processes that are not the agent run as uid %d (%s) — ps and process lists name them %s",
			len(foreign), uid, summarize(foreign), ServiceUser))
	}
	if len(others) > 0 {
		if len(others) == 1 {
			why = append(why, fmt.Sprintf("the account %s has uid %d too", others[0], uid))
		} else {
			why = append(why, fmt.Sprintf("the accounts %s have uid %d too", strings.Join(others, ", "), uid))
		}
	}
	c.Detail = strings.Join(why, "; ") + ". To the kernel they are one user: they can read the agent's token and spool, the agent could signal them, and stopping the agent removes their shared memory (RemoveIPC)"
	if !o.Root {
		c.Detail += " — run as root to move the account"
		return c
	}
	inUse, err := o.ProcUIDs()
	if err != nil {
		return c
	}
	next, ok := PickUID(inUse, o.IDTaken)
	if !ok {
		c.Detail += fmt.Sprintf(" — no free uid left in %d–%d to move it to", QuietUIDMin, QuietUIDMax)
		return c
	}
	c.FixMsg = fmt.Sprintf("move %s to uid/gid %d (unused by any account, group or process) and hand its files over", ServiceUser, next)
	c.Fix = moveAccount(o, uid, gid, next)
	return c
}

// summarize names the groups of foreign processes, biggest first:
// "sogod ×21 in docker:3f2a9c1b7d4e, cron".
func summarize(ps []Proc) string {
	type key struct{ comm, where string }
	n := map[key]int{}
	for _, p := range ps {
		n[key{p.Comm, p.Container}]++
	}
	keys := make([]key, 0, len(n))
	for k := range n {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if n[keys[i]] != n[keys[j]] {
			return n[keys[i]] > n[keys[j]]
		}
		return keys[i].comm < keys[j].comm
	})
	var parts []string
	for i, k := range keys {
		if i == 3 {
			parts = append(parts, "…")
			break
		}
		s := k.comm
		if n[k] > 1 {
			s += fmt.Sprintf(" ×%d", n[k])
		}
		if k.where != "" {
			s += " in " + k.where
		} else {
			s += " on the host"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// moveAccount gives the service account (and its group, when the group is
// its own) the uid/gid next, then hands the agent's files from the old ids
// to the new: the config folder and agent.yml (group), the token (owner),
// the state folder and everything in it. The service is stopped for it —
// usermod refuses a user with running processes — and started again.
// Processes in containers do not count for usermod: it skips processes
// under another root.
func moveAccount(o Options, oldUID, oldGID, next int) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		active := o.HaveSystemctl() && trim(o.Exec(ctx, "systemctl", "is-active", update.ServiceUnit)) == "active"
		if active {
			if out, err := o.Exec(ctx, "systemctl", "stop", update.ServiceUnit); err != nil {
				return fmt.Errorf("systemctl stop: %w: %s", err, trimBytes(out))
			}
		}
		err := func() error {
			newGID := oldGID
			if g, err := o.LookupGroup(ServiceUser); err == nil && g.Gid == strconv.Itoa(oldGID) {
				if out, err := o.Exec(ctx, "groupmod", "-g", strconv.Itoa(next), ServiceUser); err != nil {
					return fmt.Errorf("groupmod: %w: %s", err, trimBytes(out))
				}
				newGID = next
			}
			if out, err := o.Exec(ctx, "usermod", "-u", strconv.Itoa(next), "-g", strconv.Itoa(newGID), ServiceUser); err != nil {
				return fmt.Errorf("usermod: %w: %s", err, trimBytes(out))
			}
			return rehome(o.Layout, ids4{oldUID, oldGID, next, newGID})
		}()
		if active {
			if out, serr := o.Exec(ctx, "systemctl", "start", update.ServiceUnit); serr != nil && err == nil {
				err = fmt.Errorf("systemctl start: %w: %s", serr, trimBytes(out))
			}
		}
		return err
	}
}

type ids4 struct{ oldUID, oldGID, newUID, newGID int }

// rehome moves ownership from the old ids to the new on the installer's
// paths only, by descriptor, refusing symlinks — the same ground rules as
// every other repair. Owners that are not the old ids (root) stay.
func rehome(l Layout, m ids4) error {
	var errs []error
	for _, p := range []struct {
		path string
		dir  bool
	}{{filepath.Dir(l.Config), true}, {l.Config, false}, {l.Token, false}, {l.Spool, true}} {
		if p.path == "" || p.path == "." {
			continue
		}
		fd, st, err := openNoFollow(p.path, p.dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := chownMapped(fd, st, m); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.path, err))
		}
		if p.dir && p.path == l.Spool {
			if err := rehomeEntries(fd, p.path, m); err != nil {
				errs = append(errs, err)
			}
		}
		_ = syscall.Close(fd)
	}
	return errors.Join(errs...)
}

// rehomeEntries does the state folder's files, each opened relative to the
// folder's descriptor without following links.
func rehomeEntries(dirfd int, dir string, m ids4) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		fd, err := syscall.Openat(dirfd, e.Name(), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if err != nil {
			continue // a symlink (ELOOP) or a file gone meanwhile: not ours to touch
		}
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err == nil {
			if err := chownMapped(fd, st, m); err != nil {
				errs = append(errs, fmt.Errorf("%s/%s: %w", dir, e.Name(), err))
			}
		}
		_ = syscall.Close(fd)
	}
	return errors.Join(errs...)
}

func chownMapped(fd int, st syscall.Stat_t, m ids4) error {
	uid, gid := int(st.Uid), int(st.Gid)
	if uid == m.oldUID {
		uid = m.newUID
	}
	if gid == m.oldGID {
		gid = m.newGID
	}
	if uid == int(st.Uid) && gid == int(st.Gid) {
		return nil
	}
	return syscall.Fchown(fd, uid, gid)
}

// schedule catches the agent installed twice over: the service and the
// cron entry both. The second one now steps aside (one agent per
// state folder), but it still starts every minute for nothing, and an
// older binary next to it would push every batch twice.
func schedule(ctx context.Context, o Options) (Check, bool) {
	c := Check{Name: "one way to run"}
	if o.Layout.Cron == "" {
		return c, false
	}
	if _, err := os.Lstat(o.Layout.Cron); err != nil {
		return c, false // no cron entry: nothing to say
	}
	service := false
	if o.HaveSystemctl() {
		if _, err := os.Stat(o.UnitFile); err == nil {
			service = trim(o.Exec(ctx, "systemctl", "is-enabled", update.ServiceUnit)) == "enabled" ||
				trim(o.Exec(ctx, "systemctl", "is-active", update.ServiceUnit)) == "active"
		}
	}
	if !service {
		c.State, c.Detail = OK, "cron runs `once` every minute · "+o.Layout.Cron
		return c, true
	}
	c.State = Warn
	c.Detail = "both the systemd service and " + o.Layout.Cron + " run the agent — the cron run steps aside every minute; keep one"
	if o.Root {
		c.FixMsg = "remove " + o.Layout.Cron + " (the service stays)"
		c.Fix = func(context.Context) error { return removeFile(o.Layout.Cron) }
	}
	return c, true
}

// removeFile deletes a plain file under a closed parent, never through a
// symlink in its place.
func removeFile(path string) error {
	if err := closedParent(path); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a plain file", path)
	}
	return os.Remove(path)
}

// accepted reports how WatchFor answered the agent's last push, from the
// state the daemon (or the last `once`) recorded.
func accepted(o Options) Check {
	c := Check{Name: "WatchFor accepts this host"}
	dir := o.Config.Spool.Dir
	st, ok := agent.ReadPushState(dir)
	if !ok {
		if _, err := os.ReadDir(dir); errors.Is(err, os.ErrPermission) {
			c.State, c.Detail = Skipped, "the state folder is private to the agent — run as root to see"
			return c
		}
		if rec, sticky := rejection(o); sticky {
			c.State, c.Detail = Fail, fmt.Sprintf("WatchFor rejected this host's token %d times since %s — the host was removed in WatchFor or its token rotated; paste the new one in Settings → Server", rec.Count, rec.First.Local().Format("2006-01-02 15:04"))
			return c
		}
		c.State, c.Detail = Skipped, "no push recorded yet"
		return c
	}
	since := " since " + st.Since.Local().Format("2006-01-02 15:04")
	switch st.Result {
	case agent.PushAccepted:
		c.State, c.Detail = OK, "batches accepted"+since
	case agent.PushTokenRejected:
		c.State, c.Detail = Fail, "WatchFor does not know this host's token"+since+" — the host was removed in WatchFor or its token rotated; paste the new one in Settings → Server"
	case agent.PushPlanPaused:
		c.State, c.Detail = Warn, "WatchFor paused this host"+since+": the plan does not include server monitoring, or the organization is over its host limit — the agent asks again every 30 minutes and resumes by itself"
	case agent.PushNoAnswer:
		c.State, c.Detail = Warn, "no answer from WatchFor"+since+" — batches wait in the spool and are sent when it answers"
	case agent.PushRefused:
		c.State, c.Detail = Warn, "WatchFor refused a batch"+since+" — the log says why (journalctl -u watchfor-agent)"
	default:
		c.State, c.Detail = Skipped, "unknown state "+st.Result
	}
	return c
}

// rejection is the rejected-token record for the token configured now.
func rejection(o Options) (agent.Rejection, bool) {
	tok := o.Config.Server.Token
	if o.Config.Server.TokenFile != "" {
		b, err := os.ReadFile(o.Config.Server.TokenFile)
		if err != nil {
			return agent.Rejection{}, false
		}
		tok = strings.TrimSpace(string(b))
	}
	if tok == "" || strings.HasPrefix(tok, "env:") {
		return agent.Rejection{}, false
	}
	rec, ok := agent.ReadRejection(o.Config.Spool.Dir, push.Fingerprint(tok))
	return rec, ok && rec.Sticky()
}
