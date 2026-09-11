package apitypes

import (
	"strings"
	"time"
)

// ChannelType is the transport used by a notification channel.
type ChannelType string

const (
	ChannelEmail     ChannelType = "email"
	ChannelEmailHTTP ChannelType = "email-http"
	ChannelNtfy      ChannelType = "ntfy"
)

// ChannelTypes lists every supported channel type.
func ChannelTypes() []ChannelType {
	return []ChannelType{ChannelEmail, ChannelEmailHTTP, ChannelNtfy}
}

// ChannelTypeList renders ChannelTypes as a comma-separated list for messages.
func ChannelTypeList() string {
	names := make([]string, 0, len(ChannelTypes()))
	for _, t := range ChannelTypes() {
		names = append(names, string(t))
	}
	return strings.Join(names, ", ")
}

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

	// Keys used by the email-http channel type.
	ConfigProvider  = "provider"
	ConfigAPIKey    = "api_key"
	ConfigAccountID = "account_id"
	ConfigFromName  = "from_name"
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
