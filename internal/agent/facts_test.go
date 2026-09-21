package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/watchfor-io/agent/internal/metric"
)

// A host with dozens of interfaces must not lose its CPU, memory and
// hardware facts to an address list the server would refuse whole.
func TestFactsStayUnderTheServersCap(t *testing.T) {
	f := metric.Facts{CPUModel: "Test CPU", CPUCores: 64, MemTotal: 1 << 40, PrimaryIface: "eth0", Addresses: map[string][]string{}}
	for i := 0; i < 32; i++ {
		name := fmt.Sprintf("veth%d", i)
		if i == 0 {
			name = "eth0"
		}
		for j := 0; j < 8; j++ {
			f.Addresses[name] = append(f.Addresses[name], fmt.Sprintf("2001:db8:%x:%x::%x", i, j, j))
		}
	}
	before, _ := json.Marshal(f)
	if len(before) <= factsMaxBytes {
		t.Fatalf("the fixture is not big enough to matter: %d bytes", len(before))
	}
	shrinkFacts(&f, factsMaxBytes)
	after, _ := json.Marshal(f)
	if len(after) > factsMaxBytes {
		t.Fatalf("still %d bytes after shrinking", len(after))
	}
	if len(f.Addresses["eth0"]) == 0 || f.CPUModel != "Test CPU" || f.CPUCores != 64 {
		t.Fatalf("the wrong things were dropped: %+v", f)
	}
}
