package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultServerValues(t *testing.T) {
	cfg := DefaultServer()
	if cfg.Listen != DefaultListen {
		t.Errorf("Listen = %q, want %q", cfg.Listen, DefaultListen)
	}
	if cfg.CheckInterval.Std() != DefaultCheckInterval {
		t.Errorf("CheckInterval = %s, want %s", cfg.CheckInterval, DefaultCheckInterval)
	}
	if cfg.CheckConcurrency != DefaultCheckConcurrency {
		t.Errorf("CheckConcurrency = %d, want %d", cfg.CheckConcurrency, DefaultCheckConcurrency)
	}
	if cfg.NotifyAttempts != DefaultNotifyAttempts {
		t.Errorf("NotifyAttempts = %d, want %d", cfg.NotifyAttempts, DefaultNotifyAttempts)
	}
}

func TestLoadServerYAMLAndEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
listen: ":9999"
data_dir: /tmp/dit-data
check_interval: 5m
check_concurrency: 8
check_timeout: 45s
notify_attempts: 5
notify_backoff: 2s
log_level: debug
default_channels:
  - ops
  - oncall
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(EnvAPIToken, "from-env")

	cfg, err := LoadServer(path)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if cfg.Listen != ":9999" {
		t.Errorf("Listen = %q, want :9999", cfg.Listen)
	}
	if cfg.CheckInterval.Std() != 5*time.Minute {
		t.Errorf("CheckInterval = %s, want 5m", cfg.CheckInterval)
	}
	if cfg.CheckConcurrency != 8 {
		t.Errorf("CheckConcurrency = %d, want 8", cfg.CheckConcurrency)
	}
	if cfg.NotifyBackoff.Std() != 2*time.Second {
		t.Errorf("NotifyBackoff = %s, want 2s", cfg.NotifyBackoff)
	}
	if len(cfg.DefaultChannels) != 2 {
		t.Errorf("DefaultChannels = %v, want two entries", cfg.DefaultChannels)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
	// The database path is derived from data_dir when not set explicitly.
	if cfg.DBPath != filepath.Join("/tmp/dit-data", DefaultDBFile) {
		t.Errorf("DBPath = %q, want it derived from data_dir", cfg.DBPath)
	}

	// The environment overrides the file.
	t.Setenv(EnvListen, ":7777")
	t.Setenv(EnvCheckInterval, "90s")
	t.Setenv(EnvCheckConcur, "16")

	cfg, err = LoadServer(path)
	if err != nil {
		t.Fatalf("LoadServer with env overrides: %v", err)
	}
	if cfg.Listen != ":7777" {
		t.Errorf("Listen = %q, want :7777 from the environment", cfg.Listen)
	}
	if cfg.CheckInterval.Std() != 90*time.Second {
		t.Errorf("CheckInterval = %s, want 90s from the environment", cfg.CheckInterval)
	}
	if cfg.CheckConcurrency != 16 {
		t.Errorf("CheckConcurrency = %d, want 16 from the environment", cfg.CheckConcurrency)
	}
}

func TestLoadServerDefaultsWithoutFile(t *testing.T) {
	// A non-existent path supplied through the environment is an error: the
	// operator clearly expected it to be read.
	t.Setenv(EnvConfig, filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv(EnvAPIToken, "token")
	if _, err := LoadServer(""); err == nil {
		t.Error("LoadServer with a missing DIT_CONFIG file: expected an error")
	}
}

func TestServerValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Server)
		want   string
	}{
		{"missing token", func(s *Server) { s.APIToken = "" }, "DIT_API_TOKEN"},
		{"zero concurrency", func(s *Server) { s.CheckConcurrency = 0 }, "check_concurrency"},
		{"excessive concurrency", func(s *Server) { s.CheckConcurrency = 100 }, "check_concurrency"},
		{"zero interval", func(s *Server) { s.CheckInterval = 0 }, "check_interval"},
		{"zero timeout", func(s *Server) { s.CheckTimeout = 0 }, "check_timeout"},
		{"zero attempts", func(s *Server) { s.NotifyAttempts = 0 }, "notify_attempts"},
		{"bad log level", func(s *Server) { s.LogLevel = "loud" }, "log_level"},
		{"bad master key", func(s *Server) { s.MasterKey = "not-base64!!" }, "DIT_MASTER_KEY"},
		{"short master key", func(s *Server) { s.MasterKey = "c2hvcnQ=" }, "master key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultServer()
			cfg.APIToken = "token"
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted an invalid config")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// A valid default plus token passes.
	cfg := DefaultServer()
	cfg.APIToken = "token"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate on a valid config: %v", err)
	}
}

