package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Chocola-X/GopherInk/core/models"
)

const (
	commentGuardTokenTTL   = 15 * time.Minute
	commentGuardVisitorTTL = 24 * time.Hour
	commentGuardMaxUsed    = 10000
)

type commentGuardResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

func (a *App) frontCommentGuard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !a.activeThemeUsesCommentGuard(r.Context()) {
		http.NotFound(w, r)
		return
	}
	if !strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest") || r.Header.Get("X-GopherInk-Comment") != "guard" {
		http.Error(w, "invalid comment guard request", http.StatusForbidden)
		return
	}

	cid, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("cid")), 10, 64)
	if err != nil || cid <= 0 {
		http.Error(w, "invalid content", http.StatusBadRequest)
		return
	}
	content, err := a.Contents.ByID(r.Context(), cid)
	if err != nil || content.Status != models.ContentStatusPost || content.AllowComment != "1" || (content.Type != models.ContentTypePost && content.Type != models.ContentTypePage) {
		http.NotFound(w, r)
		return
	}
	if !a.validCommentReferer(r, a.contentURL(r.Context(), content)) {
		http.Error(w, "invalid comment source", http.StatusForbidden)
		return
	}

	visitorID, ok := a.commentGuardVisitor(r)
	if !ok {
		visitorID, err = randomCommentGuardValue(24)
		if err != nil {
			http.Error(w, "comment guard unavailable", http.StatusInternalServerError)
			return
		}
		a.setCommentGuardVisitor(w, r, visitorID)
	}
	token, expiresAt, err := a.issueCommentGuardToken(r, cid, visitorID)
	if err != nil {
		http.Error(w, "comment guard unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(commentGuardResponse{Token: token, ExpiresAt: expiresAt})
}

func (a *App) activeThemeUsesCommentGuard(ctx context.Context) bool {
	theme, ok := a.activeTheme(ctx)
	return ok && theme.Capabilities.CommentGuard
}

func (a *App) issueCommentGuardToken(r *http.Request, cid int64, visitorID string) (string, int64, error) {
	nonce, err := randomCommentGuardValue(24)
	if err != nil {
		return "", 0, err
	}
	issuedAt := time.Now().Unix()
	payload := fmt.Sprintf("v1.%d.%d.%s.%s", issuedAt, cid, nonce, commentGuardVisitorDigest(visitorID))
	token := payload + "." + commentGuardSign(a.commentGuardSecret(r), "token", payload)
	return token, issuedAt + int64(commentGuardTokenTTL/time.Second), nil
}

func (a *App) consumeCommentGuard(r *http.Request, cid int64) bool {
	if !strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest") || r.Header.Get("X-GopherInk-Comment") != "submit" {
		return false
	}
	token := strings.TrimSpace(r.FormValue("_comment_guard"))
	parts := strings.Split(token, ".")
	if len(parts) != 6 || parts[0] != "v1" {
		return false
	}
	issuedAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	tokenCID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || tokenCID != cid {
		return false
	}
	now := time.Now()
	age := now.Unix() - issuedAt
	if age < 0 || age > int64(commentGuardTokenTTL/time.Second) {
		return false
	}
	payload := strings.Join(parts[:5], ".")
	expected := commentGuardSign(a.commentGuardSecret(r), "token", payload)
	if !hmac.Equal([]byte(expected), []byte(parts[5])) {
		return false
	}
	visitorID, ok := a.commentGuardVisitor(r)
	if !ok || !hmac.Equal([]byte(commentGuardVisitorDigest(visitorID)), []byte(parts[4])) {
		return false
	}

	keyBytes := sha256.Sum256([]byte(token))
	key := base64.RawURLEncoding.EncodeToString(keyBytes[:])
	a.commentGuardMu.Lock()
	defer a.commentGuardMu.Unlock()
	if a.commentGuardUsed == nil {
		a.commentGuardUsed = make(map[string]time.Time)
	}
	for usedKey, expiresAt := range a.commentGuardUsed {
		if !expiresAt.After(now) {
			delete(a.commentGuardUsed, usedKey)
		}
	}
	if _, used := a.commentGuardUsed[key]; used {
		return false
	}
	if len(a.commentGuardUsed) >= commentGuardMaxUsed {
		for usedKey := range a.commentGuardUsed {
			delete(a.commentGuardUsed, usedKey)
			if len(a.commentGuardUsed) < commentGuardMaxUsed {
				break
			}
		}
	}
	a.commentGuardUsed[key] = time.Unix(issuedAt, 0).Add(commentGuardTokenTTL)
	return true
}

func (a *App) commentGuardVisitor(r *http.Request) (string, bool) {
	options := a.cookieOptions(r.Context())
	cookie, err := r.Cookie(options.Name("comment_guard_visitor"))
	if err != nil {
		return "", false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", false
	}
	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > expiresAt {
		return "", false
	}
	payload := strings.Join(parts[:3], ".")
	expected := commentGuardSign(a.commentGuardSecret(r), "visitor", payload)
	if !hmac.Equal([]byte(expected), []byte(parts[3])) {
		return "", false
	}
	return parts[2], true
}

func (a *App) setCommentGuardVisitor(w http.ResponseWriter, r *http.Request, visitorID string) {
	expiresAt := time.Now().Add(commentGuardVisitorTTL)
	payload := fmt.Sprintf("v1.%d.%s", expiresAt.Unix(), visitorID)
	value := payload + "." + commentGuardSign(a.commentGuardSecret(r), "visitor", payload)
	options := a.cookieOptions(r.Context())
	http.SetCookie(w, &http.Cookie{
		Name:     options.Name("comment_guard_visitor"),
		Value:    value,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(commentGuardVisitorTTL / time.Second),
		HttpOnly: true,
		SameSite: options.SameSite,
		Secure:   options.Secure,
	})
}

func (a *App) commentGuardSecret(r *http.Request) string {
	return a.option(r.Context(), "auth_secret", "gopherink")
}

func commentGuardSign(secret, purpose, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("comment-guard\x00" + purpose + "\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func commentGuardVisitorDigest(visitorID string) string {
	sum := sha256.Sum256([]byte(visitorID))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomCommentGuardValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
