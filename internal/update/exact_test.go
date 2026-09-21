package update

import (
	"context"
	"strings"
	"testing"
)

// The binary that came out of the archive must report exactly the version
// asked for: "9.9.90" is not "9.9.9" with something after it.
func TestDownloadedBinaryMustReportTheExactVersion(t *testing.T) {
	s := newSigner(t)
	r := newRelease(t, s, "9.9.9", "#!/bin/sh\necho watchfor-agent 9.9.90\n", nil)
	o := testOptions(t, r, s, "0.2.0")
	if err := WriteState(o.StateDir, "9.9.9"); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "does not report") || res.Updated {
		t.Fatalf("a binary of another version was accepted: %+v, %v", res, err)
	}
}

// A pre-release suffix ends up in a URL path, so it is letters, digits,
// dots and dashes — nothing that walks anywhere.
func TestPreReleaseSuffixIsPlain(t *testing.T) {
	for _, ok := range []string{"1.2.3-rc.1", "1.2.3-beta-2", "v0.7.0-dev"} {
		if _, err := Parse(ok); err != nil {
			t.Errorf("Parse(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"1.2.3-../../x", "1.2.3-a?b", "1.2.3-#", "1.2.3-"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}
