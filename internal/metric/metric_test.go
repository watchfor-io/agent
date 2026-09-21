package metric

import (
	"math"
	"strings"
	"testing"
	"time"
)

// A value JSON cannot carry is dropped, and a label that came from the
// machine is cleaned on the way in: every module and every pane goes
// through here, so this is where it holds for all of them.
func TestAddDropsNonNumbersAndCleansLabels(t *testing.T) {
	b := NewBatch(time.Now())
	b.Add("a", math.NaN())
	b.Add("b", math.Inf(1))
	b.Add("c", 1, Labels{"if": "eth0\x1b]0;pwned\x07", "ok": "wlan0"})
	if b.Len() != 1 {
		t.Fatalf("NaN or Inf was kept: %d samples", b.Len())
	}
	s := b.Samples()[0]
	if strings.ContainsAny(s.L["if"], "\x1b\x07") {
		t.Fatalf("a label kept its escape sequence: %q", s.L["if"])
	}
	if s.L["ok"] != "wlan0" {
		t.Fatalf("a clean label was changed: %q", s.L["ok"])
	}
}
