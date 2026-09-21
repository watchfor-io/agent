package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

func TestParseAndCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.2.0", -1},
		{"v0.2.0", "0.2.0", 0},
		{"1.0.0", "0.9.9", 1},
		{"1.2.0-rc1", "1.2.0", -1},
		{"1.2.0-rc1", "1.2.0-rc2", -1},
		{"1.2.0-rc.2", "1.2.0-rc.10", -1},
		{"1.2.0+build7", "1.2.0", 0},
	} {
		a, err := Parse(tc.a)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.a, err)
		}
		b, err := Parse(tc.b)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.b, err)
		}
		if got := Compare(a, b); got != tc.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	for _, bad := range []string{"", "dev", "1.2", "1.2.3.4", "01.2.3", "1.2.-3", "1.2.3-"} {
		if IsRelease(bad) {
			t.Errorf("IsRelease(%q) = true", bad)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	sums := parseChecksums([]byte("abc  x\n" +
		strings.Repeat("a", 64) + "  watchfor-agent_0.2.0_linux_amd64.tar.gz\n" +
		strings.Repeat("B", 64) + " *watchfor-agent_0.2.0_linux_arm64.tar.gz\n"))
	if len(sums) != 2 || sums["watchfor-agent_0.2.0_linux_arm64.tar.gz"] != strings.Repeat("b", 64) {
		t.Fatalf("parseChecksums = %v", sums)
	}
}

// signer builds minisign-format keys and signatures for tests.
type signer struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	id   [8]byte
}

func newSigner(t *testing.T) *signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &signer{pub: pub, priv: priv}
	copy(s.id[:], []byte("keyid-01"))
	return s
}

func (s *signer) publicKey() string {
	return base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), s.id[:]...), s.pub...))
}

// sign produces a .minisig file; prehashed selects the "ED" (BLAKE2b) mode.
func (s *signer) sign(data []byte, trusted string, prehashed bool) []byte {
	alg, msg := "Ed", data
	if prehashed {
		h := blake2b.Sum512(data)
		alg, msg = "ED", h[:]
	}
	sig := ed25519.Sign(s.priv, msg)
	global := ed25519.Sign(s.priv, append(append([]byte{}, sig...), []byte(trusted)...))
	return []byte("untrusted comment: test\n" +
		base64.StdEncoding.EncodeToString(append(append([]byte(alg), s.id[:]...), sig...)) + "\n" +
		"trusted comment: " + trusted + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n")
}

func TestVerifyMinisign(t *testing.T) {
	s := newSigner(t)
	data := []byte("hello\n")
	for _, prehashed := range []bool{false, true} {
		got, err := verifyMinisign(s.publicKey(), s.sign(data, "watchfor-agent 9.9.9 checksums", prehashed), data)
		if err != nil || got != "watchfor-agent 9.9.9 checksums" {
			t.Fatalf("prehashed=%v: %q, %v", prehashed, got, err)
		}
		if _, err := verifyMinisign(s.publicKey(), s.sign(data, "x", prehashed), []byte("tampered\n")); err == nil {
			t.Fatalf("prehashed=%v: tampered data verified", prehashed)
		}
	}
	// A different key, same key id: must fail.
	other := newSigner(t)
	if _, err := verifyMinisign(s.publicKey(), other.sign(data, "x", true), data); err == nil {
		t.Fatal("foreign signature verified")
	}
	// A tampered trusted comment must fail the global signature.
	sig := s.sign(data, "watchfor-agent 9.9.9 checksums", true)
	sig = bytes.Replace(sig, []byte("9.9.9"), []byte("9.9.8"), 1)
	if _, err := verifyMinisign(s.publicKey(), sig, data); err == nil {
		t.Fatal("tampered trusted comment verified")
	}
	// The real release key parses.
	if _, err := parseMinisignKey(PublicKey); err != nil {
		t.Fatal(err)
	}
}

func tgz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("docs")}, {name, content}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractFile(t *testing.T) {
	a := tgz(t, "watchfor-agent", []byte("binary"))
	got, err := extractFile(a, "watchfor-agent", 1<<20)
	if err != nil || string(got) != "binary" {
		t.Fatalf("extractFile: %q, %v", got, err)
	}
	if _, err := extractFile(a, "other", 1<<20); err == nil {
		t.Fatal("missing entry extracted")
	}
	if _, err := extractFile(a, "watchfor-agent", 3); err == nil {
		t.Fatal("oversized entry extracted")
	}
}

