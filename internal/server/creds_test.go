package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/registry"
)

func TestRegistryCredentialsLifecycle(t *testing.T) {
	h := newHarness(t)

	// Nothing stored yet.
	rec := h.authed(http.MethodGet, "/api/v1/registries/ghcr.io/credentials", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET credentials before storing: status %d, want 404", rec.Code)
	}

	// Store.
	rec = h.authed(http.MethodPut, "/api/v1/registries/ghcr.io/credentials", apitypes.CredentialsRequest{
		Username: "andriotis",
		Password: "ghp_averysecretvalue",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT credentials: status %d, body %s", rec.Code, rec.Body.String())
	}
	var info apitypes.CredentialsInfo
	h.decode(rec, &info)
	if info.Host != "ghcr.io" || info.Username != "andriotis" {
		t.Errorf("info = %+v, want ghcr.io/andriotis", info)
	}
	if !info.HasSecret {
		t.Error("HasSecret = false after storing a secret")
	}

	// Read back: metadata only.
	rec = h.authed(http.MethodGet, "/api/v1/registries/ghcr.io/credentials", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET credentials: status %d", rec.Code)
	}
	h.decode(rec, &info)
	if info.Username != "andriotis" {
		t.Errorf("Username = %q, want andriotis", info.Username)
	}
	// The response must not carry the secret anywhere.
	if body := rec.Body.String(); contains(body, "ghp_averysecretvalue") {
		t.Error("the credentials response leaked the secret")
	}

	// Delete.
	rec = h.authed(http.MethodDelete, "/api/v1/registries/ghcr.io/credentials", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE credentials: status %d, want 204", rec.Code)
	}
	rec = h.authed(http.MethodGet, "/api/v1/registries/ghcr.io/credentials", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete: status %d, want 404", rec.Code)
	}
}

func TestCredentialsNeverAppearInRegistriesListing(t *testing.T) {
	h := newHarness(t)
	const secret = "ghp_supersecret"
	h.authed(http.MethodPut, "/api/v1/registries/ghcr.io/credentials", apitypes.CredentialsRequest{
		Username: "andriotis", Password: secret,
	})

	rec := h.authed(http.MethodGet, "/api/v1/registries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); contains(body, secret) {
		t.Error("the registries listing leaked the secret")
	}
	var list apitypes.List[apitypes.Registry]
	h.decode(rec, &list)
	if len(list.Items) != 1 || list.Items[0].Host != "ghcr.io" {
		t.Fatalf("items = %+v, want the ghcr.io entry", list.Items)
	}
	if !list.Items[0].HasCredentials {
		t.Error("HasCredentials = false, want true")
	}
}

func TestCredentialsHostNormalisation(t *testing.T) {
	h := newHarness(t)

	rec := h.authed(http.MethodPut, "/api/v1/registries/ghcr.io/credentials", apitypes.CredentialsRequest{
		Username: "u", Password: "p",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: status %d, body %s", rec.Code, rec.Body.String())
	}
	var info apitypes.CredentialsInfo
	h.decode(rec, &info)
	if info.Host != "ghcr.io" {
		t.Errorf("Host = %q, want ghcr.io", info.Host)
	}

	// Reading through a differently-cased host must find the same record.
	rec = h.authed(http.MethodGet, "/api/v1/registries/GHCR.IO/credentials", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("GET with an uppercase host: status %d, want 200", rec.Code)
	}
}

// TestNormaliseHost covers the scheme- and slash-stripping directly: those
// forms cannot survive a URL path (the router would collapse them), but they
// reach the handler through the CLI's own normalisation.
func TestNormaliseHost(t *testing.T) {
	cases := map[string]string{
		"ghcr.io":                   "ghcr.io",
		"GHCR.IO":                   "ghcr.io",
		"https://ghcr.io":           "ghcr.io",
		"http://ghcr.io":            "ghcr.io",
		"https://ghcr.io/":          "ghcr.io",
		"https://ghcr.io/v1":        "ghcr.io",
		"  ghcr.io  ":               "ghcr.io",
		"registry.example.com:5000": "registry.example.com:5000",
		"":                          "",
	}
	for input, want := range cases {
		if got := normaliseHost(input); got != want {
			t.Errorf("normaliseHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCredentialsValidation(t *testing.T) {
	h := newHarness(t)

	rec := h.authed(http.MethodPut, "/api/v1/registries/ghcr.io/credentials", apitypes.CredentialsRequest{
		Username: "u", Password: "",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("PUT with an empty password: status %d, want 422", rec.Code)
	}

	rec = h.authed(http.MethodPut, "/api/v1/registries/ghcr.io/credentials", apitypes.CredentialsRequest{
		Username: "u", Password: "p", Kind: "voodoo",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("PUT with an unknown kind: status %d, want 422", rec.Code)
	}

	rec = h.authed(http.MethodDelete, "/api/v1/registries/ghcr.io/credentials", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE with nothing stored: status %d, want 404", rec.Code)
	}
}

func TestRegistriesListingIncludesWatchHosts(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("quay.io", "team/app", registryProbeExists())

	h.createWatch("quay.io/team/app:latest", apitypes.CreateWatchRequest{})

	rec := h.authed(http.MethodGet, "/api/v1/registries", nil)
	var list apitypes.List[apitypes.Registry]
	h.decode(rec, &list)
	if len(list.Items) != 1 {
		t.Fatalf("items = %+v, want one registry", list.Items)
	}
	if list.Items[0].Host != "quay.io" {
		t.Errorf("host = %q, want quay.io", list.Items[0].Host)
	}
	if list.Items[0].HasCredentials {
		t.Error("HasCredentials = true for a registry with no stored credentials")
	}
	if list.Items[0].Watches != 1 {
		t.Errorf("watches = %d, want 1", list.Items[0].Watches)
	}
}

func TestStoredCredentialsAreUsedForChecks(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	created := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})

	// Store credentials and confirm the check still runs (the fake ignores
	// them, but the code path that loads and decrypts them is exercised).
	h.authed(http.MethodPut, "/api/v1/registries/ghcr.io/credentials", apitypes.CredentialsRequest{
		Username: "u", Password: "p",
	})
	h.registry.SetDigest("ghcr.io", "owner/app", "v1", testutilDigest(1))

	rec := h.authed(http.MethodPost, "/api/v1/watches/"+created.Created[0]+"/check", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("check with stored credentials: status %d, body %s", rec.Code, rec.Body.String())
	}

	// The credential row must be readable from the store with the secret.
	creds, err := h.store.GetCredentials(context.Background(), "ghcr.io")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.Secret != "p" {
		t.Errorf("stored secret = %q, want p", creds.Secret)
	}
}

func registryProbeExists() registry.ProbeResult {
	return registry.ProbeResult{Exists: true}
}
