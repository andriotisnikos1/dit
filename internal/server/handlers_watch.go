package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/check"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
)

// handleCreateWatch creates one watch per requested tag or pattern.
//
// The privacy probe runs first, as the plan requires: if the repository cannot
// be read anonymously the request fails with 428 credentials_required, which
// is the CLI's cue to prompt for credentials and replay the request.
func (s *Server) handleCreateWatch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	var req apitypes.CreateWatchRequest
	if !decodeJSON(w, r, &req, s.log) {
		return
	}

	image, err := registry.ParseImage(req.Image)
	if err != nil {
		writeError(w, r, apitypes.Errorf(apitypes.CodeValidationFailed, "%v", err), s.log)
		return
	}

	tags := cleanList(req.Tags)
	patterns := cleanList(req.Patterns)
	switch {
	case len(tags) == 0 && len(patterns) == 0:
		// A digest reference names an immutable image, so there is nothing to
		// drift; reject it rather than silently watching "latest".
		if image.Digest != "" {
			writeError(w, r, apitypes.NewError(apitypes.CodeValidationFailed,
				"a watch tracks tags, not digests: pass --tag or --pattern for a digest-addressed image"), s.log)
			return
		}
		// Bare `dit watch add nginx` means "watch the tag it names", falling
		// back to latest.
		tag := image.Tag
		if tag == "" {
			tag = "latest"
		}
		tags = []string{tag}
	case len(tags) > 0 && len(patterns) > 0:
		writeError(w, r, apitypes.NewError(apitypes.CodeValidationFailed,
			"a watch is either tag-based or pattern-based: supply --tag or --pattern, not both"), s.log)
		return
	}

	for _, tag := range tags {
		if err := registry.ValidateTag(tag); err != nil {
			writeError(w, r, apitypes.Errorf(apitypes.CodeValidationFailed, "%v", err), s.log)
			return
		}
	}
	for _, pattern := range patterns {
		if err := registry.ValidatePattern(pattern); err != nil {
			writeError(w, r, apitypes.Errorf(apitypes.CodeValidationFailed, "%v", err), s.log)
			return
		}
	}

	// Resolve channel names to IDs up front so a typo fails before anything
	// is written.
	channelIDs, apiErr := s.resolveChannels(ctx, req.Channels)
	if apiErr != nil {
		writeError(w, r, *apiErr, s.log)
		return
	}

	notifyOnFailure := true
	if req.NotifyOnFailure != nil {
		notifyOnFailure = *req.NotifyOnFailure
	}
	enabled := !req.Disabled

	// Probe once: every watch in this request targets the same repository.
	if apiErr := s.probeRepository(ctx, image.Registry, image.Repository); apiErr != nil {
		writeError(w, r, *apiErr, s.log)
		return
	}

	response := apitypes.CreateWatchResponse{
		Image:    image.String(),
		Registry: image.Registry,
		Watches:  []apitypes.Watch{},
		Created:  []string{},
		Existing: []string{},
	}

	type spec struct {
		kind apitypes.WatchKind
		ref  string
	}
	specs := make([]spec, 0, len(tags)+len(patterns))
	for _, tag := range tags {
		specs = append(specs, spec{kind: apitypes.WatchKindTag, ref: tag})
	}
	for _, pattern := range patterns {
		specs = append(specs, spec{kind: apitypes.WatchKindPattern, ref: pattern})
	}

	for _, sp := range specs {
		existing, err := s.store.FindWatch(ctx, image.Registry, image.Repository, sp.kind, sp.ref)
		switch {
		case err == nil:
			response.Existing = append(response.Existing, sp.ref)
			response.Watches = append(response.Watches, s.toAPIWatch(existing))
			continue
		case !store.IsNotFound(err):
			writeStoreError(w, r, err, "watch", s.log)
			return
		}

		created, err := s.store.CreateWatch(ctx, store.CreateWatchInput{
			Image:           image.String(),
			Registry:        image.Registry,
			Repository:      image.Repository,
			Kind:            sp.kind,
			Ref:             sp.ref,
			Enabled:         enabled,
			NotifyOnFailure: notifyOnFailure,
			Channels:        channelIDs,
		})
		if err != nil {
			if store.IsDuplicate(err) {
				// Lost a race with a concurrent create; treat it as existing.
				response.Existing = append(response.Existing, sp.ref)
				continue
			}
			writeStoreError(w, r, err, "watch", s.log)
			return
		}
		response.Created = append(response.Created, created.ID)
		response.Watches = append(response.Watches, s.toAPIWatch(created))
	}

	status := http.StatusCreated
	if len(response.Created) == 0 {
		status = http.StatusOK
	}
	writeJSON(w, r, status, response)
}

