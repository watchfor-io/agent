package text

import (
	"strings"
	"testing"
)

// A hostile process name must lose its teeth before it reaches a
// terminal, a log line or the wire.
func TestCleanDisarms(t *testing.T) {
	cases := map[string]string{
		"nginx":                "nginx",
		"evil\x1b]0;pwned\x07": "evil\ufffd]0;pwned\ufffd",
		"a\x1b[2Jb":            "a\ufffd[2Jb",
		"tab\tsep":             "tab sep",
		"line\nbreak":          "line\ufffdbreak",
		"c1\u0085ctl":          "c1\ufffdctl",
		"bad\xffutf8":          "bad\ufffdutf8",
		"ok — unicode · ✓":     "ok — unicode · ✓",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("x", 600)
	if got := Clean(long); len([]rune(got)) != 513 || !strings.HasSuffix(got, "…") {
		t.Errorf("a %d-rune name became %d runes", 600, len([]rune(got)))
	}
}
