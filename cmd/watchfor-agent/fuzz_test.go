//go:build linux

package main

import "testing"

// What a person types into the "add by hand" box is read by the same
// parser that reads agent.yml watch lines; it must never panic.
func FuzzParseWatches(f *testing.F) {
	for _, seed := range []string{"", "nginx", "cmdline:gunicorn user:www", "a, b, c", "cmdline:", " user:", ",,,", "two words"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		_, _ = parseWatches(text)
	})
}
