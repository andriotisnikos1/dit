package notify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// DefaultNtfyURL is the public ntfy server.
const DefaultNtfyURL = "https://ntfy.sh"

// NtfyChannel publishes notifications to an ntfy topic.
//
// The message body is the plain-text notification body; Title, Priority, Tags
// and Click are carried in the headers ntfy defines.
type NtfyChannel struct {
	URL      string
	Topic    string
	Token    string
	Priority string
	Tags     []string

	// Client is the HTTP client to use. Defaults to a 30s-timeout client.
	Client *http.Client
	// Timeout bounds a single publish. Defaults to 30s.
	Timeout time.Duration
}

var _ Channel = (*NtfyChannel)(nil)

// Type implements Channel.
func (c *NtfyChannel) Type() string { return string(apitypes.ChannelNtfy) }

// NewNtfy builds an NtfyChannel from a stored config map.
func NewNtfy(cfg map[string]string) (*NtfyChannel, error) {
	c := &NtfyChannel{
		URL:      strings.TrimSpace(cfg[apitypes.ConfigURL]),
		Topic:    strings.TrimSpace(cfg[apitypes.ConfigTopic]),
		Token:    strings.TrimSpace(cfg[apitypes.ConfigToken]),
		Priority: NormalisePriority(cfg[apitypes.ConfigPriority]),
		Tags:     SplitList(cfg[apitypes.ConfigTags]),
	}
	if c.URL == "" {
		c.URL = DefaultNtfyURL
	}
	if c.Topic == "" {
		return nil, ErrConfig(string(apitypes.ChannelNtfy), apitypes.ConfigTopic)
	}
	if _, err := url.Parse(c.URL); err != nil {
		return nil, fmt.Errorf("notify: invalid ntfy url %q: %w", c.URL, err)
	}
	c.Timeout = 30 * time.Second
	return c, nil
}

// endpoint is the publish URL: {url}/{topic}.
func (c *NtfyChannel) endpoint() string {
	return strings.TrimRight(c.URL, "/") + "/" + url.PathEscape(c.Topic)
}

// Send implements Channel.
func (c *NtfyChannel) Send(ctx context.Context, msg Message) error {
	body := strings.TrimSpace(msg.Body)
	if msg.URL != "" && !strings.Contains(body, msg.URL) {
		body = strings.TrimRight(body, "\n") + "\n" + msg.URL
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader([]byte(body)))
	if err != nil {
		return fmt.Errorf("notify: build ntfy request: %w", err)
	}

	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if title := strings.TrimSpace(msg.Title); title != "" {
		req.Header.Set("Title", encodeHeaderValue(title))
	}
	if priority := NormalisePriority(firstNonEmpty(msg.Priority, c.Priority)); priority != "" {
		req.Header.Set("Priority", priority)
	}
	if tags := firstNonEmptyList(msg.Tags, c.Tags); len(tags) > 0 {
		req.Header.Set("Tags", strings.Join(tags, ","))
	}
	if msg.URL != "" {
		req.Header.Set("Click", msg.URL)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: c.timeout()}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: publish to ntfy %s: %w", c.endpoint(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("notify: ntfy %s returned %s: %s",
			c.endpoint(), resp.Status, strings.TrimSpace(string(detail)))
	}
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (c *NtfyChannel) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 30 * time.Second
	}
	return c.Timeout
}

// encodeHeaderValue replaces newlines, which are illegal in HTTP header values.
func encodeHeaderValue(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstNonEmptyList(values ...[]string) []string {
	for _, v := range values {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

// Build constructs a Channel from a stored channel type and config. This is
// the only place that knows the mapping from config keys to transports.
func Build(channelType string, cfg map[string]string) (Channel, error) {
	switch apitypes.ChannelType(channelType) {
	case apitypes.ChannelEmail:
		return NewEmail(cfg)
	case apitypes.ChannelNtfy:
		return NewNtfy(cfg)
	default:
		return nil, fmt.Errorf("notify: unsupported channel type %q", channelType)
	}
}
