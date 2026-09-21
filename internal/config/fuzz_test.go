package config

import "testing"

// Whatever is in agent.yml, parsing it may fail but must not panic.
func FuzzParseText(f *testing.F) {
	for _, seed := range []string{
		"",
		"server:\n  url: https://ingest.watchfor.io\n  token: x\n",
		"server:\n  url: not a url\ninterval: 1s\n",
		"modules:\n  disk:\n    mounts: [/]\n  processes:\n    watch:\n      - name: nginx\n",
		"host:\n  tags: {a: b, c: d}\n",
		"interval: 5m\nspool:\n  max_mb: -1\n",
		"- a list\n",
		"{",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		_, _ = ParseText(text)
	})
}
