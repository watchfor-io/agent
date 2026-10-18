//go:build linux

package health

import (
	"context"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/agent"
	"github.com/watchfor-io/agent/internal/config"
	"github.com/watchfor-io/agent/internal/push"
)

// recorder is an Exec that answers from replies and remembers each call.
type recorder struct {
	replies map[string]string
	calls   []string
}

func (r *recorder) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, line)
	return []byte(r.replies[line]), nil
}

func agentAccount(uid string) func(string) (*user.User, error) {
	return func(string) (*user.User, error) {
		return &user.User{Username: ServiceUser, Uid: uid, Gid: uid}, nil
	}
}

// The field report: mailcow's SOGo runs as uid 999 in its container, the
// host gave watchfor-agent 999, ps said "watchfor-agent". Health names the
// processes, explains the risk and, as root, moves the account to a quiet
// uid; the agent's own processes never count.
func TestUIDShareFoundAndMoved(t *testing.T) {
	sogo := Proc{UID: 999, Comm: "sogod", Container: "docker:3f2a9c1b7d4e"}
	procs := []Proc{{PID: 1, UID: 999, Comm: "watchfor-agent"}}
	for i := range 21 {
		p := sogo
		p.PID = 100 + i
		procs = append(procs, p)
	}
	rec := &recorder{replies: map[string]string{"systemctl is-active watchfor-agent.service": "active"}}
	o := Options{Root: true, Exec: rec.exec,
		LookupUser:      agentAccount("999"),
		LookupGroup:     func(string) (*user.Group, error) { return &user.Group{Name: ServiceUser, Gid: "999"}, nil },
		HaveSystemctl:   func() bool { return true },
		ProcsOf:         func(int) ([]Proc, error) { return procs, nil },
		ProcUIDs:        func() (map[int]bool, error) { return map[int]bool{0: true, 999: true, QuietUIDMin: true}, nil },
		AccountsWithUID: func(int) []string { return []string{ServiceUser} },
		IDTaken:         func(id int) bool { return id == QuietUIDMin+1 },
	}
	c := uidShare(o)
	if c.State != Warn || !strings.Contains(c.Detail, "sogod ×21 in docker:3f2a9c1b7d4e") || !strings.Contains(c.Detail, "21 processes") {
		t.Fatalf("uid share = %v: %s", c.State, c.Detail)
	}
	want := QuietUIDMin + 2 // QuietUIDMin is a running process's uid, +1 an account's
	if c.Fix == nil || !strings.Contains(c.FixMsg, "uid/gid 60580") {
		t.Fatalf("fix = %q, want a move to %d", c.FixMsg, want)
	}
	if err := c.Fix(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(rec.calls, "\n")
	for _, step := range []string{"systemctl stop watchfor-agent.service", "groupmod -g 60580 watchfor-agent", "usermod -u 60580 -g 60580 watchfor-agent", "systemctl start watchfor-agent.service"} {
		if !strings.Contains(got, step) {
			t.Errorf("the move did not run %q; calls:\n%s", step, got)
		}
	}
	if strings.Index(got, "stop") > strings.Index(got, "usermod") || strings.Index(got, "usermod") > strings.Index(got, "start") {
		t.Errorf("stop → usermod → start out of order:\n%s", got)
	}

	// without root it explains, offers nothing
	o.Root = false
	if c := uidShare(o); c.Fix != nil || !strings.Contains(c.Detail, "run as root") {
		t.Errorf("non-root: %+v", c)
	}
	// only the agent itself: fine
	o.ProcsOf = func(int) ([]Proc, error) { return procs[:1], nil }
	if c := uidShare(o); c.State != OK {
		t.Errorf("agent alone = %v: %s", c.State, c.Detail)
	}
	// a second account with the same uid is the same problem
	o.AccountsWithUID = func(int) []string { return []string{"sogo", ServiceUser} }
	if c := uidShare(o); c.State != Warn || !strings.Contains(c.Detail, "sogo") {
		t.Errorf("duplicate account = %v: %s", c.State, c.Detail)
	}
}

func TestPickUIDStaysInTheQuietBand(t *testing.T) {
	if id, ok := PickUID(nil, func(int) bool { return false }); !ok || id != QuietUIDMin {
		t.Errorf("empty machine: %d %v", id, ok)
	}
	if _, ok := PickUID(nil, func(int) bool { return true }); ok {
		t.Error("a full band must say so, not wander off")
	}
	for _, common := range []int{0, 33, 101, 999, 998, 1000, 1001, 65532, 65534} {
		if common >= QuietUIDMin && common <= QuietUIDMax {
			t.Errorf("uid %d, common in container images, is inside the quiet band", common)
		}
	}
}

// Service and cron both installed: said, and as root the cron entry goes.
func TestServiceAndCronBoth(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cron := filepath.Join(dir, "watchfor-agent")
	unit := filepath.Join(dir, "watchfor-agent.service")
	for _, p := range []string{cron, unit} {
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{replies: map[string]string{"systemctl is-enabled watchfor-agent.service": "enabled"}}
	o := Options{Root: true, Exec: rec.exec, Layout: Layout{Cron: cron}, UnitFile: unit, HaveSystemctl: func() bool { return true }}
	c, ok := schedule(context.Background(), o)
	if !ok || c.State != Warn || c.Fix == nil {
		t.Fatalf("both installed = %+v", c)
	}
	if err := c.Fix(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cron); !os.IsNotExist(err) {
		t.Error("the cron entry is still there")
	}
	if _, ok := schedule(context.Background(), o); ok {
		t.Error("no cron entry should mean no line")
	}
	// cron only: fine
	if err := os.WriteFile(cron, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec.replies = nil
	if c, _ := schedule(context.Background(), o); c.State != OK {
		t.Errorf("cron only = %+v", c)
	}
}

func withState(t *testing.T) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte("wfh_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseText("server:\n  url: https://ingest.watchfor.io\n  token_file: " + tok + "\nspool:\n  dir: " + dir + "\n")
	if err != nil {
		t.Fatal(err)
	}
	return cfg, dir
}

// "WatchFor accepts this host" reads what the agent recorded.
func TestAcceptedLine(t *testing.T) {
	cfg, dir := withState(t)
	o := Options{Config: cfg}
	if c := accepted(o); c.State != Skipped {
		t.Errorf("nothing recorded = %+v", c)
	}
	for result, want := range map[string]State{agent.PushAccepted: OK, agent.PushPlanPaused: Warn, agent.PushTokenRejected: Fail, agent.PushNoAnswer: Warn} {
		if err := os.WriteFile(filepath.Join(dir, agent.PushStateFile), []byte(result+" 2026-10-18T10:00:00Z\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if c := accepted(o); c.State != want {
			t.Errorf("%s = %v (%s), want %v", result, c.State, c.Detail, want)
		}
	}
}

// After exit 3 "start it again" would only exit 3 again: no such fix while
// the token is the rejected one; offered once the token has changed.
func TestExit3OffersNoPointlessRestart(t *testing.T) {
	cfg, dir := withState(t)
	unitFile := filepath.Join(dir, "watchfor-agent.service")
	if err := os.WriteFile(unitFile, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-3 * time.Hour)
	for i := range 6 {
		if _, err := agent.RecordRejection(dir, push.Fingerprint("wfh_x"), "token_unknown", t0.Add(time.Duration(i)*30*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{replies: map[string]string{
		"systemctl is-active watchfor-agent":                      "failed",
		"systemctl show -p ExecMainStatus --value watchfor-agent": "3",
	}}
	o := Options{Config: cfg, Root: true, Exec: rec.exec, UnitFile: unitFile, HaveSystemctl: func() bool { return true }, Layout: Layout{Token: cfg.Server.TokenFile}}
	if c := unit(context.Background(), o); c.State != Fail || c.Fix != nil || !strings.Contains(c.Detail, "exit 3") {
		t.Errorf("exit 3, same token = %+v", c)
	}
	if err := os.WriteFile(cfg.Server.TokenFile, []byte("wfh_new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := unit(context.Background(), o); c.Fix == nil {
		t.Errorf("exit 3, new token: no restart offered (%s)", c.Detail)
	}
}

// The reachability check trusts what the agent trusts: with ca_file, that
// CA and nothing else.
func TestReachableHonoursCAFile(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshake is the point
	srv.StartTLS()
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	ctx := context.Background()
	if err := tlsDial(ctx, "tcp", addr, ""); err == nil {
		t.Error("a self-signed server passed against the system roots")
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tlsDial(ctx, "tcp", addr, ca); err != nil {
		t.Errorf("with its CA as ca_file: %v", err)
	}
}

// health shows the rejected-token record; a stale one (written by 0.8,
// for a plan pause, or before the token file) comes with a fix that
// removes it and starts a service it had stopped.
func TestRejectedRecordCheck(t *testing.T) {
	cfg, dir := withState(t)
	rec := &recorder{replies: map[string]string{
		"systemctl is-active watchfor-agent":                      "inactive",
		"systemctl show -p ExecMainStatus --value watchfor-agent": "3",
	}}
	o := Options{Config: cfg, Root: true, Exec: rec.exec, HaveSystemctl: func() bool { return true }, Layout: Layout{Spool: dir, Token: cfg.Server.TokenFile}}
	if _, ok := rejectedRecord(context.Background(), o); ok {
		t.Fatal("no record, but a check")
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(cfg.Server.TokenFile, old, old); err != nil {
		t.Fatal(err)
	}
	// written by 0.8.x: no reason field
	t0 := time.Now().Add(-3 * time.Hour).UTC()
	legacy := push.Fingerprint("wfh_x") + " 5 " + t0.Format(time.RFC3339) + " " + t0.Add(2*time.Hour).Format(time.RFC3339) + "\n"
	if err := os.WriteFile(filepath.Join(dir, agent.RejectedMarker), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	c, ok := rejectedRecord(context.Background(), o)
	if !ok || c.State != Warn || c.Fix == nil || !strings.Contains(c.Detail, "before 0.9") {
		t.Fatalf("legacy record = %+v", c)
	}
	if err := c.Fix(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, agent.RejectedMarker)); !os.IsNotExist(err) {
		t.Fatal("the fix left the record")
	}
	if !strings.Contains(strings.Join(rec.calls, ";"), "systemctl restart watchfor-agent") {
		t.Fatalf("the service stopped over it was not started: %v", rec.calls)
	}
	// A confirmed rejection of the current token: fail, no automatic fix.
	for i := range 6 {
		if _, err := agent.RecordRejection(dir, push.Fingerprint("wfh_x"), "token_unknown", t0.Add(time.Duration(i)*30*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if c, ok := rejectedRecord(context.Background(), o); !ok || c.State != Fail || c.Fix != nil || !strings.Contains(c.Detail, "does not know this token") {
		t.Fatalf("sticky record = %+v", c)
	}
}

// An agent.yml others can write is a failure, not a warning: whoever can
// change it can redirect the agent and receive the token. The installed
// file gets the root fix; a world-writable token folder fails too.
func TestWritableConfigAndTokenFolderFail(t *testing.T) {
	cfg, dir := withState(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent.yml")
	if err := os.WriteFile(path, []byte("server:\n  url: https://ingest.watchfor.io\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := Options{ConfigPath: path, Config: cfg, Layout: Layout{Config: path, Token: cfg.Server.TokenFile}}
	if c := configFile(o); c.State != OK {
		t.Fatalf("0644 = %+v", c)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if c := configFile(o); c.State != Fail || c.Fix == nil || !strings.Contains(c.FixMsg, "root") {
		t.Errorf("0666, installed path = %+v", c)
	}
	o.Layout.Config = "/etc/watchfor-agent/agent.yml"
	if c := configFile(o); c.State != Fail || c.Fix != nil {
		t.Errorf("0666 elsewhere = %+v (no repair outside the installed layout)", c)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if c := configFile(o); c.State != Fail || !strings.Contains(c.Detail, "folder") {
		t.Errorf("world-writable folder, agent.yml = %+v", c)
	}
	if c := tokenFile(o); c.State != Fail || c.Fix == nil {
		t.Errorf("world-writable folder, token = %+v", c)
	}
}
