package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func TestChannelAddEmailHTTPBuildsConfig(t *testing.T) {
	h := newCLIHarness(t)
	var got apitypes.CreateChannelRequest
	h.api.createChannelFn = func(_ context.Context, req apitypes.CreateChannelRequest) (apitypes.Channel, error) {
		got = req
		return apitypes.Channel{ID: "c_1", Name: req.Name, Type: req.Type, Enabled: true}, nil
	}

	h.mustRun("channel", "add", "email-http",
		"--name", "cf-mail",
		"--provider", "cloudflare",
		"--account-id", "acct123",
		"--from", "dit@services.andriotis.dev",
		"--from-name", "dit",
		"--to", "nikos@andriotis.dev",
	)

	if got.Type != apitypes.ChannelEmailHTTP {
		t.Errorf("type = %q, want email-http", got.Type)
	}
	if got.Config[apitypes.ConfigProvider] != "cloudflare" {
		t.Errorf("provider = %q", got.Config[apitypes.ConfigProvider])
	}
	if got.Config[apitypes.ConfigAccountID] != "acct123" {
		t.Errorf("account_id = %q", got.Config[apitypes.ConfigAccountID])
	}
	if got.Config[apitypes.ConfigFrom] != "dit@services.andriotis.dev" {
		t.Errorf("from = %q", got.Config[apitypes.ConfigFrom])
	}
	if got.Config[apitypes.ConfigFromName] != "dit" {
		t.Errorf("from_name = %q", got.Config[apitypes.ConfigFromName])
	}
	if got.Config[apitypes.ConfigTo] != "nikos@andriotis.dev" {
		t.Errorf("to = %q", got.Config[apitypes.ConfigTo])
	}
}

func TestChannelAddEmailHTTPRequiresCoreFlags(t *testing.T) {
	cases := [][]string{
		{"channel", "add", "email-http", "--provider", "resend", "--from", "a@b.c", "--to", "d@e.f"}, // no name
		{"channel", "add", "email-http", "--name", "n", "--from", "a@b.c", "--to", "d@e.f"},          // no provider
		{"channel", "add", "email-http", "--name", "n", "--provider", "resend", "--to", "d@e.f"},     // no from
		{"channel", "add", "email-http", "--name", "n", "--provider", "resend", "--from", "a@b.c"},   // no to
	}
	for _, args := range cases {
		h := newCLIHarness(t)
		if err := h.run(args...); err == nil {
			t.Errorf("%v: expected a validation error", args)
		}
	}
}

func TestChannelAddEmailHTTPRejectsEmptyAPIKey(t *testing.T) {
	// The server validates the config, so an omitted key must fail loudly
	// there rather than silently creating an unusable channel.
	h := newCLIHarness(t)
	h.api.createChannelFn = func(_ context.Context, req apitypes.CreateChannelRequest) (apitypes.Channel, error) {
		if _, ok := req.Config[apitypes.ConfigAPIKey]; ok {
			t.Errorf("an empty API key should not be sent as a config value")
		}
		return apitypes.Channel{ID: "c_1", Type: req.Type}, nil
	}
	h.mustRun("channel", "add", "email-http", "--name", "n",
		"--provider", "resend", "--from", "a@b.c", "--to", "d@e.f")
}

func TestChannelListRendersEmailHTTPDetail(t *testing.T) {
	h := newCLIHarness(t)
	h.api.listChannelsFn = func(context.Context) (apitypes.List[apitypes.Channel], error) {
		return apitypes.List[apitypes.Channel]{
			Items: []apitypes.Channel{
				{
					ID: "c_2", Name: "cf-mail", Type: apitypes.ChannelEmailHTTP,
					Enabled: true, HasSecret: true,
					Config: map[string]string{
						apitypes.ConfigProvider: "cloudflare",
						apitypes.ConfigTo:       "nikos@andriotis.dev",
					},
				},
			},
			Total: 1,
		}, nil
	}

	out := h.mustRun("channel", "list")
	for _, want := range []string{"c_2", "cf-mail", "email-http", "cloudflare"} {
		if !strings.Contains(out, want) {
			t.Errorf("channel list is missing %q:\n%s", want, out)
		}
	}
}
