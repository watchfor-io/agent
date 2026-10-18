//go:build linux

package processes

import (
	"os/user"
	"strconv"

	"github.com/watchfor-io/agent/internal/procfs"
)

// owners turns a process's uid into the user label. The name comes from
// this machine's /etc/passwd, which is only true for processes that live
// here: a container without a user namespace runs its own uid 999 as host
// uid 999, and the host's name for 999 (often whichever account
// `useradd --system` created last — this agent's, say) would be a lie.
// So a process in another container is reported by its number, "999",
// with the container named beside it — the same as ps shows a uid this
// machine has no name for.
type owners struct {
	names map[int]string
	self  procfs.Container // the agent's own container: its neighbours share its passwd
	// where caches each pid's container by start time and name: a cgroup
	// read per process per tick would cost thousands of opens on a busy
	// host, and a process changes cgroup only before it execs its payload
	// (runc init joins first), which the name change catches.
	where map[int]placed
}

type placed struct {
	start uint64
	comm  string
	in    procfs.Container
}

func newOwners() *owners {
	o := &owners{names: map[int]string{}, where: map[int]placed{}}
	o.self, _ = procfs.ReadContainer("self")
	return o
}

// container is where p runs, empty when that is here (the host, or the
// agent's own container).
func (o *owners) container(p procfs.Process) string {
	if c, ok := o.where[p.PID]; ok && c.start == p.StartTicks && c.comm == p.Comm {
		return c.in.String()
	}
	in, err := procfs.ReadContainerPID(p.PID)
	if err != nil || in == o.self {
		in = procfs.Container{}
	}
	o.where[p.PID] = placed{start: p.StartTicks, comm: p.Comm, in: in}
	return in.String()
}

// user is the label for p's uid: the host's account name for a host
// process, the bare number for one in another container.
func (o *owners) user(p procfs.Process) string {
	if p.UID < 0 {
		return ""
	}
	if o.container(p) != "" {
		return strconv.Itoa(p.UID)
	}
	return o.name(p.UID)
}

func (o *owners) name(uid int) string {
	if name, ok := o.names[uid]; ok {
		return name
	}
	name := strconv.Itoa(uid)
	if u, err := user.LookupId(name); err == nil {
		name = procfs.Clean(u.Username)
	}
	o.names[uid] = name
	return name
}

// forget drops the cache entries of processes that are gone, so the map
// follows the process table instead of growing with every pid ever seen.
func (o *owners) forget(alive map[int]cpuKey) {
	for pid := range o.where {
		if _, ok := alive[pid]; !ok {
			delete(o.where, pid)
		}
	}
}
