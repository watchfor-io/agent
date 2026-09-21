package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// VerifyChecksums checks a checksums file against its .minisig with the
// built-in release key and returns the signed (trusted) comment.
func VerifyChecksums(checksums, sig []byte) (string, error) {
	return verifyAny(publicKeys, sig, checksums)
}

// VerifyFiles compares each file's sha256 with the entry of the same
// base name in the (already verified) checksums. Every file must be
// listed; the first mismatch or missing entry is the error.
func VerifyFiles(checksums []byte, paths []string) error {
	sums := parseChecksums(checksums)
	if len(sums) == 0 {
		return errors.New("checksums: no sha256 lines")
	}
	sorted := append([]string{}, paths...)
	sort.Strings(sorted)
	for _, p := range sorted {
		want, ok := sums[filepath.Base(p)]
		if !ok {
			return fmt.Errorf("%s: not listed in the checksums", filepath.Base(p))
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if got := sha256Hex(data); got != want {
			return fmt.Errorf("%s: sha256 mismatch", filepath.Base(p))
		}
	}
	return nil
}

// BinaryDigest is the sha256 the installed watchfor-agent must have if it
// really is the release it claims to be. The checksums file is verified
// against the built-in key first, the archive is then checked against the
// checksums, and the binary inside it is hashed. It downloads the whole
// archive, so callers ask for it deliberately rather than routinely.
func BinaryDigest(ctx context.Context, o Options, version string) (string, error) {
	o = o.withDefaults()
	v, err := Parse(version)
	if err != nil {
		return "", err
	}
	bin, err := fetchVerifiedBinary(ctx, o, v.String())
	if err != nil {
		return "", err
	}
	return sha256Hex(bin), nil
}

// FileDigest is the sha256 of a file on disk, in the same hex form.
func FileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
