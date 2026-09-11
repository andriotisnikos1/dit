package registry

import (
	"fmt"
	"path"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// ImageRef is a parsed, normalised image reference.
type ImageRef struct {
	// Image is the input as the operator wrote it (registry/repo[:tag]).
	Image string
	// Registry is the registry host, e.g. index.docker.io or ghcr.io.
	Registry string
	// Repository is the repository path, e.g. library/nginx.
	Repository string
	// Tag is the tag that was supplied, if any.
	Tag string
	// Digest is the digest that was supplied, if any.
	Digest string
}

// Repository returns the name.Repository for API calls.
func (r ImageRef) RepositoryRef() (name.Repository, error) {
	return name.NewRepository(r.Registry+"/"+r.Repository, name.WithDefaultRegistry(name.DefaultRegistry))
}

// String renders the canonical form, registry included.
func (r ImageRef) String() string {
	ref := r.Registry + "/" + r.Repository
	switch {
	case r.Digest != "":
		return ref + "@" + r.Digest
	case r.Tag != "":
		return ref + ":" + r.Tag
	default:
		return ref
	}
}

// ParseImage parses an operator-supplied image string and normalises it:
// "nginx:1.27" becomes registry index.docker.io, repository library/nginx.
//
// A reference with no tag gets the OCI default tag "latest" in Tag, matching
// what a registry would resolve. Digest references carry Digest and no Tag.
func ParseImage(image string) (ImageRef, error) {
	raw := strings.TrimSpace(image)
	if raw == "" {
		return ImageRef{}, fmt.Errorf("image reference is empty")
	}
	// Reject a trailing colon with nothing after it ("nginx:") early, because
	// name.ParseReference would happily treat it as a repository.
	if strings.HasSuffix(raw, ":") {
		return ImageRef{}, fmt.Errorf("image reference %q has an empty tag", raw)
	}

	ref, err := name.ParseReference(raw, name.WithDefaultRegistry(name.DefaultRegistry))
	if err != nil {
		return ImageRef{}, fmt.Errorf("parse image %q: %w", raw, err)
	}

	out := ImageRef{
		Image:      raw,
		Registry:   ref.Context().RegistryStr(),
		Repository: ref.Context().RepositoryStr(),
	}
	switch r := ref.(type) {
	case name.Tag:
		out.Tag = r.TagStr()
	case name.Digest:
		out.Digest = r.DigestStr()
	}
	return out, nil
}

// ParseTagRef builds a tag reference for a repository.
func ParseTagRef(repository, tag string) (name.Tag, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		tag = "latest"
	}
	if err := ValidateTag(tag); err != nil {
		return name.Tag{}, err
	}
	return name.NewTag(repository+":"+tag, name.WithDefaultRegistry(name.DefaultRegistry))
}

// ParseRepositoryRef builds a repository reference from "registry/repo".
func ParseRepositoryRef(registry, repository string) (name.Repository, error) {
	full := strings.TrimSpace(repository)
	if reg := strings.TrimSpace(registry); reg != "" && !strings.HasPrefix(full, reg+"/") {
		full = reg + "/" + full
	}
	repo, err := name.NewRepository(full, name.WithDefaultRegistry(name.DefaultRegistry))
	if err != nil {
		return name.Repository{}, fmt.Errorf("parse repository %q: %w", full, err)
	}
	return repo, nil
}

// ValidateTag rejects tags that cannot exist in an OCI registry. Glob patterns
// are validated separately, because they are not tags.
func ValidateTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("tag is empty")
	}
	if len(tag) > 128 {
		return fmt.Errorf("tag %q is longer than 128 characters", tag)
	}
	if strings.ContainsAny(tag, " /:@") {
		return fmt.Errorf("tag %q contains an invalid character", tag)
	}
	for _, r := range tag {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-'
		if !ok {
			return fmt.Errorf("tag %q contains an invalid character %q", tag, r)
		}
	}
	return nil
}

// ValidatePattern checks a glob pattern used by a pattern watch. Globs are
// matched with path.Match, so only its metacharacters are allowed on top of
// the tag alphabet.
func ValidatePattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("pattern is empty")
	}
	if len(pattern) > 128 {
		return fmt.Errorf("pattern %q is longer than 128 characters", pattern)
	}
	if strings.ContainsAny(pattern, " /:@") {
		return fmt.Errorf("pattern %q contains an invalid character", pattern)
	}
	// path.Match reports a syntax error for a malformed pattern; use it as the
	// validator so the two can never disagree.
	if _, err := path.Match(pattern, "probe"); err != nil {
		return fmt.Errorf("pattern %q is not a valid glob: %w", pattern, err)
	}
	return nil
}

// MatchTags returns the tags matching a glob pattern, in input order.
// Matching is case-sensitive, per the plan.
func MatchTags(pattern string, tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		ok, err := path.Match(pattern, tag)
		if err != nil {
			continue // invalid pattern: nothing matches
		}
		if ok {
			out = append(out, tag)
		}
	}
	return out
}
