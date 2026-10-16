package flags

import (
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func TestResolveDefaults(t *testing.T) {
	s, err := Resolve(nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s != DefaultSettings() {
		t.Errorf("Resolve(nil, nil) = %+v, want %+v", s, DefaultSettings())
	}
}

func TestResolvePrecedence(t *testing.T) {
	s, err := Resolve(
		map[string]any{"history": float64(5)},                    // local wins
		map[string]any{"history": "10", "notify_threshold": "3"}, // global beats default
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.History != 5 {
		t.Errorf("History = %d, want 5 (local beats global)", s.History)
	}
	if s.NotifyThreshold != 3 {
		t.Errorf("NotifyThreshold = %d, want 3 (global beats default)", s.NotifyThreshold)
	}
	if s.Semver {
		t.Error("Semver = true, want the default false")
	}
}

func TestParseValueRejectsBadInput(t *testing.T) {
	history, _ := Lookup("history")
	if _, err := ParseValue(history, 0); err == nil {
		t.Error("history accepts 0, want rejection (min is -1)")
	}
	if _, err := ParseValue(history, -2); err == nil {
		t.Error("history accepts -2, want rejection (min is -1)")
	}
	if _, err := ParseValue(history, "abc"); err == nil {
		t.Error("history accepts a non-integer string")
	}
	if _, err := ParseValue(history, 1.5); err == nil {
		t.Error("history accepts a non-integral number")
	}
	threshold, _ := Lookup("notify_threshold")
	if _, err := ParseValue(threshold, 0); err == nil {
		t.Error("notify_threshold accepts 0, want rejection (min is 1)")
	}
	semver, _ := Lookup("semver")
	if _, err := ParseValue(semver, "maybe"); err == nil {
		t.Error("semver accepts a non-boolean string")
	}
	if _, err := ParseValue(semver, "1"); err != nil {
		t.Errorf("semver rejects '1', want ParseBool semantics: %v", err)
	}
}

func TestValidForKind(t *testing.T) {
	semver, _ := Lookup("semver")
	if ValidForKind(semver, apitypes.WatchKindTag) {
		t.Error("semver applies to tag watches, want pattern only")
	}
	if !ValidForKind(semver, apitypes.WatchKindPattern) {
		t.Error("semver rejected for pattern watches")
	}
	history, _ := Lookup("history")
	if !ValidForKind(history, apitypes.WatchKindTag) {
		t.Error("history rejected for tag watches")
	}
}

func TestResolveRejectsUnknownNames(t *testing.T) {
	if _, err := Resolve(map[string]any{"nope": 1}, nil); err == nil {
		t.Error("unknown watch flag accepted")
	}
	if _, err := Resolve(nil, map[string]any{"nope": 1}); err == nil {
		t.Error("unknown global flag accepted")
	}
}
