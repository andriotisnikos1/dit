package registry

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// fakeRegistry starts an in-process OCI registry and returns a client pointed
// at it, plus the registry host name to use in references.
func fakeRegistry(t *testing.T) (*Remote, string) {
	t.Helper()

	handler := newTestRegistry(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	// name.Registry uses http for the localhost prefix, which is what an
	// httptest server speaks.
	host := "localhost:" + parsed.Port()
	return NewRemote(), host
}

// pushImage writes a small random image to the test registry.
func pushImage(t *testing.T, host, repository, tag string) v1.Hash {
	t.Helper()
	ref, err := name.NewTag(host + "/" + repository + ":" + tag)
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("remote.Write: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}
	return digest
}

func TestRemoteProbeFindsPublicRepository(t *testing.T) {
	client, host := fakeRegistry(t)
	pushImage(t, host, "library/nginx", "latest")

	repo, err := name.NewRepository(host + "/library/nginx")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}

	result, err := client.Probe(t.Context(), repo)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !result.Exists {
		t.Error("Probe.Exists = false, want true")
	}
	if result.Private {
		t.Error("Probe.Private = true, want false")
	}
	if !strings.HasPrefix(result.Digest, "sha256:") {
		t.Errorf("Probe.Digest = %q, want a sha256 digest", result.Digest)
	}
}

func TestRemoteProbeReportsMissingRepository(t *testing.T) {
	client, host := fakeRegistry(t)

	repo, err := name.NewRepository(host + "/nobody/here")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	_, err = client.Probe(t.Context(), repo)
	if err == nil {
		t.Fatal("Probe of a missing repository: expected an error")
	}
	if !IsNotFound(err) && !IsUnauthorized(err) {
		t.Errorf("Probe error = %v, want a not_found or unauthorized classification", err)
	}
}

func TestRemoteResolveDigest(t *testing.T) {
	client, host := fakeRegistry(t)
	want := pushImage(t, host, "library/nginx", "1.27")

	ref, err := ParseTagRef(host+"/library/nginx", "1.27")
	if err != nil {
		t.Fatalf("ParseTagRef: %v", err)
	}
	got, err := client.ResolveDigest(t.Context(), ref, Anonymous())
	if err != nil {
		t.Fatalf("ResolveDigest: %v", err)
	}
	if got != want {
		t.Errorf("ResolveDigest = %s, want %s", got, want)
	}

	// An unknown tag must classify as not found.
	missing, err := ParseTagRef(host+"/library/nginx", "does-not-exist")
	if err != nil {
		t.Fatalf("ParseTagRef: %v", err)
	}
	if _, err := client.ResolveDigest(t.Context(), missing, Anonymous()); !IsNotFound(err) {
		t.Errorf("ResolveDigest for an unknown tag: got %v, want not_found", err)
	}
}

func TestRemoteListTags(t *testing.T) {
	client, host := fakeRegistry(t)
	pushImage(t, host, "library/nginx", "1.27.0")
	pushImage(t, host, "library/nginx", "1.27.1")
	pushImage(t, host, "library/nginx", "1.26.9")

	repo, err := name.NewRepository(host + "/library/nginx")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	tags, err := client.ListTags(t.Context(), repo, Anonymous())
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}

	want := map[string]bool{"1.27.0": true, "1.27.1": true, "1.26.9": true}
	if len(tags) != len(want) {
		t.Fatalf("ListTags = %v, want %d tags", tags, len(want))
	}
	for _, tag := range tags {
		if !want[tag] {
			t.Errorf("ListTags returned unexpected tag %q", tag)
		}
	}
}

func TestRemoteClassifiesUnauthorized(t *testing.T) {
	// A registry that rejects every anonymous request, like a private one.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`))
	}))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	host := "localhost:" + parsed.Port()

	client := NewRemote()
	repo, err := name.NewRepository(host + "/private/app")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}

	result, err := client.Probe(t.Context(), repo)
	if err == nil {
		t.Fatal("Probe against a 401 registry: expected an error")
	}
	if !IsUnauthorized(err) {
		t.Errorf("Probe error = %v, want unauthorized", err)
	}
	if !result.Private {
		t.Error("Probe.Private = false, want true for a 401 response")
	}

	ref, err := ParseTagRef(host+"/private/app", "latest")
	if err != nil {
		t.Fatalf("ParseTagRef: %v", err)
	}
	if _, err := client.ResolveDigest(t.Context(), ref, Anonymous()); !IsUnauthorized(err) {
		t.Errorf("ResolveDigest against a 401 registry: got %v, want unauthorized", err)
	}
	if _, err := client.ListTags(t.Context(), repo, Anonymous()); !IsUnauthorized(err) {
		t.Errorf("ListTags against a 401 registry: got %v, want unauthorized", err)
	}
}

func TestRemoteClassifiesTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	host := "localhost:" + parsed.Port()

	client := NewRemote()
	repo, err := name.NewRepository(host + "/flaky/app")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	if _, err := client.ListTags(t.Context(), repo, Anonymous()); !IsTransient(err) {
		t.Errorf("ListTags against a 500 registry: got %v, want transient", err)
	}
}
