package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// unmarshalYAML is a small helper so duration tests can stay table-driven.
func unmarshalYAML(t *testing.T, body string, dst any) error {
	t.Helper()
	return yaml.Unmarshal([]byte(body), dst)
}
