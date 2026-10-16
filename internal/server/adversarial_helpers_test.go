package server

// Helpers shared by the adversarial probe files. Kept in their own file so the
// per-bug probe files can be committed alongside the fix they justify.

import (
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// checkPtr compares an optional flag value against an expectation; want nil
// means the pointer must be absent.
func checkPtr(t *testing.T, label string, got *any, want any) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("%s = %v, want absent", label, *got)
		}
		return
	}
	if got == nil {
		t.Errorf("%s = absent, want %v", label, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %v, want %v", label, *got, want)
	}
}

// asAPIError reports whether err carries an apitypes.APIError and copies it.
func asAPIError(err error, target *apitypes.APIError) bool {
	if apiErr, ok := err.(apitypes.APIError); ok {
		*target = apiErr
		return true
	}
	return false
}
