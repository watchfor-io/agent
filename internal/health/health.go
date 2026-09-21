//go:build linux

// Package health answers "is this agent installed properly?" — the
// questions someone would otherwise ask by hand: are the files where they
// should be, may the agent read its token and write its spool, is the
// service running, can it reach WatchFor. Each answer carries a fix when
// there is one that is safe to apply.
package health

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/update"
)

// State is the outcome of one Check.
type State int

const (
	// OK means the check passed.
	OK State = iota
	// Warn means something is off but the agent can still do its job.
	Warn
	// Fail means the agent cannot work until this is put right.
	Fail
	// Skipped means the check had nothing to check against, usually
	// because there is no config.
	Skipped
)

// String is the word the report prints for the state.
func (s State) String() string {
	switch s {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	case Fail:
		return "fail"
	}
	return "skipped"
}

// Check is one answer. Fix is nil when nothing can be done automatically.
type Check struct {
	Name   string
	State  State
	Detail string
	Fix    func(ctx context.Context) error
	FixMsg string // what the fix would do, in plain words
}

// ServiceUser is the unprivileged account the agent runs as.
const ServiceUser = "watchfor-agent"

// Layout is where the installer puts things. Repairs run as root, and
// root touches nothing but these paths: a file that agent.yml points at
// somewhere else is reported, never repaired — on a machine with many
// users, a path the operator can be talked into is a path an attacker
// can prepare.
type Layout struct {
	Config, Token, Spool, Binary string
}

// Installed is the layout install.sh creates.
var Installed = Layout{ // #nosec G101 -- paths, not credentials
	Config: "/etc/watchfor-agent/agent.yml",
	Token:  "/etc/watchfor-agent/token",
	Spool:  "/var/lib/watchfor-agent",
	Binary: "/usr/local/bin/watchfor-agent",
}

// standard says whether path is exactly the installer's — compared as a
// cleaned string, never resolved: resolving is what an attacker wants.
func standard(path, want string) bool { return want != "" && filepath.Clean(path) == want }

// elsewhere is what a check says instead of offering a fix.
const elsewhere = " — not the installed location, so it is not repaired from here"

// Exec runs a command; the screen passes its own so tests can stub it.
type Exec = update.Exec

// Options describe the installation under test — the config path, what
// it parsed to, the running version — and how the checks ask the machine,
// replaceable so tests answer instead.
type Options struct {
	ConfigPath string
	Config     *config.Config
	ConfigErr  error
	Version    string
	Exec       Exec
	// Now root: fixes that need it are offered rather than explained.
	Root bool
	// Deep also compares the binary on disk with the signed release. It
	// downloads the release archive, so it is asked for by hand.
	Deep bool

	// Layout is the only set of paths a repair may touch; the zero value
	// is Installed. Tests point it at a temporary directory.
	Layout Layout

	// What the checks ask of the machine, replaceable so that tests ask a
	// stand-in. Nil means the real thing.
	Dial          func(ctx context.Context, network, addr string) error // a TLS connection to the server, closed at once
	LookupUser    func(name string) (*user.User, error)
	HaveSystemctl func() bool
	UnitFile      string // the service unit; default under update.UnitDir
}

