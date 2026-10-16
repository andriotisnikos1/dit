// Package flags defines dit's behaviour flags: named options, set globally
// and/or per watch, that change how the server treats a watch.
//
// This package is the single source of truth for flag definitions, parsing,
// validation and resolution. The store persists raw values, the server
// validates writes and resolves reads, the check engine consumes the
// resolved Settings, and the CLI imports it so a bad value fails before it
// ever reaches the network.
package flags

import (
	"fmt"
	"math"
	"strconv"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// Type is the value type of a flag.
type Type string

const (
	TypeInt  Type = "int"
	TypeBool Type = "bool"
)

// Definition describes one flag.
type Definition struct {
	Name        string
	Type        Type
	Description string
	// Default is the value used when neither the watch nor the global
	// settings override the flag: an int for TypeInt, a bool for TypeBool.
	Default any
	// PatternOnly marks a flag that only applies to pattern watches.
	PatternOnly bool
	// Min is the inclusive lower bound for int flags; nil = unbounded.
	Min *int
}

func ptr(n int) *int { return &n }

// Definitions lists every flag, in stable display order.
func Definitions() []Definition {
	return []Definition{
		{
			Name: "history",
			Type: TypeInt,
			Description: "Recent update events to retain per watch, newest first; " +
				"-1 keeps all. Events still waiting for their notification are always " +
				"kept. Failure events are capped to the same count.",
			Default: 1,
			Min:     ptr(-1),
		},
		{
			Name: "notify_threshold",
			Type: TypeInt,
			Description: "Update events to accumulate before notifying: a digest " +
				"change or a batch of new tags counts as one update. 1 notifies on every " +
				"change. Failure and recovery notifications are never deferred.",
			Default: 1,
			Min:     ptr(1),
		},
		{
			Name: "semver",
			Type: TypeBool,
			Description: "Pattern watches: track the highest semver version among " +
				"matched tags and report version bumps instead of every new tag. Tags " +
				"that do not parse as semver are ignored.",
			Default:     false,
			PatternOnly: true,
		},
		{
			Name: "semver_stable",
			Type: TypeBool,
			Description: "Pattern watches in semver mode: ignore prerelease versions " +
				"entirely (a -rc.1 tag never counts as the highest version).",
			Default:     false,
			PatternOnly: true,
		},
		{
			Name: "semver_notify_threshold",
			Type: TypeInt,
			Description: "Semver bumps to accumulate before notifying; 1 notifies on " +
				"every bump. Independent of notify_threshold.",
			Default:     1,
			PatternOnly: true,
			Min:         ptr(1),
		},
	}
}

// Lookup returns the definition of a flag by name.
func Lookup(name string) (Definition, bool) {
	for _, def := range Definitions() {
		if def.Name == name {
			return def, true
		}
	}
	return Definition{}, false
}

// ValidForKind reports whether a flag may be set on a watch of the kind.
func ValidForKind(def Definition, kind apitypes.WatchKind) bool {
	return !def.PatternOnly || kind == apitypes.WatchKindPattern
}

// ParseValue converts a raw scalar (JSON-decoded or CLI string) into the
// flag's typed value. Strings are accepted for both types so YAML round-trips
// and CLI arguments work; JSON numbers arrive as float64.
func ParseValue(def Definition, raw any) (any, error) {
	switch def.Type {
	case TypeInt:
		return parseInt(def, raw)
	case TypeBool:
		return parseBool(def, raw)
	default:
		return nil, fmt.Errorf("flags: unknown type %q", def.Type)
	}
}

func parseInt(def Definition, raw any) (int, error) {
	var n int
	switch v := raw.(type) {
	case int:
		n = v
	case int64:
		n = int(v)
	case float64:
		// JSON numbers decode as float64; the value must be integral.
		if v != math.Trunc(v) {
			return 0, fmt.Errorf("flag %q expects an integer, got %v", def.Name, v)
		}
		n = int(v)
	case string:
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("flag %q expects an integer, got %q", def.Name, v)
		}
		n = parsed
	default:
		return 0, fmt.Errorf("flag %q expects an integer, got %T", def.Name, raw)
	}
	if def.Min != nil && n < *def.Min {
		return 0, fmt.Errorf("flag %q must be >= %d, got %d", def.Name, *def.Min, n)
	}
	// 0 is not a meaningful count: history is -1 (keep all) or a positive
	// number, and the thresholds are >= 1, so no int flag accepts 0.
	if n == 0 {
		return 0, fmt.Errorf("flag %q must not be 0", def.Name)
	}
	return n, nil
}

func parseBool(def Definition, raw any) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("flag %q expects a boolean (true/false/1/0), got %q", def.Name, v)
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("flag %q expects a boolean, got %T", def.Name, raw)
	}
}

// FormatValue renders a typed value the way the settings table stores it.
func FormatValue(def Definition, value any) (string, error) {
	switch def.Type {
	case TypeInt:
		n, ok := value.(int)
		if !ok {
			return "", fmt.Errorf("flags: flag %q: not an int: %T", def.Name, value)
		}
		return strconv.Itoa(n), nil
	case TypeBool:
		b, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("flags: flag %q: not a bool: %T", def.Name, value)
		}
		return strconv.FormatBool(b), nil
	default:
		return "", fmt.Errorf("flags: unknown type %q", def.Type)
	}
}

// Settings are the resolved flag values one watch runs with.
type Settings struct {
	History               int
	NotifyThreshold       int
	Semver                bool
	SemverStable          bool
	SemverNotifyThreshold int
}

// DefaultSettings are the settings every watch gets when nothing overrides
// the flag defaults.
func DefaultSettings() Settings {
	return Settings{
		History:               1,
		NotifyThreshold:       1,
		Semver:                false,
		SemverStable:          false,
		SemverNotifyThreshold: 1,
	}
}

// Resolve computes the effective settings: a per-watch override wins over a
// global value, which wins over the flag default. Unknown flag names are
// reported so corrupted state is surfaced rather than silently ignored.
func Resolve(watchFlags, globalFlags map[string]any) (Settings, error) {
	known := map[string]struct{}{}
	for _, def := range Definitions() {
		known[def.Name] = struct{}{}
	}
	for name := range watchFlags {
		if _, ok := known[name]; !ok {
			return Settings{}, fmt.Errorf("flags: unknown flag %q on watch", name)
		}
	}
	for name := range globalFlags {
		if _, ok := known[name]; !ok {
			return Settings{}, fmt.Errorf("flags: unknown flag %q in global settings", name)
		}
	}

	value := func(def Definition) (any, error) {
		if v, ok := watchFlags[def.Name]; ok {
			return ParseValue(def, v)
		}
		if v, ok := globalFlags[def.Name]; ok {
			return ParseValue(def, v)
		}
		return def.Default, nil
	}

	out := DefaultSettings()
	for _, def := range Definitions() {
		v, err := value(def)
		if err != nil {
			return Settings{}, err
		}
		switch def.Name {
		case "history":
			out.History = v.(int)
		case "notify_threshold":
			out.NotifyThreshold = v.(int)
		case "semver":
			out.Semver = v.(bool)
		case "semver_stable":
			out.SemverStable = v.(bool)
		case "semver_notify_threshold":
			out.SemverNotifyThreshold = v.(int)
		}
	}
	return out, nil
}
