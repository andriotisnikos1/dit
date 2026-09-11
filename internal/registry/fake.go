package registry

import (
	"context"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// StaticAuth is a fixed username/password (or token) authenticator.
func StaticAuth(username, password string) authn.Authenticator {
	return authn.FromConfig(authn.AuthConfig{
		Username: username,
		Password: password,
	})
}

// Anonymous is the no-credentials authenticator.
func Anonymous() authn.Authenticator { return authn.Anonymous }

// Fake is an in-memory Client for tests. It records every call so tests can
// assert on the request pattern, and it can be programmed to fail.
type Fake struct {
	// ProbeResults maps "registry/repository" to the probe outcome.
	ProbeResults map[string]ProbeResult
	// ProbeErr maps "registry/repository" to a probe failure.
	ProbeErr map[string]error

	// AuthenticatedProbeResults maps "registry/repository" to the outcome of
	// an authenticated probe, modelling a registry that is private
	// anonymously but readable with credentials.
	AuthenticatedProbeResults map[string]ProbeResult
	// AuthenticatedProbeErr maps "registry/repository" to an authenticated
	// probe failure.
	AuthenticatedProbeErr map[string]error

	// Digests maps "registry/repository:tag" to a digest string.
	Digests map[string]string
	// DigestErr maps "registry/repository:tag" to a failure.
	DigestErr map[string]error

	// Tags maps "registry/repository" to the tag list.
	Tags map[string][]string
	// TagsErr maps "registry/repository" to a failure.
	TagsErr map[string]error

	// Calls records the method names invoked, in order.
	Calls []string
}

// NewFake builds an empty Fake with initialised maps.
func NewFake() *Fake {
	return &Fake{
		ProbeResults:              map[string]ProbeResult{},
		ProbeErr:                  map[string]error{},
		AuthenticatedProbeResults: map[string]ProbeResult{},
		AuthenticatedProbeErr:     map[string]error{},
		Digests:                   map[string]string{},
		DigestErr:                 map[string]error{},
		Tags:                      map[string][]string{},
		TagsErr:                   map[string]error{},
	}
}

var _ Client = (*Fake)(nil)

// SetTags programs the tag list for a repository.
func (f *Fake) SetTags(registry, repository string, tags ...string) {
	if f.Tags == nil {
		f.Tags = map[string][]string{}
	}
	f.Tags[registry+"/"+repository] = tags
}

// SetDigest programs the digest for a tag.
func (f *Fake) SetDigest(registry, repository, tag, digest string) {
	if f.Digests == nil {
		f.Digests = map[string]string{}
	}
	f.Digests[registry+"/"+repository+":"+tag] = digest
}

// SetProbe programs the probe outcome for a repository.
func (f *Fake) SetProbe(registry, repository string, result ProbeResult) {
	if f.ProbeResults == nil {
		f.ProbeResults = map[string]ProbeResult{}
	}
	f.ProbeResults[registry+"/"+repository] = result
}

// SetProbeErr programs a probe failure for a repository.
func (f *Fake) SetProbeErr(registry, repository string, err error) {
	if f.ProbeErr == nil {
		f.ProbeErr = map[string]error{}
	}
	if err == nil {
		delete(f.ProbeErr, registry+"/"+repository)
		return
	}
	f.ProbeErr[registry+"/"+repository] = err
}

// SetAuthenticatedProbe programs the outcome of an authenticated probe.
func (f *Fake) SetAuthenticatedProbe(registry, repository string, result ProbeResult) {
	if f.AuthenticatedProbeResults == nil {
		f.AuthenticatedProbeResults = map[string]ProbeResult{}
	}
	f.AuthenticatedProbeResults[registry+"/"+repository] = result
}

// SetAuthenticatedProbeErr programs an authenticated probe failure.
func (f *Fake) SetAuthenticatedProbeErr(registry, repository string, err error) {
	if f.AuthenticatedProbeErr == nil {
		f.AuthenticatedProbeErr = map[string]error{}
	}
	if err == nil {
		delete(f.AuthenticatedProbeErr, registry+"/"+repository)
		return
	}
	f.AuthenticatedProbeErr[registry+"/"+repository] = err
}

// SetTagsErr programs a tag listing failure for a repository.
func (f *Fake) SetTagsErr(registry, repository string, err error) {
	if f.TagsErr == nil {
		f.TagsErr = map[string]error{}
	}
	f.TagsErr[registry+"/"+repository] = err
}

// SetDigestErr programs a digest resolution failure for a tag.
func (f *Fake) SetDigestErr(registry, repository, tag string, err error) {
	if f.DigestErr == nil {
		f.DigestErr = map[string]error{}
	}
	f.DigestErr[registry+"/"+repository+":"+tag] = err
}

// Probe implements Client.
func (f *Fake) Probe(_ context.Context, repo name.Repository) (ProbeResult, error) {
	return f.probe(repo, false)
}

// ProbeWithAuth implements Client.
func (f *Fake) ProbeWithAuth(_ context.Context, repo name.Repository, _ authn.Authenticator) (ProbeResult, error) {
	return f.probe(repo, true)
}

func (f *Fake) probe(repo name.Repository, authenticated bool) (ProbeResult, error) {
	f.Calls = append(f.Calls, "probe")
	key := repo.RegistryStr() + "/" + repo.RepositoryStr()
	if authenticated {
		// AuthenticatedProbeResults lets a test model a registry that is
		// private anonymously but readable with credentials.
		if err, ok := f.AuthenticatedProbeErr[key]; ok {
			return ProbeResult{}, err
		}
		if res, ok := f.AuthenticatedProbeResults[key]; ok {
			return res, nil
		}
	}
	if err, ok := f.ProbeErr[key]; ok {
		return ProbeResult{}, err
	}
	if res, ok := f.ProbeResults[key]; ok {
		return res, nil
	}
	return ProbeResult{Exists: true}, nil
}

// ResolveDigest implements Client.
func (f *Fake) ResolveDigest(_ context.Context, ref name.Reference, _ authn.Authenticator) (v1.Hash, error) {
	f.Calls = append(f.Calls, "resolve")
	key := ref.Context().RegistryStr() + "/" + ref.Context().RepositoryStr() + ":" + ref.Identifier()
	if err, ok := f.DigestErr[key]; ok {
		return v1.Hash{}, err
	}
	digest, ok := f.Digests[key]
	if !ok {
		return v1.Hash{}, &Error{
			Kind:       KindNotFound,
			Registry:   ref.Context().RegistryStr(),
			Repository: ref.Context().RepositoryStr(),
			Ref:        ref.Identifier(),
		}
	}
	return v1.NewHash(digest)
}

// ListTags implements Client.
func (f *Fake) ListTags(_ context.Context, repo name.Repository, _ authn.Authenticator) ([]string, error) {
	f.Calls = append(f.Calls, "list")
	key := repo.RegistryStr() + "/" + repo.RepositoryStr()
	if err, ok := f.TagsErr[key]; ok {
		return nil, err
	}
	return f.Tags[key], nil
}