// Run performs every check, in the order a person would.
func Run(ctx context.Context, o Options) []Check {
	if o.Exec == nil {
		o.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	if o.Dial == nil {
		o.Dial = tlsDial
	}
	if o.LookupUser == nil {
		o.LookupUser = user.Lookup
	}
	if o.HaveSystemctl == nil {
		o.HaveSystemctl = func() bool { _, err := exec.LookPath("systemctl"); return err == nil }
	}
	if o.UnitFile == "" {
		o.UnitFile = filepath.Join(update.UnitDir, "watchfor-agent.service")
	}
	if o.Layout == (Layout{}) {
		o.Layout = Installed
	}
	skip := func(name string) Check {
		return Check{Name: name, State: Skipped, Detail: "no config to check against"}
	}
	out := []Check{configFile(o), binary(o), serviceUser(o)}
	if o.Config != nil {
		out = append(out, tokenFile(o), spoolDir(o))
	} else {
		out = append(out, skip("host token"), skip("spool folder"))
	}
	if o.Deep {
		out = append(out, releaseBinary(ctx, o))
	}
	out = append(out, unit(ctx, o), timer(ctx, o))
	if o.Config != nil {
		out = append(out, reachable(ctx, o))
	} else {
		out = append(out, skip("WatchFor reachable"))
	}
	return out
}

func configFile(o Options) Check {
	c := Check{Name: "config file"}
	st, err := os.Stat(o.ConfigPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		c.State, c.Detail = Fail, "missing — Settings → The file writes one"
		return c
	case err != nil:
		c.State, c.Detail = Fail, err.Error()
		return c
	case o.ConfigErr != nil:
		c.State, c.Detail = Fail, "the agent would not accept it: "+o.ConfigErr.Error()
		return c
	}
	inline := o.Config != nil && o.Config.Server.Token != "" && !strings.HasPrefix(o.Config.Server.Token, "env:")
	switch {
	case inline && st.Mode().Perm()&0o044 != 0:
		c.State = Fail
		c.Detail = fmt.Sprintf("holds the token inline and is readable by others (%04o)", st.Mode().Perm())
		if standard(o.ConfigPath, o.Layout.Config) {
			c.FixMsg = "set it to 0640, root and the service group"
			c.Fix = func(context.Context) error { return GroupOnly(o.ConfigPath, ServiceUser) }
		} else {
			c.Detail += elsewhere
		}
		return c
	case st.Mode().Perm()&0o022 != 0:
		c.State = Warn
		c.Detail = fmt.Sprintf("writable by others (%04o)", st.Mode().Perm())
		if standard(o.ConfigPath, o.Layout.Config) {
			c.FixMsg = "set it to 0644"
			c.Fix = func(context.Context) error { return mode(o.ConfigPath, 0o644) }
		} else {
			c.Detail += elsewhere
		}
		return c
	}
	c.State, c.Detail = OK, fmt.Sprintf("reads cleanly · %04o", st.Mode().Perm())
	return c
}

func binary(o Options) Check {
	c := Check{Name: "agent binary"}
	exe, err := os.Executable()
	if err != nil {
		c.State, c.Detail = Warn, err.Error()
		return c
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	st, err := os.Stat(exe)
	if err != nil {
		c.State, c.Detail = Fail, err.Error()
		return c
	}
	if st.Mode().Perm()&0o022 != 0 {
		c.State = Fail
		c.Detail = fmt.Sprintf("writable by others (%04o) — anyone could replace it", st.Mode().Perm())
		if standard(exe, o.Layout.Binary) {
			c.FixMsg = "set it to 0755"
			c.Fix = func(context.Context) error { return mode(exe, 0o755) }
		} else {
			c.Detail += elsewhere
		}
		return c
	}
	c.State, c.Detail = OK, fmt.Sprintf("%s · %s", o.Version, exe)
	return c
}

// releaseBinary answers the question nothing local can: is the file on
// disk the one WatchFor signed? The checksums are verified against the key
// built into this binary, so a tampered download cannot claim to match.
func releaseBinary(ctx context.Context, o Options) Check {
	c := Check{Name: "release checksum"}
	if _, err := update.Parse(o.Version); err != nil {
		c.State, c.Detail = Skipped, "a development build has no published release to compare against"
		return c
	}
	exe, err := os.Executable()
	if err != nil {
		c.State, c.Detail = Warn, err.Error()
		return c
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	got, err := update.FileDigest(exe)
	if err != nil {
		c.State, c.Detail = Fail, err.Error()
		return c
	}
	want, err := update.BinaryDigest(ctx, update.Options{Current: o.Version}, o.Version)
	if err != nil {
		c.State, c.Detail = Warn, "could not read the signed release: "+err.Error()
		return c
	}
	if got != want {
		c.State = Fail
		c.Detail = "not the binary signed for " + o.Version + " — reinstall it"
		return c
	}
	c.State, c.Detail = OK, "matches the signed release · "+got[:12]+"…"
	return c
}

func serviceUser(o Options) Check {
	c := Check{Name: "service user"}
	u, err := o.LookupUser(ServiceUser)
	if err != nil {
		c.State, c.Detail = Warn, "no "+ServiceUser+" account — fine for a cron install"
		if o.Root {
			c.FixMsg = "create the system user"
			c.Fix = func(ctx context.Context) error {
				_, err := o.Exec(ctx, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", ServiceUser)
				return err
			}
		}
		return c
	}
	c.State, c.Detail = OK, fmt.Sprintf("%s (uid %s)", u.Username, u.Uid)
	return c
}

func tokenFile(o Options) Check {
	c := Check{Name: "host token"}
	path := o.Config.Server.TokenFile
	if path == "" {
		if o.Config.Server.Token != "" {
			c.State, c.Detail = Warn, "kept inside agent.yml — a 0600 token_file is safer"
			return c
		}
		c.State, c.Detail = Fail, "none — the agent cannot send (Settings → Server)"
		return c
	}
	st, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		c.State, c.Detail = Fail, "missing — paste the token in Settings → Server"
		return c
	case err != nil:
		c.State, c.Detail = Fail, err.Error()
		return c
	case st.Mode()&os.ModeSymlink != 0:
		c.State, c.Detail = Fail, "a symlink — the token must be a plain file, nothing here follows links"
		return c
	case !st.Mode().IsRegular():
		c.State, c.Detail = Fail, "not a plain file"
		return c
	case st.Size() == 0:
		c.State, c.Detail = Fail, "empty — paste the token in Settings → Server"
		return c
	}
	perm := st.Mode().Perm()
	owner := fileOwner(st)
	if perm&0o077 != 0 || !ownerOK(owner) {
		c.State = Fail
		c.Detail = fmt.Sprintf("%04o, owned by %s — only the agent may read it", perm, orUnknown(owner))
		if standard(path, o.Layout.Token) {
			c.FixMsg = "chmod 0600 and give it to " + ServiceUser
			c.Fix = func(context.Context) error { return Private(path, ServiceUser) }
		} else {
			c.Detail += elsewhere
		}
		return c
	}
	c.State, c.Detail = OK, fmt.Sprintf("%04o · %s", perm, path)
	return c
}

func spoolDir(o Options) Check {
	c := Check{Name: "spool folder"}
	dir := o.Config.Spool.Dir
	if dir == "" {
		c.State, c.Detail = Skipped, "no spool configured"
		return c
	}
	st, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		c.State = Fail
		c.Detail = "missing — unsent batches would be lost · " + dir
		if standard(dir, o.Layout.Spool) {
			c.FixMsg = "create it for " + ServiceUser
			c.Fix = func(context.Context) error { return createDir(dir, ServiceUser) }
		} else {
			c.Detail += elsewhere
		}
		return c
	}
	if err != nil {
		c.State, c.Detail = Fail, err.Error()
		return c
	}
	if st.Mode()&os.ModeSymlink != 0 {
		c.State, c.Detail = Fail, "a symlink — the spool must be a real folder, nothing here follows links"
		return c
	}
	if !st.IsDir() {
		c.State, c.Detail = Fail, "not a folder · "+dir
		return c
	}
	owner := fileOwner(st)
	if !ownerOK(owner) {
		c.State = Warn
		c.Detail = fmt.Sprintf("owned by %s, not %s", owner, ServiceUser)
		if standard(dir, o.Layout.Spool) {
			c.FixMsg = "give it to " + ServiceUser
			c.Fix = func(context.Context) error { return ownDir(dir, ServiceUser) }
		} else {
			c.Detail += elsewhere
		}
		return c
	}
	c.State, c.Detail = OK, fmt.Sprintf("%04o · %s", st.Mode().Perm(), dir)
	return c
}

func unit(ctx context.Context, o Options) Check {
	c := Check{Name: "systemd service"}
	if !o.HaveSystemctl() {
		c.State, c.Detail = Skipped, "no systemd on this machine"
		return c
	}
	if _, err := os.Stat(o.UnitFile); errors.Is(err, os.ErrNotExist) {
		c.State, c.Detail = Warn, "no unit file — not installed as a service"
		return c
	}
	active := trim(o.Exec(ctx, "systemctl", "is-active", "watchfor-agent"))
	enabled := trim(o.Exec(ctx, "systemctl", "is-enabled", "watchfor-agent"))
	switch {
	case active == "active" && enabled == "enabled":
		c.State, c.Detail = OK, "running and enabled at boot"
	case active == "active":
		c.State = Warn
		c.Detail = "running, but not enabled at boot (" + enabled + ")"
		c.FixMsg = "enable it at boot"
		c.Fix = fixCmd(o, "systemctl", "enable", "watchfor-agent")
	case active == "failed":
		c.State = Fail
		c.Detail = "the service failed — Maintenance → Service & log shows why"
		c.FixMsg = "start it again"
		c.Fix = fixCmd(o, "systemctl", "restart", "watchfor-agent")
	default:
		c.State = Fail
		c.Detail = "not running (" + active + ") — nothing is being sent"
		c.FixMsg = "start it and enable it at boot"
		c.Fix = fixCmd(o, "systemctl", "enable", "--now", "watchfor-agent")
	}
	return c
}

func timer(ctx context.Context, o Options) Check {
	c := Check{Name: "auto-update timer"}
	if !o.HaveSystemctl() {
		c.State, c.Detail = Skipped, "no systemd on this machine"
		return c
	}
	want := o.Config != nil && o.Config.Updates.Auto != nil && *o.Config.Updates.Auto
	on := trim(o.Exec(ctx, "systemctl", "is-enabled", update.UpdateTimer)) == "enabled"
	switch {
	case want == on && on:
		c.State, c.Detail = OK, "on, as agent.yml says"
	case want == on:
		c.State, c.Detail = OK, "off, as agent.yml says"
	case want:
		c.State = Warn
		c.Detail = "agent.yml asks for daily updates, the timer is off"
		c.FixMsg = "enable the timer"
		c.Fix = func(ctx context.Context) error { return update.EnableTimer(ctx, "", update.Exec(o.Exec)) }
	default:
		c.State = Warn
		c.Detail = "the timer is enabled although agent.yml has auto-update off"
		c.FixMsg = "remove the timer"
		c.Fix = func(ctx context.Context) error { return update.DisableTimer(ctx, "", update.Exec(o.Exec)) }
	}
	return c
}

func reachable(ctx context.Context, o Options) Check {
	c := Check{Name: "WatchFor reachable"}
	raw := o.Config.Server.URL
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if raw == "" {
			c.State, c.Detail = Fail, "server.url is not set"
			return c
		}
		c.State, c.Detail = Fail, "server.url is not a URL: "+raw
		return c
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "http" {
			host += ":80"
		} else {
			host += ":443"
		}
	}
	start := time.Now()
	if err := o.Dial(ctx, "tcp", host); err != nil {
		c.State = Fail
		c.Detail = fmt.Sprintf("cannot reach %s — %v", u.Host, err)
		return c
	}
	c.State, c.Detail = OK, fmt.Sprintf("%s · TLS in %s", u.Host, time.Since(start).Round(time.Millisecond))
	return c
}

// ── helpers ──────────────────────────────────────────────────────────────

func fixCmd(o Options, name string, args ...string) func(context.Context) error {
	return func(ctx context.Context) error {
		out, err := o.Exec(ctx, name, args...)
		if err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, trimBytes(out))
		}
		return nil
	}
}

