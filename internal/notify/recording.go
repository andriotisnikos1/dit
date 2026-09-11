package notify

import (
	"context"
	"sync"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// Recording is a Channel that remembers every message it was handed, for tests.
type Recording struct {
	mu       sync.Mutex
	channel  string
	messages []Message
	err      error
	// FailFor, when set, makes Send fail while it returns true for the title.
	FailFor func(Message) bool
}

var _ Channel = (*Recording)(nil)

// NewRecording builds a recording channel of the given type.
func NewRecording(channelType string) *Recording {
	return &Recording{channel: channelType}
}

// Type implements Channel.
func (r *Recording) Type() string { return r.channel }

// Send implements Channel.
func (r *Recording) Send(_ context.Context, msg Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.FailFor != nil && r.FailFor(msg) {
		r.err = ErrConfig(r.channel, "simulated failure")
		return r.err
	}
	r.messages = append(r.messages, msg)
	return nil
}

// Messages returns a copy of everything sent so far.
func (r *Recording) Messages() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Message, len(r.messages))
	copy(out, r.messages)
	return out
}

// Count returns how many messages were sent successfully.
func (r *Recording) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

// Reset clears the recorded messages.
func (r *Recording) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = nil
}

// FakeBuilder is a notify.Builder that hands out recording channels, for tests.
type FakeBuilder struct {
	mu       sync.Mutex
	byID     map[string]*Recording
	failures map[string]error
}

// NewFakeBuilder builds an empty FakeBuilder.
func NewFakeBuilder() *FakeBuilder {
	return &FakeBuilder{byID: map[string]*Recording{}, failures: map[string]error{}}
}

// Set registers a recording channel for a channel ID.
func (f *FakeBuilder) Set(channelID, channelType string) *Recording {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := NewRecording(channelType)
	f.byID[channelID] = rec
	return rec
}

// Fail makes Build return an error for a channel ID.
func (f *FakeBuilder) Fail(channelID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[channelID] = err
}

// Get returns the recording channel for an ID, if any.
func (f *FakeBuilder) Get(channelID string) (*Recording, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.byID[channelID]
	return rec, ok
}

// Build implements Builder.
func (f *FakeBuilder) Build(channelID, channelType string, _ map[string]string) (Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failures[channelID]; ok {
		return nil, err
	}
	if rec, ok := f.byID[channelID]; ok {
		return rec, nil
	}
	rec := NewRecording(channelType)
	f.byID[channelID] = rec
	return rec, nil
}

// Builder turns a stored channel into a transport. It is an interface so tests
// can substitute recording channels without touching the network.
type Builder interface {
	Build(channelID, channelType string, config map[string]string) (Channel, error)
}

// DefaultBuilder builds real channels from stored configs.
type DefaultBuilder struct{}

// Build implements Builder.
func (DefaultBuilder) Build(_ string, channelType string, config map[string]string) (Channel, error) {
	return Build(channelType, config)
}

var _ Builder = DefaultBuilder{}

// ChannelTypeOf reports the wire type of a channel; kept here so tests and the
// engine share one conversion.
func ChannelTypeOf(t apitypes.ChannelType) string { return string(t) }
