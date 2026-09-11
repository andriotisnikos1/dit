package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// EmailProvider is a transactional email service that accepts sends over an
// HTTPS API rather than SMTP. These work from any environment, including hosts
// that block outbound SMTP entirely.
type EmailProvider string

const (
	ProviderCloudflare EmailProvider = "cloudflare"
	ProviderResend     EmailProvider = "resend"
	ProviderPostmark   EmailProvider = "postmark"
	ProviderSendGrid   EmailProvider = "sendgrid"
)

// EmailProviders lists every supported provider name, in the order the CLI
// prints them.
func EmailProviders() []EmailProvider {
	return []EmailProvider{ProviderCloudflare, ProviderResend, ProviderPostmark, ProviderSendGrid}
}

// ProviderNames renders EmailProviders as plain strings.
func ProviderNames() []string {
	out := make([]string, 0, len(EmailProviders()))
	for _, p := range EmailProviders() {
		out = append(out, string(p))
	}
	return out
}

// ErrUnknownProvider reports a provider that dit does not implement.
var ErrUnknownProvider = errors.New("notify: unknown email provider")

// providerSpec is everything that differs between email providers. Keeping it
// as data rather than a type per provider means a new provider is one entry
// here plus a payload function.
type providerSpec struct {
	name EmailProvider
	// url builds the endpoint from the base URL and account id.
	url func(base, accountID string) (string, error)
	// auth renders the credentials into a header.
	auth func(key string) (header, value string)
	// payload renders the provider's request body.
	payload func(req sendRequest) (any, error)
	// accept inspects a 2xx response, since some providers report failures in
	// a 200 body rather than a status code.
	accept func(body []byte) error
	// needsAccountID marks providers whose URL carries a Cloudflare account id.
	needsAccountID bool
	// defaultURL is used when the channel config does not set one.
	defaultURL string
}

// sendRequest is the provider-neutral form of one email. HTML and Text are both
// always populated: the caller renders them once and each provider is handed
// the pair.
type sendRequest struct {
	From     string
	FromName string
	To       []string
	Subject  string
	HTML     string
	Text     string
}

