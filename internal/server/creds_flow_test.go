package server

import (
	"net/http"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/registry"
)

// TestCredentialsRequiredRetrySucceedsAfterStoring walks the full documented
// flow: an anonymous 401 produces 428, the CLI stores credentials, and the
// retry succeeds because the server now probes with those credentials.
//
// This is the regression test for the retry loop: probing anonymously on every
// attempt would return 428 forever, so the CLI could never get past the prompt.
func TestCredentialsRequiredRetrySucceedsAfterStoring(t *testing.T) {
	h := newHarness(t)

	// The registry is private anonymously...
	h.registry.SetProbeErr("ghcr.io", "owner/app", &registry.Error{
		Kind: registry.KindUnauthorized, StatusCode: http.StatusUnauthorized,
		Registry: "ghcr.io", Repository: "owner/app",
	})
	// ...but readable with credentials.
	h.registry.SetAuthenticatedProbe("ghcr.io", "owner/app", registry.ProbeResult{Exists: true})

	// 1. Anonymous attempt: 428 naming the registry.
	rec := h.authed(http.MethodPost, "/api/v1/watches", apitypes.CreateWatchRequest{
		Image: "ghcr.io/owner/app:v1",
	})
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("first attempt: status %d, want 428", rec.Code)
	}
	var envelope apitypes.ErrorEnvelope
	h.decode(rec, &envelope)
	if envelope.Error.Registry != "ghcr.io" {
		t.Fatalf("428 did not name the registry: %+v", envelope.Error)
	}

	// 2. The CLI stores credentials for the named registry.
	rec = h.authed(http.MethodPut, "/api/v1/registries/ghcr.io/credentials", apitypes.CredentialsRequest{
		Username: "andriotis", Password: "ghp_secret",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT credentials: status %d, body %s", rec.Code, rec.Body.String())
	}

	// 3. The retry succeeds.
	resp := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	if len(resp.Created) != 1 {
		t.Fatalf("the retry created %d watches, want 1", len(resp.Created))
	}

	// 4. The credential row records that it was used successfully.
	creds, err := h.store.GetCredentials(t.Context(), "ghcr.io")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.LastUsedAt == nil {
		t.Error("LastUsedAt = nil after the credentials were used")
	}
	if creds.LastOKAt == nil {
		t.Error("LastOKAt = nil after the credentials were accepted")
	}
}

// TestStoredCredentialsRejectedReportsUnauthorized guards the other half: when
// stored credentials do not work, the server must say so plainly instead of
// asking for credentials again, which would trap the CLI in a prompt loop.
func TestStoredCredentialsRejectedReportsUnauthorized(t *testing.T) {
	h := newHarness(t)

	h.registry.SetProbeErr("ghcr.io", "owner/app", &registry.Error{
		Kind: registry.KindUnauthorized, StatusCode: http.StatusUnauthorized,
		Registry: "ghcr.io", Repository: "owner/app",
	})
	// Even with credentials the registry rejects access.
	h.registry.SetAuthenticatedProbeErr("ghcr.io", "owner/app", &registry.Error{
		Kind: registry.KindUnauthorized, StatusCode: http.StatusUnauthorized,
		Registry: "ghcr.io", Repository: "owner/app",
	})

	rec := h.authed(http.MethodPut, "/api/v1/registries/ghcr.io/credentials", apitypes.CredentialsRequest{
		Username: "andriotis", Password: "wrong-token",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT credentials: status %d", rec.Code)
	}

	rec = h.authed(http.MethodPost, "/api/v1/watches", apitypes.CreateWatchRequest{
		Image: "ghcr.io/owner/app:v1",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (not 428: the CLI must not be asked again)", rec.Code)
	}
	var envelope apitypes.ErrorEnvelope
	h.decode(rec, &envelope)
	if envelope.Error.Code != apitypes.CodeUnauthorized {
		t.Errorf("error code = %q, want %q", envelope.Error.Code, apitypes.CodeUnauthorized)
	}
	if !contains(envelope.Error.Message, "dit creds set ghcr.io") {
		t.Errorf("message does not tell the operator how to fix it: %q", envelope.Error.Message)
	}

	// The failed use is recorded.
	creds, err := h.store.GetCredentials(t.Context(), "ghcr.io")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.LastUsedAt == nil {
		t.Error("LastUsedAt = nil after the credentials were used")
	}
	if creds.LastOKAt != nil {
		t.Error("LastOKAt is set even though the registry rejected the credentials")
	}
}

// TestPublicRegistryIgnoresStoredCredentials checks that a repository readable
// anonymously does not depend on the credential path.
func TestPublicRegistryIgnoresStoredCredentials(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("index.docker.io", "library/nginx", registry.ProbeResult{Exists: true})

	resp := h.createWatch("nginx:1.27", apitypes.CreateWatchRequest{})
	if len(resp.Created) != 1 {
		t.Fatalf("created %d watches, want 1", len(resp.Created))
	}
}
