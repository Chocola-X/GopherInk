// Package auth's csrf.go implements a session-bound CSRF token service.
//
// The previous implementation signed CSRF tokens with a day-granular timestamp
// and accepted the previous day's token, giving each token up to 48 hours of
// validity and making them replayable across sessions once the user had
// re-logged in. This module replaces that with:
//
//   - A per-session identifier bound into the HMAC input, so signing out
//     invalidates the token.
//   - A short-lived issued-at timestamp with an explicit TTL, defaulting to
//     30 minutes and capped at 2 hours.
//   - A purpose tag that differentiates login, register, install, comment,
//     admin, and public forms; a token minted for one purpose does not
//     authenticate another form.
//   - Purpose-scoped key derivation via SecretManager.Derive so the raw
//     secret is never used directly.
//   - Constant-time comparison of both the payload and the signature.
package auth

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// CSRFService issues and verifies session-bound CSRF tokens.
type CSRFService struct {
	secrets *SecretManager
	ttl     time.Duration
	// leeway allows a small skew between the clock that signed the token and
	// the clock that verifies it. It is intentionally small.
	leeway time.Duration
	now    func() time.Time
}

// CSRFConfig controls token lifetime and clock skew tolerance.
type CSRFConfig struct {
	TTL    time.Duration
	Leeway time.Duration
	Now    func() time.Time
}

// NewCSRFService returns a service backed by the given secret manager.
func NewCSRFService(secrets *SecretManager, cfg CSRFConfig) *CSRFService {
	if cfg.TTL <= 0 {
		cfg.TTL = 30 * time.Minute
	}
	if cfg.TTL > 2*time.Hour {
		cfg.TTL = 2 * time.Hour
	}
	if cfg.Leeway <= 0 {
		cfg.Leeway = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &CSRFService{secrets: secrets, ttl: cfg.TTL, leeway: cfg.Leeway, now: cfg.Now}
}

// ErrCSRF indicates a token failed verification for any reason. Callers must
// not surface the underlying reason: a leaked reason would help an attacker
// distinguish "expired" from "wrong session" from "forged".
var ErrCSRF = errors.New("auth: csrf token rejected")

// Subject identifies who a token is bound to. Sess is a per-session token
// (see cookie.go) that rotates on password change or explicit revocation.
type Subject struct {
	UID     int64
	Session string
}

// Anonymous returns the subject used for unauthenticated forms (login,
// register, install, public comment posted while logged out).
func Anonymous() Subject { return Subject{UID: 0, Session: "anon"} }

// Issue returns a new CSRF token for the given subject and purpose.
func (s *CSRFService) Issue(ctx context.Context, subj Subject, purpose string) (string, error) {
	if s == nil {
		return "", ErrCSRF
	}
	issued := s.now().UTC().Unix()
	payload := csrfPayload(subj, purpose, issued)
	sig, err := s.secrets.Sign(ctx, "csrf:"+purpose, payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." + sig, nil
}

// Verify returns nil when the token is valid for the given subject/purpose
// and still within TTL.
func (s *CSRFService) Verify(ctx context.Context, subj Subject, purpose, token string) error {
	if s == nil || token == "" {
		return ErrCSRF
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return ErrCSRF
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrCSRF
	}
	if !s.secrets.Verify(ctx, "csrf:"+purpose, payload, parts[1]) {
		return ErrCSRF
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 4 {
		return ErrCSRF
	}
	if fields[0] != strconv.FormatInt(subj.UID, 10) {
		return ErrCSRF
	}
	if !hmac.Equal([]byte(fields[1]), []byte(subj.Session)) {
		return ErrCSRF
	}
	if fields[2] != purpose {
		return ErrCSRF
	}
	issued, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return ErrCSRF
	}
	now := s.now().UTC().Unix()
	if issued > now+int64(s.leeway/time.Second) {
		return ErrCSRF
	}
	if now-issued > int64((s.ttl+s.leeway)/time.Second) {
		return ErrCSRF
	}
	return nil
}

func csrfPayload(subj Subject, purpose string, issuedAt int64) []byte {
	return []byte(
		strconv.FormatInt(subj.UID, 10) + "|" +
			subj.Session + "|" +
			purpose + "|" +
			strconv.FormatInt(issuedAt, 10),
	)
}
