// Package text is where strings the agent did not write are made safe to
// show, log and send.
package text

import (
	"strings"
	"unicode/utf8"
)

// Clean makes a string read from /proc, /sys or another process safe to
// show, log and send. Another user's process names its own command line,
// and the kernel passes it through untouched, so a name may carry an
// escape sequence that would retitle the admin's terminal, hide text or
// forge a log line. Control characters and invalid UTF-8 are replaced by
// a visible marker, and the result is capped — nothing downstream has to
// think about it again.
func Clean(s string) string {
	const maxRunes = 512
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == utf8.RuneError }) && utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range strings.ToValidUTF8(s, "\ufffd") {
		if n >= maxRunes {
			b.WriteRune('…')
			break
		}
		switch {
		case r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			b.WriteRune('\ufffd')
		default:
			b.WriteRune(r)
		}
		n++
	}
	return b.String()
}

// Clip cuts s to at most n bytes on a rune boundary, marking the cut.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
