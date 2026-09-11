package server

import (
	"net/http"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/store"
)

// handleRetryNotification re-delivers a recorded notification.
//
// A failed delivery never affects the check result, so the only way to recover
// one is to retry it: this endpoint re-sends the original event through its
// channel without re-running the check.
func (s *Server) handleRetryNotification(w http.ResponseWriter, r *http.Request) {
	engine := s.engineOrErr(w, r)
	if engine == nil {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()

	id := pathValue(r, "id")
	record, err := s.store.GetNotification(ctx, id)
	if err != nil {
		writeStoreError(w, r, err, "notification", s.log)
		return
	}
	// A delivery that already succeeded is not retried: re-sending it would
	// duplicate a notification the operator has seen.
	if record.Status == apitypes.NotificationSent {
		writeError(w, r, apitypes.Errorf(apitypes.CodeConflict,
			"notification %s was already delivered at %s", record.ID, record.SentAt.Format("2006-01-02T15:04:05Z")), s.log)
		return
	}

	if err := engine.RetryNotification(ctx, id); err != nil {
		// The retry ran but delivery failed again: report the transport error
		// alongside the refreshed record so the caller sees what happened.
		refreshed, getErr := s.store.GetNotification(ctx, id)
		if getErr != nil {
			writeStoreError(w, r, getErr, "notification", s.log)
			return
		}
		writeJSON(w, r, http.StatusOK, retryResult(refreshed, err))
		return
	}

	refreshed, err := s.store.GetNotification(ctx, id)
	if err != nil {
		writeStoreError(w, r, err, "notification", s.log)
		return
	}
	writeJSON(w, r, http.StatusOK, retryResult(refreshed, nil))
}

// NotificationRetryResult reports the outcome of a retry.
func retryResult(record *store.NotificationRecord, retryErr error) apitypes.NotificationRetryResult {
	out := apitypes.NotificationRetryResult{
		Notification: *record.ToAPIType(),
		OK:           retryErr == nil,
	}
	if retryErr != nil {
		out.Error = retryErr.Error()
	}
	return out
}
