// Package auth's cookie.go issues and parses the versioned session cookie.
//
// A cookie carries four fields joined by ':' — user id, expiry (unix seconds),
// base64url-encoded auth version, HMAC. The HMAC is derived from the
// SecretManager under the "session" purpose so that rotating the secret
// invalidates every session. The auth version, in turn, is refreshed whenever
// the user changes their password or the admin revokes sessions, so a
// compromised cookie can be invalidated without rotating the global secret.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func hmacNew256(secret []byte) hash.Hash {
	return hmac.New(sha256.New, secret)
}

const CookieName = "gopherink_session"

// ErrSession is returned by ParseSession on any validation failure. Callers
// should treat it as "no session" and never surface the underlying reason.
var ErrSession = errors.New("auth: invalid session cookie")

// CookieOptions bundles the framework-level cookie parameters. Secure is not
// an admin-supplied toggle; it is derived from the request (TLS on/off) and
// enforced whenever HTTPS is active.
type CookieOptions struct {
	Prefix   string
	Secure   bool
	HTTPOnly bool
	SameSite http.SameSite
}

func (o CookieOptions) Name(base string) string {
	if o.Prefix == "" {
		return base
	}
	return o.Prefix + base
}

// SessionIssuer signs and clears session cookies.
type SessionIssuer struct {
	secrets *SecretManager
	ttl     time.Duration
	now     func() time.Time
}

// NewSessionIssuer returns an issuer with the given TTL. ttl <= 0 defaults to
// 7 days.
func NewSessionIssuer(secrets *SecretManager, ttl time.Duration) *SessionIssuer {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return &SessionIssuer{secrets: secrets, ttl: ttl, now: time.Now}
}

// Session is the decoded cookie body.
type Session struct {
	UID     int64
	Expires int64
	Version string
}

// Issue writes a fresh session cookie for uid to w. The cookie is HttpOnly by
// default and Secure whenever options.Secure is true.
func (s *SessionIssuer) Issue(ctx context.Context, w http.ResponseWriter, uid int64, version string, options CookieOptions) error {
	if s == nil {
		return ErrSecretMissing
	}
	if !options.HTTPOnly {
		options.HTTPOnly = true
	}
	if options.SameSite == 0 {
		options.SameSite = http.SameSiteLaxMode
	}
	exp := s.now().Add(s.ttl).Unix()
	encodedVersion := base64.RawURLEncoding.EncodeToString([]byte(version))
	payload := fmt.Sprintf("%d:%d:%s", uid, exp, encodedVersion)
	sig, err := s.secrets.Sign(ctx, "session", []byte(payload))
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     options.Name(CookieName),
		Value:    payload + ":" + sig,
		Path:     "/",
		HttpOnly: options.HTTPOnly,
		SameSite: options.SameSite,
		Secure:   options.Secure,
		Expires:  time.Unix(exp, 0),
	})
	return nil
}

// Clear expires the cookie on the client. It is safe to call even if the
// client had no cookie.
func (s *SessionIssuer) Clear(w http.ResponseWriter, options CookieOptions) {
	if !options.HTTPOnly {
		options.HTTPOnly = true
	}
	if options.SameSite == 0 {
		options.SameSite = http.SameSiteLaxMode
	}
	http.SetCookie(w, &http.Cookie{
		Name:     options.Name(CookieName),
		Value:    "",
		Path:     "/",
		HttpOnly: options.HTTPOnly,
		SameSite: options.SameSite,
		Secure:   options.Secure,
		MaxAge:   -1,
	})
}

// Parse decodes and verifies the incoming session cookie.
func (s *SessionIssuer) Parse(ctx context.Context, r *http.Request, options CookieOptions) (Session, error) {
	if s == nil {
		return Session{}, ErrSession
	}
	cookie, err := r.Cookie(options.Name(CookieName))
	if err != nil {
		return Session{}, ErrSession
	}
	parts := strings.Split(cookie.Value, ":")
	if len(parts) != 3 && len(parts) != 4 {
		return Session{}, ErrSession
	}
	payload := parts[0] + ":" + parts[1]
	signature := parts[2]
	version := ""
	if len(parts) == 4 {
		payload += ":" + parts[2]
		signature = parts[3]
		decoded, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			return Session{}, ErrSession
		}
		version = string(decoded)
	}
	expected, err := s.secrets.Sign(ctx, "session", []byte(payload))
	if err != nil {
		return Session{}, ErrSession
	}
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return Session{}, ErrSession
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || s.now().Unix() > exp {
		return Session{}, ErrSession
	}
	uid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Session{}, ErrSession
	}
	return Session{UID: uid, Expires: exp, Version: version}, nil
}

