package update

import "testing"

// A checksums file, a signature and a version string all come off the
// network; each parser may refuse them but must not panic.
func FuzzParseChecksums(f *testing.F) {
	f.Add([]byte("abc  watchfor-agent_1.0.0_linux_amd64.tar.gz\n"))
	f.Add([]byte("no spaces here\n\n\n"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = parseChecksums(data)
	})
}

func FuzzMinisignSignature(f *testing.F) {
	f.Add([]byte("untrusted comment: x\nRWQ\ntrusted comment: y\nAAAA\n"))
	f.Add([]byte(""))
	f.Add([]byte("untrusted comment:\n"))
	f.Fuzz(func(t *testing.T, sig []byte) {
		_, _ = verifyAny(publicKeys, sig, []byte("data"))
	})
}

func FuzzParseVersion(f *testing.F) {
	for _, seed := range []string{"1.2.3", "v0.7.0", "0.7.0-rc1", "", "x.y.z", "1..2", "99999999999999999999.0.0"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v string) {
		_, _ = Parse(v)
	})
}
