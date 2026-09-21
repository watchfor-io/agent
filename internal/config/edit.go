package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// kind is what a setting's value looks like, so `config set` can check it.
type kind int

const (
	kindString kind = iota
	kindInt
	kindBool
	kindDuration
)

// editable is every setting the CLI may read and write in agent.yml. The
// token itself is deliberately absent: it belongs in token_file (0600),
// never inline.
var editable = map[string]kind{
	"interval":             kindDuration,
	"server.url":           kindString,
	"server.token_file":    kindString,
	"server.ca_file":       kindString,
	"server.timeout":       kindDuration,
	"host.name":            kindString,
	"log.level":            kindString,
	"facts.cloud_metadata": kindBool,
	"spool.dir":            kindString,
	"spool.max_mb":         kindInt,
	"updates.auto":         kindBool,
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
		return strconv.Itoa(cfg.Spool.Max()), nil
	case "updates.auto":
		if cfg.Updates.Auto == nil {
			return "unset", nil
		}
		return strconv.FormatBool(*cfg.Updates.Auto), nil
	case "facts.cloud_metadata":
		return strconv.FormatBool(cfg.Facts.CloudMetadataEnabled()), nil
	}
	return cfg.Host.Tags[strings.TrimPrefix(key, "host.tags.")], nil
}

// Set writes key = value into the YAML file at path, keeping every other
// line (comments included) as it was, and returns the re-parsed config —
// a value the agent would refuse never reaches the file. A file that is
// not there is not invented — a one-line agent.yml would run on defaults
// nobody chose. Root-owned files keep their owner and mode.
func Set(path, key, value string) (*Config, error) {
	k, err := kindOf(key)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s does not exist — write one first: watchfor-agent config init -out %s", path, path)
	}
	node, err := scalarFor(k, value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}

	var doc yaml.Node
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		// A key that already has its own line is edited in place, so the
		// comments and their alignment in a generated agent.yml survive;
		// the encoder below would flatten them.
		if text, ok := setInText(string(raw), key, value); ok {
			cfg, err := parseConfig(strings.NewReader(text), false)
			if err != nil {
				return nil, fmt.Errorf("refusing to write: the agent would not accept the result: %w", err)
			}
			if err := writeAtomic(path, []byte(text)); err != nil {
				return nil, err
			}
			return cfg, nil
		}
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
	if err := ensureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot write %s: only root may — run with sudo", path)
		}
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // #nosec G104 -- a no-op once the file is renamed into place
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if uid >= 0 {
		_ = tmp.Chown(uid, gid) // best effort; only root can, and root is who edits this file
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ensureDir makes the folder agent.yml lives in when it is not there yet
// — the first save on a machine the installer never touched. As root it
// is given the installer's shape (root:watchfor-agent, 0750) when that
// group exists; anyone else gets told plainly what is missing rather than
// a temp-file name they never asked for.
func ensureDir(dir string) error {
	st, err := os.Stat(dir)
	switch {
	case err == nil && st.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("cannot write there: %s is not a folder", dir)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("cannot write there: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- tightened to 0750 for the service group below, once it exists
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%s does not exist and only root can create it — run with sudo, or point at a file you own with -config", dir)
		}
		return fmt.Errorf("cannot create %s: %w", dir, err)
	}
	if os.Geteuid() == 0 {
		if g, err := user.LookupGroup("watchfor-agent"); err == nil {
			if gid, err := strconv.Atoi(g.Gid); err == nil {
				_ = os.Chown(dir, 0, gid)
				_ = os.Chmod(dir, 0o750) // #nosec G302 -- a directory the service group must enter
			}
		}
	}
	return nil
}

var plainScalar = regexp.MustCompile(`^[A-Za-z0-9_./:@+=-]+$`)

// setInText replaces the value of key ("a.b.c") on its existing line and
// keeps everything else byte for byte — the trailing comment stays in its
// column when the new value fits. ok is false when the key has no line of
// its own (commented out, missing, or inside a flow mapping) or the value
// would need quoting; the caller then falls back to the node editor.
func setInText(text, key, value string) (string, bool) {
	if !plainScalar.MatchString(value) {
		return "", false
	}
	parts := strings.Split(key, ".")
	lines := strings.Split(text, "\n")
	start, indent := 0, 0
	for depth, part := range parts {
		found := -1
		for i := start; i < len(lines); i++ {
			l := lines[i]
			trimmed := strings.TrimLeft(l, " ")
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			ind := len(l) - len(trimmed)
			if ind < indent {
				break // left the parent block
			}
			if ind != indent {
				continue
			}
			if trimmed == part+":" || strings.HasPrefix(trimmed, part+": ") || strings.HasPrefix(trimmed, part+":\t") {
				found = i
				break
			}
		}
		if found < 0 {
			return "", false
		}
		if depth < len(parts)-1 {
			if strings.TrimSpace(lines[found]) != part+":" {
				return "", false // "host: { name: x }" style — not line-editable
			}
			start, indent = found+1, indent+2
			continue
		}
		l := lines[found]
		body, comment := l, ""
		if k := strings.Index(l, " #"); k >= 0 {
			body, comment = l[:k], l[k:]
		}
		prefix := strings.Repeat(" ", indent) + part + ": " + value
		if comment != "" {
			// body ends right before " #": the '#' sat at len(body)+1
			if col := len(body) + 1; col > len(prefix) {
				prefix += strings.Repeat(" ", col-len(prefix))
			} else {
				prefix += " "
			}
			comment = strings.TrimLeft(comment, " ")
		}
		lines[found] = prefix + comment
		return strings.Join(lines, "\n"), true
	}
	return "", false
}

// WriteConfig validates text, keeps the previous file as <path>.bak and
// writes atomically, preserving the old file's mode and owner.
func WriteConfig(path, text string) (*Config, error) {
	cfg, err := ParseText(text)
	if err != nil {
		return nil, fmt.Errorf("refusing to write: the agent would not accept the result: %w", err)
	}
	if old, err := os.ReadFile(path); err == nil {
		_ = writeFresh(path+".bak", old) // best effort: the backup is a courtesy
	}
	if err := writeAtomic(path, []byte(text)); err != nil {
		return nil, err
	}
	return cfg, nil
}

// writeFresh writes a private file that did not exist a moment ago: an
// existing entry is removed first and the new one created exclusively,
// without following a symlink — the shape a root-run write must have in
// a directory it does not fully control.
func writeFresh(path string, data []byte) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
