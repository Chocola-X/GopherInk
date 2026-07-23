// Package auth's secret manager centralises the derivation and rotation of
// cryptographic keys used by session cookies, CSRF tokens, preview links and
// flash cookies. Every subsystem that needs a signing key must obtain it
// through SecretManager.Get; there is intentionally no fallback constant.
//
// A boot-time check refuses to run the server when the persisted secret is
// missing or too short. This eliminates the previous class of bug where a
// disappearing "auth_secret" option silently downgraded every HMAC to a hard
// coded string ("gopherink"), making CSRF/preview tokens forgeable.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
)

const (
	// SecretMinLen is the minimum acceptable length in bytes of the raw
	// (hex-decoded) secret. 32 bytes = 256 bits.
	SecretMinLen = 32
)

var (
	// ErrSecretMissing is returned when the caller starts a signing operation
	// while no secret is configured.
	ErrSecretMissing = errors.New("auth: signing secret is not configured")
	// ErrSecretWeak is returned when the persisted secret is shorter than
	// SecretMinLen after decoding.
	ErrSecretWeak = errors.New("auth: signing secret is too short; regenerate it")
)

// OptionStore is the small slice of the option service used by the secret
// manager. Kept as an interface so tests can inject a fake without pulling in
// the whole services package.
type OptionStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// SecretManager reads, caches and rotates the signing secret. It is safe for
// concurrent use.
type SecretManager struct {
	store OptionStore
	key   string

	mu     sync.RWMutex
	cached []byte
}

// NewSecretManager returns a manager backed by the given option store. The
// key parameter is the option-table key used for persistence.
func NewSecretManager(store OptionStore, optionKey string) *SecretManager {
	if optionKey == "" {
		optionKey = "auth_secret"
	}
	return &SecretManager{store: store, key: optionKey}
}

// EnsureBootstrap must be called on process start. It reads the existing
// secret if any, generates one when absent, and stores it. It returns an
// error when the secret cannot be persisted so the caller can refuse to serve.
func (m *SecretManager) EnsureBootstrap(ctx context.Context) error {
	if m == nil {
		return ErrSecretMissing
	}
	raw, err := m.store.Get(ctx, m.key)
	if err != nil {
		return err
	}
	raw = strings.TrimSpace(raw)
	if raw != "" {
		decoded, err := decodeSecret(raw)
		if err == nil && len(decoded) >= SecretMinLen {
			m.mu.Lock()
			m.cached = decoded
			m.mu.Unlock()
			return nil
		}
	}
	// Generate a fresh secret. We do not use a fallback constant.
	buf := make([]byte, SecretMinLen)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	encoded := hex.EncodeToString(buf)
	if err := m.store.Set(ctx, m.key, encoded); err != nil {
		return err
	}
	m.mu.Lock()
	m.cached = buf
	m.mu.Unlock()
	return nil
}

// Rotate generates and persists a brand-new secret. All previously issued
// CSRF tokens, preview links and session cookies immediately become invalid.
func (m *SecretManager) Rotate(ctx context.Context) error {
	if m == nil {
		return ErrSecretMissing
	}
	buf := make([]byte, SecretMinLen)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	if err := m.store.Set(ctx, m.key, hex.EncodeToString(buf)); err != nil {
		return err
	}
	m.mu.Lock()
	m.cached = buf
	m.mu.Unlock()
	return nil
}

// Bytes returns the cached secret. It refreshes from the option store on cache
// miss so tests can pre-populate the store and skip EnsureBootstrap.
func (m *SecretManager) Bytes(ctx context.Context) ([]byte, error) {
	if m == nil {
		return nil, ErrSecretMissing
	}
	m.mu.RLock()
	cached := m.cached
	m.mu.RUnlock()
	if len(cached) >= SecretMinLen {
		return cached, nil
	}
	raw, err := m.store.Get(ctx, m.key)
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrSecretMissing
	}
	decoded, err := decodeSecret(raw)
	if err != nil {
		return nil, err
	}
	if len(decoded) < SecretMinLen {
		return nil, ErrSecretWeak
	}
	m.mu.Lock()
	m.cached = decoded
	m.mu.Unlock()
	return decoded, nil
}

// Derive returns a purpose-scoped subkey using HKDF-Expand-like construction
// on top of HMAC-SHA256. Different subsystems get non-interchangeable keys.
func (m *SecretManager) Derive(ctx context.Context, purpose string) ([]byte, error) {
	base, err := m.Bytes(ctx)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, base)
	_, _ = mac.Write([]byte("gopherink:v1:"))
	_, _ = mac.Write([]byte(purpose))
	return mac.Sum(nil), nil
}

// Sign returns a base64url-encoded HMAC of payload using the purpose-scoped
// subkey. Callers must not use the raw secret directly.
func (m *SecretManager) Sign(ctx context.Context, purpose string, payload []byte) (string, error) {
	key, err := m.Derive(ctx, purpose)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Verify checks that signature matches HMAC(payload) under the purpose key
// using constant-time comparison.
func (m *SecretManager) Verify(ctx context.Context, purpose string, payload []byte, signature string) bool {
	expected, err := m.Sign(ctx, purpose, payload)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(expected), []byte(signature))
}

func decodeSecret(raw string) ([]byte, error) {
	if b, err := hex.DecodeString(raw); err == nil {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return b, nil
	}
	// Fall back to raw bytes so migrating deployments that stored an ASCII
	// secret still work, provided the string is long enough.
	if len(raw) >= SecretMinLen {
		return []byte(raw), nil
	}
	return nil, ErrSecretWeak
}
