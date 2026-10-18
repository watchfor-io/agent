package text

import (
	"strings"
	"testing"
	"unicode/utf8"
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
		// bidi controls and zero-width characters spoof labels
		"sshd\u202egnp.exe":            "sshd\ufffdgnp.exe",
		"a\u202ab\u202bc\u202cd\u202d": "a\ufffdb\ufffdc\ufffdd\ufffd",
		"\u2066x\u2067y\u2068z\u2069":  "\ufffdx\ufffdy\ufffdz\ufffd",
		"l\u200em\u200fa\u061c":        "l\ufffdm\ufffda\ufffd",
		"ng\u200binx":                  "ng\ufffdinx",
		"z\u200cw\u200dj\u2060w\ufeff": "z\ufffdw\ufffdj\ufffdw\ufffd",
		"line\u2028sep":                "line\ufffdsep",
		"שלום":                         "שלום", // RTL text itself is fine
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

// Whatever goes in, what comes out is valid UTF-8, free of every rune
// Clean promises to remove, capped, and stable when cleaned again.
func FuzzClean(f *testing.F) {
	for _, s := range []string{"nginx", "evil\x1b]0;x\x07", "sshd\u202e", "\ufeffa\u200d", "bad\xff", strings.Repeat("é", 600)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Clean(s)
		if !utf8.ValidString(out) {
			t.Fatalf("Clean(%q) = %q is not valid UTF-8", s, out)
		}
		if n := utf8.RuneCountInString(out); n > 513 {
			t.Fatalf("Clean(%q) has %d runes", s, n)
		}
		for _, r := range out {
			if unsafeRune(r) || r == '\t' {
				t.Fatalf("Clean(%q) = %q still carries %U", s, out, r)
			}
		}
		if utf8.RuneCountInString(out) <= 512 && Clean(out) != out {
			t.Fatalf("Clean is not stable on %q", out)
		}
	})
}
