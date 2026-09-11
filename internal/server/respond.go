package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/store"
)

// writeJSON writes a 200-style JSON response.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		// The status line is already out; nothing useful is left to do but
		// stop the connection from being reused with a truncated body.
		panic(http.ErrAbortHandler)
	}
}

// writeError writes the uniform error envelope.
func writeError(w http.ResponseWriter, r *http.Request, apiErr apitypes.APIError, log *slog.Logger) {
	if apiErr.Code == "" {
		apiErr.Code = apitypes.CodeInternalError
	}
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(apitypes.StatusCode(apiErr.Code))
	}
	// Server-side faults are worth a log line; client mistakes are not.
	if apitypes.StatusCode(apiErr.Code) >= 500 {
		log.Error("request failed", "method", r.Method, "path", r.URL.Path,
			"code", apiErr.Code, "message", apiErr.Message)
	}
	writeJSON(w, r, apitypes.StatusCode(apiErr.Code), apitypes.ErrorEnvelope{Error: apiErr})
}

// writeStoreError maps a store sentinel onto the right envelope.
func writeStoreError(w http.ResponseWriter, r *http.Request, err error, what string, log *slog.Logger) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, r, apitypes.Errorf(apitypes.CodeNotFound, "%s not found", what), log)
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, r, apitypes.Errorf(apitypes.CodeConflict, "%s already exists", what), log)
	default:
		log.Error("store error", "what", what, "error", err)
		writeError(w, r, apitypes.NewError(apitypes.CodeInternalError, "internal server error"), log)
	}
}

// decodeJSON decodes a request body, rejecting unknown fields so that typos in
// a client payload fail loudly instead of being silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, log *slog.Logger) bool {
	if r.Body == nil {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, "request body is required"), log)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, r, apitypes.Errorf(apitypes.CodeBadRequest, "malformed JSON body: %v", err), log)
		return false
	}
	return true
}

// ---------- query parameters ----------

// intParam reads an integer query parameter with a default.
func intParam(r *http.Request, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return n, nil
}

// boolParam reads a boolean query parameter. Absent yields nil.
func boolParam(r *http.Request, name string) (*bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be true or false", name)
	}
	return &v, nil
}

// pagination reads the standard limit/offset pair.
func pagination(r *http.Request) (limit, offset int, err error) {
	if limit, err = intParam(r, "limit", apitypes.DefaultLimit); err != nil {
		return 0, 0, err
	}
	if offset, err = intParam(r, "offset", 0); err != nil {
		return 0, 0, err
	}
	if offset < 0 {
		return 0, 0, fmt.Errorf("offset must not be negative")
	}
	return apitypes.ClampLimit(limit), offset, nil
}

// pathValue reads a path variable.
func pathValue(r *http.Request, name string) string {
	return strings.TrimSpace(r.PathValue(name))
}
