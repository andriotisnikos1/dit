// Package server implements the dit HTTP API: routing, authentication and
// handlers. It owns no state of its own — everything lives in the store and
// the check engine.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/check"
	"github.com/andriotisnikos1/dit/internal/config"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
)

// Options configures a Server.
type Options struct {
	Config   *config.Server
	Store    *store.DB
	Engine   *check.Engine
	Registry registry.Client
	Notifier notify.Builder
	Logger   *slog.Logger
	Version  string
	// Now is the clock, overridable in tests.
	Now func() time.Time
}

// Server serves the dit API.
type Server struct {
	cfg      *config.Server
	store    *store.DB
	engine   *check.Engine
	registry registry.Client
	notifier notify.Builder
	log      *slog.Logger
	version  string
	started  time.Time
	now      func() time.Time

	mux http.Handler
}

// New builds a Server and its routing table.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("server: store is required")
	}
	if opts.Config == nil {
		return nil, errors.New("server: config is required")
	}
	if opts.Registry == nil {
		opts.Registry = registry.NewRemote()
	}
	if opts.Notifier == nil {
		opts.Notifier = notify.DefaultBuilder{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}

	s := &Server{
		cfg:      opts.Config,
		store:    opts.Store,
		engine:   opts.Engine,
		registry: opts.Registry,
		notifier: opts.Notifier,
		log:      opts.Logger,
		version:  opts.Version,
		started:  opts.Now(),
		now:      opts.Now,
	}
	s.mux = s.routes()
	return s, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// routes builds the routing table.
//
// Go's ServeMux pattern syntax gives us path variables ({id}) and method
// matching for free, so no third-party router is needed.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated liveness probe.
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Authenticated API.
	api := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.authenticate(h))
	}

	api("GET /api/v1/status", s.handleStatus)

	api("POST /api/v1/watches", s.handleCreateWatch)
	api("GET /api/v1/watches", s.handleListWatches)
	api("GET /api/v1/watches/{id}", s.handleGetWatch)
	api("PATCH /api/v1/watches/{id}", s.handleUpdateWatch)
	api("DELETE /api/v1/watches/{id}", s.handleDeleteWatch)
	api("POST /api/v1/watches/{id}/check", s.handleCheckWatch)
	api("GET /api/v1/watches/{id}/events", s.handleWatchEvents)

	api("GET /api/v1/events", s.handleListEvents)

	api("GET /api/v1/channels", s.handleListChannels)
	api("POST /api/v1/channels", s.handleCreateChannel)
	api("GET /api/v1/channels/{id}", s.handleGetChannel)
	api("PATCH /api/v1/channels/{id}", s.handleUpdateChannel)
	api("DELETE /api/v1/channels/{id}", s.handleDeleteChannel)
	api("POST /api/v1/channels/{id}/test", s.handleTestChannel)

	api("GET /api/v1/registries", s.handleListRegistries)
	api("GET /api/v1/registries/{host}/credentials", s.handleGetCredentials)
	api("PUT /api/v1/registries/{host}/credentials", s.handlePutCredentials)
	api("DELETE /api/v1/registries/{host}/credentials", s.handleDeleteCredentials)

	api("GET /api/v1/notifications", s.handleListNotifications)
	api("POST /api/v1/notifications/{id}/retry", s.handleRetryNotification)

	// Anything else is a 404 in the standard envelope.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, apitypes.Errorf(apitypes.CodeNotFound, "no such endpoint: %s %s",
			r.Method, r.URL.Path), s.log)
	})

	return withRecovery(withRequestLog(s.log, mux), s.log)
}

// authenticate enforces the shared static secret with a constant-time compare.
func (s *Server) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := bearerToken(r)
		if provided == "" {
			writeError(w, r, apitypes.NewError(apitypes.CodeUnauthorized,
				"missing Authorization header; expected `Authorization: Bearer <token>`"), s.log)
			return
		}
		if !constantTimeEqual(provided, s.cfg.APIToken) {
			writeError(w, r, apitypes.NewError(apitypes.CodeUnauthorized,
				"invalid API token"), s.log)
			return
		}
		next(w, r)
	}
}

// bearerToken extracts the token from the Authorization header. Surrounding
// whitespace is tolerated: a token pasted into a shell or a header is a
// frequent source of stray spaces, and rejecting them helps nobody.
func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// constantTimeEqual compares two secrets without leaking length or content
// through timing. Both sides are hashed first so the comparison itself is
// always over a fixed 32 bytes.
func constantTimeEqual(a, b string) bool {
	sumA := sha256.Sum256([]byte(a))
	sumB := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(sumA[:], sumB[:]) == 1
}

// ---------- middleware ----------

// withRequestLog logs each request without ever touching the Authorization
// header.
func withRequestLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr)
	})
}

// withRecovery turns a panic into a 500 rather than a dropped connection.
func withRecovery(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic serving request", "path", r.URL.Path, "panic", rec)
				writeError(w, r, apitypes.NewError(apitypes.CodeInternalError,
					"internal server error"), log)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status for logging.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHeader {
		return
	}
	s.status = code
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer when it supports flushing, which
// keeps streaming responses working through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ---------- engine access ----------

// engineOrErr returns the check engine, or writes a 500 when it is missing.
func (s *Server) engineOrErr(w http.ResponseWriter, r *http.Request) *check.Engine {
	if s.engine == nil {
		writeError(w, r, apitypes.NewError(apitypes.CodeInternalError,
			"check engine is not available"), s.log)
		return nil
	}
	return s.engine
}

// context returns a context bounded by the server's check timeout.
func (s *Server) context(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.cfg.CheckTimeout.Std())
}