func TestServerRedactedHidesSecrets(t *testing.T) {
	cfg := DefaultServer()
	cfg.APIToken = "super-secret-token"
	cfg.MasterKey = "c2VjcmV0"

	redacted := cfg.Redacted()
	if redacted["api_token"] != "***" {
		t.Errorf("api_token = %v, want ***", redacted["api_token"])
	}
	if redacted["master_key"] != "***" {
		t.Errorf("master_key = %v, want ***", redacted["master_key"])
	}
	for _, v := range redacted {
		if s, ok := v.(string); ok && strings.Contains(s, "super-secret-token") {
			t.Error("Redacted leaked the API token")
		}
	}
}

func TestDurationUnmarshal(t *testing.T) {
	cases := map[string]time.Duration{
		`check_interval: 15m`:  15 * time.Minute,
		`check_interval: 90s`:  90 * time.Second,
		`check_interval: 3600`: time.Hour,
		`check_interval: 0`:    0,
	}
	for body, want := range cases {
		var cfg Server
		if err := unmarshalYAML(t, body, &cfg); err != nil {
			t.Errorf("unmarshal %q: %v", body, err)
			continue
		}
		if cfg.CheckInterval.Std() != want {
			t.Errorf("%q gave %s, want %s", body, cfg.CheckInterval, want)
		}
	}

	var cfg Server
	if err := unmarshalYAML(t, `check_interval: not-a-duration`, &cfg); err == nil {
		t.Error("unmarshal of an invalid duration: expected an error")
	}
}

func TestCLIConfigPrecedenceAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := &CLI{Server: "http://file:8080", Token: "file-token", Output: "table"}
	cfg.SetPath(path)
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config permissions = %o, want 600", perm)
	}

	loaded, err := LoadCLI(path)
	if err != nil {
		t.Fatalf("LoadCLI: %v", err)
	}
	if loaded.Server != "http://file:8080" || loaded.Token != "file-token" {
		t.Errorf("loaded = %+v, want the file values", loaded)
	}

	// The environment wins over the file.
	t.Setenv(EnvServerURL, "http://env:9090")
	t.Setenv(EnvAPIToken, "env-token")
	loaded, err = LoadCLI(path)
	if err != nil {
		t.Fatalf("LoadCLI with env: %v", err)
	}
	if loaded.Server != "http://env:9090" {
		t.Errorf("Server = %q, want the environment value", loaded.Server)
	}
	if loaded.Token != "env-token" {
		t.Errorf("Token = %q, want the environment value", loaded.Token)
	}

	// LoadCLIFile ignores the environment, which is what writers need.
	fileOnly, err := LoadCLIFile(path)
	if err != nil {
		t.Fatalf("LoadCLIFile: %v", err)
	}
	if fileOnly.Server != "http://file:8080" {
		t.Errorf("LoadCLIFile Server = %q, want the file value", fileOnly.Server)
	}
}

func TestCLIConfigMaskingAndValidation(t *testing.T) {
	cfg := &CLI{Server: "http://x:1", Token: "abcdefghijklmnop"}
	redacted := cfg.Redacted()
	if redacted.Token == "abcdefghijklmnop" {
		t.Error("Redacted did not mask the token")
	}
	if !strings.Contains(redacted.Token, "*") {
		t.Errorf("masked token = %q, want asterisks", redacted.Token)
	}

	m := cfg.RedactedMap()
	if m["token"] == "abcdefghijklmnop" {
		t.Error("RedactedMap leaked the token")
	}
	if m["server"] != "http://x:1" {
		t.Errorf("RedactedMap server = %q", m["server"])
	}

	// A short token is masked entirely rather than partially revealed.
	short := (&CLI{Token: "abc"}).RedactedMap()
	if short["token"] != "****" {
		t.Errorf("short token mask = %q, want ****", short["token"])
	}

	if err := (&CLI{}).Validate(); err == nil {
		t.Error("Validate with no server: expected an error")
	}
	if err := (&CLI{Server: "http://x"}).Validate(); err == nil {
		t.Error("Validate with no token: expected an error")
	}
	if err := (&CLI{Server: "http://x", Token: "t"}).Validate(); err != nil {
		t.Errorf("Validate on a complete config: %v", err)
	}
}

func TestDefaultCLIPathHonoursEnv(t *testing.T) {
	t.Setenv(EnvConfig, "/tmp/explicit.yaml")
	if got := DefaultCLIPath(); got != "/tmp/explicit.yaml" {
		t.Errorf("DefaultCLIPath = %q, want DIT_CONFIG", got)
	}

	os.Unsetenv(EnvConfig)
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := DefaultCLIPath(); got != filepath.Join("/tmp/xdg", "dit", "config.yaml") {
		t.Errorf("DefaultCLIPath = %q, want the XDG path", got)
	}
}

func TestLoadCLIMissingFileIsNotAnError(t *testing.T) {
	cfg, err := LoadCLI(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("LoadCLI on a missing file: %v", err)
	}
	if cfg.Output != "table" {
		t.Errorf("Output = %q, want the table default", cfg.Output)
	}
}
