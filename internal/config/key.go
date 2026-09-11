package config

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// decodeMasterKey decodes the base64 master key carried in the environment,
// without importing the crypto package (which imports config-free helpers of
// its own and would otherwise create a cycle in tests that stub the decoder).
func decodeMasterKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("master key is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("master key is not valid base64: %w", err)
		}
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("master key must decode to 32 bytes (got %d)", len(raw))
	}
	return raw, nil
}
