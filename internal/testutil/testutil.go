// Package testutil is shared test scaffolding: the /proc fixture root, YAML
// nodes from strings, and a name set over a batch.
package testutil

import (
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/watchfor-io/agent/internal/metric"
)

// ProcRoot is the captured /proc fixture under internal/procfs/testdata,
// found from this file's location so any package's tests can use it.
func ProcRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "procfs", "testdata", "proc")
}

// Node parses a YAML snippet into the node a module factory receives —
// the document's root, as agent.yml's modules: subtree arrives.
func Node(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(src), &n); err != nil {
		t.Fatal(err)
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return &n
}

// Names is the set of metric names in a batch, for asserting what a
// module emits.
func Names(b *metric.Batch) map[string]bool {
	out := map[string]bool{}
	for _, s := range b.Samples() {
		out[s.M] = true
	}
	return out
}

// Find picks the first sample with that name whose labels include every
// pair given; false when none does.
func Find(b *metric.Batch, name string, labels metric.Labels) (metric.Sample, bool) {
	for _, s := range b.Samples() {
		if s.M != name {
			continue
		}
		match := true
		for k, v := range labels {
			if s.L[k] != v {
				match = false
			}
		}
		if match {
			return s, true
		}
	}
	return metric.Sample{}, false
}

// UseProcRoot points a package's procfs root at the fixture for one test
// and puts it back afterwards, so tests cannot leak a fixture into the
// next one.
func UseProcRoot(t *testing.T, root *string) {
	t.Helper()
	old := *root
	*root = ProcRoot(t)
	t.Cleanup(func() { *root = old })
}
