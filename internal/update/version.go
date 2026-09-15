// Package update upgrades the running binary to a signed release: the
// version comes from the server (or the operator), the archive from the
// release page, and nothing is installed unless the checksums file is
// signed by the WatchFor release key and the archive matches it.
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a release version: MAJOR.MINOR.PATCH with an optional
// pre-release suffix. "1.2.0-rc1" sorts before "1.2.0".
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// Parse reads "1.2.3", "v1.2.3" or "1.2.3-rc1". Build metadata after "+"
// is ignored, as semver says.
func Parse(s string) (Version, error) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "v"))
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v Version
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.Pre = s[i+1:]
		s = s[:i]
		if v.Pre == "" {
			return Version{}, fmt.Errorf("invalid version %q", s)
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid version %q: want MAJOR.MINOR.PATCH", s)
	}
	nums := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("invalid version %q", s)
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, nil
}

// IsRelease reports whether s parses as a release version. "dev" (an
// unstamped local build) and "" are not.
func IsRelease(s string) bool {
	_, err := Parse(s)
	return err == nil
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare returns -1, 0 or 1 as a is lower than, equal to or higher than b.
func Compare(a, b Version) int {
	for _, d := range [3]int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "": // a release outranks its own pre-releases
		return 1
	case b.Pre == "":
		return -1
	}
	return comparePre(a.Pre, b.Pre)
}

// comparePre orders pre-release suffixes dot by dot: numeric parts by
// value, the rest lexically, a shorter prefix first.
func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		var c int
		switch {
		case aErr == nil && bErr == nil:
			c = cmpInt(an, bn)
		case aErr == nil:
			c = -1 // numeric identifiers sort before alphanumeric ones
		case bErr == nil:
			c = 1
		default:
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmpInt(len(as), len(bs))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
