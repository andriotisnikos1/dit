package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/store"
)

// handleListChannels lists channels with secrets redacted.
func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	limit, offset, err := pagination(r)
	if err != nil {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, err.Error()), s.log)
		return
	}

	filter := store.ChannelFilter{
		Type:   apitypes.ChannelType(r.URL.Query().Get("type")),
		Limit:  limit,
		Offset: offset,
	}
	if filter.Type != "" && !filter.Type.Valid() {
		writeError(w, r, apitypes.Errorf(apitypes.CodeValidationFailed,
			"unknown channel type %q", filter.Type), s.log)
		return
	}
	if filter.Enabled, err = boolParam(r, "enabled"); err != nil {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, err.Error()), s.log)
		return
	}

	channels, total, err := s.store.ListChannels(ctx, filter)
	if err != nil {
		writeStoreError(w, r, err, "channels", s.log)
		return
	}

	items := make([]apitypes.Channel, 0, len(channels))
	for _, ch := range channels {
		items = append(items, toAPIChannel(ch))
	}
	writeJSON(w, r, http.StatusOK, apitypes.NewList(items, total, limit, offset))
}

// handleCreateChannel creates a notification channel.
func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	var req apitypes.CreateChannelRequest
	if !decodeJSON(w, r, &req, s.log) {
		return
	}
	if !req.Type.Valid() {
		writeError(w, r, apitypes.Errorf(apitypes.CodeValidationFailed,
			"channel type must be one of email, ntfy (got %q)", req.Type), s.log)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, r, apitypes.NewError(apitypes.CodeValidationFailed,
			"channel name is required"), s.log)
		return
	}
	if apiErr := validateChannelConfig(req.Type, req.Config); apiErr != nil {
		writeError(w, r, *apiErr, s.log)
		return
	}

	channel, err := s.store.CreateChannel(ctx, store.CreateChannelInput{
		Name:    req.Name,
		Type:    req.Type,
		Config:  req.Config,
		Enabled: true,
	})
	if err != nil {
		if store.IsDuplicate(err) {
			writeError(w, r, apitypes.Errorf(apitypes.CodeConflict,
				"a channel named %q already exists", req.Name), s.log)
			return
		}
		writeStoreError(w, r, err, "channel", s.log)
		return
	}
	writeJSON(w, r, http.StatusCreated, toAPIChannel(channel))
}

// lookupChannel resolves a channel by ID or, failing that, by name. The CLI
// documents name-based usage (`dit channel test ops`), so the API has to accept
// both; a name that matches no channel is a 404.
func (s *Server) lookupChannel(ctx context.Context, idOrName string) (*store.ChannelRecord, error) {
	ref := strings.TrimSpace(idOrName)
	if ref == "" {
		return nil, store.ErrNotFound
	}
	channel, err := s.store.GetChannel(ctx, ref)
	if err == nil {
		return channel, nil
	}
	if !store.IsNotFound(err) {
		return nil, err
	}
	return s.store.FindChannelByName(ctx, ref)
}

// handleGetChannel returns one channel with secrets redacted.
func (s *Server) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	channel, err := s.lookupChannel(ctx, pathValue(r, "id"))
	if err != nil {
		writeStoreError(w, r, err, "channel", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, toAPIChannel(channel))
}

// handleUpdateChannel updates a channel; it also handles the enable/disable and
// default toggles the CLI exposes.
func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	var req apitypes.UpdateChannelRequest
	if !decodeJSON(w, r, &req, s.log) {
		return
	}

	// Resolve by ID or name first, so the merged-config validation and the
	// write agree on which channel is being changed.
	current, err := s.lookupChannel(ctx, pathValue(r, "id"))
	if err != nil {
		writeStoreError(w, r, err, "channel", s.log)
		return
	}
	if req.Config != nil {
		merged := map[string]string{}
		for k, v := range current.Config {
			merged[k] = v
		}
		for k, v := range *req.Config {
			if v == "" && isSecretConfigKey(k) {
				continue // keep the stored secret
			}
			merged[k] = v
		}
		if apiErr := validateChannelConfig(current.Type, merged); apiErr != nil {
			writeError(w, r, *apiErr, s.log)
			return
		}
	}

	channel, err := s.store.UpdateChannel(ctx, current.ID, store.UpdateChannelInput{
		Name:      req.Name,
		Config:    req.Config,
		Enabled:   req.Enabled,
		IsDefault: req.IsDefault,
	})
	if err != nil {
		if store.IsDuplicate(err) {
			writeError(w, r, apitypes.NewError(apitypes.CodeConflict,
				"a channel with that name already exists"), s.log)
			return
		}
		writeStoreError(w, r, err, "channel", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, toAPIChannel(channel))
}

// handleDeleteChannel removes a channel.
func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	channel, err := s.lookupChannel(ctx, pathValue(r, "id"))
	if err != nil {
		writeStoreError(w, r, err, "channel", s.log)
		return
	}
	if err := s.store.DeleteChannel(ctx, channel.ID); err != nil {
		writeStoreError(w, r, err, "channel", s.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestChannel sends a synthetic notification through a channel.
func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	engine := s.engineOrErr(w, r)
	if engine == nil {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()

	channel, err := s.lookupChannel(ctx, pathValue(r, "id"))
	if err != nil {
		writeStoreError(w, r, err, "channel", s.log)
		return
	}

	result := apitypes.ChannelTestResult{ChannelID: channel.ID}
	if err := engine.TestChannel(ctx, channel); err != nil {
		result.OK = false
		result.Error = err.Error()
		// A test that fails to deliver is a successful API call reporting a
		// negative result, so this is a 200, not a 5xx.
		writeJSON(w, r, http.StatusOK, result)
		return
	}
	result.OK = true
	writeJSON(w, r, http.StatusOK, result)
}

// validateChannelConfig checks that a channel config can build a transport.
// Building is the only reliable validation: it catches a missing topic or a
// malformed port without duplicating the rules here.
func validateChannelConfig(channelType apitypes.ChannelType, cfg map[string]string) *apitypes.APIError {
	if _, err := notify.Build(string(channelType), cfg); err != nil {
		apiErr := apitypes.Errorf(apitypes.CodeValidationFailed, "%v", err)
		return &apiErr
	}
	return nil
}

// toAPIChannel converts a stored channel into its redacted wire form.
func toAPIChannel(c *store.ChannelRecord) apitypes.Channel {
	redacted := c.Redacted()
	return apitypes.Channel{
		ID:        redacted.ID,
		Name:      redacted.Name,
		Type:      redacted.Type,
		Config:    redacted.Config,
		HasSecret: c.HasSecret(),
		Enabled:   redacted.Enabled,
		IsDefault: redacted.IsDefault,
		CreatedAt: redacted.CreatedAt,
		UpdatedAt: redacted.UpdatedAt,
	}
}

func isSecretConfigKey(key string) bool {
	for _, k := range store.SecretConfigKeys {
		if k == key {
			return true
		}
	}
	return false
}

// formatPort is a small helper used by the CLI-side config builders.
func formatPort(port int) string {
	if port <= 0 {
		return ""
	}
	return strconv.Itoa(port)
}
