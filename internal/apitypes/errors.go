// Package apitypes holds the request/response DTOs shared by the dit-server
// HTTP API and the dit CLI client. It is the single source of truth for the
// wire format of the REST API.
package apitypes

import (
	"fmt"
	"net/http"
)

// Error codes carried in the uniform error envelope.
const (
	CodeBadRequest          = "bad_request"
	CodeUnauthorized        = "unauthorized"
	CodeNotFound            = "not_found"
	CodeConflict            = "conflict"
	CodeValidationFailed    = "validation_failed"
	CodeCredentialsRequired = "credentials_required"
	CodeUpstreamError       = "upstream_error"
	CodeInternalError       = "internal_error"
)

// APIError is the body of every non-2xx response:
//
//	{"error":{"code":"credentials_required","message":"…","registry":"ghcr.io"}}
type APIError struct {
	Code     string            `json:"code"                 yaml:"code"`
	Message  string            `json:"message"              yaml:"message"`
	Registry string            `json:"registry,omitempty"   yaml:"registry,omitempty"`
	Details  map[string]string `json:"details,omitempty"    yaml:"details,omitempty"`
}

func (e APIError) Error() string {
	if e.Registry != "" {
		return fmt.Sprintf("%s: %s (registry %s)", e.Code, e.Message, e.Registry)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// ErrorEnvelope wraps an APIError for the wire.
type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

// NewError builds an APIError.
func NewError(code, message string) APIError {
	return APIError{Code: code, Message: message}
}

// Errorf builds an APIError with a formatted message.
func Errorf(code, format string, args ...any) APIError {
	return APIError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithRegistry returns a copy of the error annotated with a registry host.
func (e APIError) WithRegistry(registry string) APIError {
	e.Registry = registry
	return e
}

// WithDetails returns a copy of the error annotated with detail fields.
func (e APIError) WithDetails(details map[string]string) APIError {
	e.Details = details
	return e
}

// StatusCode maps an error code to the HTTP status code the server returns.
//
// The 428 response is the prompt-and-retry trigger: the CLI sees
// credentials_required, prompts the operator, stores the credentials and
// replays the original request.
func StatusCode(code string) int {
	switch code {
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeValidationFailed:
		return http.StatusUnprocessableEntity
	case CodeCredentialsRequired:
		return http.StatusPreconditionRequired // 428
	case CodeUpstreamError:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// IsCredentialsRequired reports whether err is (or wraps) the 428 trigger.
func IsCredentialsRequired(err error) bool {
	var apiErr APIError
	if ok := asAPIError(err, &apiErr); ok {
		return apiErr.Code == CodeCredentialsRequired
	}
	return false
}

func asAPIError(err error, target *APIError) bool {
	for err != nil {
		if e, ok := err.(APIError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
