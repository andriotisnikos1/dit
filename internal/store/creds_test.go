package store

import (
	"context"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func TestRegistryCredentialsRoundTripAndSealing(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	const secret = "ghp_averysecrettokenvalue"
	creds, err := db.PutCredentials(ctx, "ghcr.io", "andriotis", secret, apitypes.CredentialBasic)
	if err != nil {
		t.Fatalf("PutCredentials: %v", err)
	}
	if creds.Registry != "ghcr.io" || creds.Username != "andriotis" {
		t.Errorf("stored credentials = %+v, want ghcr.io/andriotis", creds)
	}

	// The raw column must be sealed.
	var stored string
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT secret_enc FROM registry_creds WHERE registry = ?`, "ghcr.io").Scan(&stored); err != nil {
		t.Fatalf("read raw secret: %v", err)
	}
	if contains(stored, secret) {
		t.Error("the registry secret is stored in plaintext")
	}

	// Reading decrypts.
	loaded, err := db.GetCredentials(ctx, "ghcr.io")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if loaded.Secret != secret {
		t.Error("the secret did not round-trip through the sealer")
	}

	// The listing is metadata-only: Secret must stay empty.
	listed, err := db.ListCredentials(ctx)
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListCredentials returned %d rows, want 1", len(listed))
	}
	if listed[0].Secret != "" {
		t.Error("ListCredentials leaked the secret")
	}
	if listed[0].Username != "andriotis" {
		t.Error("ListCredentials dropped the username")
	}
}

func TestPutCredentialsReplaces(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	if _, err := db.PutCredentials(ctx, "ghcr.io", "old", "old-secret", apitypes.CredentialBasic); err != nil {
		t.Fatalf("PutCredentials: %v", err)
	}
	if _, err := db.PutCredentials(ctx, "ghcr.io", "new", "new-secret", apitypes.CredentialToken); err != nil {
		t.Fatalf("PutCredentials (replace): %v", err)
	}

	loaded, err := db.GetCredentials(ctx, "ghcr.io")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if loaded.Username != "new" || loaded.Secret != "new-secret" || loaded.Kind != apitypes.CredentialToken {
		t.Errorf("credentials = %+v, want the replaced values", loaded)
	}

	listed, err := db.ListCredentials(ctx)
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	if len(listed) != 1 {
		t.Errorf("ListCredentials returned %d rows, want 1 after a replace", len(listed))
	}
}

func TestPutCredentialsValidation(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	if _, err := db.PutCredentials(ctx, "", "u", "s", apitypes.CredentialBasic); err == nil {
		t.Error("PutCredentials with an empty registry: expected an error")
	}
	if _, err := db.PutCredentials(ctx, "ghcr.io", "u", "", apitypes.CredentialBasic); err == nil {
		t.Error("PutCredentials with an empty secret: expected an error")
	}
}

func TestDeleteAndTouchCredentials(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	if _, err := db.PutCredentials(ctx, "ghcr.io", "u", "s", apitypes.CredentialBasic); err != nil {
		t.Fatalf("PutCredentials: %v", err)
	}
	if err := db.TouchCredentials(ctx, "ghcr.io", true); err != nil {
		t.Fatalf("TouchCredentials: %v", err)
	}
	loaded, err := db.GetCredentials(ctx, "ghcr.io")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if loaded.LastUsedAt == nil {
		t.Error("LastUsedAt = nil after TouchCredentials")
	}
	if loaded.LastOKAt == nil {
		t.Error("LastOKAt = nil after a successful touch")
	}

	if err := db.TouchCredentials(ctx, "missing.example.com", true); !IsNotFound(err) {
		t.Errorf("TouchCredentials on an unknown registry: got %v, want ErrNotFound", err)
	}

	if err := db.DeleteCredentials(ctx, "ghcr.io"); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	if _, err := db.GetCredentials(ctx, "ghcr.io"); !IsNotFound(err) {
		t.Errorf("GetCredentials after delete: got %v, want ErrNotFound", err)
	}
	if err := db.DeleteCredentials(ctx, "ghcr.io"); !IsNotFound(err) {
		t.Errorf("second DeleteCredentials: got %v, want ErrNotFound", err)
	}
}

func TestRegistryHostsUnion(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	// One registry with credentials and watches, one with watches only, one
	// with credentials only.
	if _, err := db.PutCredentials(ctx, "ghcr.io", "u", "s", apitypes.CredentialBasic); err != nil {
		t.Fatalf("PutCredentials: %v", err)
	}
	mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)
	mustCreateWatch(t, db, "ghcr.io", "owner/other", "v1", nil)
	mustCreateWatch(t, db, "quay.io", "team/app", "latest", nil)

	registries, err := db.RegistryHosts(ctx)
	if err != nil {
		t.Fatalf("RegistryHosts: %v", err)
	}
	byHost := map[string]*apitypes.Registry{}
	for _, r := range registries {
		byHost[r.Host] = r
	}
	if len(byHost) != 2 {
		t.Fatalf("RegistryHosts returned %d hosts (%v), want 2", len(byHost), byHost)
	}

	ghcr := byHost["ghcr.io"]
	if ghcr == nil {
		t.Fatal("ghcr.io missing from RegistryHosts")
	}
	if !ghcr.HasCredentials {
		t.Error("ghcr.io HasCredentials = false, want true")
	}
	if ghcr.Watches != 2 {
		t.Errorf("ghcr.io Watches = %d, want 2", ghcr.Watches)
	}
	if ghcr.Username != "u" {
		t.Errorf("ghcr.io Username = %q, want u", ghcr.Username)
	}

	quay := byHost["quay.io"]
	if quay == nil {
		t.Fatal("quay.io missing from RegistryHosts")
	}
	if quay.HasCredentials {
		t.Error("quay.io HasCredentials = true, want false")
	}
	if quay.Watches != 1 {
		t.Errorf("quay.io Watches = %d, want 1", quay.Watches)
	}

	count, err := db.CountRegistries(ctx)
	if err != nil {
		t.Fatalf("CountRegistries: %v", err)
	}
	if count != 2 {
		t.Errorf("CountRegistries = %d, want 2", count)
	}
}

func TestStats(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)
	mustCreateWatch(t, db, "ghcr.io", "owner/app", "v2", nil)
	disabled := false
	if _, err := db.UpdateWatch(ctx, watch.ID, UpdateWatchInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	mustCreateChannel(t, db, "ops")
	if err := db.MarkWatchFailure(ctx, watch.ID, "boom", nowUTC()); err != nil {
		t.Fatalf("MarkWatchFailure: %v", err)
	}
	if _, err := db.CreateEvent(ctx, CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventCheckFailed,
	}); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Watches.Total != 2 || stats.Watches.Enabled != 1 || stats.Watches.Disabled != 1 {
		t.Errorf("watch counts = %+v, want total 2 / enabled 1 / disabled 1", stats.Watches)
	}
	if stats.Watches.Failing != 1 {
		t.Errorf("failing watches = %d, want 1", stats.Watches.Failing)
	}
	if stats.Channels != 1 {
		t.Errorf("channels = %d, want 1", stats.Channels)
	}
	if stats.Events != 1 {
		t.Errorf("events = %d, want 1", stats.Events)
	}
	if stats.Registries != 1 {
		t.Errorf("registries = %d, want 1", stats.Registries)
	}
}
