package cli

// Adversarial probe for the `dit flag` command group and the `watch show`
// Flags row: argument routing, typed value parsing before the network, output
// modes and the source labelling of effective flags.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestAdversarialFlagListScopesAndColumns pins the table shape for both scopes.
func TestAdversarialFlagListScopesAndColumns(t *testing.T) {
	h := newCLIHarness(t)
	value := any(10)
	h.api.listFlagsFn = func(_ context.Context, watchID string) (apitypes.FlagList, error) {
		if watchID != "" {
			t.Errorf("global listing passed watch scope %q", watchID)
		}
		return apitypes.FlagList{
			{Name: "history", Type: "int", Default: 1, Global: &value},
			{Name: "semver", Type: "bool", Default: false, PatternOnly: true},
		}, nil
	}

	out := h.mustRun("flag", "list")
	header := strings.SplitN(out, "\n", 2)[0]
	for _, want := range []string{"NAME", "TYPE", "DEFAULT", "GLOBAL"} {
		if !strings.Contains(header, want) {
			t.Errorf("global header %q is missing %q", header, want)
		}
	}
	if strings.Contains(header, "LOCAL") || strings.Contains(header, "EFFECTIVE") {
		t.Errorf("global header %q must not carry watch columns", header)
	}
	if !strings.Contains(out, "10") {
		t.Errorf("global listing does not show the set value:\n%s", out)
	}
}

// TestAdversarialFlagListWatchScopeRouting pins --watch routing and columns.
func TestAdversarialFlagListWatchScopeRouting(t *testing.T) {
	h := newCLIHarness(t)
	var gotWatch string
	five := any(5)
	h.api.listFlagsFn = func(_ context.Context, watchID string) (apitypes.FlagList, error) {
		gotWatch = watchID
		return apitypes.FlagList{
			{Name: "history", Type: "int", Default: 1, Global: &five, Local: &five, Effective: &five},
			{Name: "notify_threshold", Type: "int", Default: 1},
		}, nil
	}

	out := h.mustRun("flag", "list", "--watch", "w_abc")
	if gotWatch != "w_abc" {
		t.Errorf("ListFlags called with watch %q, want w_abc", gotWatch)
	}
	header := strings.SplitN(out, "\n", 2)[0]
	for _, want := range []string{"NAME", "TYPE", "DEFAULT", "LOCAL", "EFFECTIVE"} {
		if !strings.Contains(header, want) {
			t.Errorf("watch header %q is missing %q", header, want)
		}
	}
	if strings.Contains(header, "GLOBAL") {
		t.Errorf("watch header %q must not carry the GLOBAL column", header)
	}
	// An unset override renders as a dash, not as the default.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "notify_threshold") {
			if strings.Count(line, "-") < 2 {
				t.Errorf("unset local/effective row = %q, want dashes", line)
			}
		}
	}
}

// TestAdversarialFlagSetTypedValues is the typed-parsing matrix (including the
// documented -1) plus the no-network-on-rejection rule.
func TestAdversarialFlagSetTypedValues(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    any
		wantErr bool
	}{
		{"int history 5", []string{"flag", "set", "history", "5"}, 5, false},
		{"int history -1 keeps all", []string{"flag", "set", "history", "-1"}, -1, false},
		{"int threshold 1", []string{"flag", "set", "notify_threshold", "1"}, 1, false},
		{"int threshold -1", []string{"flag", "set", "notify_threshold", "-1"}, nil, true},
		{"int threshold 0", []string{"flag", "set", "notify_threshold", "0"}, nil, true},
		{"int history 0", []string{"flag", "set", "history", "0"}, nil, true},
		{"int junk", []string{"flag", "set", "history", "abc"}, nil, true},
		{"int float", []string{"flag", "set", "history", "1.5"}, nil, true},
		{"bool true", []string{"flag", "set", "semver", "true"}, true, false},
		{"bool 1", []string{"flag", "set", "semver", "1"}, true, false},
		{"bool false", []string{"flag", "set", "semver", "false"}, false, false},
		{"bool 0", []string{"flag", "set", "semver", "0"}, false, false},
		{"bool junk", []string{"flag", "set", "semver", "yes-please"}, nil, true},
		{"bool -1", []string{"flag", "set", "semver", "-1"}, nil, true},
		{"unknown flag", []string{"flag", "set", "nope", "1"}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newCLIHarness(t)
			var gotName string
			var gotValue any
			h.api.setFlagFn = func(_ context.Context, _, name string, value any) (apitypes.Flag, error) {
				gotName, gotValue = name, value
				return apitypes.Flag{Name: name}, nil
			}
			err := h.run(c.args...)
			if c.wantErr {
				if err == nil {
					t.Fatalf("%v accepted, want an error", c.args)
				}
				// A rejected value must never reach the network.
				for _, call := range h.api.Calls {
					if call == "setFlag" {
						t.Errorf("%v reached the API before validation", c.args)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %v", c.args, err)
			}
			if gotName == "" {
				t.Fatalf("%v did not call SetFlag", c.args)
			}
			if gotValue != c.want {
				t.Errorf("SetFlag value = %#v, want %#v", gotValue, c.want)
			}
		})
	}
}

