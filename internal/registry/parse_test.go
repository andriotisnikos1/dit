package registry

import (
	"errors"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
)

func TestParseImageNormalisesDockerHub(t *testing.T) {
	cases := []struct {
		input      string
		registry   string
		repository string
		tag        string
		digest     string
	}{
		{"nginx", "index.docker.io", "library/nginx", "latest", ""},
		{"nginx:1.27", "index.docker.io", "library/nginx", "1.27", ""},
		{"library/nginx:1.27", "index.docker.io", "library/nginx", "1.27", ""},
		{"ghcr.io/owner/app:v1.2.3", "ghcr.io", "owner/app", "v1.2.3", ""},
		{"quay.io/org/team/image", "quay.io", "org/team/image", "latest", ""},
		{"registry.example.com:5000/team/app:dev", "registry.example.com:5000", "team/app", "dev", ""},
		{"  nginx:1.27  ", "index.docker.io", "library/nginx", "1.27", ""},
	}

	for _, tc := range cases {
		got, err := ParseImage(tc.input)
		if err != nil {
			t.Errorf("ParseImage(%q): %v", tc.input, err)
			continue
		}
		if got.Registry != tc.registry {
			t.Errorf("ParseImage(%q).Registry = %q, want %q", tc.input, got.Registry, tc.registry)
		}
		if got.Repository != tc.repository {
			t.Errorf("ParseImage(%q).Repository = %q, want %q", tc.input, got.Repository, tc.repository)
		}
		if got.Tag != tc.tag {
			t.Errorf("ParseImage(%q).Tag = %q, want %q", tc.input, got.Tag, tc.tag)
		}
		if got.Digest != tc.digest {
			t.Errorf("ParseImage(%q).Digest = %q, want %q", tc.input, got.Digest, tc.digest)
		}
	}
}

func TestParseImageDigestReference(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got, err := ParseImage("ghcr.io/owner/app@" + digest)
	if err != nil {
		t.Fatalf("ParseImage: %v", err)
	}
	if got.Digest != digest {
		t.Errorf("Digest = %q, want %q", got.Digest, digest)
	}
	if got.Tag != "" {
		t.Errorf("Tag = %q, want empty for a digest reference", got.Tag)
	}
}

func TestParseImageRejectsBadInput(t *testing.T) {
	for _, input := range []string{"", "   ", "nginx:", "UPPER/Repo:tag is bad", "ghcr.io/a b:tag"} {
		if _, err := ParseImage(input); err == nil {
			t.Errorf("ParseImage(%q): expected an error", input)
		}
	}
}

func TestParseTagRef(t *testing.T) {
	ref, err := ParseTagRef("index.docker.io/library/nginx", "1.27")
	if err != nil {
		t.Fatalf("ParseTagRef: %v", err)
	}
	if ref.TagStr() != "1.27" {
		t.Errorf("TagStr = %q, want 1.27", ref.TagStr())
	}
	if ref.Context().RegistryStr() != "index.docker.io" {
		t.Errorf("Registry = %q, want index.docker.io", ref.Context().RegistryStr())
	}

	// An empty tag defaults to latest.
	ref, err = ParseTagRef("index.docker.io/library/nginx", "")
	if err != nil {
		t.Fatalf("ParseTagRef(\"\"): %v", err)
	}
	if ref.TagStr() != "latest" {
		t.Errorf("TagStr = %q, want latest", ref.TagStr())
	}

	if _, err := ParseTagRef("index.docker.io/library/nginx", "bad tag"); err == nil {
		t.Error("ParseTagRef with an invalid tag: expected an error")
	}
}

func TestParseRepositoryRef(t *testing.T) {
	repo, err := ParseRepositoryRef("ghcr.io", "owner/app")
	if err != nil {
		t.Fatalf("ParseRepositoryRef: %v", err)
	}
	if repo.RegistryStr() != "ghcr.io" || repo.RepositoryStr() != "owner/app" {
		t.Errorf("got %s/%s, want ghcr.io/owner/app", repo.RegistryStr(), repo.RepositoryStr())
	}

	// A repository that already carries the registry must not be doubled up.
	repo, err = ParseRepositoryRef("ghcr.io", "ghcr.io/owner/app")
	if err != nil {
		t.Fatalf("ParseRepositoryRef: %v", err)
	}
	if repo.RegistryStr() != "ghcr.io" || repo.RepositoryStr() != "owner/app" {
		t.Errorf("got %s/%s, want ghcr.io/owner/app", repo.RegistryStr(), repo.RepositoryStr())
	}
}

