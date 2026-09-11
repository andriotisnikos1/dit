package server

import (
	"net/http"
	"strings"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/store"
)

// handleGetCredentials returns credential metadata only: the secret is never
// readable over the API.
func (s *Server) handleGetCredentials(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	host := normaliseHost(pathValue(r, "host"))
	if host == "" {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, "registry host is required"), s.log)
		return
	}

	// Read through the metadata-only listing so a secret cannot leak even if
	// this handler is later refactored.
	creds, err := s.store.ListCredentials(ctx)
	if err != nil {
		writeStoreError(w, r, err, "registry credentials", s.log)
		return
	}
	for _, c := range creds {
		if c.Registry != host {
			continue
		}
		writeJSON(w, r, http.StatusOK, credentialsInfo(c))
		return
	}
	writeError(w, r, apitypes.Errorf(apitypes.CodeNotFound,
		"no credentials stored for %s", host), s.log)
}

// handlePutCredentials stores credentials for a registry.
func (s *Server) handlePutCredentials(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	host := normaliseHost(pathValue(r, "host"))
	if host == "" {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, "registry host is required"), s.log)
		return
	}

	var req apitypes.CredentialsRequest
	if !decodeJSON(w, r, &req, s.log) {
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		writeError(w, r, apitypes.NewError(apitypes.CodeValidationFailed,
			"password must not be empty"), s.log)
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = apitypes.CredentialBasic
	}
	if kind != apitypes.CredentialBasic && kind != apitypes.CredentialToken {
		writeError(w, r, apitypes.Errorf(apitypes.CodeValidationFailed,
			"kind must be %q or %q (got %q)", apitypes.CredentialBasic, apitypes.CredentialToken, kind), s.log)
		return
	}

	creds, err := s.store.PutCredentials(ctx, host, req.Username, req.Password, kind)
	if err != nil {
		writeStoreError(w, r, err, "registry credentials", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, credentialsInfo(creds))
}

// handleDeleteCredentials removes stored credentials.
func (s *Server) handleDeleteCredentials(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	host := normaliseHost(pathValue(r, "host"))
	if host == "" {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, "registry host is required"), s.log)
		return
	}
	if err := s.store.DeleteCredentials(ctx, host); err != nil {
		writeStoreError(w, r, err, "registry credentials", s.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// credentialsInfo renders metadata only.
func credentialsInfo(c *store.CredentialRecord) apitypes.CredentialsInfo {
	return apitypes.CredentialsInfo{
		Host:       c.Registry,
		Username:   c.Username,
		Kind:       c.Kind,
		HasSecret:  c.Secret != "" || c.Registry != "",
		CreatedAt:  &c.CreatedAt,
		UpdatedAt:  &c.UpdatedAt,
		LastUsedAt: c.LastUsedAt,
		LastOKAt:   c.LastOKAt,
	}
}

// normaliseHost lowercases a registry host and strips any scheme, so that
// "https://ghcr.io/" and "GHCR.IO" address the same credentials.
func normaliseHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	host = strings.TrimSuffix(host, "/v1")
	return strings.ToLower(host)
}