// TestAdversarialFlagSetWatchRouting pins --watch on set and unset.
func TestAdversarialFlagSetWatchRouting(t *testing.T) {
	h := newCLIHarness(t)
	var gotWatch, gotName string
	var gotValue any
	h.api.setFlagFn = func(_ context.Context, watchID, name string, value any) (apitypes.Flag, error) {
		gotWatch, gotName, gotValue = watchID, name, value
		return apitypes.Flag{Name: name}, nil
	}
	if out := h.mustRun("flag", "set", "history", "3", "--watch", "w_abc"); !strings.Contains(out, "history") {
		t.Errorf("set output = %q, want a confirmation", out)
	}
	if gotWatch != "w_abc" || gotName != "history" || gotValue != 3 {
		t.Errorf("SetFlag(%q, %q, %#v), want (w_abc, history, 3)", gotWatch, gotName, gotValue)
	}

	// Global set leaves the watch scope empty.
	gotWatch = "sentinel"
	h.mustRun("flag", "set", "history", "3")
	if gotWatch != "" {
		t.Errorf("global set used watch scope %q, want empty", gotWatch)
	}

	var unsetWatch, unsetName string
	h.api.unsetFlagFn = func(_ context.Context, watchID, name string) error {
		unsetWatch, unsetName = watchID, name
		return nil
	}
	h.mustRun("flag", "unset", "notify_threshold", "--watch", "w_xyz")
	if unsetWatch != "w_xyz" || unsetName != "notify_threshold" {
		t.Errorf("UnsetFlag(%q, %q), want (w_xyz, notify_threshold)", unsetWatch, unsetName)
	}
	h.mustRun("flag", "unset", "notify_threshold")
	if unsetWatch != "" || unsetName != "notify_threshold" {
		t.Errorf("global UnsetFlag(%q, %q), want (\"\", notify_threshold)", unsetWatch, unsetName)
	}

	// An unknown flag is rejected locally.
	if err := h.run("flag", "unset", "nope"); err == nil {
		t.Error("unset of an unknown flag was accepted")
	}
}

