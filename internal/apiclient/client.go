// Package apiclient is the typed HTTP client the CLI uses to talk to
// dit-server. It is the only place that knows the REST shape of the API.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// Client talks to one dit-server.
type Client struct {
	BaseURL   string
	Token     string
	UserAgent string
	HTTP      *http.Client
}

// New builds a Client for a server URL.
func New(baseURL, token string) (*Client, error) {
	normalised, err := NormaliseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL:   normalised,
		Token:     strings.TrimSpace(token),
		UserAgent: "dit-cli/1 (+https://github.com/andriotisnikos1/dit)",
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// NormaliseURL validates a server URL and strips trailing slashes.
func NormaliseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("server URL is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid server URL %q: %w", raw, err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid server URL %q: no host", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid server URL %q: scheme must be http or https", raw)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

// API is the surface the CLI depends on. It exists so commands can be tested
// with a stub instead of an HTTP server.
type API interface {
	Health(ctx context.Context) (apitypes.Health, error)
	Status(ctx context.Context) (apitypes.Status, error)

	CreateWatch(ctx context.Context, req apitypes.CreateWatchRequest) (apitypes.CreateWatchResponse, error)
	ListWatches(ctx context.Context, q WatchQuery) (apitypes.List[apitypes.Watch], error)
	GetWatch(ctx context.Context, id string) (apitypes.Watch, error)
	UpdateWatch(ctx context.Context, id string, req apitypes.UpdateWatchRequest) (apitypes.Watch, error)
	DeleteWatch(ctx context.Context, id string) error
	CheckWatch(ctx context.Context, id string) (apitypes.CheckResult, error)
	WatchEvents(ctx context.Context, id string, limit int) (apitypes.List[apitypes.Event], error)

	Events(ctx context.Context, q EventQuery) (apitypes.List[apitypes.Event], error)
	Notifications(ctx context.Context, limit int) (apitypes.List[apitypes.Notification], error)

	ListChannels(ctx context.Context) (apitypes.List[apitypes.Channel], error)
	CreateChannel(ctx context.Context, req apitypes.CreateChannelRequest) (apitypes.Channel, error)
	GetChannel(ctx context.Context, id string) (apitypes.Channel, error)
	UpdateChannel(ctx context.Context, id string, req apitypes.UpdateChannelRequest) (apitypes.Channel, error)
	DeleteChannel(ctx context.Context, id string) error
	TestChannel(ctx context.Context, id string) (apitypes.ChannelTestResult, error)

	ListRegistries(ctx context.Context) (apitypes.List[apitypes.Registry], error)
	GetCredentials(ctx context.Context, host string) (apitypes.CredentialsInfo, error)
	PutCredentials(ctx context.Context, host string, req apitypes.CredentialsRequest) (apitypes.CredentialsInfo, error)
	DeleteCredentials(ctx context.Context, host string) error
}

var _ API = (*Client)(nil)

// WatchQuery are the filters accepted by GET /api/v1/watches.
type WatchQuery struct {
	Enabled  *bool
	Registry string
	Search   string
	Limit    int
	Offset   int
}

// EventQuery are the filters accepted by GET /api/v1/events.
type EventQuery struct {
	WatchID string
	Type    apitypes.EventType
	Limit   int
	Offset  int
}

// ---------- endpoints ----------

// Health calls the unauthenticated liveness probe.
func (c *Client) Health(ctx context.Context) (apitypes.Health, error) {
	var out apitypes.Health
	err := c.do(ctx, http.MethodGet, "/healthz", nil, nil, &out, true)
	return out, err
}

// Status fetches the server status.
func (c *Client) Status(ctx context.Context) (apitypes.Status, error) {
	var out apitypes.Status
	err := c.do(ctx, http.MethodGet, "/api/v1/status", nil, nil, &out, false)
	return out, err
}

// CreateWatch creates one watch per tag or pattern.
func (c *Client) CreateWatch(ctx context.Context, req apitypes.CreateWatchRequest) (apitypes.CreateWatchResponse, error) {
	var out apitypes.CreateWatchResponse
	err := c.do(ctx, http.MethodPost, "/api/v1/watches", nil, req, &out, false)
	return out, err
}

// ListWatches lists watches.
func (c *Client) ListWatches(ctx context.Context, q WatchQuery) (apitypes.List[apitypes.Watch], error) {
	var out apitypes.List[apitypes.Watch]
	query := url.Values{}
	if q.Enabled != nil {
		query.Set("enabled", strconv.FormatBool(*q.Enabled))
	}
	if q.Registry != "" {
		query.Set("registry", q.Registry)
	}
	if q.Search != "" {
		query.Set("q", q.Search)
	}
	if q.Limit > 0 {
		query.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		query.Set("offset", strconv.Itoa(q.Offset))
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/watches", query, nil, &out, false)
	return out, err
}

// GetWatch fetches one watch.
func (c *Client) GetWatch(ctx context.Context, id string) (apitypes.Watch, error) {
	var out apitypes.Watch
	err := c.do(ctx, http.MethodGet, "/api/v1/watches/"+url.PathEscape(id), nil, nil, &out, false)
	return out, err
}

// UpdateWatch patches a watch.
func (c *Client) UpdateWatch(ctx context.Context, id string, req apitypes.UpdateWatchRequest) (apitypes.Watch, error) {
	var out apitypes.Watch
	err := c.do(ctx, http.MethodPatch, "/api/v1/watches/"+url.PathEscape(id), nil, req, &out, false)
	return out, err
}

// DeleteWatch removes a watch.
func (c *Client) DeleteWatch(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/watches/"+url.PathEscape(id), nil, nil, nil, false)
}

// CheckWatch forces an immediate check.
func (c *Client) CheckWatch(ctx context.Context, id string) (apitypes.CheckResult, error) {
	var out apitypes.CheckResult
	err := c.do(ctx, http.MethodPost, "/api/v1/watches/"+url.PathEscape(id)+"/check", nil, nil, &out, false)
	return out, err
}

// WatchEvents lists a watch's history.
func (c *Client) WatchEvents(ctx context.Context, id string, limit int) (apitypes.List[apitypes.Event], error) {
	var out apitypes.List[apitypes.Event]
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/watches/"+url.PathEscape(id)+"/events", query, nil, &out, false)
	return out, err
}

// Events lists the global event feed.
func (c *Client) Events(ctx context.Context, q EventQuery) (apitypes.List[apitypes.Event], error) {
	var out apitypes.List[apitypes.Event]
	query := url.Values{}
	if q.WatchID != "" {
		query.Set("watch", q.WatchID)
	}
	if q.Type != "" {
		query.Set("type", string(q.Type))
	}
	if q.Limit > 0 {
		query.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		query.Set("offset", strconv.Itoa(q.Offset))
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/events", query, nil, &out, false)
	return out, err
}

// Notifications lists the delivery log.
func (c *Client) Notifications(ctx context.Context, limit int) (apitypes.List[apitypes.Notification], error) {
	var out apitypes.List[apitypes.Notification]
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/notifications", query, nil, &out, false)
	return out, err
}

// ListChannels lists channels.
func (c *Client) ListChannels(ctx context.Context) (apitypes.List[apitypes.Channel], error) {
	var out apitypes.List[apitypes.Channel]
	err := c.do(ctx, http.MethodGet, "/api/v1/channels", nil, nil, &out, false)
	return out, err
}

// CreateChannel creates a channel.
func (c *Client) CreateChannel(ctx context.Context, req apitypes.CreateChannelRequest) (apitypes.Channel, error) {
	var out apitypes.Channel
	err := c.do(ctx, http.MethodPost, "/api/v1/channels", nil, req, &out, false)
	return out, err
}

// GetChannel fetches one channel.
func (c *Client) GetChannel(ctx context.Context, id string) (apitypes.Channel, error) {
	var out apitypes.Channel
	err := c.do(ctx, http.MethodGet, "/api/v1/channels/"+url.PathEscape(id), nil, nil, &out, false)
	return out, err
}

// UpdateChannel patches a channel.
func (c *Client) UpdateChannel(ctx context.Context, id string, req apitypes.UpdateChannelRequest) (apitypes.Channel, error) {
	var out apitypes.Channel
	err := c.do(ctx, http.MethodPatch, "/api/v1/channels/"+url.PathEscape(id), nil, req, &out, false)
	return out, err
}

// DeleteChannel removes a channel.
func (c *Client) DeleteChannel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/channels/"+url.PathEscape(id), nil, nil, nil, false)
}

// TestChannel sends a test notification.
func (c *Client) TestChannel(ctx context.Context, id string) (apitypes.ChannelTestResult, error) {
	var out apitypes.ChannelTestResult
	err := c.do(ctx, http.MethodPost, "/api/v1/channels/"+url.PathEscape(id)+"/test", nil, nil, &out, false)
	return out, err
}

// ListRegistries lists known registry hosts.
func (c *Client) ListRegistries(ctx context.Context) (apitypes.List[apitypes.Registry], error) {
	var out apitypes.List[apitypes.Registry]
	err := c.do(ctx, http.MethodGet, "/api/v1/registries", nil, nil, &out, false)
	return out, err
}

// GetCredentials reads credential metadata.
func (c *Client) GetCredentials(ctx context.Context, host string) (apitypes.CredentialsInfo, error) {
	var out apitypes.CredentialsInfo
	err := c.do(ctx, http.MethodGet, "/api/v1/registries/"+url.PathEscape(host)+"/credentials", nil, nil, &out, false)
	return out, err
}

// PutCredentials stores credentials.
func (c *Client) PutCredentials(ctx context.Context, host string, req apitypes.CredentialsRequest) (apitypes.CredentialsInfo, error) {
	var out apitypes.CredentialsInfo
	err := c.do(ctx, http.MethodPut, "/api/v1/registries/"+url.PathEscape(host)+"/credentials", nil, req, &out, false)
	return out, err
}

// DeleteCredentials removes credentials.
func (c *Client) DeleteCredentials(ctx context.Context, host string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/registries/"+url.PathEscape(host)+"/credentials", nil, nil, nil, false)
}

// ---------- transport ----------

// do performs one request and decodes the response.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any, skipAuth bool) error {
	endpoint := c.BaseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !skipAuth {
		if c.Token == "" {
			return fmt.Errorf("no API token configured")
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp.StatusCode, raw)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response from %s: %w", endpoint, err)
	}
	return nil
}

// decodeError turns an error response into an apitypes.APIError. The 428
// credentials_required case keeps its code so callers can branch on it.
func decodeError(status int, raw []byte) error {
	var envelope apitypes.ErrorEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Code != "" {
		return envelope.Error
	}
	// Fall back to a code derived from the status.
	code := apitypes.CodeInternalError
	switch status {
	case http.StatusBadRequest:
		code = apitypes.CodeBadRequest
	case http.StatusUnauthorized:
		code = apitypes.CodeUnauthorized
	case http.StatusNotFound:
		code = apitypes.CodeNotFound
	case http.StatusConflict:
		code = apitypes.CodeConflict
	case http.StatusUnprocessableEntity:
		code = apitypes.CodeValidationFailed
	case http.StatusPreconditionRequired:
		code = apitypes.CodeCredentialsRequired
	case http.StatusBadGateway:
		code = apitypes.CodeUpstreamError
	}
	message := strings.TrimSpace(string(raw))
	if message == "" {
		message = http.StatusText(status)
	}
	return apitypes.APIError{Code: code, Message: message}
}

// IsCredentialsRequired reports whether err is the 428 trigger.
func IsCredentialsRequired(err error) bool {
	var apiErr apitypes.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == apitypes.CodeCredentialsRequired
	}
	return false
}

// CredentialsRequiredRegistry extracts the registry host from a 428 error.
func CredentialsRequiredRegistry(err error) string {
	var apiErr apitypes.APIError
	if errors.As(err, &apiErr) && apiErr.Code == apitypes.CodeCredentialsRequired {
		return apiErr.Registry
	}
	return ""
}
