package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

const (
	// DefaultUnit is the systemd service restarted after a successful swap.
	DefaultUnit = "watchfor-agent"
	binaryName  = "watchfor-agent"

	maxChecksums = 64 << 10
	maxSignature = 4 << 10
	maxArchive   = 64 << 20

	// ExitUpdateAvailable is what `upgrade -check` exits with when a newer
	// release exists — scripts can branch on it without parsing output.
	ExitUpdateAvailable = 10
)

// Options drive one upgrade. Zero values mean: resolve the target from
// the server's hint, then the release API; install to the running binary;
// restart the service if it is active.
type Options struct {
	// Current is the running version (main.Version).
	Current string
	// Target pins a version ("0.3.0" or "v0.3.0"). Empty = resolve.
	Target string
	// StateDir is where the daemon leaves its update-available hint (the
	// spool directory).
	StateDir string
	// IfAvailable acts only on the daemon's hint and never asks the release
	// API — the update timer runs this way so hosts stay quiet.
	IfAvailable bool
	// AllowDowngrade permits installing a lower version than Current.
	AllowDowngrade bool
	// Restart runs `systemctl restart <Unit>` when the unit is active.
	Restart bool
	// ExePath is the binary to replace; default: the running executable.
	ExePath string
	// Unit is the systemd service name; default DefaultUnit.
	Unit string
	// OS and Arch select the release archive; default: this build's.
	OS, Arch string
	Source   *Source
	Out      io.Writer
	// Exec runs a command and returns its combined output (tests stub it).
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

	publicKey string // tests sign with their own key; production uses PublicKey
}

// Status is what `upgrade -check` reports.
type Status struct {
	Current   string
	Latest    string // "" when nothing is known
	Origin    string // "explicit", "server", "watchfor.io" or "none"
	Available bool
}

// Result describes what Run did.
type Result struct {
	From, To  string
	Updated   bool
	Restarted bool
}

