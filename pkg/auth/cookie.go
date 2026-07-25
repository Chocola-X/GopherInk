package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const CookieName = "gopherink_session"

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

func SetSession(w http.ResponseWriter, secret string, uid int64) {
	SetSessionWithOptions(w, secret, uid, CookieOptions{})
}

func SetSessionWithOptions(w http.ResponseWriter, secret string, uid int64, options CookieOptions) {
	SetVersionedSessionWithOptions(w, secret, uid, "", options)
}

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
	sig := sign(secret, payload)
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

func ClearSession(w http.ResponseWriter) {
	ClearSessionWithOptions(w, CookieOptions{})
}

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

func ParseSession(r *http.Request, secret string) (int64, bool) {
	return ParseSessionWithOptions(r, secret, CookieOptions{})
}

func ParseSessionWithOptions(r *http.Request, secret string, options CookieOptions) (int64, bool) {
	session, ok := ParseVersionedSessionWithOptions(r, secret, options)
	return session.UID, ok
}

type Session struct {
	UID     int64
	Expires int64
	Version string
}

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
	if !hmac.Equal([]byte(sign(secret, payload)), []byte(signature)) {
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

func sign(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
