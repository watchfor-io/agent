//go:build linux

package disk

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/watchfor-io/agent/internal/procfs"
	"github.com/watchfor-io/agent/internal/safefile"
)

// A statfs stuck on a dead hard mount cannot be cancelled, and it does not
// even let its process end: the thread sits in the kernel, so the process
// stays a zombie that never finishes exiting — and whatever waits for it
// (cron's shell, the installer waiting for `config init`) waits for ever.
// A daemon can live with one stuck thread; a command that is meant to end
// cannot. So the short-lived commands (once, check, config init/detect)
// ask a helper process — the agent binary itself, with HelperArg — and
// never wait for one that does not answer: the helper is left behind,
// stuck, and the command ends on time.
//
// Across runs, the mounts that hung are remembered in the state folder
// with the process that is stuck on them (HungFile): while that process is
// still there, nobody asks the mount again, so cron does not leave one
// more stuck helper behind every minute.

// HelperArg is the hidden command that makes the agent binary a statfs
// helper: `watchfor-agent __statfs <mount>…` prints one line per mount.
const HelperArg = "__statfs"

// HungFile is the record of hung mounts in the state folder.
const HungFile = "hung-mounts"

var (
	settingsMu sync.Mutex
	helperExe  string // set: statfs goes through the helper
	helperNet  bool   // only for network and FUSE filesystems
	hungDir    string // set: hung mounts are remembered here
)

// Isolate makes every statfs run in a helper process (exe is the agent
// binary) for modules built after the call — for commands that must end.
func Isolate(exe string) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	helperExe, helperNet = exe, false
}

// IsolateNetwork is Isolate for the filesystems that can hang — network
// and FUSE mounts — only: the daemon asks local disks itself, and a mount
// that hangs then costs it a helper process, not a thread that would keep
// it from ever finishing a stop.
func IsolateNetwork(exe string) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	helperExe, helperNet = exe, true
}

// mayHang is a filesystem whose answers depend on something other than a
// local disk: a server (NFS, SMB, Ceph, Gluster, 9p, AFS, Lustre …) or a
// userspace process (FUSE).
func mayHang(fsType string) bool {
	switch fsType {
	case "cifs", "smb3", "smbfs", "ceph", "glusterfs", "9p", "afs", "lustre", "gpfs", "beegfs", "davfs", "ocfs2", "gfs2", "virtiofs":
		return true
	}
	return strings.HasPrefix(fsType, "nfs") || strings.HasPrefix(fsType, "fuse")
}

// RememberHungIn keeps the record of hung mounts in dir (the state
// folder) for modules built after the call.
func RememberHungIn(dir string) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	hungDir = dir
}

func settings() (exe string, netOnly bool, dir string) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return helperExe, helperNet, hungDir
}

// Helper is the helper's side: statfs each point in turn and print the
// answer at once, so the caller knows which one hangs.
func Helper(points []string) int {
	for _, p := range points {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err != nil {
			var errno syscall.Errno
			if !errors.As(err, &errno) {
				errno = syscall.EIO
			}
			fmt.Printf("err %d\n", int(errno))
			continue
		}
		fmt.Printf("ok %d %d %d %d %d\n", uint64(st.Blocks), uint64(st.Bfree), uint64(st.Bavail), int64(st.Bsize), int64(st.Frsize))
	}
	return 0
}

func parseHelperLine(line string) (syscall.Statfs_t, error) {
	var st syscall.Statfs_t
	f := strings.Fields(line)
	switch {
	case len(f) == 2 && f[0] == "err":
		n, err := strconv.Atoi(f[1])
		if err != nil {
			return st, fmt.Errorf("statfs helper: %q", line)
		}
		return st, syscall.Errno(n)
	case len(f) == 6 && f[0] == "ok":
		// Block counts are unsigned, sizes signed — as in Statfs_t.
		var blocks [3]uint64
		for i := range blocks {
			n, err := strconv.ParseUint(f[i+1], 10, 64)
			if err != nil {
				return st, fmt.Errorf("statfs helper: %q", line)
			}
			blocks[i] = n
		}
		var sizes [2]int64
		for i := range sizes {
			n, err := strconv.ParseInt(f[i+4], 10, 64)
			if err != nil || n < 0 {
				return st, fmt.Errorf("statfs helper: %q", line)
			}
			sizes[i] = n
		}
		st.Blocks, st.Bfree, st.Bavail = blocks[0], blocks[1], blocks[2]
		st.Bsize, st.Frsize = sizes[0], sizes[1]
		return st, nil
	}
	return st, fmt.Errorf("statfs helper: %q", line)
}

