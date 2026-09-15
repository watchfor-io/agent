package update

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// PublicKey is the WatchFor release signing key (minisign). Releases are
// signed in CI with its private half; install.sh and the README carry the
// same string. A build with a different key here would be a different
// product, so it is a constant, not configuration.
const PublicKey = "RWTUApo01PH7RyjD76wN2Vu7l5sO7Ys5psNQE9I7QYdWVfWSf0CetQje"

// minisign key and signature layouts — https://jedisct1.github.io/minisign/
//
//	public key : "Ed" || key_id(8) || ed25519 public key(32)
//	signature  : alg(2) || key_id(8) || signature(64)
//	             alg "Ed" signs the file, alg "ED" signs BLAKE2b-512(file)
//	global sig : ed25519(signature || trusted_comment)
const (
	algLegacy    = "Ed"
	algPrehashed = "ED"
)

type minisignKey struct {
	id  [8]byte
	key ed25519.PublicKey
}

type minisignSig struct {
	alg     string
	id      [8]byte
	sig     [ed25519.SignatureSize]byte
	trusted string
	global  [ed25519.SignatureSize]byte
}

func parseMinisignKey(b64 string) (minisignKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return minisignKey{}, fmt.Errorf("public key: %w", err)
	}
	if len(raw) != 2+8+ed25519.PublicKeySize || string(raw[:2]) != algLegacy {
		return minisignKey{}, errors.New("public key: not a minisign Ed25519 key")
	}
	var k minisignKey
	copy(k.id[:], raw[2:10])
	k.key = ed25519.PublicKey(raw[10:])
	return k, nil
}

// parseMinisignSig reads a .minisig file: an untrusted comment line, the
// signature, a "trusted comment:" line and the global signature.
func parseMinisignSig(text []byte) (minisignSig, error) {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(text))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 4 {
		return minisignSig{}, errors.New("signature: want 4 lines")
	}
	var s minisignSig
	raw, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(raw) != 2+8+ed25519.SignatureSize {
		return minisignSig{}, errors.New("signature: malformed signature line")
	}
	s.alg = string(raw[:2])
	if s.alg != algLegacy && s.alg != algPrehashed {
		return minisignSig{}, fmt.Errorf("signature: unsupported algorithm %q", s.alg)
	}
	copy(s.id[:], raw[2:10])
	copy(s.sig[:], raw[10:])
	const prefix = "trusted comment:"
	if !strings.HasPrefix(lines[2], prefix) {
		return minisignSig{}, errors.New("signature: missing trusted comment")
	}
	s.trusted = strings.TrimSpace(strings.TrimPrefix(lines[2], prefix))
	g, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || len(g) != ed25519.SignatureSize {
		return minisignSig{}, errors.New("signature: malformed global signature")
	}
	copy(s.global[:], g)
	return s, nil
}

// verifyMinisign checks data against a .minisig file with the given public
// key and returns the trusted comment (itself covered by the global
// signature, so it can be relied on).
func verifyMinisign(publicKey string, sigFile, data []byte) (string, error) {
	k, err := parseMinisignKey(publicKey)
	if err != nil {
		return "", err
	}
	s, err := parseMinisignSig(sigFile)
	if err != nil {
		return "", err
	}
	if s.id != k.id {
		return "", errors.New("signature: made with a different key")
	}
	msg := data
	if s.alg == algPrehashed {
		h := blake2b.Sum512(data)
		msg = h[:]
	}
	if !ed25519.Verify(k.key, msg, s.sig[:]) {
		return "", errors.New("signature: verification failed")
	}
	global := append(append([]byte{}, s.sig[:]...), []byte(s.trusted)...)
	if !ed25519.Verify(k.key, global, s.global[:]) {
		return "", errors.New("signature: trusted comment verification failed")
	}
	return s.trusted, nil
}
