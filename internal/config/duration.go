package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from either a Go duration string
// ("15m", "30s") or a plain integer number of seconds.
type Duration time.Duration

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String renders the duration in Go syntax.
func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML implements yaml.Unmarshaler.
//
// The node's tag is inspected rather than trying one decode and falling back
// to the other: yaml.v3 happily coerces the integer 3600 into the string
// "3600", so a decode-first strategy would feed a bare number to
// time.ParseDuration and reject a perfectly reasonable config.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("invalid duration: expected a scalar, got %s", value.Tag)
	}

	switch value.Tag {
	case "!!int":
		var seconds int64
		if err := value.Decode(&seconds); err != nil {
			return fmt.Errorf("invalid duration %q: %w", value.Value, err)
		}
		*d = Duration(time.Duration(seconds) * time.Second)
		return nil
	case "!!float":
		var seconds float64
		if err := value.Decode(&seconds); err != nil {
			return fmt.Errorf("invalid duration %q: %w", value.Value, err)
		}
		*d = Duration(time.Duration(seconds * float64(time.Second)))
		return nil
	default:
		parsed, err := time.ParseDuration(value.Value)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", value.Value, err)
		}
		*d = Duration(parsed)
		return nil
	}
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }
