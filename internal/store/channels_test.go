package store

import (
	"context"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func TestChannelSecretsAreSealedAtRest(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	const password = "super-secret-smtp-password"
	channel, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "mail",
		Type: apitypes.ChannelEmail,
		Config: map[string]string{
			apitypes.ConfigSMTPHost: "smtp.example.com",
			apitypes.ConfigFrom:     "dit@example.com",
			apitypes.ConfigTo:       "ops@example.com",
			apitypes.ConfigUsername: "dit",
			apitypes.ConfigPassword: password,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if !channel.HasSecret() {
		t.Error("HasSecret = false, want true")
	}

	// The raw column must not contain the plaintext.
	var stored string
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT config_json FROM channels WHERE id = ?`, channel.ID).Scan(&stored); err != nil {
		t.Fatalf("read raw config: %v", err)
	}
	if contains(stored, password) {
		t.Error("the SMTP password is stored in plaintext")
	}
	if !contains(stored, "sealed:") {
		t.Error("the stored config carries no sealed marker")
	}

	// Reading back decrypts.
	loaded, err := db.GetChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if loaded.Config[apitypes.ConfigPassword] != password {
		t.Error("the password did not round-trip through the sealer")
	}

	// The redacted view strips it.
	redacted := loaded.Redacted()
	if redacted.Config[apitypes.ConfigPassword] != "" {
		t.Error("Redacted() leaked the password")
	}
	if redacted.Config[apitypes.ConfigSMTPHost] != "smtp.example.com" {
		t.Error("Redacted() stripped a non-secret field")
	}
	if !loaded.HasSecret() {
		t.Error("HasSecret = false on the loaded channel")
	}
}

func TestChannelUpdatePreservesOmittedSecret(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	channel, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "ntfy", Type: apitypes.ChannelNtfy,
		Config: map[string]string{
			apitypes.ConfigTopic: "alerts",
			apitypes.ConfigToken: "tk_secret",
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	// An update that echoes the redacted config back must not wipe the token.
	updated, err := db.UpdateChannel(ctx, channel.ID, UpdateChannelInput{
		Config: &map[string]string{
			apitypes.ConfigTopic: "alerts-renamed",
			apitypes.ConfigToken: "",
		},
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if updated.Config[apitypes.ConfigToken] != "tk_secret" {
		t.Errorf("token = %q after an update that omitted it, want the stored value", updated.Config[apitypes.ConfigToken])
	}
	if updated.Config[apitypes.ConfigTopic] != "alerts-renamed" {
		t.Errorf("topic = %q, want alerts-renamed", updated.Config[apitypes.ConfigTopic])
	}

	// An explicit new token replaces it.
	updated, err = db.UpdateChannel(ctx, channel.ID, UpdateChannelInput{
		Config: &map[string]string{apitypes.ConfigToken: "tk_new"},
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if updated.Config[apitypes.ConfigToken] != "tk_new" {
		t.Errorf("token = %q, want tk_new", updated.Config[apitypes.ConfigToken])
	}
}

func TestChannelNameIsUnique(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	mustCreateChannel(t, db, "ops")
	if _, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"}, Enabled: true,
	}); !IsDuplicate(err) {
		t.Errorf("duplicate channel name: got %v, want ErrDuplicate", err)
	}

	// Renaming onto an existing name must also conflict.
	other := mustCreateChannel(t, db, "dev")
	name := "ops"
	if _, err := db.UpdateChannel(ctx, other.ID, UpdateChannelInput{Name: &name}); !IsDuplicate(err) {
		t.Errorf("rename onto an existing name: got %v, want ErrDuplicate", err)
	}
}

func TestChannelDefaultsAndResolution(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	defaults := mustCreateChannel(t, db, "default-ops")
	if _, err := db.UpdateChannel(ctx, defaults.ID, UpdateChannelInput{IsDefault: boolPtr(true)}); err != nil {
		t.Fatalf("mark default: %v", err)
	}
	other := mustCreateChannel(t, db, "extra")

	// A watch with no subscriptions resolves to the default set.
	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)
	resolved, err := db.ChannelsForWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("ChannelsForWatch: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != defaults.ID {
		t.Fatalf("ChannelsForWatch = %v, want only the default channel", resolved)
	}

	// An explicit subscription overrides the defaults.
	if err := db.SetWatchChannels(ctx, watch.ID, []string{other.ID}); err != nil {
		t.Fatalf("SetWatchChannels: %v", err)
	}
	resolved, err = db.ChannelsForWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("ChannelsForWatch: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != other.ID {
		t.Fatalf("ChannelsForWatch = %v, want the explicit subscription", resolved)
	}

	// A disabled channel is skipped.
	disabled := false
	if _, err := db.UpdateChannel(ctx, other.ID, UpdateChannelInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable channel: %v", err)
	}
	resolved, err = db.ChannelsForWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("ChannelsForWatch: %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("ChannelsForWatch = %v, want empty for a disabled subscription", resolved)
	}

	// A disabled default is skipped too.
	if _, err := db.UpdateChannel(ctx, defaults.ID, UpdateChannelInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable default: %v", err)
	}
	if err := db.SetWatchChannels(ctx, watch.ID, nil); err != nil {
		t.Fatalf("clear subscriptions: %v", err)
	}
	resolved, err = db.ChannelsForWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("ChannelsForWatch: %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("ChannelsForWatch = %v, want empty when every default is disabled", resolved)
	}
}

func TestChannelConfigValidation(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	if _, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "bad-type", Type: "carrier-pigeon",
		Config: map[string]string{}, Enabled: true,
	}); err == nil {
		t.Error("CreateChannel with an unknown type: expected an error")
	}
	if _, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"}, Enabled: true,
	}); err == nil {
		t.Error("CreateChannel with an empty name: expected an error")
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func boolPtr(v bool) *bool { return &v }
