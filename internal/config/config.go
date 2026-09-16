// Package config loads agent.yml. Secrets are never inline: the token comes
// from a 0600 file or an environment variable, and the file's mode is
// checked so a world-readable token fails loudly instead of leaking quietly.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   Server               `yaml:"server"`
	Host     Host                 `yaml:"host"`
	Interval time.Duration        `yaml:"interval"`
	Spool    Spool                `yaml:"spool"`
	Log      Log                  `yaml:"log"`
	Updates  Updates              `yaml:"updates"`
	Modules  map[string]yaml.Node `yaml:"modules"`
}

type Server struct {
	URL       string        `yaml:"url"`
	Token     string        `yaml:"token"`
	TokenFile string        `yaml:"token_file"`
	CAFile    string        `yaml:"ca_file"`
	Timeout   time.Duration `yaml:"timeout"`
}

type Host struct {
	Name string            `yaml:"name"`
	Tags map[string]string `yaml:"tags"`
}

type Spool struct {
	Dir   string `yaml:"dir"`
	MaxMB int    `yaml:"max_mb"`
}

type Log struct {
	Level string `yaml:"level"`
}

// Updates is the agent's own record of the auto-update setting.
// `watchfor-agent auto-update on|off` writes it together with enabling or
// disabling the systemd timer, and `upgrade -if-available` (what the
// timer runs) does nothing while it is explicitly false. Unset means "no
// opinion" — the timer alone decides.
type Updates struct {
	Auto *bool `yaml:"auto"`
}

const (
	MinInterval     = 5 * time.Second
	DefaultInterval = 15 * time.Second
	DefaultTimeout  = 15 * time.Second
	DefaultSpoolDir = "/var/lib/watchfor-agent"
	DefaultSpoolMB  = 64
	MaxTags         = 32
)

var DefaultModules = []string{"system", "disk", "network", "processes"}

var (
	hostNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
	tagKeyRe   = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,63}$`)
)

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func Parse(r io.Reader) (*Config, error) { return parseConfig(r, true) }

// parseConfig decodes and validates; withToken also reads the token source
// (file / env), which the CLI's config editor skips — a host whose token
// is not written yet must still be able to change its settings.
func parseConfig(r io.Reader, withToken bool) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	if withToken {
		if err := cfg.resolveToken(); err != nil {
			return nil, err
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() error {
	if c.Interval == 0 {
		c.Interval = DefaultInterval
	}
	if c.Server.Timeout == 0 {
		c.Server.Timeout = DefaultTimeout
	}
	if c.Spool.Dir == "" {
		c.Spool.Dir = DefaultSpoolDir
	}
	if c.Spool.MaxMB == 0 {
		c.Spool.MaxMB = DefaultSpoolMB
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Host.Name == "" {
		name, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("host.name not set and hostname lookup failed: %w", err)
		}
		c.Host.Name = strings.SplitN(name, ".", 2)[0]
	}
	if c.Modules == nil {
		c.Modules = make(map[string]yaml.Node, len(DefaultModules))
		for _, m := range DefaultModules {
			c.Modules[m] = yaml.Node{}
		}
	}
	return nil
}

// resolveToken turns `token: env:NAME` and `token_file` into the literal
// token. A literal `token:` in the file is accepted but discouraged; the
// README says why.
func (c *Config) resolveToken() error {
	s := &c.Server
	if s.Token != "" && s.TokenFile != "" {
		return errors.New("server: set either token or token_file, not both")
	}
	if env, ok := strings.CutPrefix(s.Token, "env:"); ok {
		v := os.Getenv(env)
		if v == "" {
			return fmt.Errorf("server.token: environment variable %s is empty", env)
		}
		s.Token = v
	}
	if s.TokenFile != "" {
		tok, err := readSecretFile(s.TokenFile)
		if err != nil {
			return fmt.Errorf("server.token_file: %w", err)
		}
		s.Token = tok
	}
	s.Token = strings.TrimSpace(s.Token)
	return nil
}

func readSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by other users (mode %04o); chmod 600 it", path, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (c *Config) validate() error {
	if c.Interval < MinInterval {
		return fmt.Errorf("interval %s is below the minimum %s", c.Interval, MinInterval)
	}
	if c.Server.URL != "" {
		u, err := url.Parse(c.Server.URL)
		if err != nil || u.Host == "" {
			return fmt.Errorf("server.url %q is not a valid URL", c.Server.URL)
		}
		if u.Scheme != "https" && !isLoopback(u.Hostname()) {
			return fmt.Errorf("server.url must use https (got %s)", u.Scheme)
		}
	}
	if !hostNameRe.MatchString(c.Host.Name) {
		return fmt.Errorf("host.name %q: letters, digits, dot, underscore and dash only", c.Host.Name)
	}
	if len(c.Host.Tags) > MaxTags {
		return fmt.Errorf("host.tags: at most %d tags", MaxTags)
	}
	for k, v := range c.Host.Tags {
		if !tagKeyRe.MatchString(k) {
			return fmt.Errorf("host.tags: key %q must match %s", k, tagKeyRe)
		}
		if len(v) > 128 {
			return fmt.Errorf("host.tags: value for %q is longer than 128 characters", k)
		}
	}
	if c.Spool.MaxMB < 0 {
		return errors.New("spool.max_mb must not be negative")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level %q: use debug, info, warn or error", c.Log.Level)
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// HasToken reports whether a token was configured; `check` runs without one.
func (c *Config) HasToken() bool { return c.Server.Token != "" }

// IsNotExist lets the CLI distinguish "no config yet" from a broken one.
func IsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
