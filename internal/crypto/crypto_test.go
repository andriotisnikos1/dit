package crypto

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	return bytes.Repeat([]byte{0x11}, KeySize)
}

func TestSealOpenRoundtrip(t *testing.T) {
	sealer, err := NewSealer(testKey(t))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}

	cases := []string{
		"",
		"hunter2",
		"a longer secret with spaces and symbols !@#$%^&*()",
		strings.Repeat("x", 4096),
	}
	for _, plaintext := range cases {
		encoded, err := sealer.SealString(plaintext)
		if err != nil {
			t.Fatalf("SealString(%q): %v", plaintext, err)
		}
		got, err := sealer.OpenString(encoded)
		if err != nil {
			t.Fatalf("OpenString(%q): %v", plaintext, err)
		}
		if got != plaintext {
			t.Errorf("roundtrip mismatch: got %q, want %q", got, plaintext)
		}
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	sealer, err := NewSealer(testKey(t))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}

	first, err := sealer.SealString("same input")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	second, err := sealer.SealString("same input")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	if first == second {
		t.Error("two seals of the same plaintext are identical: the nonce is not random")
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	sealer, err := NewSealer(testKey(t))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	sealed, err := sealer.SealString("top secret")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}

	other, err := NewSealer(bytes.Repeat([]byte{0x22}, KeySize))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	if _, err := other.OpenString(sealed); !errors.Is(err, ErrDecrypt) {
		t.Errorf("OpenString with wrong key: got %v, want ErrDecrypt", err)
	}
}

func TestOpenTamperedCiphertextFails(t *testing.T) {
	sealer, err := NewSealer(testKey(t))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	sealed, err := sealer.SealString("integrity matters")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatalf("decode sealed: %v", err)
	}
	// Flip one bit in the ciphertext (past the nonce).
	raw[len(raw)-1] ^= 0x01
	tampered := base64.StdEncoding.EncodeToString(raw)

	if _, err := sealer.OpenString(tampered); !errors.Is(err, ErrDecrypt) {
		t.Errorf("OpenString on tampered data: got %v, want ErrDecrypt", err)
	}
}

func TestOpenTooShortFails(t *testing.T) {
	sealer, err := NewSealer(testKey(t))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	if _, err := sealer.Open(make([]byte, NonceSize)); !errors.Is(err, ErrTooShort) {
		t.Errorf("Open on a short blob: got %v, want ErrTooShort", err)
	}
}

func TestNewSealerRejectsWrongKeyLength(t *testing.T) {
	if _, err := NewSealer([]byte("short")); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("NewSealer with a short key: got %v, want ErrInvalidKey", err)
	}
	if _, err := NewSealer(bytes.Repeat([]byte{1}, 64)); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("NewSealer with a long key: got %v, want ErrInvalidKey", err)
	}
}

func TestSealerFromBase64(t *testing.T) {
	encoded, err := GenerateKeyBase64()
	if err != nil {
		t.Fatalf("GenerateKeyBase64: %v", err)
	}
	if _, err := SealerFromBase64(encoded); err != nil {
		t.Fatalf("SealerFromBase64: %v", err)
	}
	// Unpadded base64 must be accepted too.
	unpadded := strings.TrimRight(encoded, "=")
	if _, err := SealerFromBase64(unpadded); err != nil {
		t.Fatalf("SealerFromBase64(unpadded): %v", err)
	}
	if _, err := SealerFromBase64(""); !errors.Is(err, ErrMissingKey) {
		t.Errorf("SealerFromBase64(\"\"): got %v, want ErrMissingKey", err)
	}
	if _, err := SealerFromBase64("not base64!!"); err == nil {
		t.Error("SealerFromBase64 with garbage: expected an error")
	}
}

func TestResolveMasterKeyPrefersEnvironment(t *testing.T) {
	dir := t.TempDir()
	encoded, err := GenerateKeyBase64()
	if err != nil {
		t.Fatalf("GenerateKeyBase64: %v", err)
	}

	source, err := ResolveMasterKey(encoded, "", dir)
	if err != nil {
		t.Fatalf("ResolveMasterKey: %v", err)
	}
	if source.Source != "env" {
		t.Errorf("Source = %q, want %q", source.Source, "env")
	}
	if source.Generated {
		t.Error("Generated = true, want false")
	}
	// Nothing should have been written.
	if _, err := os.Stat(filepath.Join(dir, "master.key")); !errors.Is(err, os.ErrNotExist) {
		t.Error("an environment-supplied key must not create a key file")
	}
}

func TestResolveMasterKeyReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	encoded, err := GenerateKeyBase64()
	if err != nil {
		t.Fatalf("GenerateKeyBase64: %v", err)
	}
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	source, err := ResolveMasterKey("", path, dir)
	if err != nil {
		t.Fatalf("ResolveMasterKey: %v", err)
	}
	if source.Source != "file" {
		t.Errorf("Source = %q, want %q", source.Source, "file")
	}
	if source.Path != path {
		t.Errorf("Path = %q, want %q", source.Path, path)
	}
}

func TestResolveMasterKeyGeneratesWith0600(t *testing.T) {
	dir := t.TempDir()

	source, err := ResolveMasterKey("", "", dir)
	if err != nil {
		t.Fatalf("ResolveMasterKey: %v", err)
	}
	if !source.Generated || source.Source != "generated" {
		t.Fatalf("Source = %q Generated = %v, want generated/true", source.Source, source.Generated)
	}

	info, err := os.Stat(source.Path)
	if err != nil {
		t.Fatalf("stat generated key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("generated key permissions = %o, want 600", perm)
	}

	// The generated key must be usable and stable across a second resolution.
	sealed, err := source.Sealer.SealString("secret")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	again, err := ResolveMasterKey("", "", dir)
	if err != nil {
		t.Fatalf("ResolveMasterKey (second): %v", err)
	}
	if again.Generated {
		t.Error("second resolution regenerated the key instead of reading it")
	}
	plain, err := again.Sealer.OpenString(sealed)
	if err != nil {
		t.Fatalf("OpenString with re-read key: %v", err)
	}
	if plain != "secret" {
		t.Errorf("decrypted %q, want %q", plain, "secret")
	}
}
