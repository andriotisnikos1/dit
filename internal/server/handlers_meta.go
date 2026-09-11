package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/store"
)

// handleHealthz serves the unauthenticated liveness probe.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, apitypes.Health{Status: "ok", Version: s.version})
}

// handleStatus reports version, uptime, watch counts and scheduler state.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	counts, err := s.store.Stats(ctx)
	if err != nil {
		writeStoreError(w, r, err, "status", s.log)
		return
	}

	status := apitypes.Status{
		Version:       s.version,
		UptimeSeconds: int64(s.now().Sub(s.started).Seconds()),
		Watches:       counts.Watches,
		Channels:      counts.Channels,
		Registries:    counts.Registries,
		Events:        counts.Events,
		CheckInterval: s.cfg.CheckInterval.String(),
	}
	if s.engine != nil {
		status.CheckConcurrency = s.engine.Concurrency()
		status.Ticks = s.engine.Ticks()
		status.LastTick = s.engine.LastTick()
		status.NextTick = s.engine.NextTick()
	} else {
		status.CheckConcurrency = s.cfg.CheckConcurrency
	}
	writeJSON(w, r, http.StatusOK, status)
}

// handleListEvents serves the global event feed.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	// The global feed takes the watch from the query string, which is what
	// `dit events --watch <id>` sends; the per-watch endpoint takes it from
	// the path. Both funnel into the same listing.
	s.listEvents(w, r, pathValue(r, "id"), r.URL.Query().Get("watch"))
}

// handleWatchEvents serves the per-watch history.
func (s *Server) handleWatchEvents(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if id == "" {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, "watch id is required"), s.log)
		return
	}
	s.listEvents(w, r, id, "")
}

// listEvents is the shared implementation of the two event endpoints. The
// watch ID may arrive from either the path or the query, so both parameters
// are accepted and a conflict is rejected rather than silently resolved.
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, pathWatchID, queryWatchID string) {
	ctx, cancel := s.context(r)
	defer cancel()

	pathWatchID = strings.TrimSpace(pathWatchID)
	queryWatchID = strings.TrimSpace(queryWatchID)
	if pathWatchID != "" && queryWatchID != "" && pathWatchID != queryWatchID {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest,
			"the watch in the path and the watch in the query string disagree"), s.log)
		return
	}
	watchID := pathWatchID
	if watchID == "" {
		watchID = queryWatchID
	}

	limit, offset, err := pagination(r)
	if err != nil {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, err.Error()), s.log)
		return
	}

	eventType := apitypes.EventType(r.URL.Query().Get("type"))
	if eventType != "" && !eventType.Valid() {
		writeError(w, r, apitypes.Errorf(apitypes.CodeValidationFailed,
			"unknown event type %q", eventType), s.log)
		return
	}

	// A per-watch listing must prove the watch exists, so an unknown ID is a
	// 404 rather than an empty page.
	if watchID != "" {
		if _, err := s.store.GetWatch(ctx, watchID); err != nil {
			writeStoreError(w, r, err, "watch", s.log)
			return
		}
	}

	events, total, err := s.store.ListEvents(ctx, store.EventFilter{
		WatchID: watchID,
		Type:    eventType,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		writeStoreError(w, r, err, "events", s.log)
		return
	}

	items := make([]apitypes.Event, 0, len(events))
	for _, e := range events {
		apiEvent := apitypes.Event{
			ID:        e.ID,
			WatchID:   e.WatchID,
			Type:      e.Type,
			Tag:       e.Tag,
			OldDigest: e.OldDigest,
			NewDigest: e.NewDigest,
			Detail:    e.Detail,
			CreatedAt: e.CreatedAt,
			Image:     e.Image,
		}
		notifications, err := s.store.NotificationsForEvent(ctx, e.ID)
		if err != nil {
			writeStoreError(w, r, err, "notifications", s.log)
			return
		}
		apiEvent.Notifications = make([]apitypes.Notification, 0, len(notifications))
		for _, n := range notifications {
			apiEvent.Notifications = append(apiEvent.Notifications, *n)
		}
		items = append(items, apiEvent)
	}

	writeJSON(w, r, http.StatusOK, apitypes.NewList(items, total, limit, offset))
}

// handleListNotifications serves the delivery log.
func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	limit, offset, err := pagination(r)
	if err != nil {
		writeError(w, r, apitypes.NewError(apitypes.CodeBadRequest, err.Error()), s.log)
		return
	}

	filter := store.NotificationFilter{
		EventID: pathValue(r, "event_id"),
		Limit:   limit,
		Offset:  offset,
	}
	if raw := r.URL.Query().Get("status"); raw != "" {
		filter.Status = apitypes.NotificationStatus(raw)
	}
	if raw := r.URL.Query().Get("event"); raw != "" {
		filter.EventID = raw
	}

	records, total, err := s.store.ListNotifications(ctx, filter)
	if err != nil {
		writeStoreError(w, r, err, "notifications", s.log)
		return
	}

	items := make([]apitypes.Notification, 0, len(records))
	for _, rec := range records {
		items = append(items, *rec.ToAPIType())
	}
	writeJSON(w, r, http.StatusOK, apitypes.NewList(items, total, limit, offset))
}

// handleListRegistries reports known registry hosts and whether credentials
// are stored for them.
func (s *Server) handleListRegistries(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	registries, err := s.store.RegistryHosts(ctx)
	if err != nil {
		writeStoreError(w, r, err, "registries", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, apitypes.NewList(registries, len(registries), len(registries), 0))
}

// uptimeString is a helper for human output.
func (s *Server) uptimeString() string {
	return time.Since(s.started).Round(time.Second).String()
}
