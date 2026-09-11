package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Environment variables read by the dit CLI.
const (
	EnvServerURL = "DIT_SERVER_URL"
	EnvOutput    = "DIT_OUTPUT"
)

// CLI is the on-disk configuration of the operator CLI. The CLI keeps no other
// local state: everything else lives on the server.
type CLI struct {
	Server string `yaml:"server"`
	Token  string `yaml:"token,omitempty"`
	Output string `yaml:"output,omitempty"`

	path string
}

// DefaultCLIPath resolves the config file location:
// $DIT_CONFIG, else $XDG_CONFIG_HOME/dit/config.yaml, else
// ~/.config/dit/config.yaml.
func DefaultCLIPath() string {
	if v := strings.TrimSpace(os.Getenv(EnvConfig)); v != "" {
		return v
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "dit", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".config", "dit", "config.yaml")
	}
	return filepath.Join(home, ".config", "dit", "config.yaml")
}

// LoadCLI reads the CLI config file. A missing file yields an empty config so
// that flags and environment alone are enough to drive the CLI.
func LoadCLI(path string) (*CLI, error) {
	cfg, err := LoadCLIFile(path)
	if err != nil {
		return nil, err
	}

	if v := strings.TrimSpace(os.Getenv(EnvServerURL)); v != "" {
		cfg.Server = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvAPIToken)); v != "" {
		cfg.Token = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvOutput)); v != "" {
		cfg.Output = v
	}
	if cfg.Output == "" {
		cfg.Output = "table"
	}
	return cfg, nil
}

// LoadCLIFile reads only the config file, ignoring environment overrides.
// Writers must use this: persisting an environment value into the file would
// silently turn a one-off override into a permanent setting.
func LoadCLIFile(path string) (*CLI, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultCLIPath()
	}
	cfg := &CLI{path: path}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		// fine: flags and env may supply everything.
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return cfg, nil
}

// Path returns the file this config was (or will be) read from.
func (c *CLI) Path() string {
	if c.path == "" {
		c.path = DefaultCLIPath()
	}
	return c.path
}

// SetPath overrides the config file location.
func (c *CLI) SetPath(path string) {
	if strings.TrimSpace(path) != "" {
		c.path = path
	}
}

// Save writes the config file with 0600 permissions, creating its directory
// with 0700 if needed.
func (c *CLI) Save() error {
	path := c.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	header := "# dit CLI configuration. Holds the server URL and the shared secret.\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), raw...), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Redacted returns a copy whose token is masked, for `dit config show`.
func (c *CLI) Redacted() *CLI {
	clone := *c
	if clone.Token != "" {
		clone.Token = maskSecret(clone.Token)
	}
	return &clone
}

// RedactedMap is the map form used for table rendering.
func (c *CLI) RedactedMap() map[string]string {
	out := map[string]string{
		"path":   c.Path(),
		"server": c.Server,
		"output": c.Output,
		"token":  "",
	}
	if c.Token != "" {
		out["token"] = maskSecret(c.Token)
	}
	return out
}

// Validate reports a configuration that cannot drive the API.
func (c *CLI) Validate() error {
	if strings.TrimSpace(c.Server) == "" {
		return fmt.Errorf("no server configured: run `dit config set-server <url>` or set %s", EnvServerURL)
	}
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("no API token configured: run `dit config set-token` or set %s", EnvAPIToken)
	}
	return nil
}

func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + strings.Repeat("*", 8) + s[len(s)-2:]
}