// Backwards-compatible convenience wrappers. The rest of the codebase will be
// migrated to the SessionIssuer interface; these helpers keep the transitional
// call sites working without importing SecretManager directly.

// Deprecated: use SessionIssuer. Retained temporarily while call sites migrate.
func SetSession(w http.ResponseWriter, secret string, uid int64) {
	SetVersionedSessionWithOptions(w, secret, uid, "", CookieOptions{})
}

// Deprecated: use SessionIssuer.
func SetSessionWithOptions(w http.ResponseWriter, secret string, uid int64, options CookieOptions) {
	SetVersionedSessionWithOptions(w, secret, uid, "", options)
}

// Deprecated: use SessionIssuer.
func SetVersionedSessionWithOptions(w http.ResponseWriter, secret string, uid int64, version string, options CookieOptions) {
	if !options.HTTPOnly {
		options.HTTPOnly = true
	}
	if options.SameSite == 0 {
		options.SameSite = http.SameSiteLaxMode
	}
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	encodedVersion := base64.RawURLEncoding.EncodeToString([]byte(version))
	payload := fmt.Sprintf("%d:%d:%s", uid, exp, encodedVersion)
	sig := legacySign(secret, payload)
	http.SetCookie(w, &http.Cookie{
		Name:     options.Name(CookieName),
		Value:    payload + ":" + sig,
		Path:     "/",
		HttpOnly: options.HTTPOnly,
		SameSite: options.SameSite,
		Secure:   options.Secure,
		Expires:  time.Unix(exp, 0),
	})
}

// Deprecated: use SessionIssuer.Clear.
func ClearSession(w http.ResponseWriter) {
	ClearSessionWithOptions(w, CookieOptions{})
}

// Deprecated: use SessionIssuer.Clear.
func ClearSessionWithOptions(w http.ResponseWriter, options CookieOptions) {
	if !options.HTTPOnly {
		options.HTTPOnly = true
	}
	if options.SameSite == 0 {
		options.SameSite = http.SameSiteLaxMode
	}
	http.SetCookie(w, &http.Cookie{
		Name:     options.Name(CookieName),
		Value:    "",
		Path:     "/",
		HttpOnly: options.HTTPOnly,
		SameSite: options.SameSite,
		Secure:   options.Secure,
		MaxAge:   -1,
	})
}

// Deprecated: use SessionIssuer.Parse.
func ParseSession(r *http.Request, secret string) (int64, bool) {
	session, ok := ParseVersionedSessionWithOptions(r, secret, CookieOptions{})
	return session.UID, ok
}

// Deprecated: use SessionIssuer.Parse.
func ParseSessionWithOptions(r *http.Request, secret string, options CookieOptions) (int64, bool) {
	session, ok := ParseVersionedSessionWithOptions(r, secret, options)
	return session.UID, ok
}

// Deprecated: use SessionIssuer.Parse.
func ParseVersionedSessionWithOptions(r *http.Request, secret string, options CookieOptions) (Session, bool) {
	cookie, err := r.Cookie(options.Name(CookieName))
	if err != nil {
		return Session{}, false
	}

	parts := strings.Split(cookie.Value, ":")
	if len(parts) != 3 && len(parts) != 4 {
		return Session{}, false
	}

	payload := parts[0] + ":" + parts[1]
	signature := parts[2]
	version := ""
	if len(parts) == 4 {
		payload += ":" + parts[2]
		signature = parts[3]
		decoded, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			return Session{}, false
		}
		version = string(decoded)
	}
	if !hmac.Equal([]byte(legacySign(secret, payload)), []byte(signature)) {
		return Session{}, false
	}

	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return Session{}, false
	}

	uid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Session{}, false
	}
	return Session{UID: uid, Expires: exp, Version: version}, true
}

// legacySign preserves the old signing algorithm so the deprecated wrappers
// keep working. New code must route through SessionIssuer instead.
func legacySign(secret, payload string) string {
	mac := hmacNew256([]byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
