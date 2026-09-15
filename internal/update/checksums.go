package update

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// parseChecksums reads a sha256sum-style file ("<hex>  <name>", one per
// line; a "*" before the name marks binary mode) into name → lowercase hex.
// Malformed lines are skipped: a signature over the file is what makes it
// trustworthy, not its shape.
func parseChecksums(data []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