func TestValidateTag(t *testing.T) {
	valid := []string{"latest", "1.27", "v1.2.3-rc.1", "2026_09_11", "A"}
	for _, tag := range valid {
		if err := ValidateTag(tag); err != nil {
			t.Errorf("ValidateTag(%q): unexpected error %v", tag, err)
		}
	}
	invalid := []string{"", "bad tag", "a/b", "a:b", "a@b", "emoji😀", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, tag := range invalid {
		if err := ValidateTag(tag); err == nil {
			t.Errorf("ValidateTag(%q): expected an error", tag)
		}
	}
}

func TestValidatePattern(t *testing.T) {
	valid := []string{"v1.*", "*", "1.27.*", "sha-*", "release-?-x"}
	for _, pattern := range valid {
		if err := ValidatePattern(pattern); err != nil {
			t.Errorf("ValidatePattern(%q): unexpected error %v", pattern, err)
		}
	}
	invalid := []string{"", "bad pattern", "a[b", "a/b", "a:b"}
	for _, pattern := range invalid {
		if err := ValidatePattern(pattern); err == nil {
			t.Errorf("ValidatePattern(%q): expected an error", pattern)
		}
	}
}

func TestMatchTags(t *testing.T) {
	tags := []string{"latest", "1.27.0", "1.27.1", "1.26.9", "V1.27.0", "sha-abc"}

	got := MatchTags("1.27.*", tags)
	want := []string{"1.27.0", "1.27.1"}
	if len(got) != len(want) {
		t.Fatalf("MatchTags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("MatchTags[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Matching is case-sensitive: "V1.27.0" must not match "1.27.*".
	for _, tag := range MatchTags("1.27.*", tags) {
		if tag == "V1.27.0" {
			t.Error("MatchTags matched case-insensitively")
		}
	}

	if got := MatchTags("nomatch-*", tags); len(got) != 0 {
		t.Errorf("MatchTags with no matches = %v, want empty", got)
	}
	if got := MatchTags("*", tags); len(got) != len(tags) {
		t.Errorf("MatchTags(\"*\") returned %d tags, want %d", len(got), len(tags))
	}
	// A malformed pattern matches nothing rather than panicking.
	if got := MatchTags("a[b", tags); len(got) != 0 {
		t.Errorf("MatchTags with a malformed pattern = %v, want empty", got)
	}
}

func TestClassifyErrorKinds(t *testing.T) {
	if got := classify(errors.New("connection refused")); got != KindTransient {
		t.Errorf("classify(connection refused) = %q, want %q", got, KindTransient)
	}
	if got := classify(errors.New("no such host")); got != KindTransient {
		t.Errorf("classify(no such host) = %q, want %q", got, KindTransient)
	}
	if got := classify(errors.New("something else entirely")); got != KindUnknown {
		t.Errorf("classify(other) = %q, want %q", got, KindUnknown)
	}
	if got := classify(nil); got != "" {
		t.Errorf("classify(nil) = %q, want empty", got)
	}
}

func TestFakeClientImplementsInterface(t *testing.T) {
	fake := NewFake()
	const fakeDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fake.SetTags("ghcr.io", "owner/app", "v1", "v2")
	fake.SetDigest("ghcr.io", "owner/app", "v1", fakeDigest)
	fake.SetProbe("ghcr.io", "owner/app", ProbeResult{Exists: true})

	repo, err := name.NewRepository("ghcr.io/owner/app")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	ctx := t.Context()

	probe, err := fake.Probe(ctx, repo)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !probe.Exists {
		t.Error("Probe.Exists = false, want true")
	}

	tags, err := fake.ListTags(ctx, repo, Anonymous())
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 2 {
		t.Errorf("ListTags returned %d tags, want 2", len(tags))
	}

	ref, err := ParseTagRef("ghcr.io/owner/app", "v1")
	if err != nil {
		t.Fatalf("ParseTagRef: %v", err)
	}
	digest, err := fake.ResolveDigest(ctx, ref, Anonymous())
	if err != nil {
		t.Fatalf("ResolveDigest: %v", err)
	}
	if digest.String() != fakeDigest {
		t.Errorf("digest = %q, want %s", digest.String(), fakeDigest)
	}

	// An unprogrammed tag resolves to a not-found error.
	missing, err := ParseTagRef("ghcr.io/owner/app", "v9")
	if err != nil {
		t.Fatalf("ParseTagRef: %v", err)
	}
	if _, err := fake.ResolveDigest(ctx, missing, Anonymous()); !IsNotFound(err) {
		t.Errorf("ResolveDigest for an unknown tag: got %v, want a not_found error", err)
	}
}
