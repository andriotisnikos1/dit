package check

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
)

// authenticator returns the credentials to use for a registry, or the
// anonymous authenticator when none are stored.
func (e *Engine) authenticator(ctx context.Context, host string) (authn.Authenticator, error) {
	creds, err := e.store.GetCredentials(ctx, host)
	if err != nil {
		if store.IsNotFound(err) {
			return registry.Anonymous(), nil
		}
		return nil, fmt.Errorf("load credentials for %s: %w", host, err)
	}
	auth := registry.StaticAuth(creds.Username, creds.Secret)
	return auth, nil
}

// deliver sends an event to every channel the watch notifies and records each
// attempt in the notifications table.
//
// It returns how many channels accepted the message. Delivery failures are
// logged and recorded, never returned: a broken channel must not corrupt the
// check result.
func (e *Engine) deliver(ctx context.Context, watch *store.WatchRecord, event *store.EventRecord) int {
	channels, err := e.store.ChannelsForWatch(ctx, watch.ID)
	if err != nil {
		e.log.Error("resolve notification channels", "watch", watch.ID, "error", err)
		return 0
	}
	if len(channels) == 0 {
		e.log.Debug("no channels to notify", "watch", watch.ID)
		return 0
	}

	msg := BuildMessage(watch, event)
	sent := 0
	for _, ch := range channels {
		record, err := e.store.CreateNotification(ctx, event.ID, ch.ID)
		if err != nil {
			e.log.Error("record notification", "channel", ch.ID, "error", err)
			continue
		}

		transport, err := e.notifier.Build(ch.ID, string(ch.Type), ch.Config)
		if err != nil {
			e.log.Error("build channel", "channel", ch.Name, "type", ch.Type, "error", err)
			if uerr := e.store.UpdateNotification(ctx, record.ID,
				"failed", 0, "build channel: "+err.Error()); uerr != nil {
				e.log.Error("record notification failure", "notification", record.ID, "error", uerr)
			}
			continue
		}

		attempts, lastErr := e.sendWithRetries(ctx, transport, msg)
		status := "sent"
		errMsg := ""
		if lastErr != nil {
			status = "failed"
			errMsg = lastErr.Error()
			e.log.Warn("notification delivery failed",
				"channel", ch.Name, "type", ch.Type,
				"event", event.ID, "attempts", attempts, "error", errMsg)
		} else {
			sent++
			e.log.Info("notification sent",
				"channel", ch.Name, "type", ch.Type, "event", event.ID, "attempts", attempts)
		}

		if err := e.store.UpdateNotification(ctx, record.ID,
			notificationStatus(status), attempts, errMsg); err != nil {
			e.log.Error("record notification result", "notification", record.ID, "error", err)
		}
	}
	return sent
}

// sendWithRetries attempts delivery up to notifyAttempts times, backing off
// between attempts. It returns the number of attempts made and the last error.
func (e *Engine) sendWithRetries(ctx context.Context, ch notify.Channel, msg notify.Message) (int, error) {
	attempts := 0
	var lastErr error
	for attempts < e.notifyAttempts {
		attempts++
		if err := ch.Send(ctx, msg); err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return attempts, lastErr
			}
			if attempts < e.notifyAttempts {
				delay := e.notifyBackoff * time.Duration(attempts)
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return attempts, lastErr
				case <-timer.C:
				}
			}
			continue
		}
		return attempts, nil
	}
	return attempts, lastErr
}

// RetryNotification re-delivers a previously recorded notification. Used by the
// API to retry a failed delivery without re-running the check.
func (e *Engine) RetryNotification(ctx context.Context, notificationID string) error {
	record, err := e.store.GetNotification(ctx, notificationID)
	if err != nil {
		return err
	}
	channel, err := e.store.GetChannel(ctx, record.ChannelID)
	if err != nil {
		return err
	}
	event, err := e.store.GetEvent(ctx, record.EventID)
	if err != nil {
		return err
	}
	watch, err := e.store.GetWatch(ctx, event.WatchID)
	if err != nil {
		return err
	}

	transport, err := e.notifier.Build(channel.ID, string(channel.Type), channel.Config)
	if err != nil {
		return err
	}
	attempts, lastErr := e.sendWithRetries(ctx, transport, BuildMessage(watch, event))
	if lastErr != nil {
		if uerr := e.store.UpdateNotification(ctx, notificationID,
			notificationStatus("failed"), attempts, lastErr.Error()); uerr != nil {
			return errors.Join(lastErr, uerr)
		}
		return lastErr
	}
	return e.store.UpdateNotification(ctx, notificationID, notificationStatus("sent"), attempts, "")
}

// TestChannel sends a synthetic message through a channel, for
// POST /api/v1/channels/{id}/test.
func (e *Engine) TestChannel(ctx context.Context, channel *store.ChannelRecord) error {
	transport, err := e.notifier.Build(channel.ID, string(channel.Type), channel.Config)
	if err != nil {
		return err
	}
	testCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	return transport.Send(testCtx, notify.Message{
		Title:    "dit test notification",
		Body:     fmt.Sprintf("This is a test message from dit for channel %q (%s).", channel.Name, channel.Type),
		Priority: "default",
		Tags:     []string{"test", "dit"},
	})
}
