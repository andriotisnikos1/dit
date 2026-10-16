package server

import (
	"context"
	"net/http"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/flags"
	"github.com/andriotisnikos1/dit/internal/store"
)

// globalFlagValues loads the global settings as a flags.Resolve input.
func (s *Server) globalFlagValues(ctx context.Context) (map[string]any, error) {
	global, err := s.store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	asAny := make(map[string]any, len(global))
	for k, v := range global {
		asAny[k] = v
	}
	return asAny, nil
}

// effectiveValue projects a resolved Settings onto one flag definition.
func effectiveValue(def flags.Definition, settings flags.Settings) any {
	switch def.Name {
	case "history":
		return settings.History
	case "notify_threshold":
		return settings.NotifyThreshold
	case "semver":
		return settings.Semver
	case "semver_stable":
		return settings.SemverStable
	case "semver_notify_threshold":
		return settings.SemverNotifyThreshold
	default:
		return def.Default
	}
}

// buildFlagList assembles the flag listing: definitions with the global
// values, and — for a watch — the local override and the effective value.
func (s *Server) buildFlagList(ctx context.Context, watchID string) (apitypes.FlagList, error) {
	globalAny, err := s.globalFlagValues(ctx)
	if err != nil {
		return nil, err
	}

	var (
		watch    *store.WatchRecord
		localAny map[string]any
	)
	if watchID != "" {
		if watch, err = s.store.GetWatch(ctx, watchID); err != nil {
			return nil, err
		}
		localAny = watch.Flags
	}
	effective, err := flags.Resolve(localAny, globalAny)
	if err != nil {
		return nil, err
	}

	out := apitypes.FlagList{}
	for _, def := range flags.Definitions() {
		f := apitypes.Flag{
			Name:        def.Name,
			Type:        apitypes.FlagType(def.Type),
			Description: def.Description,
			Default:     def.Default,
			PatternOnly: def.PatternOnly,
		}
		if v, ok := globalAny[def.Name]; ok {
			if parsed, err := flags.ParseValue(def, v); err == nil {
				f.Global = &parsed
			}
		}
		if watch != nil {
			if v, ok := localAny[def.Name]; ok {
				if parsed, err := flags.ParseValue(def, v); err == nil {
					f.Local = &parsed
				}
			}
			if flags.ValidForKind(def, watch.Kind) {
				v := effectiveValue(def, effective)
				f.Effective = &v
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// storeFlagError maps a store error from a flag write onto an API error.
func storeFlagError(err error, what string) *apitypes.APIError {
	if store.IsNotFound(err) {
		apiErr := apitypes.Errorf(apitypes.CodeNotFound, "no such %s", what)
		return &apiErr
	}
	apiErr := apitypes.NewError(apitypes.CodeInternalError, "internal server error")
	return &apiErr
}

// handleListFlags serves GET /api/v1/flags, with an optional ?watch= scope.
func (s *Server) handleListFlags(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	watchID := ""
	if raw := r.URL.Query().Get("watch"); raw != "" {
		watchID = raw
		if _, err := s.store.GetWatch(ctx, watchID); err != nil {
			writeStoreError(w, r, err, "watch", s.log)
			return
		}
	}
	list, err := s.buildFlagList(ctx, watchID)
	if err != nil {
		writeStoreError(w, r, err, "flags", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, list)
}

// handleListWatchFlags serves GET /api/v1/watches/{id}/flags.
func (s *Server) handleListWatchFlags(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	list, err := s.buildFlagList(ctx, pathValue(r, "id"))
	if err != nil {
		writeStoreError(w, r, err, "flags", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, list)
}

// setFlag validates and stores one flag value, globally or on a watch, and
// returns the updated flag view.
func (s *Server) setFlag(ctx context.Context, watchID, name string, raw any) (*apitypes.Flag, *apitypes.APIError) {
	def, ok := flags.Lookup(name)
	if !ok {
		apiErr := apitypes.Errorf(apitypes.CodeNotFound, "unknown flag %q", name)
		return nil, &apiErr
	}
	if raw == nil {
		apiErr := apitypes.Errorf(apitypes.CodeValidationFailed,
			"value is required; DELETE the flag to unset it")
		return nil, &apiErr
	}
	value, err := flags.ParseValue(def, raw)
	if err != nil {
		apiErr := apitypes.Errorf(apitypes.CodeValidationFailed, "%v", err)
		return nil, &apiErr
	}

	if watchID != "" {
		watch, sErr := s.store.GetWatch(ctx, watchID)
		if sErr != nil {
			return nil, storeFlagError(sErr, "watch")
		}
		if !flags.ValidForKind(def, watch.Kind) {
			apiErr := apitypes.Errorf(apitypes.CodeValidationFailed,
				"flag %q applies to pattern watches only; %s is a %s watch",
				name, watch.ID, watch.Kind)
			return nil, &apiErr
		}
		if sErr := s.store.SetWatchFlag(ctx, watchID, name, value); sErr != nil {
			return nil, storeFlagError(sErr, "watch")
		}
	} else {
		encoded, encErr := flags.FormatValue(def, value)
		if encErr != nil {
			apiErr := apitypes.NewError(apitypes.CodeInternalError, "internal server error")
			return nil, &apiErr
		}
		if sErr := s.store.SetSetting(ctx, name, encoded); sErr != nil {
			return nil, storeFlagError(sErr, "setting")
		}
	}

	list, lErr := s.buildFlagList(ctx, watchID)
	if lErr != nil {
		apiErr := apitypes.NewError(apitypes.CodeInternalError, "internal server error")
		return nil, &apiErr
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	apiErr := apitypes.NewError(apitypes.CodeInternalError, "internal server error")
	return nil, &apiErr
}

// handleSetGlobalFlag serves PUT /api/v1/flags/{name}.
func (s *Server) handleSetGlobalFlag(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	var req apitypes.SetFlagRequest
	if !decodeJSON(w, r, &req, s.log) {
		return
	}
	flag, apiErr := s.setFlag(ctx, "", pathValue(r, "name"), req.Value)
	if apiErr != nil {
		writeError(w, r, *apiErr, s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, flag)
}

// handleSetWatchFlag serves PUT /api/v1/watches/{id}/flags/{name}.
func (s *Server) handleSetWatchFlag(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	var req apitypes.SetFlagRequest
	if !decodeJSON(w, r, &req, s.log) {
		return
	}
	flag, apiErr := s.setFlag(ctx, pathValue(r, "id"), pathValue(r, "name"), req.Value)
	if apiErr != nil {
		writeError(w, r, *apiErr, s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, flag)
}

// handleUnsetGlobalFlag serves DELETE /api/v1/flags/{name}; idempotent.
func (s *Server) handleUnsetGlobalFlag(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	name := pathValue(r, "name")
	if _, ok := flags.Lookup(name); !ok {
		writeError(w, r, apitypes.Errorf(apitypes.CodeNotFound, "unknown flag %q", name), s.log)
		return
	}
	// Absent is already the state the caller wants: stay idempotent.
	if err := s.store.UnsetSetting(ctx, name); err != nil && !store.IsNotFound(err) {
		writeStoreError(w, r, err, "flag", s.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUnsetWatchFlag serves DELETE /api/v1/watches/{id}/flags/{name};
// idempotent for an unset flag, 404 for an unknown watch.
func (s *Server) handleUnsetWatchFlag(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	id := pathValue(r, "id")
	if _, err := s.store.GetWatch(ctx, id); err != nil {
		writeStoreError(w, r, err, "watch", s.log)
		return
	}
	name := pathValue(r, "name")
	if _, ok := flags.Lookup(name); !ok {
		writeError(w, r, apitypes.Errorf(apitypes.CodeNotFound, "unknown flag %q", name), s.log)
		return
	}
	if err := s.store.UnsetWatchFlag(ctx, id, name); err != nil && !store.IsNotFound(err) {
		writeStoreError(w, r, err, "flag", s.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