func TestState(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := ReadState(dir); ok {
		t.Fatal("empty dir reports a hint")
	}
	if err := WriteState(dir, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	if v, _, ok := ReadState(dir); !ok || v != "0.3.0" {
		t.Fatalf("ReadState = %q, %v", v, ok)
	}
	if err := ClearState(dir); err != nil {
		t.Fatal(err)
	}
	if err := ClearState(dir); err != nil {
		t.Fatal("second clear errs:", err)
	}
	if _, _, ok := ReadState(dir); ok {
		t.Fatal("hint survived ClearState")
	}
}

// release serves a fake GitHub: the API's latest release and the assets
// of one version, signed with the test key.
type release struct {
	srv     *httptest.Server
	source  *Source
	version string
	hits    []string
}

func newRelease(t *testing.T, s *signer, version string, script string, tamper func(name string, b []byte) []byte) *release {
	t.Helper()
	r := &release{version: version}
	archive := tgz(t, "watchfor-agent", []byte(script))
	name := "watchfor-agent_" + version + "_linux_amd64.tar.gz"
	sums := []byte(sha256Hex(archive) + "  " + name + "\n")
	assets := map[string][]byte{
		"checksums.txt":         sums,
		"checksums.txt.minisig": s.sign(sums, "watchfor-agent "+version+" checksums", true),
		name:                    archive,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/latest", func(w http.ResponseWriter, req *http.Request) {
		r.hits = append(r.hits, req.URL.Path)
		w.Write([]byte("v" + version + "\n"))
	})
	mux.HandleFunc("/watchfor-io/agent/releases/download/v"+version+"/", func(w http.ResponseWriter, req *http.Request) {
		r.hits = append(r.hits, req.URL.Path)
		b, ok := assets[filepath.Base(req.URL.Path)]
		if !ok {
			http.NotFound(w, req)
			return
		}
		if tamper != nil {
			b = tamper(filepath.Base(req.URL.Path), b)
		}
		w.Write(b)
	})
	mux.HandleFunc("/elsewhere/", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "http://localhost:1/x", http.StatusFound)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	r.source = &Source{Repo: DefaultRepo, Releases: r.srv.URL, ReleasesURL: r.srv.URL + "/agent", Hosts: []string{"127.0.0.1"}, insecure: true}
	r.source.Client = &http.Client{CheckRedirect: r.source.checkRedirect}
	return r
}

func TestSourceRefusesOtherHosts(t *testing.T) {
	s := newSigner(t)
	r := newRelease(t, s, "9.9.9", "", nil)
	if _, err := r.source.get(context.Background(), r.srv.URL+"/elsewhere/", "", 1<<10); err == nil || !strings.Contains(err.Error(), "not a release host") {
		t.Fatalf("redirect to another host followed: %v", err)
	}
	if _, err := NewSource().get(context.Background(), "http://github.com/x", "", 1<<10); err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("plain http accepted: %v", err)
	}
	if _, err := NewSource().get(context.Background(), "https://example.com/x", "", 1<<10); err == nil || !strings.Contains(err.Error(), "not a release host") {
		t.Fatalf("unknown host accepted: %v", err)
	}
}

func testOptions(t *testing.T, r *release, s *signer, current string) Options {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "watchfor-agent")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho watchfor-agent "+current+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Options{
		Current:  current,
		StateDir: t.TempDir(),
		Restart:  false,
		ExePath:  exe,
		OS:       "linux",
		Arch:     "amd64",
		Source:   r.source,
		Out:      &bytes.Buffer{},
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "systemctl" {
				return nil, os.ErrNotExist
			}
			return execScript(ctx, name, args...)
		},
		publicKeys: []string{s.publicKey()},
	}
}