// helperAll asks the mounts at idx through helper processes, one at a
// time per helper, each answer within s.timeout. A mount that does not
// answer is marked hung (with the stuck helper's pid) and the rest go to a
// fresh helper. rest is what is left unasked because no helper could be
// started — for the caller to ask itself.
func (s *statfser) helperAll(ctx context.Context, mounts []procfs.Mount, idx []int, out []statResult) (rest []int) {
helpers:
	for len(idx) > 0 {
		if ctx.Err() != nil {
			for _, i := range idx {
				out[i].err = ctx.Err()
			}
			return nil
		}
		args := []string{HelperArg}
		for _, i := range idx {
			args = append(args, mounts[i].Point)
		}
		cmd := exec.Command(s.exe, args...) // #nosec G204 -- the agent's own binary, mount points as arguments
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return idx
		}
		if err := cmd.Start(); err != nil {
			return idx
		}
		lines := make(chan string)
		go func() {
			defer close(lines)
			sc := bufio.NewScanner(pipe)
			for sc.Scan() {
				lines <- sc.Text()
			}
		}()
		done := 0
	read:
		for done < len(idx) {
			t := time.NewTimer(s.timeout)
			select {
			case line, ok := <-lines:
				t.Stop()
				if !ok {
					for _, i := range idx[done:] {
						out[i].err = errors.New("statfs helper ended early")
					}
					break read
				}
				out[idx[done]].st, out[idx[done]].err = parseHelperLine(line)
				if out[idx[done]].err == nil {
					s.answered(mounts[idx[done]].Point)
				}
				done++
			case <-t.C:
				// This mount hangs. The helper is left where it is — killing
				// it does nothing while it sits in the kernel, and waiting for
				// it is what must not happen.
				i := idx[done]
				out[i].err = errHung
				s.hung(mounts[i].Point, cmd.Process.Pid)
				killHelper(cmd.Process)
				go func() { _ = cmd.Wait() }() // reaped if it ever ends
				idx = idx[done+1:]
				continue helpers // the rest go to a fresh helper
			case <-ctx.Done():
				t.Stop()
				_ = cmd.Process.Kill()
				go func() { _ = cmd.Wait() }()
				for _, i := range idx[done:] {
					out[i].err = ctx.Err()
				}
				return nil
			}
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	return nil
}

// killHelper ends a helper that did not answer. Over a hard NFS mount
// SIGKILL works (its waits are killable); in an uninterruptible wait —
// FUSE, some network filesystems — it does nothing, and the record keeps
// later runs from starting another. Tests stand in for the latter.
var killHelper = func(p *os.Process) { _ = p.Kill() }

// hung notes a mount that did not answer: no new call for the backoff in
// this process, and — in the record — none from any run while pid (the
// process stuck on it) is still there.
func (s *statfser) hung(point string, pid int) {
	s.mu.Lock()
	s.hungUntil[point] = s.now().Add(s.backoff)
	s.mu.Unlock()
	if s.store != nil {
		s.store.mark(point, pid)
	}
}

// answered clears a mount's record once it answers again.
func (s *statfser) answered(point string) {
	s.mu.Lock()
	was := s.recorded[point]
	delete(s.recorded, point)
	s.mu.Unlock()
	if was && s.store != nil {
		s.store.clear(point)
	}
}

// hungStore is HungFile: which mount hangs, and which process is stuck on
// it. Written only when that changes.
type hungStore struct {
	dir string
	mu  sync.Mutex
}

type hungRecord struct {
	pid   int
	point string
}

func (h *hungStore) read() []hungRecord {
	b, err := safefile.ReadFile(filepath.Join(h.dir, HungFile), 64<<10)
	if err != nil {
		return nil
	}
	var out []hungRecord
	for _, line := range strings.Split(string(b), "\n") {
		pidText, quoted, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidText)
		point, qerr := strconv.Unquote(quoted)
		if err != nil || qerr != nil || pid <= 0 {
			continue
		}
		out = append(out, hungRecord{pid, point})
	}
	return out
}

func (h *hungStore) write(recs []hungRecord) {
	path := filepath.Join(h.dir, HungFile)
	if len(recs) == 0 {
		_ = os.Remove(path)
		return
	}
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "%d %s\n", r.pid, strconv.Quote(r.point))
	}
	_ = safefile.WriteAtomic(h.dir, HungFile, []byte(b.String()), 0o600)
}

// live maps the mounts a process is still stuck on to that process;
// records of processes that are gone are dropped (and the file rewritten).
func (h *hungStore) live() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	recs := h.read()
	if len(recs) == 0 {
		return nil
	}
	keep := recs[:0]
	set := map[string]int{}
	for _, r := range recs {
		if stuckProcess(r.pid) {
			keep = append(keep, r)
			set[r.point] = r.pid
		}
	}
	if len(keep) != len(recs) {
		h.write(keep)
	}
	return set
}

func (h *hungStore) mark(point string, pid int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	recs := h.read()
	for _, r := range recs {
		if r.point == point && r.pid == pid {
			return
		}
	}
	h.write(append(recs, hungRecord{pid, point}))
}

func (h *hungStore) clear(point string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	recs := h.read()
	kept := recs[:0]
	for _, r := range recs {
		if r.point != point {
			kept = append(kept, r)
		}
	}
	if len(kept) != len(recs) {
		h.write(kept)
	}
}

// stuckProcess: pid is still there and is the agent — a daemon that could
// not finish exiting (its comm), or a helper (its command line) — not a
// recycled pid.
var stuckProcess = func(pid int) bool {
	dir := filepath.Join("/proc", strconv.Itoa(pid)) // the live processes, never a fixture
	if b, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil && strings.TrimSpace(string(b)) == "watchfor-agent" {
		return true
	}
	b, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	return err == nil && strings.Contains(string(b), "\x00"+HelperArg+"\x00")
}

// SelfExe is the binary to run as the helper: this one, by its path while
// that is still this binary, else through /proc/self/exe (an upgrade
// replaced the file meanwhile).
func SelfExe() string {
	exe, err := os.Executable()
	if err != nil || strings.HasSuffix(exe, " (deleted)") {
		return "/proc/self/exe"
	}
	self, err1 := os.Stat("/proc/self/exe")
	file, err2 := os.Stat(exe)
	if err1 != nil || err2 != nil || !os.SameFile(self, file) {
		return "/proc/self/exe"
	}
	return exe
}
