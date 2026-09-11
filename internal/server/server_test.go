package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/check"
	"github.com/andriotisnikos1/dit/internal/config"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

const testToken = "test-shared-secret"

// harness bundles a running test server with its dependencies.
type harness struct {
	t        *testing.T
	server   *Server
	store    *store.DB
	registry *registry.Fake
	notifier *notify.FakeBuilder
	handler  http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := testutil.Store(t)
	fake := registry.NewFake()
	builder := notify.NewFakeBuilder()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := config.DefaultServer()
	cfg.APIToken = testToken
	cfg.CheckTimeout = config.Duration(5 * time.Second)

	engine, err := check.New(check.Options{
		Store:          db,
		Registry:       fake,
		Notifier:       builder,
		Logger:         logger,
		Interval:       time.Hour,
		Concurrency:    2,
		Timeout:        5 * time.Second,
		NotifyAttempts: 1,
		NotifyBackoff:  time.Millisecond,
		Now:            func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}

	srv, err := New(Options{
		Config:   cfg,
		Store:    db,
		Engine:   engine,
		Registry: fake,
		Notifier: builder,
		Logger:   logger,
		Version:  "test",
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	return &harness{t: t, server: srv, store: db, registry: fake, notifier: builder, handler: srv.Handler()}
}

// do performs a request, adding the Authorization header unless token is the
// empty sentinel "\x00none".
func (h *harness) do(method, path string, body any, token string) *httptest.ResponseRecorder {
	h.t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "\x00none" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// authed performs an authenticated request.
func (h *harness) authed(method, path string, body any) *httptest.ResponseRecorder {
	return h.do(method, path, body, testToken)
}

// decode unmarshals a response body into dst.
func (h *harness) decode(rec *httptest.ResponseRecorder, dst any) {
	h.t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		h.t.Fatalf("decode response (status %d): %v\nbody: %s", rec.Code, err, rec.Body.String())
	}
}

// errorCode extracts the error code from an error response.
func (h *harness) errorCode(rec *httptest.ResponseRecorder) string {
	h.t.Helper()
	var envelope apitypes.ErrorEnvelope
	h.decode(rec, &envelope)
	return envelope.Error.Code
}

// createWatch is a shortcut for a successful watch creation.
func (h *harness) createWatch(image string, req apitypes.CreateWatchRequest) apitypes.CreateWatchResponse {
	h.t.Helper()
	req.Image = image
	rec := h.authed(http.MethodPost, "/api/v1/watches", req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		h.t.Fatalf("create watch: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp apitypes.CreateWatchResponse
	h.decode(rec, &resp)
	return resp
}

func TestHealthzIsUnauthenticated(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodGet, "/healthz", nil, "\x00none")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var health apitypes.Health
	h.decode(rec, &health)
	if health.Status != "ok" {
		t.Errorf("status field = %q, want ok", health.Status)
	}
	if health.Version != "test" {
		t.Errorf("version = %q, want test", health.Version)
	}
}

func TestAuthMiddleware(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"valid token", testToken, http.StatusOK},
		{"missing header", "\x00none", http.StatusUnauthorized},
		{"wrong token", "not-the-token", http.StatusUnauthorized},
		{"empty token", "", http.StatusUnauthorized},
		{"token with surrounding whitespace", " " + testToken + " ", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodGet, "/api/v1/status", nil, tc.token)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusUnauthorized {
				if code := h.errorCode(rec); code != apitypes.CodeUnauthorized {
					t.Errorf("error code = %q, want %q", code, apitypes.CodeUnauthorized)
				}
			}
		})
	}
}

func TestAuthRejectsNonBearerScheme(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Authorization", "Basic "+testToken)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a Basic scheme", rec.Code)
	}
}

func TestStatusReportsCounts(t *testing.T) {
	h := newHarness(t)
	h.createWatch("ghcr.io/owner/app:v1", apitypes.CreateWatchRequest{})
	h.createWatch("ghcr.io/owner/app:v2", apitypes.CreateWatchRequest{})
	h.authed(http.MethodPost, "/api/v1/channels", apitypes.CreateChannelRequest{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"},
	})

	rec := h.authed(http.MethodGet, "/api/v1/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var status apitypes.Status
	h.decode(rec, &status)

	if status.Version != "test" {
		t.Errorf("version = %q, want test", status.Version)
	}
	if status.Watches.Total != 2 || status.Watches.Enabled != 2 {
		t.Errorf("watch counts = %+v, want 2 total / 2 enabled", status.Watches)
	}
	if status.Channels != 1 {
		t.Errorf("channels = %d, want 1", status.Channels)
	}
	if status.CheckInterval == "" {
		t.Error("check_interval is empty")
	}
}

func TestUnknownEndpointReturnsEnvelope(t *testing.T) {
	h := newHarness(t)
	rec := h.authed(http.MethodGet, "/api/v1/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if code := h.errorCode(rec); code != apitypes.CodeNotFound {
		t.Errorf("error code = %q, want %q", code, apitypes.CodeNotFound)
	}
}

func TestMethodMismatchIsRejected(t *testing.T) {
	h := newHarness(t)
	// The router is method-aware: DELETE on the collection is not a route.
	rec := h.authed(http.MethodDelete, "/api/v1/watches", nil)
	if rec.Code == http.StatusOK || rec.Code == http.StatusNoContent {
		t.Errorf("status = %d, want an error for an unsupported method", rec.Code)
	}
}

func TestResponseContentTypeIsJSON(t *testing.T) {
	h := newHarness(t)
	rec := h.authed(http.MethodGet, "/api/v1/status", nil)
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

func TestMalformedJSONBodyIsRejected(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/watches", strings.NewReader("{not json"))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if code := h.errorCode(rec); code != apitypes.CodeBadRequest {
		t.Errorf("error code = %q, want %q", code, apitypes.CodeBadRequest)
	}
}

func TestUnknownJSONFieldIsRejected(t *testing.T) {
	h := newHarness(t)

	body := `{"image":"nginx:latest","typo_field":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/watches", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown field", rec.Code)
	}
}

func TestContextCancellationIsHandled(t *testing.T) {
	h := newHarness(t)
	// A cancelled request context must not panic or hang the handler.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	// Either an error status or an empty body is acceptable; a panic is not.
	if rec.Code == 0 {
		t.Error("the handler wrote no status")
	}
}