// handleListWatches lists watches with the supported filters.
func (s *Server) handleListWatches(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	limit, offset, err := pagination(r)
	if err != nil {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, err.Error()), s.log)
		return
	}

	filter := store.WatchFilter{
		Registry: r.URL.Query().Get("registry"),
		Query:    r.URL.Query().Get("q"),
		Limit:    limit,
		Offset:   offset,
	}
	if filter.Enabled, err = boolParam(r, "enabled"); err != nil {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, err.Error()), s.log)
		return
	}

	watches, total, err := s.store.ListWatches(ctx, filter)
	if err != nil {
		writeStoreError(w, r, err, "watches", s.log)
		return
	}

	items := make([]apitypes.Watch, 0, len(watches))
	for _, watch := range watches {
		items = append(items, s.toAPIWatch(watch))
	}
	writeJSON(w, r, http.StatusOK, apitypes.NewList(items, total, limit, offset))
}

// handleGetWatch returns one watch.
func (s *Server) handleGetWatch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	watch, err := s.store.GetWatch(ctx, pathValue(r, "id"))
	if err != nil {
		writeStoreError(w, r, err, "watch", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, s.toAPIWatch(watch))
}

// handleUpdateWatch toggles a watch and manages its channel subscriptions.
func (s *Server) handleUpdateWatch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	id := pathValue(r, "id")
	var req apitypes.UpdateWatchRequest
	if !decodeJSON(w, r, &req, s.log) {
		return
	}

	in := store.UpdateWatchInput{
		Enabled:         req.Enabled,
		NotifyOnFailure: req.NotifyOnFailure,
	}
	if req.Channels != nil {
		channelIDs, apiErr := s.resolveChannels(ctx, *req.Channels)
		if apiErr != nil {
			writeError(w, r, *apiErr, s.log)
			return
		}
		in.Channels = &channelIDs
	}

	watch, err := s.store.UpdateWatch(ctx, id, in)
	if err != nil {
		writeStoreError(w, r, err, "watch", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, s.toAPIWatch(watch))
}