func (o Options) withDefaults() Options {
	if o.Unit == "" {
		o.Unit = DefaultUnit
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if o.Source == nil {
		o.Source = NewSource()
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Exec == nil {
		o.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	if o.publicKey == "" {
		o.publicKey = PublicKey
	}
	return o
}

// Resolve decides which version to install: an explicit Target, else the
// version the server reported to the daemon, else (unless IfAvailable)
// the newest release watchfor.io names. An empty target means "nothing to do".
func Resolve(ctx context.Context, o Options) (target, origin string, err error) {
	o = o.withDefaults()
	if o.Target != "" {
		v, err := Parse(o.Target)
		if err != nil {
			return "", "", err
		}
		return v.String(), "explicit", nil
	}
	if v, _, ok := ReadState(o.StateDir); ok {
		return v, "server", nil
	}
	if o.IfAvailable {
		return "", "none", nil
	}
	v, err := o.Source.Latest(ctx)
	if err != nil {
		return "", "", err
	}
	return v, "watchfor.io", nil
}

// Check compares the running version with the resolved target.
func Check(ctx context.Context, o Options) (Status, error) {
	target, origin, err := Resolve(ctx, o)
	if err != nil {
		return Status{}, err
	}
	st := Status{Current: o.Current, Latest: target, Origin: origin}
	if target == "" {
		return st, nil
	}
	cur, err := Parse(o.Current)
	if err != nil {
		// A development build: any release is "available" only if asked for.
		st.Available = o.Target != ""
		return st, nil
	}
	tv, _ := Parse(target)
	st.Available = Compare(tv, cur) > 0
	return st, nil
}

// Run performs the upgrade. The order is deliberate: the signed checksums
// file is fetched and verified first, the archive must match it, the new
// binary must identify itself as the expected version, and only then is
// it renamed over the old one (atomic on the same filesystem).
func Run(ctx context.Context, o Options) (Result, error) {
	o = o.withDefaults()
	res := Result{From: o.Current}

	target, origin, err := Resolve(ctx, o)
	if err != nil {
		return res, err
	}
	if target == "" {
		fmt.Fprintln(o.Out, "no update reported by the server; nothing to do")
		return res, nil
	}
	res.To = target
	if cur, err := Parse(o.Current); err == nil {
		tv, _ := Parse(target)
		switch c := Compare(tv, cur); {
		case c == 0:
			fmt.Fprintf(o.Out, "watchfor-agent %s is already installed\n", target)
			return res, nil
		case c < 0 && !o.AllowDowngrade:
			return res, fmt.Errorf("refusing to downgrade %s → %s (use -allow-downgrade)", o.Current, target)
		}
	} else if o.Target == "" {
		return res, fmt.Errorf("running a development build (%q): pass -version to install a release", o.Current)
	}

	exe := o.ExePath
	if exe == "" {
		if exe, err = os.Executable(); err != nil {
			return res, err
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return res, err
		}
	}
	unlock, err := lock()
	if err != nil {
		return res, err
	}
	defer unlock()

	// Fail fast on permissions, before any download.
	tmp := filepath.Join(filepath.Dir(exe), "."+binaryName+".upgrade."+strconv.Itoa(os.Getpid()))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return res, fmt.Errorf("cannot write to %s: run as root (sudo)", filepath.Dir(exe))
		}
		return res, err
	}
	defer os.Remove(tmp) // no-op once renamed

	fmt.Fprintf(o.Out, "upgrading watchfor-agent %s → %s (version from %s)\n", o.Current, target, origin)
	sums, err := o.Source.Asset(ctx, target, "checksums.txt", maxChecksums)
	if err != nil {
		f.Close()
		return res, err
	}
	sig, err := o.Source.Asset(ctx, target, "checksums.txt.minisig", maxSignature)
	if err != nil {
		f.Close()
		return res, err
	}
	trusted, err := verifyMinisign(o.publicKey, sig, sums)
	if err != nil {
		f.Close()
		return res, fmt.Errorf("release %s: %w", target, err)
	}
	if !strings.Contains(" "+trusted+" ", " "+target+" ") {
		f.Close()
		return res, fmt.Errorf("release %s: signed checksums are for %q", target, trusted)
	}
	name := fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, target, o.OS, o.Arch)
	want, ok := parseChecksums(sums)[name]
	if !ok {
		f.Close()
		return res, fmt.Errorf("release %s has no build for %s/%s", target, o.OS, o.Arch)
	}
	fmt.Fprintf(o.Out, "verified signature; downloading %s\n", name)
	tgz, err := o.Source.Asset(ctx, target, name, maxArchive)
	if err != nil {
		f.Close()
		return res, err
	}
	if sha256Hex(tgz) != want {
		f.Close()
		return res, fmt.Errorf("%s: checksum mismatch", name)
	}
	bin, err := extractFile(tgz, binaryName, maxArchive)
	if err != nil {
		f.Close()
		return res, err
	}
	if _, err := f.Write(bin); err != nil {
		f.Close()
		return res, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return res, err
	}
	if err := f.Close(); err != nil {
		return res, err
	}
	out, err := o.Exec(ctx, tmp, "version")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), binaryName+" "+target) {
		return res, fmt.Errorf("downloaded binary does not report %s (got %q)", target, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, exe); err != nil {
		return res, err
	}
	res.Updated = true
	_ = ClearState(o.StateDir)
	fmt.Fprintf(o.Out, "installed watchfor-agent %s at %s\n", target, exe)

	if o.Restart {
		if _, err := o.Exec(ctx, "systemctl", "is-active", "--quiet", o.Unit); err == nil {
			if out, err := o.Exec(ctx, "systemctl", "restart", o.Unit); err != nil {
				return res, fmt.Errorf("installed, but restarting %s failed: %s", o.Unit, strings.TrimSpace(string(out)))
			}
			res.Restarted = true
			fmt.Fprintf(o.Out, "restarted %s\n", o.Unit)
		}
	}
	return res, nil
}

// lock serialises upgrades on one host; a second concurrent run fails
// instead of racing the rename.
func lock() (func(), error) {
	// /run/lock is shared by a manual `sudo watchfor-agent upgrade` and the
	// update timer's service (PrivateTmp would split /tmp between them).
	dir := "/run/lock"
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		dir = os.TempDir()
	}
	f, err := os.OpenFile(filepath.Join(dir, binaryName+"-upgrade.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		f, err = os.OpenFile(filepath.Join(os.TempDir(), binaryName+"-upgrade.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another upgrade is running")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
