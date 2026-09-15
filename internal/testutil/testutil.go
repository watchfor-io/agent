// Package testutil is shared test scaffolding: the /proc fixture root, YAML
// nodes from strings, and a name set over a batch.
package testutil

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/watchfor-io/agent/internal/metric"
)

func ProcRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "procfs", "testdata", "proc")
}

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

func Now() time.Time { return time.Now() }

func Names(b *metric.Batch) map[string]bool {
	out := map[string]bool{}
	for _, s := range b.Samples() {
		out[s.M] = true
	}
	return out
}

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
