// Package crypto seals and opens the secrets dit-server keeps at rest.
//
// Values are encrypted with NaCl secretbox: a random 24-byte nonce followed by
// the ciphertext, base64-encoded for storage in SQLite.
package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/nacl/secretbox"
)

const (
	// KeySize is the secretbox key length in bytes.
	KeySize = 32
	// NonceSize is the secretbox nonce length in bytes.
	NonceSize = 24

	keyFileMode = 0o600
	keyDirMode  = 0o700
)

var (
	// ErrInvalidKey reports a master key of the wrong length.
	ErrInvalidKey = errors.New("crypto: master key must be exactly 32 bytes")
	// ErrInvalidEncoding reports a value that is not valid base64.
	ErrInvalidEncoding = errors.New("crypto: value is not valid base64")
	// ErrTooShort reports a sealed value that cannot hold a nonce.
	ErrTooShort = errors.New("crypto: sealed value is too short")
	// ErrDecrypt reports a failed authentication: wrong key or tampered data.
	ErrDecrypt = errors.New("crypto: decryption failed (wrong key or tampered data)")
	// ErrMissingKey reports that no master key was supplied.
	ErrMissingKey = errors.New("crypto: no master key provided")
)

// Sealer seals and opens secrets with one master key.
type Sealer struct {
	key [KeySize]byte
}

// NewSealer builds a Sealer from a raw 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w (got %d bytes)", ErrInvalidKey, len(key))
	}
	s := &Sealer{}
	copy(s.key[:], key)
	return s, nil
}

// SealerFromBase64 builds a Sealer from a base64-encoded 32-byte key. Both
// padded and unpadded encodings are accepted.
func SealerFromBase64(encoded string) (*Sealer, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, ErrMissingKey
	}
	raw, err := decodeBase64(encoded)
	if err != nil {
		return nil, err
	}
	return NewSealer(raw)
}

// GenerateKeyBase64 returns a fresh, base64-encoded 32-byte key.
func GenerateKeyBase64() (string, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("crypto: generate master key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// Seal encrypts plaintext and returns nonce||ciphertext.
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	var nonce [NonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}
	out := make([]byte, 0, NonceSize+len(plaintext)+secretbox.Overhead)
	out = append(out, nonce[:]...)
	return secretbox.Seal(out, plaintext, &nonce, &s.key), nil
}

// Open decrypts a nonce||ciphertext blob produced by Seal.
func (s *Sealer) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < NonceSize+secretbox.Overhead {
		return nil, ErrTooShort
	}
	var nonce [NonceSize]byte
	copy(nonce[:], sealed[:NonceSize])
	out, ok := secretbox.Open(nil, sealed[NonceSize:], &nonce, &s.key)
	if !ok {
		return nil, ErrDecrypt
	}
	return out, nil
}

// SealString seals a string and returns base64(nonce||ciphertext).
func (s *Sealer) SealString(plaintext string) (string, error) {
	sealed, err := s.Seal([]byte(plaintext))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// OpenString opens a base64(nonce||ciphertext) string.
func (s *Sealer) OpenString(encoded string) (string, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return "", nil
	}
	raw, err := decodeBase64(encoded)
	if err != nil {
		return "", err
	}
	plain, err := s.Open(raw)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func decodeBase64(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err == nil {
		return raw, nil
	}
	raw, err2 := base64.RawStdEncoding.DecodeString(encoded)
	if err2 == nil {
		return raw, nil
	}
	return nil, fmt.Errorf("%w: %v", ErrInvalidEncoding, err)
}

// KeySource describes where the master key came from.
type KeySource struct {
	Sealer    *Sealer
	Key       []byte
	Path      string
	Source    string // "env" | "file" | "generated"
	Generated bool
}

// ResolveMasterKey finds the master key in this order:
//
//  1. explicit base64 supplied by the caller (DIT_MASTER_KEY)
//  2. the key file at keyFile, else <dataDir>/master.key
//  3. a freshly generated key written to <dataDir>/master.key with 0600
//
// The caller is expected to log a loud warning when Generated is true: a
// generated key silently protects nothing if it is lost with the data volume.
func ResolveMasterKey(explicitB64, keyFile, dataDir string) (*KeySource, error) {
	if strings.TrimSpace(explicitB64) != "" {
		raw, err := decodeBase64(strings.TrimSpace(explicitB64))
		if err != nil {
			return nil, fmt.Errorf("master key from environment: %w", err)
		}
		sealer, err := NewSealer(raw)
		if err != nil {
			return nil, err
		}
		return &KeySource{Sealer: sealer, Key: raw, Source: "env"}, nil
	}

	path := strings.TrimSpace(keyFile)
	if path == "" {
		path = filepath.Join(dataDir, "master.key")
	}

	raw, err := os.ReadFile(path)
	if err == nil {
		key, err := decodeBase64(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("master key file %s: %w", path, err)
		}
		sealer, err := NewSealer(key)
		if err != nil {
			return nil, fmt.Errorf("master key file %s: %w", path, err)
		}
		return &KeySource{Sealer: sealer, Key: key, Path: path, Source: "file"}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read master key file %s: %w", path, err)
	}

	// Nothing on disk: generate one.
	encoded, err := GenerateKeyBase64()
	if err != nil {
		return nil, err
	}
	key, err := decodeBase64(encoded)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, keyDirMode); err != nil {
			return nil, fmt.Errorf("create key dir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(encoded+"\n"), keyFileMode); err != nil {
		return nil, fmt.Errorf("write master key file %s: %w", path, err)
	}
	if err := os.Chmod(path, keyFileMode); err != nil {
		return nil, fmt.Errorf("chmod master key file %s: %w", path, err)
	}
	sealer, err := NewSealer(key)
	if err != nil {
		return nil, err
	}
	return &KeySource{Sealer: sealer, Key: key, Path: path, Source: "generated", Generated: true}, nil
}
