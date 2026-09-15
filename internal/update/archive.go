package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
)

// extractFile returns the regular file called name (any directory prefix)
// from a .tar.gz, refusing anything larger than max. Only that one entry
// is read; nothing is written to disk here.
func extractFile(tgz []byte, name string, max int64) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("archive: no %s inside", name)
		}
		if err != nil {
			return nil, fmt.Errorf("archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != name {
			continue
		}
		if h.Size <= 0 || h.Size > max {
			return nil, fmt.Errorf("archive: %s is %d bytes, refusing", name, h.Size)
		}
		out := make([]byte, h.Size)
		if _, err := io.ReadFull(tr, out); err != nil {
			return nil, fmt.Errorf("archive: %w", err)
		}
		return out, nil
	}
}
