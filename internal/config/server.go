// Package config loads and validates configuration for both dit binaries.
//
// Precedence is always: explicit flags > environment variables > YAML file >
// built-in defaults. The server binary owns service configuration; the CLI
// owns a small client config file under ~/.config/dit.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Environment variables read by dit-server.
const (
	EnvConfig          = "DIT_CONFIG"
	EnvListen          = "DIT_LISTEN"
	EnvDataDir         = "DIT_DATA_DIR"
	EnvPublicURL       = "DIT_PUBLIC_URL"
	EnvAPIToken        = "DIT_API_TOKEN"
	EnvMasterKey       = "DIT_MASTER_KEY"
	EnvMasterKeyFile   = "DIT_MASTER_KEY_FILE"
	EnvDBPath          = "DIT_DB_PATH"
	EnvCheckInterval   = "DIT_CHECK_INTERVAL"
	EnvCheckConcur     = "DIT_CHECK_CONCURRENCY"
	EnvCheckTimeout    = "DIT_CHECK_TIMEOUT"
	EnvNotifyAttempts  = "DIT_NOTIFY_ATTEMPTS"
	EnvNotifyBackoff   = "DIT_NOTIFY_BACKOFF"
	EnvLogLevel        = "DIT_LOG_LEVEL"
	EnvDefaultChannels = "DIT_DEFAULT_CHANNELS"
)

// Defaults for dit-server.
const (
	DefaultListen           = ":8080"
	DefaultDataDir          = "./data"
	DefaultCheckInterval    = 15 * time.Minute
	DefaultCheckConcurrency = 4
	DefaultCheckTimeout     = 30 * time.Second
	DefaultNotifyAttempts   = 3
	DefaultNotifyBackoff    = 5 * time.Second
	DefaultLogLevel         = "info"
	DefaultDBFile           = "dit.db"
	DefaultMasterKeyFile    = "master.key"
)

// Server is the complete dit-server configuration.
type Server struct {
	Listen           string   `yaml:"listen"`
	DataDir          string   `yaml:"data_dir"`
	PublicURL        string   `yaml:"public_url"`
	DBPath           string   `yaml:"db_path"`
	APIToken         string   `yaml:"api_token"`
	MasterKey        string   `yaml:"master_key"`
	MasterKeyFile    string   `yaml:"master_key_file"`
	CheckInterval    Duration `yaml:"check_interval"`
	CheckConcurrency int      `yaml:"check_concurrency"`
	CheckTimeout     Duration `yaml:"check_timeout"`
	NotifyAttempts   int      `yaml:"notify_attempts"`
	NotifyBackoff    Duration `yaml:"notify_backoff"`
	LogLevel         string   `yaml:"log_level"`

	// DefaultChannels names channels that newly created watches subscribe to
	// when the request does not name any. Empty means "use the channels
	// flagged is_default".
	DefaultChannels []string `yaml:"default_channels"`

	// Path is the config file that was loaded, empty when none was.
	Path string `yaml:"-"`
}

// DefaultServer returns the built-in defaults.
func DefaultServer() *Server {
	return &Server{
		Listen:           DefaultListen,
		DataDir:          DefaultDataDir,
		CheckInterval:    Duration(DefaultCheckInterval),
		CheckConcurrency: DefaultCheckConcurrency,
		CheckTimeout:     Duration(DefaultCheckTimeout),
		NotifyAttempts:   DefaultNotifyAttempts,
		NotifyBackoff:    Duration(DefaultNotifyBackoff),
		LogLevel:         DefaultLogLevel,
	}
}

