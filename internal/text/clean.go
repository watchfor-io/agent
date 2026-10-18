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
// forge a log line — or a bidi override / zero-width character that makes
// "sshd" and "sshd\u202e" look alike, or reorders a label on screen.
// Control characters, those invisible format characters and invalid UTF-8
// are replaced by a visible marker, and the result is capped — nothing
// downstream has to think about it again.
func Clean(s string) string {
	const maxRunes = 512
	if !strings.ContainsFunc(s, func(r rune) bool { return r == utf8.RuneError || unsafeRune(r) }) && utf8.RuneCountInString(s) <= maxRunes {
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
		case unsafeRune(r):
			b.WriteRune('\ufffd')
		default:
			b.WriteRune(r)
		}
		n++
	}
	return b.String()
}

// unsafeRune is a rune that must not reach a terminal, log or label as is:
// C0/C1 controls and DEL, the bidi embeddings/overrides/isolates and marks
// (U+202A–202E, U+2066–2069, U+200E/200F, U+061C), the zero-width
// characters (U+200B–200D, U+2060, U+FEFF) and the line/paragraph
// separators (U+2028/2029) that some viewers break lines on.
func unsafeRune(r rune) bool {
	switch {
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		return true
	case r >= 0x200b && r <= 0x200f, // ZWSP, ZWNJ, ZWJ, LRM, RLM
		r >= 0x202a && r <= 0x202e, // LRE, RLE, PDF, LRO, RLO
		r >= 0x2066 && r <= 0x2069, // LRI, RLI, FSI, PDI
		r == 0x2028 || r == 0x2029,
		r == 0x061c, r == 0x2060, r == 0xfeff:
		return true
	}
	return false
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