// tlsDial opens and closes one TLS connection: proof that the host is
// there, the port is open and a certificate is served.
func tlsDial(ctx context.Context, network, addr string) error {
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

func trim(out []byte, err error) string { return trimBytes(out) }

func trimBytes(out []byte) string {
	s := string(out)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func fileOwner(st os.FileInfo) string {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	if u, err := user.LookupId(strconv.FormatUint(uint64(sys.Uid), 10)); err == nil {
		return u.Username
	}
	return strconv.FormatUint(uint64(sys.Uid), 10)
}

// ownerOK says whether an owner may hold the agent's private files. On an
// installed host that is the service account (or root). Before the install,
// or on a machine where someone runs the agent by hand, it is whoever runs
// it — demanding an account that does not exist would be a failure nobody
// could clear.
func ownerOK(owner string) bool {
	if owner == "" || owner == ServiceUser {
		return true
	}
	if _, err := user.Lookup(ServiceUser); err == nil {
		return false // the account exists, so that is who must own it — root's 0600 is unreadable to it
	}
	if owner == "root" {
		return true
	}
	me, err := user.Current()
	return err == nil && (owner == me.Username || owner == me.Uid)
}

// chownTo is a no-op when the service account is not on this machine; the
// caller has already made the file private to the person running the agent.
// ids is the service account's uid and gid; ok is false when there is no
// such account, in which case ownership is left alone.
func ids(username string) (uid, gid int, ok bool, err error) {
	u, err := user.Lookup(username)
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return uid, gid, true, nil
}

// openNoFollow opens path for the checks and fixes below: a symlink is
// refused outright, and everything afterwards works on the descriptor,
// so nothing can be swapped underneath between the look and the act.
func openNoFollow(path string, dir bool) (int, syscall.Stat_t, error) {
	if err := closedParent(path); err != nil {
		return -1, syscall.Stat_t{}, err
	}
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	if dir {
		flags |= syscall.O_DIRECTORY
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return -1, syscall.Stat_t{}, fmt.Errorf("%s is a symlink; refusing to follow it", path)
		}
		return -1, syscall.Stat_t{}, fmt.Errorf("%s: %w", path, err)
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return -1, st, err
	}
	return fd, st, nil
}

// Private makes path a 0600 plain file owned by the service account —
// the shape a token file has to have. Root runs this from `health -fix`
// and the screen, so it must not be steerable: a symlink, a directory or
// a hard-linked file is refused, and the descriptor does the work.
func Private(path, owner string) error {
	fd, st, err := openNoFollow(path, false)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
		return fmt.Errorf("%s is not a plain file", path)
	}
	if err := syscall.Fchmod(fd, 0o600); err != nil {
		return err
	}
	return fchownTo(fd, owner)
}