// handleDeleteWatch removes a watch and everything that hangs off it.
func (s *Server) handleDeleteWatch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	if err := s.store.DeleteWatch(ctx, pathValue(r, "id")); err != nil {
		writeStoreError(w, r, err, "watch", s.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCheckWatch forces an immediate check and returns the diff result.
func (s *Server) handleCheckWatch(w http.ResponseWriter, r *http.Request) {
	engine := s.engineOrErr(w, r)
	if engine == nil {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()

	id := pathValue(r, "id")
	// Confirm the watch exists so an unknown ID is a 404 rather than an
	// internal error from the engine.
	if _, err := s.store.GetWatch(ctx, id); err != nil {
		writeStoreError(w, r, err, "watch", s.log)
		return
	}

	result, err := engine.CheckWatch(ctx, id, check.TriggerManual)
	if err != nil {
		s.log.Error("manual check failed", "watch", id, "error", err)
		writeError(w, r, apitypes.Errorf(apitypes.CodeInternalError, "check failed: %v", err), s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

// probeRepository runs the privacy probe and maps a 401/403 onto the
// 428 credentials_required trigger.
//
// When credentials are already stored for the registry they are used, so that
// the CLI's retry after the 428 prompt can actually succeed. Without this the
// retry would repeat the anonymous probe, get the same 401, and loop forever.
func (s *Server) probeRepository(ctx context.Context, registryHost, repository string) *apitypes.APIError {
	repo, err := registry.ParseRepositoryRef(registryHost, repository)
	if err != nil {
		apiErr := apitypes.Errorf(apitypes.CodeValidationFailed, "%v", err)
		return &apiErr
	}

	// Prefer stored credentials when they exist.
	var (
		result    registry.ProbeResult
		probeErr  error
		haveCreds bool
	)
	creds, credsErr := s.store.GetCredentials(ctx, registryHost)
	switch {
	case credsErr == nil:
		haveCreds = true
		result, probeErr = s.registry.ProbeWithAuth(ctx, repo, registry.StaticAuth(creds.Username, creds.Secret))
		if probeErr == nil {
			if err := s.store.TouchCredentials(ctx, registryHost, true); err != nil {
				s.log.Warn("record credential use", "registry", registryHost, "error", err)
			}
		} else {
			if err := s.store.TouchCredentials(ctx, registryHost, false); err != nil {
				s.log.Warn("record credential use", "registry", registryHost, "error", err)
			}
		}
	case store.IsNotFound(credsErr):
		result, probeErr = s.registry.Probe(ctx, repo)
	default:
		s.log.Error("load registry credentials", "registry", registryHost, "error", credsErr)
		apiErr := apitypes.NewError(apitypes.CodeInternalError, "internal server error")
		return &apiErr
	}

	if probeErr != nil {
		if registry.IsUnauthorized(probeErr) {
			// Credentials were supplied and still rejected: report that
			// explicitly rather than asking for them again, which would put
			// the CLI in a prompt loop.
			if haveCreds {
				apiErr := apitypes.Errorf(apitypes.CodeUnauthorized,
					"the credentials stored for %s were rejected by the registry; "+
						"check the username and token, or replace them with `dit creds set %s`",
					registryHost, registryHost)
				return &apiErr
			}
			apiErr := apitypes.NewError(apitypes.CodeCredentialsRequired,
				"the registry requires credentials for "+repo.Name()+"; note that registries such as Docker Hub "+
					"answer 401 for both private and nonexistent repositories, so this may also mean the "+
					"repository does not exist").
				WithRegistry(registryHost)
			return &apiErr
		}
		if registry.IsNotFound(probeErr) {
			apiErr := apitypes.Errorf(apitypes.CodeValidationFailed,
				"repository %s was not found on %s", repo.RepositoryStr(), registryHost)
			return &apiErr
		}
		if registry.IsTransient(probeErr) {
			apiErr := apitypes.Errorf(apitypes.CodeUpstreamError,
				"registry %s could not be reached: %v", registryHost, probeErr)
			return &apiErr
		}
		apiErr := apitypes.Errorf(apitypes.CodeUpstreamError,
			"registry %s returned an unexpected error: %v", registryHost, probeErr)
		return &apiErr
	}
	if result.Private && !haveCreds {
		apiErr := apitypes.NewError(apitypes.CodeCredentialsRequired,
			"the registry requires credentials for "+repo.Name()).
			WithRegistry(registryHost)
		return &apiErr
	}
	return nil
}

// resolveChannels turns channel names or IDs into channel IDs.
func (s *Server) resolveChannels(ctx context.Context, names []string) ([]string, *apitypes.APIError) {
	out := make([]string, 0, len(names))
	for _, name := range cleanList(names) {
		channel, err := s.store.GetChannel(ctx, name)
		if err != nil {
			if !store.IsNotFound(err) {
				apiErr := apitypes.NewError(apitypes.CodeInternalError, "internal server error")
				return nil, &apiErr
			}
			channel, err = s.store.FindChannelByName(ctx, name)
			if err != nil {
				if store.IsNotFound(err) {
					apiErr := apitypes.Errorf(apitypes.CodeValidationFailed,
						"no channel named %q", name)
					return nil, &apiErr
				}
				apiErr := apitypes.NewError(apitypes.CodeInternalError, "internal server error")
				return nil, &apiErr
			}
		}
		out = append(out, channel.ID)
	}
	return out, nil
}

// toAPIWatch converts a stored watch into its wire form.
func (s *Server) toAPIWatch(w *store.WatchRecord) apitypes.Watch {
	channels := w.ChannelIDs
	if channels == nil {
		channels = []string{}
	}
	return apitypes.Watch{
		ID:                  w.ID,
		Image:               w.Image,
		Registry:            w.Registry,
		Repository:          w.Repository,
		Kind:                w.Kind,
		Ref:                 w.Ref,
		Enabled:             w.Enabled,
		NotifyOnFailure:     w.NotifyOnFailure,
		Channels:            channels,
		LastDigest:          w.LastDigest,
		LastCheckedAt:       w.LastCheckedAt,
		LastOKAt:            w.LastOKAt,
		LastError:           w.LastError,
		ConsecutiveFailures: w.ConsecutiveFailures,
		NextAttemptAt:       w.NextAttemptAt,
		CreatedAt:           w.CreatedAt,
		UpdatedAt:           w.UpdatedAt,
	}
}

// cleanList trims and drops empty entries, preserving order.
func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
