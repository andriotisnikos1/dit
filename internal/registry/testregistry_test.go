package registry

import (
	"net/http"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
)

// newTestRegistry builds an in-process OCI registry with tag listing enabled,
// which the upstream default disables.
func newTestRegistry(t *testing.T) http.Handler {
	t.Helper()
	return registry.New(registry.WithReferrersSupport(true))
}
