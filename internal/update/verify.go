package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// VerifyChecksums checks a checksums file against its .minisig with the
// built-in release key and returns the signed (trusted) comment.
func VerifyChecksums(checksums, sig []byte) (string, error) {
	return verifyMinisign(PublicKey, sig, checksums)
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