// LoadServer loads the server configuration from an optional YAML file plus
// the environment. An empty path falls back to $DIT_CONFIG; a missing file is
// not an error.
func LoadServer(path string) (*Server, error) {
	cfg := DefaultServer()

	if path == "" {
		path = strings.TrimSpace(os.Getenv(EnvConfig))
	}
	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(raw, cfg); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
			cfg.Path = path
		case errors.Is(err, fs.ErrNotExist):
			// Explicit path that does not exist is still an error: the
			// operator clearly expected it to be read.
			if os.Getenv(EnvConfig) != "" || strings.TrimSpace(path) != "" {
				return nil, fmt.Errorf("config file %s: %w", path, err)
			}
		default:
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
	}

	if err := applyServerEnv(cfg); err != nil {
		return nil, err
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyServerEnv overlays environment variables onto cfg.
func applyServerEnv(cfg *Server) error {
	setString := func(env string, dst *string) {
		if v, ok := os.LookupEnv(env); ok && strings.TrimSpace(v) != "" {
			*dst = strings.TrimSpace(v)
		}
	}

	setString(EnvListen, &cfg.Listen)
	setString(EnvDataDir, &cfg.DataDir)
	setString(EnvPublicURL, &cfg.PublicURL)
	setString(EnvDBPath, &cfg.DBPath)
	setString(EnvAPIToken, &cfg.APIToken)
	setString(EnvMasterKey, &cfg.MasterKey)
	setString(EnvMasterKeyFile, &cfg.MasterKeyFile)
	setString(EnvLogLevel, &cfg.LogLevel)

	var err error
	if cfg.CheckInterval, err = envDuration(EnvCheckInterval, cfg.CheckInterval); err != nil {
		return err
	}
	if cfg.CheckTimeout, err = envDuration(EnvCheckTimeout, cfg.CheckTimeout); err != nil {
		return err
	}
	if cfg.NotifyBackoff, err = envDuration(EnvNotifyBackoff, cfg.NotifyBackoff); err != nil {
		return err
	}
	if cfg.CheckConcurrency, err = envInt(EnvCheckConcur, cfg.CheckConcurrency); err != nil {
		return err
	}
	if cfg.NotifyAttempts, err = envInt(EnvNotifyAttempts, cfg.NotifyAttempts); err != nil {
		return err
	}
	if raw, ok := os.LookupEnv(EnvDefaultChannels); ok {
		cfg.DefaultChannels = splitList(raw)
	}
	return nil
}

// normalize fills in values derived from other fields.
func (c *Server) normalize() {
	c.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
	if c.LogLevel == "" {
		c.LogLevel = DefaultLogLevel
	}
	if strings.TrimSpace(c.DataDir) == "" {
		c.DataDir = DefaultDataDir
	}
	c.DataDir = filepath.Clean(c.DataDir)
	if strings.TrimSpace(c.DBPath) == "" {
		c.DBPath = filepath.Join(c.DataDir, DefaultDBFile)
	}
	if strings.TrimSpace(c.MasterKeyFile) == "" {
		c.MasterKeyFile = filepath.Join(c.DataDir, DefaultMasterKeyFile)
	}
}

// Validate reports configuration that cannot work.
func (c *Server) Validate() error {
	if strings.TrimSpace(c.APIToken) == "" {
		return fmt.Errorf("%s must be set: the server refuses to run without a shared secret", EnvAPIToken)
	}
	if c.CheckConcurrency < 1 {
		return fmt.Errorf("check_concurrency must be >= 1 (got %d)", c.CheckConcurrency)
	}
	if c.CheckConcurrency > 64 {
		return fmt.Errorf("check_concurrency must be <= 64 (got %d)", c.CheckConcurrency)
	}
	if c.CheckInterval.Std() <= 0 {
		return fmt.Errorf("check_interval must be positive (got %s)", c.CheckInterval)
	}
	if c.CheckTimeout.Std() <= 0 {
		return fmt.Errorf("check_timeout must be positive (got %s)", c.CheckTimeout)
	}
	if c.NotifyAttempts < 1 {
		return fmt.Errorf("notify_attempts must be >= 1 (got %d)", c.NotifyAttempts)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level must be one of debug, info, warn, error (got %q)", c.LogLevel)
	}
	if c.MasterKey != "" {
		if _, err := decodeMasterKey(c.MasterKey); err != nil {
			return fmt.Errorf("%s: %w", EnvMasterKey, err)
		}
	}
	return nil
}

// Redacted returns a copy of the configuration with secrets masked, suitable
// for logging at startup.
func (c *Server) Redacted() map[string]any {
	token := ""
	if c.APIToken != "" {
		token = "***"
	}
	key := ""
	if c.MasterKey != "" {
		key = "***"
	}
	return map[string]any{
		"listen":            c.Listen,
		"data_dir":          c.DataDir,
		"db_path":           c.DBPath,
		"public_url":        c.PublicURL,
		"api_token":         token,
		"master_key":        key,
		"master_key_file":   c.MasterKeyFile,
		"check_interval":    c.CheckInterval.String(),
		"check_concurrency": c.CheckConcurrency,
		"check_timeout":     c.CheckTimeout.String(),
		"notify_attempts":   c.NotifyAttempts,
		"notify_backoff":    c.NotifyBackoff.String(),
		"log_level":         c.LogLevel,
		"config_file":       c.Path,
	}
}

func envDuration(env string, fallback Duration) (Duration, error) {
	raw, ok := os.LookupEnv(env)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fallback, fmt.Errorf("%s=%q: %w", env, raw, err)
	}
	return Duration(parsed), nil
}

func envInt(env string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(env)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback, fmt.Errorf("%s=%q: %w", env, raw, err)
	}
	return parsed, nil
}

func splitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
