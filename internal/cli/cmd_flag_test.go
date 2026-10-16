package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func TestFlagListTable(t *testing.T) {
	h := newCLIHarness(t)
	ten := any(10)
	h.api.listFlagsFn = func(context.Context, string) (apitypes.FlagList, error) {
		return apitypes.FlagList{
			{Name: "history", Type: "int", Default: 1, Global: &ten},
			{Name: "semver", Type: "bool", Default: false, PatternOnly: true},
		}, nil
	}
	out := h.mustRun("flag", "list")
	for _, want := range []string{"history", "semver", "10", "int", "bool"} {
		if !strings.Contains(out, want) {
			t.Errorf("flag list output missing %q:\n%s", want, out)
		}
	}
}

func TestFlagSetSendsTypedValue(t *testing.T) {
	h := newCLIHarness(t)
	var gotValue any
	var gotWatch, gotName string
	h.api.setFlagFn = func(_ context.Context, watchID, name string, value any) (apitypes.Flag, error) {
		gotWatch, gotName, gotValue = watchID, name, value
		return apitypes.Flag{Name: name, Global: &value}, nil
	}

	if out := h.mustRun("flag", "set", "history", "5"); !strings.Contains(out, "history") {
		t.Errorf("set output = %q, want it to confirm the flag", out)
	}
	if gotName != "history" || gotValue != 5 {
		t.Errorf("set called with (%q, %v), want (history, 5)", gotName, gotValue)
	}

	// Local scope routing.
	h.mustRun("flag", "set", "semver", "true", "--watch", "w_abc")
	if gotWatch != "w_abc" || gotValue != true {
		t.Errorf("set called with (%q, %v), want (w_abc, true)", gotWatch, gotValue)
	}
}

func TestFlagSetRejectsBadInputBeforeNetwork(t *testing.T) {
	h := newCLIHarness(t)
	if err := h.run("flag", "set", "history", "abc"); err == nil {
		t.Error("history=abc accepted, want an error before the network")
	}
	if err := h.run("flag", "set", "nope", "1"); err == nil {
		t.Error("unknown flag accepted, want an error")
	}
	if len(h.api.Calls) != 0 {
		t.Errorf("bad input reached the API: %v", h.api.Calls)
	}
}

func TestFlagUnset(t *testing.T) {
	h := newCLIHarness(t)
	h.api.unsetFlagFn = func(context.Context, string, string) error { return nil }
	out := h.mustRun("flag", "unset", "history", "--watch", "w_abc")
	if !strings.Contains(out, "history") {
		t.Errorf("unset output = %q, want it to confirm", out)
	}
}
