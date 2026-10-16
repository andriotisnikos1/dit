package server

// Probe for concurrent flag writes through the API: the store serialises the
// read-modify-write of flags_json, so no override may be lost when several
// PUTs land at once.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestAdversarialConcurrentFlagSetsDoNotLoseOverrides fires one PUT per flag at
// the same watch and asserts every override survived.
func TestAdversarialConcurrentFlagSetsDoNotLoseOverrides(t *testing.T) {
	h := newHarness(t)
	h.registry.SetProbe("ghcr.io", "owner/app", registryProbeExists())
	pat := h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{Patterns: []string{"v*"}})
	id := pat.Watches[0].ID

	writes := []struct {
		name  string
		value any
	}{
		{"history", 2},
		{"notify_threshold", 3},
		{"semver", true},
		{"semver_stable", true},
		{"semver_notify_threshold", 4},
	}

	var wg sync.WaitGroup
	codes := make([]int, len(writes))
	bodies := make([]string, len(writes))
	for i, w := range writes {
		wg.Add(1)
		go func(i int, name string, value any) {
			defer wg.Done()
			raw, err := json.Marshal(apitypes.SetFlagRequest{Value: value})
			if err != nil {
				return
			}
			req := httptest.NewRequest(http.MethodPut,
				"/api/v1/watches/"+id+"/flags/"+name, bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+testToken)
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			codes[i] = rec.Code
			bodies[i] = rec.Body.String()
		}(i, w.name, w.value)
	}
	wg.Wait()

	for i, w := range writes {
		if codes[i] != http.StatusOK {
			t.Errorf("PUT %s = %d (%s), want 200", w.name, codes[i], bodies[i])
		}
	}

	list := h.flagsOf(id)
	want := map[string]any{
		"history":                 float64(2),
		"notify_threshold":        float64(3),
		"semver":                  true,
		"semver_stable":           true,
		"semver_notify_threshold": float64(4),
	}
	for name, value := range want {
		f := getFlag(t, list, name)
		if f.Local == nil {
			t.Errorf("%s override was lost by a concurrent write", name)
			continue
		}
		if *f.Local != value {
			t.Errorf("%s local = %v, want %v", name, *f.Local, value)
		}
		if f.Effective == nil || *f.Effective != value {
			t.Errorf("%s effective = %v, want %v", name, f.Effective, value)
		}
	}
}