// TestAdversarialFlagSetArgumentForms pins the argument shapes of `flag set`
// around the fix for negative values: options before the positionals, options
// after them, the explicit -- separator, and the rejected forms.
func TestAdversarialFlagSetArgumentForms(t *testing.T) {
	type call struct {
		watch string
		name  string
		value any
	}
	cases := []struct {
		args  []string
		want  call
		fails bool
	}{
		{[]string{"flag", "set", "history", "5"}, call{"", "history", 5}, false},
		{[]string{"flag", "set", "history", "-1"}, call{"", "history", -1}, false},
		{[]string{"flag", "set", "history", "--", "-1"}, call{"", "history", -1}, false},
		{[]string{"flag", "set", "history", "5", "--watch", "w_x"}, call{"w_x", "history", 5}, false},
		{[]string{"flag", "set", "history", "-1", "--watch", "w_x"}, call{"w_x", "history", -1}, false},
		{[]string{"flag", "set", "--watch", "w_x", "history", "5"}, call{"w_x", "history", 5}, false},
		{[]string{"flag", "set", "--watch=w_x", "history", "5"}, call{"w_x", "history", 5}, false},
		{[]string{"flag", "set", "history", "5", "extra"}, call{}, true},
		{[]string{"flag", "set", "history", "5", "--bogus"}, call{}, true},
		{[]string{"flag", "set", "history"}, call{}, true},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			h := newCLIHarness(t)
			var got call
			h.api.setFlagFn = func(_ context.Context, watchID, name string, value any) (apitypes.Flag, error) {
				got = call{watchID, name, value}
				return apitypes.Flag{Name: name}, nil
			}
			err := h.run(c.args...)
			if c.fails {
				if err == nil {
					t.Fatalf("%v accepted, want an error", c.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %v", c.args, err)
			}
			if got != c.want {
				t.Errorf("SetFlag called with %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestAdversarialFlagOutputModes checks the JSON and YAML renderings of the
// flag listing and a set.
func TestAdversarialFlagOutputModes(t *testing.T) {
	h := newCLIHarness(t)
	ten := any(10)
	h.api.listFlagsFn = func(_ context.Context, _ string) (apitypes.FlagList, error) {
		return apitypes.FlagList{{Name: "history", Type: "int", Default: 1, Global: &ten}}, nil
	}

	jsonOut := h.mustRun("flag", "list", "-o", "json")
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("flag list JSON is not valid JSON: %v\n%s", err, jsonOut)
	}
	if len(decoded) != 1 || decoded[0]["name"] != "history" {
		t.Errorf("flag list JSON = %v, want one history flag", decoded)
	}

	yamlOut := h.mustRun("flag", "list", "-o", "yaml")
	var yamlDecoded []map[string]any
	if err := yaml.Unmarshal([]byte(yamlOut), &yamlDecoded); err != nil {
		t.Fatalf("flag list YAML is not valid YAML: %v\n%s", err, yamlOut)
	}
	if len(yamlDecoded) != 1 || yamlDecoded[0]["name"] != "history" {
		t.Errorf("flag list YAML = %v, want one history flag", yamlDecoded)
	}

	h.api.setFlagFn = func(_ context.Context, _, name string, value any) (apitypes.Flag, error) {
		return apitypes.Flag{Name: name, Global: &value}, nil
	}
	jsonSet := h.mustRun("flag", "set", "history", "6", "-o", "json")
	var setDecoded map[string]any
	if err := json.Unmarshal([]byte(jsonSet), &setDecoded); err != nil {
		t.Fatalf("flag set JSON is not valid JSON: %v\n%s", err, jsonSet)
	}
	if setDecoded["name"] != "history" {
		t.Errorf("flag set JSON = %v, want the history flag", setDecoded)
	}
}

// TestAdversarialWatchShowFlagsRow is the `watch show` rendering contract: the
// source of each non-default value, the all-defaults case and the multi-flag
// case.
func TestAdversarialWatchShowFlagsRow(t *testing.T) {
	cases := []struct {
		name  string
		flags map[string]any
		local map[string]any
		want  string
	}{
		{
			name:  "all defaults",
			flags: map[string]any{"history": 1, "notify_threshold": 1},
			want:  "(defaults)",
		},
		{
			name:  "global source",
			flags: map[string]any{"history": 5, "notify_threshold": 1},
			want:  "history=5 (global)",
		},
		{
			name:  "local source",
			flags: map[string]any{"history": 5, "notify_threshold": 1},
			local: map[string]any{"history": 5},
			want:  "history=5 (local)",
		},
		{
			name:  "local equal to the default is still local",
			flags: map[string]any{"history": 1, "notify_threshold": 1},
			local: map[string]any{"history": 1},
			want:  "history=1 (local)",
		},
		{
			name:  "multiple flags",
			flags: map[string]any{"history": 5, "notify_threshold": 3},
			local: map[string]any{"notify_threshold": 3},
			want:  "history=5 (global), notify_threshold=3 (local)",
		},
		{
			name: "pattern watch semver",
			flags: map[string]any{
				"history": 1, "notify_threshold": 1,
				"semver": true, "semver_stable": false, "semver_notify_threshold": 2,
			},
			want: "semver=true (global), semver_notify_threshold=2 (global)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newCLIHarness(t)
			h.api.getWatchFn = func(_ context.Context, id string) (apitypes.Watch, error) {
				return apitypes.Watch{
					ID: id, Image: "ghcr.io/owner/app:v1", Registry: "ghcr.io",
					Repository: "owner/app", Kind: apitypes.WatchKindTag, Ref: "v1",
					Enabled: true, Flags: c.flags, FlagsLocal: c.local,
				}, nil
			}
			out := h.mustRun("watch", "show", "w_abc")
			row := ""
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "Flags:") {
					row = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Flags:"))
				}
			}
			if row != c.want {
				t.Errorf("Flags row = %q, want %q\nfull output:\n%s", row, c.want, out)
			}
		})
	}
}