// GroupOnly makes path 0640, root and the service group: agent.yml when
// it carries the token inline.
func GroupOnly(path, group string) error {
	fd, st, err := openNoFollow(path, false)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
		return fmt.Errorf("%s is not a plain file", path)
	}
	if err := syscall.Fchmod(fd, 0o640); err != nil {
		return err
	}
	_, gid, ok, err := ids(group)
	if err != nil || !ok {
		return err
	}
	return syscall.Fchown(fd, 0, gid)
}

// ownDir hands an existing directory to the service account, by descriptor.
func ownDir(path, owner string) error {
	fd, _, err := openNoFollow(path, true)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	return fchownTo(fd, owner)
}

// closedParent is the ground rule for every repair: the directory the
// path is in must belong to whoever is repairing (root, in production)
// and be writable by nobody else. A parent another user can write is a
// parent where entries can be swapped between the look and the act.
func closedParent(path string) error {
	parent := filepath.Dir(path)
	pst, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("%s: %w", parent, err)
	}
	sys, _ := pst.Sys().(*syscall.Stat_t)
	switch {
	case !pst.IsDir() || sys == nil:
		return fmt.Errorf("%s is not a directory", parent)
	case int(sys.Uid) != os.Geteuid():
		return fmt.Errorf("%s belongs to uid %d, not to whoever is repairing; nothing is changed there", parent, sys.Uid)
	case pst.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s is writable by others; nothing is changed there", parent)
	}
	return nil
}

// mode sets the permission bits of a plain file, by descriptor.
func mode(path string, perm os.FileMode) error {
	fd, st, err := openNoFollow(path, false)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return fmt.Errorf("%s is not a plain file", path)
	}
	return syscall.Fchmod(fd, uint32(perm))
}

// createDir makes the spool directory for the service account, under a
// closed parent only.
func createDir(path, owner string) error {
	if err := closedParent(path); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	return ownDir(path, owner)
}

func fchownTo(fd int, username string) error {
	uid, gid, ok, err := ids(username)
	if err != nil || !ok {
		return err
	}
	return syscall.Fchown(fd, uid, gid)
}

// WritePrivate writes a token file: a fresh 0600 file owned by the
// service account, never through a symlink that was waiting there.
func WritePrivate(path string, data []byte, owner string) error {
	if err := closedParent(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := fchownTo(int(f.Fd()), owner); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown owner"
	}
	return s
}
