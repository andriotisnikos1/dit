package apitypes

import "time"

// ChannelType is the transport used by a notification channel.
type ChannelType string

const (
	ChannelEmail ChannelType = "email"
	ChannelNtfy  ChannelType = "ntfy"
)

// ChannelTypes lists every supported channel type.
func ChannelTypes() []ChannelType { return []ChannelType{ChannelEmail, ChannelNtfy} }

// Valid reports whether t is a known channel type.
func (t ChannelType) Valid() bool {
	for _, known := range ChannelTypes() {
		if t == known {
			return true
		}
	}
	return false
}

// Config keys understood by each channel type.
const (
	ConfigSMTPHost    = "smtp_host"
	ConfigSMTPPort    = "smtp_port"
	ConfigSTARTTLS    = "starttls"
	ConfigImplicitTLS = "implicit_tls"
	ConfigFrom        = "from"
	ConfigTo          = "to"
	ConfigUsername    = "username"
	ConfigPassword    = "password"

	ConfigURL      = "url"
	ConfigTopic    = "topic"
	ConfigToken    = "token"
	ConfigPriority = "priority"
	ConfigTags     = "tags"
)

// Channel is the wire representation of a notification channel. Secrets in
// Config are never returned by the API: the server writes an empty string for
// password and token and reports secret presence through HasSecret instead.
type Channel struct {
	ID        string            `json:"id"                   yaml:"id"`
	Name      string            `json:"name"                 yaml:"name"`
	Type      ChannelType       `json:"type"                 yaml:"type"`
	Config    map[string]string `json:"config"               yaml:"config"`
	HasSecret bool              `json:"has_secret"           yaml:"has_secret"`
	Enabled   bool              `json:"enabled"              yaml:"enabled"`
	IsDefault bool              `json:"is_default"           yaml:"is_default"`
	CreatedAt time.Time         `json:"created_at"           yaml:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"           yaml:"updated_at"`
}

// CreateChannelRequest is the body of POST /api/v1/channels.
type CreateChannelRequest struct {
	Name   string            `json:"name"   yaml:"name"`
	Type   ChannelType       `json:"type"   yaml:"type"`
	Config map[string]string `json:"config" yaml:"config"`
}

// UpdateChannelRequest is the body of PATCH /api/v1/channels/{id}. Config, when
// present, replaces the stored config; secrets omitted from it are preserved.
type UpdateChannelRequest struct {
	Name      *string            `json:"name,omitempty"       yaml:"name,omitempty"`
	Config    *map[string]string `json:"config,omitempty"     yaml:"config,omitempty"`
	Enabled   *bool              `json:"enabled,omitempty"    yaml:"enabled,omitempty"`
	IsDefault *bool              `json:"is_default,omitempty" yaml:"is_default,omitempty"`
}

// ChannelTestResult is returned by POST /api/v1/channels/{id}/test.
type ChannelTestResult struct {
	ChannelID string `json:"channel_id" yaml:"channel_id"`
	OK        bool   `json:"ok"         yaml:"ok"`
	Error     string `json:"error,omitempty" yaml:"error,omitempty"`
}
