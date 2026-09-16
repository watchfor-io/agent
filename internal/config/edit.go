package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Settings the CLI may read and write in agent.yml. The token itself is
// deliberately absent: it belongs in token_file (0600), never inline.
type kind int

const (
	kindString kind = iota
	kindInt
	kindBool
	kindDuration
)

var editable = map[string]kind{
	"interval":          kindDuration,
	"server.url":        kindString,
	"server.token_file": kindString,
	"server.ca_file":    kindString,
	"server.timeout":    kindDuration,
	"host.name":         kindString,
	"log.level":         kindString,
	"spool.dir":         kindString,
	"spool.max_mb":      kindInt,
	"updates.auto":      kindBool,
}

// Keys lists the settable keys (plus host.tags.<name> for any tag).
func Keys() []string {
	out := make([]string, 0, len(editable)+1)
	for k := range editable {
		out = append(out, k)
	}
	sort.Strings(out)
	return append(out, "host.tags.<name>")
}

func kindOf(key string) (kind, error) {
	if k, ok := editable[key]; ok {
		return k, nil
	}
	if strings.HasPrefix(key, "host.tags.") && len(key) > len("host.tags.") {
		return kindString, nil
	}
	if key == "server.token" {
		return 0, errors.New("server.token cannot be set from the CLI: put the token in the file named by server.token_file (mode 0600)")
	}
	return 0, fmt.Errorf("unknown setting %q (see: watchfor-agent config keys)", key)
}

// Get returns the effective value of key from path (defaults applied).
func Get(path, key string) (string, error) {
	if _, err := kindOf(key); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	cfg, err := parseConfig(f, false)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	switch key {
	case "interval":
		return cfg.Interval.String(), nil
	case "server.url":
		return cfg.Server.URL, nil
	case "server.token_file":
		return cfg.Server.TokenFile, nil
	case "server.ca_file":
		return cfg.Server.CAFile, nil
	case "server.timeout":
		return cfg.Server.Timeout.String(), nil
	case "host.name":
		return cfg.Host.Name, nil
	case "log.level":
		return cfg.Log.Level, nil
	case "spool.dir":
		return cfg.Spool.Dir, nil
	case "spool.max_mb":
		return strconv.Itoa(cfg.Spool.MaxMB), nil
	case "updates.auto":
		if cfg.Updates.Auto == nil {
			return "unset", nil
		}
		return strconv.FormatBool(*cfg.Updates.Auto), nil
	}
	return cfg.Host.Tags[strings.TrimPrefix(key, "host.tags.")], nil
}

// Set writes key = value into the YAML file at path, keeping every other
// line (comments included) as it was, and returns the re-parsed config —
// a value the agent would refuse never reaches the file. A missing file
// is created. Root-owned files keep their owner and mode.
func Set(path, key, value string) (*Config, error) {
	k, err := kindOf(key)
	if err != nil {
		return nil, err
	}
	node, err := scalarFor(k, value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}

	var doc yaml.Node
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: top level is not a mapping", path)
	}
	setPath(root, strings.Split(key, "."), node)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	cfg, err := parseConfig(bytes.NewReader(buf.Bytes()), false)
	if err != nil {
		return nil, fmt.Errorf("refusing to write: the agent would not accept the result: %w", err)
	}
	if err := writeAtomic(path, buf.Bytes()); err != nil {
		return nil, err
	}
	return cfg, nil
}

func scalarFor(k kind, value string) (*yaml.Node, error) {
	v := strings.TrimSpace(value)
	n := &yaml.Node{Kind: yaml.ScalarNode, Value: v}
	switch k {
	case kindBool:
		b, err := strconv.ParseBool(strings.ToLower(v))
		if err != nil {
			return nil, errors.New("want true or false")
		}
		n.Tag, n.Value = "!!bool", strconv.FormatBool(b)
	case kindInt:
		i, err := strconv.Atoi(v)
		if err != nil || i < 0 {
			return nil, errors.New("want a non-negative integer")
		}
		n.Tag = "!!int"
	case kindDuration:
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, errors.New("want a duration like 15s or 1m")
		}
		n.Tag, n.Value = "!!str", d.String()
	default:
		if v == "" {
			return nil, errors.New("want a value")
		}
		n.Tag = "!!str"
		// Keep strings that YAML would otherwise read as something else.
		switch strings.ToLower(v) {
		case "true", "false", "yes", "no", "on", "off", "null", "~":
			n.Style = yaml.DoubleQuotedStyle
		}
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			n.Style = yaml.DoubleQuotedStyle
		}
	}
	return n, nil
}

// setPath creates intermediate mappings as needed and replaces or appends
// the final key.
func setPath(m *yaml.Node, parts []string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != parts[0] {
			continue
		}
		if len(parts) == 1 {
			value.HeadComment, value.LineComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment
			m.Content[i+1] = value
			return
		}
		child := m.Content[i+1]
		if child.Kind != yaml.MappingNode {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			m.Content[i+1] = child
		}
		setPath(child, parts[1:], value)
		return
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: parts[0]}
	if len(parts) == 1 {
		m.Content = append(m.Content, keyNode, value)
		return
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, keyNode, child)
	setPath(child, parts[1:], value)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o644)
	uid, gid := -1, -1
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(sys.Uid), int(sys.Gid)
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot write %s: run as root (sudo)", path)
		}
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if uid >= 0 {
		_ = tmp.Chown(uid, gid) // best effort; only root can, and root is who edits this file
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