var providers = map[EmailProvider]providerSpec{
	ProviderCloudflare: {
		needsAccountID: true,
		defaultURL:     "https://api.cloudflare.com/client/v4",
		url: func(base, accountID string) (string, error) {
			return strings.TrimRight(base, "/") +
				"/accounts/" + url.PathEscape(accountID) + "/email/sending/send", nil
		},
		auth: func(key string) (string, string) {
			return "Authorization", "Bearer " + key
		},
		payload: func(req sendRequest) (any, error) {
			to, err := addressList(req.To)
			if err != nil {
				return nil, err
			}
			from, err := namedAddress(req.From, req.FromName)
			if err != nil {
				return nil, err
			}
			body := map[string]any{
				"to":      to,
				"from":    from,
				"subject": req.Subject,
			}
			setIfNotEmpty(body, "html", req.HTML)
			setIfNotEmpty(body, "text", req.Text)
			return body, nil
		},
		// Cloudflare answers 200 even when a recipient bounced, and carries a
		// success flag alongside the errors array.
		accept: func(body []byte) error {
			var probe struct {
				Success *bool `json:"success"`
				Errors  []struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"errors"`
			}
			if len(body) == 0 {
				return nil
			}
			if err := json.Unmarshal(body, &probe); err != nil {
				// An unparseable 2xx body is not worth failing a delivery over.
				return nil
			}
			if probe.Success != nil && !*probe.Success {
				if len(probe.Errors) > 0 {
					return fmt.Errorf("cloudflare reported failure: %d %s",
						probe.Errors[0].Code, probe.Errors[0].Message)
				}
				return errors.New("cloudflare reported failure with no error detail")
			}
			return nil
		},
	},
	ProviderResend: {
		defaultURL: "https://api.resend.com",
		url: func(base, _ string) (string, error) {
			return strings.TrimRight(base, "/") + "/emails", nil
		},
		auth: func(key string) (string, string) {
			return "Authorization", "Bearer " + key
		},
		payload: func(req sendRequest) (any, error) {
			to, err := addressList(req.To)
			if err != nil {
				return nil, err
			}
			from, err := namedAddress(req.From, req.FromName)
			if err != nil {
				return nil, err
			}
			body := map[string]any{
				"from":    from,
				"to":      to,
				"subject": req.Subject,
			}
			setIfNotEmpty(body, "html", req.HTML)
			setIfNotEmpty(body, "text", req.Text)
			return body, nil
		},
		accept: func([]byte) error { return nil },
	},
	ProviderPostmark: {
		defaultURL: "https://api.postmarkapp.com",
		url: func(base, _ string) (string, error) {
			return strings.TrimRight(base, "/") + "/email", nil
		},
		// Postmark authenticates with its own header rather than a bearer token.
		auth: func(key string) (string, string) {
			return "X-Postmark-Server-Token", key
		},
		payload: func(req sendRequest) (any, error) {
			from, err := namedAddress(req.From, req.FromName)
			if err != nil {
				return nil, err
			}
			body := map[string]any{
				"From":          from,
				"To":            strings.Join(req.To, ", "),
				"Subject":       req.Subject,
				"MessageStream": "outbound",
			}
			setIfNotEmpty(body, "HtmlBody", req.HTML)
			setIfNotEmpty(body, "TextBody", req.Text)
			return body, nil
		},
		// Postmark returns 200 with a non-zero ErrorCode for some failures.
		accept: func(body []byte) error {
			var probe struct {
				ErrorCode int    `json:"ErrorCode"`
				Message   string `json:"Message"`
			}
			if len(body) == 0 {
				return nil
			}
			if err := json.Unmarshal(body, &probe); err != nil {
				return nil
			}
			if probe.ErrorCode != 0 {
				return fmt.Errorf("postmark reported failure: %d %s", probe.ErrorCode, probe.Message)
			}
			return nil
		},
	},
	ProviderSendGrid: {
		defaultURL: "https://api.sendgrid.com",
		url: func(base, _ string) (string, error) {
			return strings.TrimRight(base, "/") + "/v3/mail/send", nil
		},
		auth: func(key string) (string, string) {
			return "Authorization", "Bearer " + key
		},
		payload: func(req sendRequest) (any, error) {
			to, err := objectAddressList(req.To)
			if err != nil {
				return nil, err
			}
			name, addr := splitAddress(req.From)
			if req.FromName != "" {
				name = req.FromName
			}
			from := map[string]any{"email": addr}
			if name != "" {
				from["name"] = name
			}
			content := make([]map[string]any, 0, 2)
			if req.Text != "" {
				content = append(content, map[string]any{"type": "text/plain", "value": req.Text})
			}
			if req.HTML != "" {
				content = append(content, map[string]any{"type": "text/html", "value": req.HTML})
			}
			return map[string]any{
				"personalizations": []map[string]any{{"to": to}},
				"from":             from,
				"subject":          req.Subject,
				"content":          content,
			}, nil
		},
		accept: func([]byte) error { return nil },
	},
}

// EmailHTTPChannel sends notifications through a transactional email provider's
// HTTPS API.
//
// This is the transport to use where outbound SMTP is unavailable — Railway
// blocks it below its Pro plan, for one — since an HTTPS POST is ordinary
// outbound web traffic.
type EmailHTTPChannel struct {
	Provider  EmailProvider
	APIKey    string
	AccountID string
	From      string
	FromName  string
	To        []string
	URL       string

	// Client is the HTTP client to use. Defaults to a timeout-bounded client.
	Client *http.Client
	// Timeout bounds a single send. Defaults to 30s.
	Timeout time.Duration
}

var _ Channel = (*EmailHTTPChannel)(nil)

// Type implements Channel.
func (c *EmailHTTPChannel) Type() string { return string(apitypes.ChannelEmailHTTP) }

// NewEmailHTTP builds an EmailHTTPChannel from a stored config map.
func NewEmailHTTP(cfg map[string]string) (*EmailHTTPChannel, error) {
	kind := string(apitypes.ChannelEmailHTTP)

	c := &EmailHTTPChannel{
		Provider:  EmailProvider(strings.ToLower(strings.TrimSpace(cfg[apitypes.ConfigProvider]))),
		APIKey:    strings.TrimSpace(cfg[apitypes.ConfigAPIKey]),
		AccountID: strings.TrimSpace(cfg[apitypes.ConfigAccountID]),
		From:      strings.TrimSpace(cfg[apitypes.ConfigFrom]),
		FromName:  strings.TrimSpace(cfg[apitypes.ConfigFromName]),
		To:        SplitList(cfg[apitypes.ConfigTo]),
		URL:       strings.TrimSpace(cfg[apitypes.ConfigURL]),
	}

	if c.Provider == "" {
		return nil, ErrConfig(kind, apitypes.ConfigProvider)
	}
	if _, ok := providers[c.Provider]; !ok {
		return nil, fmt.Errorf("%w: %q (supported: %s)",
			ErrUnknownProvider, c.Provider, strings.Join(ProviderNames(), ", "))
	}
	if c.APIKey == "" {
		return nil, ErrConfig(kind, apitypes.ConfigAPIKey)
	}
	if c.From == "" {
		return nil, ErrConfig(kind, apitypes.ConfigFrom)
	}
	if len(c.To) == 0 {
		return nil, ErrConfig(kind, apitypes.ConfigTo)
	}
	if _, bare := splitAddress(c.From); bare == "" {
		return nil, fmt.Errorf("notify: invalid from address %q", c.From)
	}
	for _, to := range c.To {
		if extractAddress(to) == "" {
			return nil, fmt.Errorf("notify: invalid to address %q", to)
		}
	}

	spec := providers[c.Provider]
	if spec.needsAccountID && c.AccountID == "" {
		return nil, ErrConfig(kind, apitypes.ConfigAccountID)
	}
	if c.URL == "" {
		c.URL = spec.defaultURL
	}
	if _, err := url.Parse(c.URL); err != nil {
		return nil, fmt.Errorf("notify: invalid url %q: %w", c.URL, err)
	}

	c.Timeout = 30 * time.Second
	return c, nil
}

// endpoint resolves the provider-specific URL for a send.
func (c *EmailHTTPChannel) endpoint() (string, error) {
	spec := providers[c.Provider]
	return spec.url(c.URL, c.AccountID)
}

// Send implements Channel.
func (c *EmailHTTPChannel) Send(ctx context.Context, msg Message) error {
	spec := providers[c.Provider]

	subject := strings.TrimSpace(msg.Title)
	if subject == "" {
		subject = "dit notification"
	}

	text := msg.Body
	if msg.URL != "" && !strings.Contains(text, msg.URL) {
		text = strings.TrimRight(text, "\n") + "\n\n" + msg.URL + "\n"
	}

	payload, err := spec.payload(sendRequest{
		From:     c.From,
		FromName: c.FromName,
		To:       c.To,
		Subject:  subject,
		HTML:     renderHTML(msg),
		Text:     text,
	})
	if err != nil {
		return fmt.Errorf("notify: build %s payload: %w", c.Provider, err)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notify: encode %s payload: %w", c.Provider, err)
	}

	endpoint, err := c.endpoint()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("notify: build %s request: %w", c.Provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	header, value := spec.auth(c.APIKey)
	req.Header.Set(header, value)

	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: c.timeout()}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: send via %s: %w", c.Provider, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = "no response body"
		}
		return fmt.Errorf("notify: %s returned %s: %s", c.Provider, resp.Status, truncate(detail, 512))
	}
	if err := spec.accept(body); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return nil
}

func (c *EmailHTTPChannel) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 30 * time.Second
	}
	return c.Timeout
}

// ---------- payload helpers ----------

// setIfNotEmpty keeps empty parts out of the request body, so a provider does
// not have to guess whether "" means "absent".
func setIfNotEmpty(body map[string]any, key, value string) {
	if value != "" {
		body[key] = value
	}
}

// addressList renders recipients as plain strings.
func addressList(to []string) ([]string, error) {
	out := make([]string, 0, len(to))
	for _, addr := range to {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		out = append(out, addr)
	}
	if len(out) == 0 {
		return nil, errors.New("no recipients")
	}
	return out, nil
}

// objectAddressList renders recipients as {email, name} objects, the form
// SendGrid's personalizations require.
func objectAddressList(to []string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(to))
	for _, raw := range to {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		name, addr := splitAddress(raw)
		entry := map[string]any{"email": addr}
		if name != "" {
			entry["name"] = name
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil, errors.New("no recipients")
	}
	return out, nil
}

// namedAddress renders `Name <a@b>` when a display name is known, and the bare
// address otherwise.
func namedAddress(addr, name string) (string, error) {
	_, bare := splitAddress(addr)
	if bare == "" {
		return "", fmt.Errorf("invalid address %q", addr)
	}
	if name = strings.TrimSpace(name); name != "" {
		return (&mail.Address{Name: name, Address: bare}).String(), nil
	}
	return addr, nil
}

// splitAddress separates an optional display name from the address itself.
func splitAddress(raw string) (name, addr string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if parsed, err := mail.ParseAddress(raw); err == nil {
		return parsed.Name, parsed.Address
	}
	// Fall back to the looser parse the SMTP channel uses, so a display name
	// that net/mail rejects does not disable an otherwise valid channel.
	return "", extractAddress(raw)
}

// truncate shortens a string for an error message.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
