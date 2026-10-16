package flags

import (
	"math"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// ---------------------------------------------------------------------------
// Adversarial probes for the flags package.
//
// Every test here was written from a falsifiable claim about a boundary path:
// a type arm, an out-of-range number, a nil override, a definition that drifts
// from DefaultSettings. They are the evidence for the audit report — passwords
// of "checked and clear" or "broken" must be backed by one of these.
// ---------------------------------------------------------------------------

// claim: a JSON number outside the int64 range is rejected exactly like the
// equivalent out-of-range *string* (strconv.Atoi reports ErrRange for
// "99999999999999999999"). If the float64 path instead converts, the value
// silently saturates to a platform-dependent integer (MaxInt64 here).
func TestParseValueRejectsOutOfRangeFloat64(t *testing.T) {
	history, ok := Lookup("history")
	if !ok {
		t.Fatal("history is not defined")
	}
	// Control: the string path rejects an out-of-range number.
	if _, err := ParseValue(history, "99999999999999999999"); err == nil {
		t.Error("ParseValue(history, \"99999999999999999999\") accepted a value strconv.Atoi cannot represent")
	}

	for _, tc := range []struct {
		name string
		raw  any
	}{
		{"1e30", 1e30},
		{"-1e30", -1e30},
		{"2^63", 9.223372036854775808e18},
		{"infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	} {
		got, err := ParseValue(history, tc.raw)
		if err == nil {
			t.Errorf("ParseValue(history, %s) = %v, nil; want a rejection — the value "+
				"does not fit in an int and must not be silently saturated", tc.name, got)
			continue
		}
		if !strings.Contains(err.Error(), "history") {
			t.Errorf("ParseValue(history, %s) error %q does not name the flag", tc.name, err)
		}
	}

	// The largest exactly-representable float64 below 2^63 must still be
	// accepted (it is a legal int64).
	if got, err := ParseValue(history, 9.223372036854775e18); err != nil {
		t.Errorf("ParseValue(history, 2^63-1024) = (%v, %v), want the legal max int64", got, err)
	}
}

// claim: Definitions() and DefaultSettings() cannot drift — every definition's
// Default is the value the corresponding Settings field carries, and it
// survives a FormatValue/ParseValue round trip (the settings table stores the
// formatted string, so a Default that cannot round-trip is unusable).
func TestDefinitionsAgreeWithDefaultSettings(t *testing.T) {
	defaults := DefaultSettings()
	byName := map[string]any{
		"history":                 defaults.History,
		"notify_threshold":        defaults.NotifyThreshold,
		"semver":                  defaults.Semver,
		"semver_stable":           defaults.SemverStable,
		"semver_notify_threshold": defaults.SemverNotifyThreshold,
	}
	seen := map[string]bool{}
	for _, def := range Definitions() {
		if seen[def.Name] {
			t.Errorf("flag %q is defined twice", def.Name)
		}
		seen[def.Name] = true

		want, ok := byName[def.Name]
		if !ok {
			t.Errorf("flag %q has no field in Settings/DefaultSettings — DefaultSettings() has drifted", def.Name)
			continue
		}
		formatted, err := FormatValue(def, def.Default)
		if err != nil {
			t.Errorf("FormatValue(%s, Default) = %v; a default that cannot be stored is unusable", def.Name, err)
			continue
		}
		parsed, err := ParseValue(def, formatted)
		if err != nil {
			t.Errorf("ParseValue(%s, %q) = %v; the stored default does not parse back", def.Name, formatted, err)
			continue
		}
		if parsed != def.Default {
			t.Errorf("%s: Default %v round-trips to %v", def.Name, def.Default, parsed)
		}
		if def.Default != want {
			t.Errorf("%s: Definitions() Default = %v, DefaultSettings() = %v — the two sources disagree",
				def.Name, def.Default, want)
		}
		if def.Min != nil && def.Default.(int) < *def.Min {
			t.Errorf("%s: Default %v is below its own Min %d", def.Name, def.Default, *def.Min)
		}
	}
	for name := range byName {
		if !seen[name] {
			t.Errorf("Settings carries field %q with no definition", name)
		}
	}
	if got, err := Resolve(nil, nil); err != nil || got != defaults {
		t.Errorf("Resolve(nil, nil) = (%+v, %v), want DefaultSettings() %+v", got, err, defaults)
	}
}

// claim: every raw-type arm of the int and bool parsers is either accepted with
// the documented value or rejected with an error naming the flag — in
// particular nil, an unknown type, a bool for an int flag and a non-bool for a
// bool flag must all be errors, never a panic or a silent zero.
func TestParseValueTypeArms(t *testing.T) {
	history, _ := Lookup("history")
	threshold, _ := Lookup("notify_threshold")
	semver, _ := Lookup("semver")
	semverThreshold, _ := Lookup("semver_notify_threshold")

	ok := []struct {
		def  Definition
		raw  any
		want any
	}{
		{history, 7, 7},
		{history, int64(7), 7},
		{history, float64(-1), -1},
		{history, "12", 12},
		{history, -1, -1},
		{threshold, "1", 1},
		{semverThreshold, 1, 1},
		{semver, true, true},
		{semver, false, false},
		{semver, "true", true},
		{semver, "0", false},
		{semver, "TRUE", true},
	}
	for _, tc := range ok {
		got, err := ParseValue(tc.def, tc.raw)
		if err != nil {
			t.Errorf("ParseValue(%s, %#v) = %v, want %v", tc.def.Name, tc.raw, err, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseValue(%s, %#v) = %#v, want %#v", tc.def.Name, tc.raw, got, tc.want)
		}
	}

	bad := []struct {
		def Definition
		raw any
	}{
		{history, nil},
		{history, 0},
		{history, -2},
		{history, "abc"},
		{history, "1.5"},
		{history, ""},
		{history, 1.5},
		{history, true},
		{history, []int{1}},
		{history, map[string]any{"n": 1}},
		{threshold, 0},
		{threshold, -1},
		{semverThreshold, -3},
		{semver, nil},
		{semver, 1},
		{semver, float64(1)},
		{semver, "maybe"},
		{semver, []bool{true}},
	}
	for _, tc := range bad {
		got, err := ParseValue(tc.def, tc.raw)
		if err == nil {
			t.Errorf("ParseValue(%s, %#v) = %#v, want an error", tc.def.Name, tc.raw, got)
			continue
		}
		if !strings.Contains(err.Error(), tc.def.Name) {
			t.Errorf("ParseValue(%s, %#v) error %q does not name the flag", tc.def.Name, tc.raw, err)
		}
	}
}

// claim: ParseValue and FormatValue must both reject a definition whose Type is
// not one of the two known values (the default tripwire arm), rather than
// falling through to a zero value.
func TestUnknownFlagTypeIsRejected(t *testing.T) {
	bogus := Definition{Name: "bogus", Type: Type("duration"), Default: 1}
	if got, err := ParseValue(bogus, 1); err == nil {
		t.Errorf("ParseValue with an unknown type = %#v, want an error", got)
	}
	if _, err := FormatValue(bogus, 1); err == nil {
		t.Error("FormatValue with an unknown type accepted the value")
	}
}

// claim: a *present but nil* watch override shadows a valid global value and
// makes the whole resolution fail (rather than falling back to the global).
// Recorded deliberately: the store can hold {"history": null} after external
// tampering, and every caller then degrades to DefaultSettings.
func TestResolveNilOverrideShadowsValidGlobal(t *testing.T) {
	_, err := Resolve(
		map[string]any{"history": nil},
		map[string]any{"history": "5"},
	)
	if err == nil {
		t.Log("a nil local override falls through to the global value")
		return
	}
	t.Logf("observed: a nil local override fails resolution even with a valid global: %v", err)
}

// claim: Resolve validates every override it is handed, including a name whose
// value would otherwise be valid, and reports which scope the bad name came
// from so corrupt state is attributable.
func TestResolveUnknownNameScopeIsReported(t *testing.T) {
	_, err := Resolve(map[string]any{"nope": 1}, nil)
	if err == nil || !strings.Contains(err.Error(), "watch") {
		t.Errorf("unknown watch flag error = %v, want it to name the watch scope", err)
	}
	_, err = Resolve(nil, map[string]any{"nope": "1"})
	if err == nil || !strings.Contains(err.Error(), "global") {
		t.Errorf("unknown global flag error = %v, want it to name the global scope", err)
	}
	// A valid name with an unusable value must still fail.
	if _, err := Resolve(nil, map[string]any{"notify_threshold": "0"}); err == nil {
		t.Error("Resolve accepted notify_threshold=0 from the global scope")
	}
	// pattern-only validity is a per-watch property, not a resolution one:
	// Resolve must not invent a rejection for a name it knows.
	if _, err := Resolve(map[string]any{"semver": true}, nil); err != nil {
		t.Errorf("Resolve rejected the known pattern-only flag semver: %v", err)
	}
}

// claim: Lookup is an exact, case-sensitive name match — a near-miss or an
// empty name returns the zero Definition and false, and callers must not be
// able to act on the zero value.
func TestLookupIsExactAndCaseSensitive(t *testing.T) {
	for _, name := range []string{"", "History", "HISTORY", "history ", " history", "notify-threshold", "semver_stable "} {
		def, ok := Lookup(name)
		if ok {
			t.Errorf("Lookup(%q) = (%+v, true), want not found", name, def)
		}
		if def.Name != "" || def.Type != "" || def.Default != nil || def.PatternOnly || def.Min != nil {
			t.Errorf("Lookup(%q) returned a non-zero Definition %+v for a miss", name, def)
		}
	}
	for _, def := range Definitions() {
		got, ok := Lookup(def.Name)
		if !ok || got.Name != def.Name {
			t.Errorf("Lookup(%q) = (%+v, %v), want the definition", def.Name, got, ok)
		}
	}
}

// claim: Definitions() hands out independent state on every call — mutating a
// returned slice, a returned Min pointer or a returned struct must not leak
// into the next call (the definitions are a shared, process-wide helper).
func TestDefinitionsReturnsIndependentState(t *testing.T) {
	first := Definitions()
	if len(first) == 0 {
		t.Fatal("no definitions")
	}
	first[0].Name = "mutated"
	first[0].Default = 999
	if first[0].Min != nil {
		*first[0].Min = 42
	}
	first[0] = Definition{Name: "replaced"}

	second := Definitions()
	if second[0].Name != "history" {
		t.Errorf("Definitions()[0].Name = %q after a caller mutated the previous slice", second[0].Name)
	}
	if second[0].Default != 1 {
		t.Errorf("Definitions()[0].Default = %v after a caller mutated the previous slice", second[0].Default)
	}
	if second[0].Min == nil || *second[0].Min != -1 {
		t.Errorf("Definitions()[0].Min = %v after a caller mutated the previous slice's pointer", second[0].Min)
	}
}

// claim: FormatValue is a pure formatter — it accepts exactly the typed value
// for each flag type, rejects everything else, and never mutates its input.
func TestFormatValueRejectsTypeMismatches(t *testing.T) {
	history, _ := Lookup("history")
	semver, _ := Lookup("semver")
	bad := []struct {
		def Definition
		v   any
	}{
		{history, "5"},
		{history, 5.0},
		{history, int64(5)},
		{history, true},
		{history, nil},
		{history, []int{5}},
		{semver, 1},
		{semver, "true"},
		{semver, nil},
	}
	for _, tc := range bad {
		if got, err := FormatValue(tc.def, tc.v); err == nil {
			t.Errorf("FormatValue(%s, %#v) = %q, want an error", tc.def.Name, tc.v, got)
		}
	}
	if got, err := FormatValue(history, -1); err != nil || got != "-1" {
		t.Errorf("FormatValue(history, -1) = (%q, %v), want (\"-1\", nil)", got, err)
	}
	if got, err := FormatValue(semver, false); err != nil || got != "false" {
		t.Errorf("FormatValue(semver, false) = (%q, %v), want (\"false\", nil)", got, err)
	}
}

// claim: ValidForKind must be false for a pattern-only flag on every non-
// pattern kind, and true for the general flags on both kinds.
func TestValidForKindCoversEveryKind(t *testing.T) {
	kinds := []string{"tag", "pattern", ""}
	for _, def := range Definitions() {
		for _, k := range kinds {
			got := ValidForKind(def, apitypes.WatchKind(k))
			want := !def.PatternOnly || k == "pattern"
			if got != want {
				t.Errorf("ValidForKind(%s, %q) = %v, want %v", def.Name, k, got, want)
			}
		}
	}
}
