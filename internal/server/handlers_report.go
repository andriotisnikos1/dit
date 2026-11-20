package server

import (
	"context"
	"net/http"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// reportContext bounds a report run. Delivering to every channel, each with
// retries, can take longer than a single registry check, so reports get at
// least a minute.
func (s *Server) reportContext(r *http.Request) (context.Context, context.CancelFunc) {
	timeout := 2 * s.cfg.CheckTimeout.Std()
	if timeout < time.Minute {
		timeout = time.Minute
	}
	return context.WithTimeout(r.Context(), timeout)
}

// reportKind reads and validates the {kind} path variable.
func (s *Server) reportKind(w http.ResponseWriter, r *http.Request) (apitypes.ReportKind, bool) {
	kind := apitypes.ReportKind(pathValue(r, "kind"))
	if !kind.Valid() {
		writeError(w, r, apitypes.Errorf(apitypes.CodeNotFound,
			"unknown report kind %q; valid: daily, weekly", kind), s.log)
		return "", false
	}
	return kind, true
}

// handleReportStatus serves GET /api/v1/reports.
func (s *Server) handleReportStatus(w http.ResponseWriter, r *http.Request) {
	engine := s.engineOrErr(w, r)
	if engine == nil {
		return
	}
	ctx, cancel := s.reportContext(r)
	defer cancel()

	status, err := engine.ReportStatus(ctx)
	if err != nil {
		writeStoreError(w, r, err, "reports", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, status)
}

// handlePreviewReport serves GET /api/v1/reports/{kind}/preview: it renders
// the next report without sending or marking anything.
func (s *Server) handlePreviewReport(w http.ResponseWriter, r *http.Request) {
	engine := s.engineOrErr(w, r)
	if engine == nil {
		return
	}
	kind, ok := s.reportKind(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.reportContext(r)
	defer cancel()

	result, err := engine.SendReport(ctx, kind, true)
	if err != nil {
		writeStoreError(w, r, err, "report", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

// handleSendReport serves POST /api/v1/reports/{kind}/send: the manual run,
// identical to what the scheduler does.
func (s *Server) handleSendReport(w http.ResponseWriter, r *http.Request) {
	engine := s.engineOrErr(w, r)
	if engine == nil {
		return
	}
	kind, ok := s.reportKind(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.reportContext(r)
	defer cancel()

	result, err := engine.SendReport(ctx, kind, false)
	if err != nil {
		writeStoreError(w, r, err, "report", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}
