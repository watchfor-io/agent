//go:build linux

package processes

import "testing"

// The kernel cuts a process name at 15 characters. What the person sees,
// and what agent.yml says, should be the whole name.
func TestFullNameBehindTheKernelCut(t *testing.T) {
	cases := []struct {
		g    Running
		full string
		want Watch
	}{
		{Running{Name: "gnome-terminal-", Cmdline: "/usr/libexec/gnome-terminal-server"}, "gnome-terminal-server", Watch{Cmdline: "gnome-terminal-server"}},
		{Running{Name: "xdg-desktop-por", Cmdline: "/usr/libexec/xdg-desktop-portal"}, "xdg-desktop-portal", Watch{Cmdline: "xdg-desktop-portal"}},
		{Running{Name: "nginx", Cmdline: "nginx: master process /usr/sbin/nginx"}, "nginx", Watch{Name: "nginx"}},
		// exactly 15 characters but not cut: the program has the same name
		{Running{Name: "fifteen-letters", Cmdline: "/usr/bin/fifteen-letters -d"}, "fifteen-letters", Watch{Name: "fifteen-letters"}},
		// a cut name whose program is something else stays as the kernel has it
		{Running{Name: "abcdefghijklmno", Cmdline: "/usr/bin/python3 worker.py"}, "abcdefghijklmno", Watch{Name: "abcdefghijklmno"}},
		{Running{Name: "abcdefghijklmno", Cmdline: ""}, "abcdefghijklmno", Watch{Name: "abcdefghijklmno"}},
	}
	for _, c := range cases {
		if got := FullName(c.g); got != c.full {
			t.Errorf("FullName(%q, %q) = %q, want %q", c.g.Name, c.g.Cmdline, got, c.full)
		}
		if got := Suggest(c.g); got != c.want {
			t.Errorf("Suggest(%q) = %+v, want %+v", c.g.Name, got, c.want)
		}
	}
}
