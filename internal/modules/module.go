// Package modules holds the collector contract and the registry the command
// line resolves module names through. A module is one YAML key under
// `modules:`; its package registers a factory in init().
package modules

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/watchfor-io/agent/internal/metric"
)

// Module is one collector: a name for logs and config, and a Collect
// that adds its samples to the tick's batch. Rates are the module's own
// affair — it keeps the previous reading between ticks.
type Module interface {
	Name() string
	Collect(ctx context.Context, b *metric.Batch) error
}

// Factory makes a Module from its YAML subtree, nil when the key has no
// value; a bad option is the error the agent refuses to start with.
type Factory func(cfg *yaml.Node) (Module, error)

var registry = map[string]Factory{}

// Register adds a factory under a module name. Packages call it from
// init(); a name taken twice is a programming error, so it panics.
func Register(name string, f Factory) {
	if _, dup := registry[name]; dup {
		panic("modules: duplicate registration of " + name)
	}
	registry[name] = f
}

// Build makes the module called name from its config. An unknown name is
// an error that lists what is available.
func Build(name string, cfg *yaml.Node) (Module, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown module %q (available: %v)", name, Names())
	}
	m, err := f(cfg)
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", name, err)
	}
	return m, nil
}

// Names lists the registered modules, sorted, for messages that say what
// the choices are.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Decode fills cfg from a module's YAML subtree. Unknown keys are an error —
// a typo in a module option should fail at start, not silently monitor
// nothing. An absent, empty or null subtree keeps the defaults.
func Decode(node *yaml.Node, cfg any) error {
	if node == nil || node.Kind == 0 || (node.Kind == yaml.ScalarNode && node.Tag == "!!null") {
		return nil
	}
	raw, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	return dec.Decode(cfg)
}
