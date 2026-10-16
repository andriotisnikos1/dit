package apitypes

// FlagType is the value type of a behaviour flag.
type FlagType string

const (
	// FlagTypeInt marks a flag whose value is an integer.
	FlagTypeInt FlagType = "int"
	// FlagTypeBool marks a flag whose value is a boolean.
	FlagTypeBool FlagType = "bool"
)

// Flag is the wire representation of a behaviour flag.
//
// Global is the explicitly-set global value (nil when unset). Local is the
// per-watch override (nil when unset, only filled for a watch scope).
// Effective is the resolved value (only filled for a watch scope).
type Flag struct {
	Name        string   `json:"name"                    yaml:"name"`
	Type        FlagType `json:"type"                    yaml:"type"`
	Description string   `json:"description"             yaml:"description"`
	Default     any      `json:"default"                 yaml:"default"`
	PatternOnly bool     `json:"pattern_only,omitempty" yaml:"pattern_only,omitempty"`
	Global      *any     `json:"global,omitempty"        yaml:"global,omitempty"`
	Local       *any     `json:"local,omitempty"         yaml:"local,omitempty"`
	Effective   *any     `json:"effective,omitempty"     yaml:"effective,omitempty"`
}

// FlagList is the response of the flag listing endpoints.
type FlagList []Flag

// SetFlagRequest is the body of the flag PUT endpoints. Value must be a
// JSON scalar matching the flag's type; use the DELETE endpoint to unset.
type SetFlagRequest struct {
	Value any `json:"value" yaml:"value"`
}
