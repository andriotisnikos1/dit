// Package registry is the OCI client: it parses image references, resolves
// digests, lists tags and probes repositories for privacy.
//
// Everything the rest of the program needs is expressed through the Client
// interface so that tests can substitute a fake, and so the check engine never
// touches go-containerregistry directly.
package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// ErrorKind classifies a registry failure so the check engine can decide
// between "needs credentials", "gone" and "try again later".
type ErrorKind string

const (
	// KindUnauthorized: 401/403 — credentials are wrong or missing.
	KindUnauthorized ErrorKind = "unauthorized"
	// KindNotFound: 404 — the repository or tag does not exist.
	KindNotFound ErrorKind = "not_found"
	// KindTransient: network errors, timeouts and 5xx/429 — retry later.
	KindTransient ErrorKind = "transient"
	// KindInvalid: the reference could not be parsed at all.
	KindInvalid ErrorKind = "invalid"
	// KindUnknown: anything else.
	KindUnknown ErrorKind = "unknown"
)

// Error is a classified registry failure.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Registry   string
	Repository string
	Ref        string
	Err        error
}

func (e *Error) Error() string {
	where := e.Registry + "/" + e.Repository
	if e.Ref != "" {
		where += ":" + e.Ref
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (%s)", where, e.Err, e.Kind)
	}
	return fmt.Sprintf("%s: %s", where, e.Kind)
}

// Unwrap exposes the underlying error for errors.Is/As.
func (e *Error) Unwrap() error { return e.Err }

// IsUnauthorized reports whether err is an authentication failure.
func IsUnauthorized(err error) bool { return kindOf(err) == KindUnauthorized }

// IsNotFound reports whether err is a missing repository or tag.
func IsNotFound(err error) bool { return kindOf(err) == KindNotFound }

// IsTransient reports whether err is worth retrying.
func IsTransient(err error) bool { return kindOf(err) == KindTransient }

func kindOf(err error) ErrorKind {
	var regErr *Error
	if errors.As(err, &regErr) {
		return regErr.Kind
	}
	return ""
}

// ProbeResult is the outcome of an anonymous manifest HEAD.
type ProbeResult struct {
	// Private is true when the registry answered 401/403 anonymously, which
	// means credentials are needed (or the repository does not exist — see
	// the package documentation of the credentials_required flow).
	Private bool
	// Exists is true when the manifest was readable anonymously.
	Exists bool
	// Digest is the resolved digest when the probe succeeded.
	Digest string
}

// Client is the registry access interface used by the check engine.
type Client interface {
	// Probe checks whether a repository can be read anonymously. A 401/403
	// means "credentials required", which is the prompt-and-retry trigger.
	Probe(ctx context.Context, repo name.Repository) (ProbeResult, error)
	// ProbeWithAuth repeats the probe with credentials. It is what decides
	// whether a stored credential actually works: without it, a retry after
	// supplying credentials would see the same anonymous 401 forever and the
	// documented 428 flow could never terminate.
	ProbeWithAuth(ctx context.Context, repo name.Repository, auth authn.Authenticator) (ProbeResult, error)
	// ResolveDigest resolves a tag or digest reference to a manifest digest.
	ResolveDigest(ctx context.Context, ref name.Reference, auth authn.Authenticator) (v1.Hash, error)
	// ListTags lists the tags in a repository.
	ListTags(ctx context.Context, repo name.Repository, auth authn.Authenticator) ([]string, error)
}

// Remote is the production Client, backed by go-containerregistry.
type Remote struct {
	userAgent string
	// headFallback resolves digests with GET when a registry rejects HEAD.
	headFallback bool
}

// DefaultUserAgent identifies dit to registries.
const DefaultUserAgent = "dit/1 (+https://github.com/andriotisnikos1/dit)"

// NewRemote builds the production registry client.
func NewRemote() *Remote {
	return &Remote{userAgent: DefaultUserAgent, headFallback: true}
}

// WithUserAgent overrides the User-Agent header.
func (r *Remote) WithUserAgent(ua string) *Remote {
	if strings.TrimSpace(ua) != "" {
		r.userAgent = ua
	}
	return r
}

func (r *Remote) options(ctx context.Context, auth authn.Authenticator) []remote.Option {
	if auth == nil {
		auth = authn.Anonymous
	}
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuth(auth),
		remote.WithUserAgent(r.userAgent),
	}
}

// Probe performs an anonymous manifest HEAD against repo:latest.
func (r *Remote) Probe(ctx context.Context, repo name.Repository) (ProbeResult, error) {
	return r.probe(ctx, repo, authn.Anonymous)
}

// ProbeWithAuth performs the same manifest HEAD with credentials, which is how
// a stored credential is verified.
func (r *Remote) ProbeWithAuth(ctx context.Context, repo name.Repository, auth authn.Authenticator) (ProbeResult, error) {
	if auth == nil {
		auth = authn.Anonymous
	}
	return r.probe(ctx, repo, auth)
}

func (r *Remote) probe(ctx context.Context, repo name.Repository, auth authn.Authenticator) (ProbeResult, error) {
	ref := repo.Tag("latest")
	desc, err := remote.Head(ref, r.options(ctx, auth)...)
	if err == nil {
		return ProbeResult{Exists: true, Digest: desc.Digest.String()}, nil
	}

	kind := classify(err)
	switch kind {
	case KindUnauthorized:
		// Docker Hub answers 401 for both private and nonexistent repos, so
		// the caller must treat this as "credentials required", not as
		// "private for sure".
		return ProbeResult{Private: true}, &Error{
			Kind:       KindUnauthorized,
			StatusCode: statusOf(err),
			Registry:   repo.RegistryStr(),
			Repository: repo.RepositoryStr(),
			Ref:        "latest",
			Err:        err,
		}
	case KindNotFound:
		return ProbeResult{}, &Error{
			Kind:       KindNotFound,
			StatusCode: http.StatusNotFound,
			Registry:   repo.RegistryStr(),
			Repository: repo.RepositoryStr(),
			Ref:        "latest",
			Err:        err,
		}
	default:
		return ProbeResult{}, &Error{
			Kind:       kind,
			StatusCode: statusOf(err),
			Registry:   repo.RegistryStr(),
			Repository: repo.RepositoryStr(),
			Ref:        "latest",
			Err:        err,
		}
	}
}

// ResolveDigest resolves ref to its manifest digest.
func (r *Remote) ResolveDigest(ctx context.Context, ref name.Reference, auth authn.Authenticator) (v1.Hash, error) {
	desc, err := remote.Head(ref, r.options(ctx, auth)...)
	if err == nil {
		return desc.Digest, nil
	}

	// Some registries reject HEAD on manifests; fall back to a GET, which
	// returns the same digest in the descriptor.
	if r.headFallback && isHeadUnsupported(err) {
		desc, getErr := remote.Get(ref, r.options(ctx, auth)...)
		if getErr == nil {
			return desc.Digest, nil
		}
		err = getErr
	}

	return v1.Hash{}, &Error{
		Kind:       classify(err),
		StatusCode: statusOf(err),
		Registry:   ref.Context().RegistryStr(),
		Repository: ref.Context().RepositoryStr(),
		Ref:        ref.Identifier(),
		Err:        err,
	}
}

// ListTags lists every tag in repo.
func (r *Remote) ListTags(ctx context.Context, repo name.Repository, auth authn.Authenticator) ([]string, error) {
	tags, err := remote.ListWithContext(ctx, repo, r.options(ctx, auth)...)
	if err != nil {
		return nil, &Error{
			Kind:       classify(err),
			StatusCode: statusOf(err),
			Registry:   repo.RegistryStr(),
			Repository: repo.RepositoryStr(),
			Err:        err,
		}
	}
	return tags, nil
}

// classify maps an error onto an ErrorKind. transport.Error.StatusCode drives
// the decision, as the plan requires.
func classify(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var terr *transport.Error
	if errors.As(err, &terr) {
		switch {
		case terr.StatusCode == http.StatusUnauthorized,
			terr.StatusCode == http.StatusForbidden:
			return KindUnauthorized
		case terr.StatusCode == http.StatusNotFound:
			return KindNotFound
		case terr.StatusCode == http.StatusTooManyRequests,
			terr.StatusCode >= 500:
			return KindTransient
		case terr.StatusCode == http.StatusMethodNotAllowed,
			terr.StatusCode == http.StatusNotImplemented:
			// HEAD is unsupported; the caller may retry with GET.
			return KindTransient
		default:
			return KindUnknown
		}
	}
	if isNetworkError(err) {
		return KindTransient
	}
	return KindUnknown
}

func statusOf(err error) int {
	var terr *transport.Error
	if errors.As(err, &terr) {
		return terr.StatusCode
	}
	return 0
}

// isHeadUnsupported reports whether the error means the registry refused HEAD.
func isHeadUnsupported(err error) bool {
	var terr *transport.Error
	if errors.As(err, &terr) {
		return terr.StatusCode == http.StatusMethodNotAllowed ||
			terr.StatusCode == http.StatusNotImplemented
	}
	return false
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"connection refused", "connection reset", "no such host",
		"timeout", "timed out", "temporary failure", "tls handshake",
		"i/o timeout", "network is unreachable", "eof",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
