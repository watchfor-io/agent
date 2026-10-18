//go:build linux

package processes

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/testutil"
)

// fakeProc writes the four files ReadProcess and ReadContainer read.
func fakeProc(t *testing.T, root string, pid int, comm string, uid int, cgroup string, rssPages int) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"stat":    fmt.Sprintf("%d (%s) S 1 %d %d 0 -1 4194560 0 0 0 0 100 50 0 0 20 0 3 0 %d 1000 %d 0 0 0 0 0 0 0 0 0 0 0 0 17 3 0 0 0 0 0", pid, comm, pid, pid, 1000+pid, rssPages),
		"statm":   fmt.Sprintf("1000 %d 0 0 0 0 0", rssPages),
		"status":  fmt.Sprintf("Name:\t%s\nUid:\t%d\t%d\t%d\t%d\n", comm, uid, uid, uid, uid),
		"cgroup":  cgroup,
		"cmdline": comm,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The field report: sogod in a mailcow container runs as the container's
// uid 999, the host gave 999 to watchfor-agent, and ps and the agent both
// said "watchfor-agent". A process in a container is "uid 999" with the
// container named; a host process keeps its host name.
func TestContainerProcessesAreNotHostUsers(t *testing.T) {
	root := t.TempDir()
	old := procfs.Root
	procfs.Root = root
	t.Cleanup(func() { procfs.Root = old })

	me, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	uid, _ := strconv.Atoi(me.Uid)
	id := strings.Repeat("ab12", 16)
	fakeProc(t, root, 100, "nginx", uid, "0::/system.slice/nginx.service\n", 300)
	fakeProc(t, root, 200, "sogod", uid, "0::/system.slice/docker-"+id+".scope\n", 200)
	fakeProc(t, root, 300, "sogod", uid, "0::/system.slice/docker-"+id+".scope\n", 100)

	m, err := New(testutil.Node(t, "top: 5\nwatch:\n  - name: sogod\n    user: "+me.Username+"\n  - name: sogod\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := metric.NewBatch(time.Now())
	if err := m.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	host, ok := testutil.Find(b, "proc.rss_bytes", metric.Labels{"pid": "100"})
	if !ok || host.L["user"] != me.Username || host.L["container"] != "" {
		t.Errorf("host process labels = %v, want user %s and no container", host.L, me.Username)
	}
	ctr, ok := testutil.Find(b, "proc.rss_bytes", metric.Labels{"pid": "200"})
	if !ok || ctr.L["user"] != me.Uid || ctr.L["container"] != "docker:ab12ab12ab12" {
		t.Errorf("container process labels = %v, want user %s, container docker:ab12ab12ab12", ctr.L, me.Uid)
	}
	if host.L["self"] != "" || ctr.L["self"] != "" {
		t.Error("self on a process that is not the agent")
	}
	// A watch narrowed to the host user no longer counts the container's
	// processes as that user's.
	if s, ok := testutil.Find(b, "procwatch.count", metric.Labels{"watch": "sogod"}); !ok {
		t.Error("no sogod watch")
	} else {
		var counts []float64
		for _, x := range b.Samples() {
			if x.M == "procwatch.count" {
				counts = append(counts, x.V)
			}
		}
		if len(counts) != 2 || counts[0] != 0 || counts[1] != 2 {
			t.Errorf("watch counts = %v (first %v), want [0 2]", counts, s.V)
		}
	}

	// A pid that went away and came back as something else is read again.
	if err := os.RemoveAll(filepath.Join(root, "200")); err != nil {
		t.Fatal(err)
	}
	fakeProc(t, root, 200, "bash", uid, "0::/user.slice/user-1000.slice/session-2.scope\n", 400)
	b = metric.NewBatch(time.Now())
	if err := m.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if s, ok := testutil.Find(b, "proc.rss_bytes", metric.Labels{"pid": "200"}); !ok || s.L["user"] != me.Username || s.L["container"] != "" {
		t.Errorf("reused pid labels = %v, want the host user", s.L)
	}
	// The cache follows the process table.
	if err := os.RemoveAll(filepath.Join(root, "300")); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), metric.NewBatch(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(*module).own.where[300]; ok {
		t.Error("an exited process stays in the cache")
	}
}

// The agent's own process is marked.
func TestSelfLabel(t *testing.T) {
	root := t.TempDir()
	old, oldSelf := procfs.Root, selfPID
	procfs.Root, selfPID = root, 4242
	t.Cleanup(func() { procfs.Root, selfPID = old, oldSelf })
	fakeProc(t, root, 4242, "watchfor-agent", 0, "0::/system.slice/watchfor-agent.service\n", 50)
	m, err := New(testutil.Node(t, "top: 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := metric.NewBatch(time.Now())
	if err := m.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if s, ok := testutil.Find(b, "proc.rss_bytes", metric.Labels{"pid": "4242"}); !ok || s.L["self"] != "true" {
		t.Errorf("agent's own labels = %v, want self=true", s.L)
	}
}

// The agent inside a container sees its neighbours with its own passwd:
// they are not "another container".
func TestOwnContainerIsHome(t *testing.T) {
	root := t.TempDir()
	old := procfs.Root
	procfs.Root = root
	t.Cleanup(func() { procfs.Root = old })
	id := strings.Repeat("cd34", 16)
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "self", "cgroup"), []byte("0::/docker/"+id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeProc(t, root, 10, "app", 0, "0::/docker/"+id+"\n", 10)
	o := newOwners()
	p, err := procfs.ReadProcess(10)
	if err != nil {
		t.Fatal(err)
	}
	if c := o.container(p); c != "" {
		t.Errorf("own container = %q, want home", c)
	}
	if u := o.user(p); u != "root" {
		t.Errorf("user = %q, want root", u)
	}
}
