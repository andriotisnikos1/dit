package apitypes

import "time"

// Credential kinds understood by the registry client.
const (
	CredentialBasic = "basic"
	CredentialToken = "token"
)

// Registry is a registry host the server knows about, either because a watch
// references it or because credentials are stored for it.
type Registry struct {
	Host           string     `json:"host"                      yaml:"host"`
	HasCredentials bool       `json:"has_credentials"           yaml:"has_credentials"`
	Username       string     `json:"username,omitempty"        yaml:"username,omitempty"`
	Kind           string     `json:"kind,omitempty"            yaml:"kind,omitempty"`
	Watches        int        `json:"watches"                   yaml:"watches"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"    yaml:"last_used_at,omitempty"`
	LastOKAt       *time.Time `json:"last_ok_at,omitempty"      yaml:"last_ok_at,omitempty"`
	CreatedAt      *time.Time `json:"created_at,omitempty"      yaml:"created_at,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"      yaml:"updated_at,omitempty"`
}

// CredentialsRequest is the body of PUT /api/v1/registries/{host}/credentials.
type CredentialsRequest struct {
	Username string `json:"username,omitempty" yaml:"username,omitempty"`
	Password string `json:"password"           yaml:"password"`
	Kind     string `json:"kind,omitempty"     yaml:"kind,omitempty"`
}

// CredentialsInfo is the metadata-only read of stored registry credentials.
type CredentialsInfo struct {
	Host       string     `json:"host"                  yaml:"host"`
	Username   string     `json:"username,omitempty"    yaml:"username,omitempty"`
	Kind       string     `json:"kind,omitempty"        yaml:"kind,omitempty"`
	HasSecret  bool       `json:"has_secret"            yaml:"has_secret"`
	CreatedAt  *time.Time `json:"created_at,omitempty"  yaml:"created_at,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"  yaml:"updated_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty" yaml:"last_used_at,omitempty"`
	LastOKAt   *time.Time `json:"last_ok_at,omitempty"  yaml:"last_ok_at,omitempty"`
}

// ClientConfig is the effective (non-secret) answer to GET
// /api/v1/config for the CLI. It carries only what the operator already knows.
type ClientConfig struct {
	Server  string `json:"server" yaml:"server"`
	Version string `json:"version" yaml:"version"`
}