func TestRunUpgrades(t *testing.T) {
	s := newSigner(t)
	r := newRelease(t, s, "9.9.9", "#!/bin/sh\necho watchfor-agent 9.9.9\n", nil)
	o := testOptions(t, r, s, "0.2.0")
	if err := WriteState(o.StateDir, "9.9.9"); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), o)
	if err != nil || !res.Updated || res.To != "9.9.9" {
		t.Fatalf("Run: %+v, %v (%s)", res, err, o.Out.(*bytes.Buffer))
	}
	out, err := execScript(context.Background(), o.ExePath, "version")
	if err != nil || strings.TrimSpace(string(out)) != "watchfor-agent 9.9.9" {
		t.Fatalf("installed binary: %q, %v", out, err)
	}
	if _, _, ok := ReadState(o.StateDir); ok {
		t.Fatal("hint not cleared after upgrade")
	}
	for _, h := range r.hits {
		if strings.HasSuffix(h, "/latest") {
			t.Fatal("asked watchfor.io although the server had reported a version")
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(o.ExePath)); len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestRunRefusesBadRelease(t *testing.T) {
	s := newSigner(t)
	for name, tamper := range map[string]func(string, []byte) []byte{
		"archive": func(n string, b []byte) []byte {
			if strings.HasSuffix(n, ".tar.gz") {
				return append(b, 0)
			}
			return b
		},
		"checksums": func(n string, b []byte) []byte {
			if n == "checksums.txt" {
				return append(b, []byte("x  y\n")...)
			}
			return b
		},
		"signature": func(n string, b []byte) []byte {
			if n == "checksums.txt.minisig" {
				return newSigner(t).sign(b, "watchfor-agent 9.9.9 checksums", true)
			}
			return b
		},
	} {
		r := newRelease(t, s, "9.9.9", "#!/bin/sh\necho watchfor-agent 9.9.9\n", tamper)
		o := testOptions(t, r, s, "0.2.0")
		o.Target = "9.9.9"
		before, _ := os.ReadFile(o.ExePath)
		if res, err := Run(context.Background(), o); err == nil || res.Updated {
			t.Fatalf("%s: tampered release installed: %+v", name, res)
		}
		after, _ := os.ReadFile(o.ExePath)
		if !bytes.Equal(before, after) {
			t.Fatalf("%s: binary changed although the upgrade failed", name)
		}
	}
	// A binary that does not identify as the release is not installed.
	r := newRelease(t, s, "9.9.9", "#!/bin/sh\necho watchfor-agent 1.0.0\n", nil)
	o := testOptions(t, r, s, "0.2.0")
	o.Target = "9.9.9"
	if res, err := Run(context.Background(), o); err == nil || res.Updated {
		t.Fatalf("mislabelled binary installed: %+v", res)
	}
}

func TestRunPolicies(t *testing.T) {
	s := newSigner(t)
	r := newRelease(t, s, "0.1.0", "#!/bin/sh\necho watchfor-agent 0.1.0\n", nil)
	o := testOptions(t, r, s, "0.2.0")
	// Downgrade refused without the flag …
	o.Target = "0.1.0"
	if _, err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("downgrade allowed: %v", err)
	}
	// … and performed with it.
	o.AllowDowngrade = true
	if res, err := Run(context.Background(), o); err != nil || !res.Updated {
		t.Fatalf("explicit downgrade: %+v, %v", res, err)
	}
	// Same version: nothing happens.
	o = testOptions(t, r, s, "0.1.0")
	o.Target = "0.1.0"
	if res, err := Run(context.Background(), o); err != nil || res.Updated {
		t.Fatalf("same version reinstalled: %+v, %v", res, err)
	}
	// -if-available with no hint: no network, nothing to do.
	o = testOptions(t, r, s, "0.0.1")
	o.IfAvailable = true
	hits := len(r.hits)
	if res, err := Run(context.Background(), o); err != nil || res.Updated || len(r.hits) != hits {
		t.Fatalf("if-available without hint: %+v, %v, hits %d→%d", res, err, hits, len(r.hits))
	}
	// A development build needs an explicit version.
	o = testOptions(t, r, s, "dev")
	if _, err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Fatalf("dev build resolved implicitly: %v", err)
	}
	// Check asks watchfor.io when there is no hint.
	o = testOptions(t, r, s, "0.0.1")
	st, err := Check(context.Background(), o)
	if err != nil || !st.Available || st.Origin != "watchfor.io" || st.Latest != "0.1.0" {
		t.Fatalf("Check: %+v, %v", st, err)
	}
}

func TestRunRestartsActiveUnit(t *testing.T) {
	s := newSigner(t)
	r := newRelease(t, s, "9.9.9", "#!/bin/sh\necho watchfor-agent 9.9.9\n", nil)
	o := testOptions(t, r, s, "0.2.0")
	o.Target, o.Restart = "9.9.9", true
	var calls []string
	o.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" {
			calls = append(calls, strings.Join(args, " "))
			return nil, nil
		}
		return execScript(ctx, name, args...)
	}
	res, err := Run(context.Background(), o)
	if err != nil || !res.Restarted {
		t.Fatalf("Run: %+v, %v", res, err)
	}
	if strings.Join(calls, ";") != "is-active --quiet watchfor-agent;restart watchfor-agent" {
		t.Fatalf("systemctl calls: %v", calls)
	}
}

// BinaryDigest must name the hash of the binary inside the signed archive,
// and refuse a checksums file signed by anyone else.
func TestBinaryDigest(t *testing.T) {
	s := newSigner(t)
	script := "#!/bin/sh\necho watchfor-agent 9.9.9\n"
	r := newRelease(t, s, "9.9.9", script, nil)
	o := testOptions(t, r, s, "9.9.9")
	got, err := BinaryDigest(context.Background(), o, "9.9.9")
	if err != nil {
		t.Fatalf("BinaryDigest: %v", err)
	}
	if want := sha256Hex([]byte(script)); got != want {
		t.Fatalf("digest %s, want %s", got, want)
	}
	// the same number as the file on disk once it is installed
	if _, err := Run(context.Background(), Options{
		Current: "0.1.0", Target: "9.9.9", ExePath: o.ExePath, OS: "linux", Arch: "amd64",
		Source: r.source, Out: o.Out, Exec: o.Exec, publicKeys: []string{s.publicKey()}, StateDir: o.StateDir,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	onDisk, err := FileDigest(o.ExePath)
	if err != nil || onDisk != got {
		t.Fatalf("installed file %s, release %s (%v)", onDisk, got, err)
	}
	// another key's signature is not the WatchFor key's
	o.publicKeys = []string{newSigner(t).publicKey()}
	if _, err := BinaryDigest(context.Background(), o, "9.9.9"); err == nil {
		t.Fatal("a foreign signature was accepted")
	}
}

// A rotation: the checksums are signed by a new key that the agent
// already trusts alongside the old one; a key on neither list is refused.
func TestRotatedKeyIsTrusted(t *testing.T) {
	oldKey, newKey, stranger := newSigner(t), newSigner(t), newSigner(t)
	sums := []byte("abc  watchfor-agent_9.9.9_linux_amd64.tar.gz\n")
	trusted := []string{oldKey.publicKey(), newKey.publicKey()}
	for _, s := range []*signer{oldKey, newKey} {
		if _, err := verifyAny(trusted, s.sign(sums, "watchfor-agent 9.9.9 checksums", true), sums); err != nil {
			t.Errorf("a trusted key was refused: %v", err)
		}
	}
	if _, err := verifyAny(trusted, stranger.sign(sums, "x", true), sums); err == nil {
		t.Error("a key on no list was accepted")
	}
}

// A lock file that someone else left as a symlink is not followed: root
// must not open whatever the link points at.
func TestLockRefusesAPlantedSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, LockName)); err != nil {
		t.Fatal(err)
	}
	if unlock, err := lockIn(dir); err == nil {
		unlock()
		t.Fatal("the lock followed a symlink")
	}
	// and an honest directory still locks, once
	honest := t.TempDir()
	unlock, err := lockIn(honest)
	if err != nil {
		t.Fatalf("could not lock an empty directory: %v", err)
	}
	if _, err := lockIn(honest); err == nil {
		t.Fatal("the lock was taken twice")
	}
	unlock()
}
