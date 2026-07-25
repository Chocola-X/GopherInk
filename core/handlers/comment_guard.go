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
	"github.com/Chocola-X/GopherInk/core/plugin"
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
	token := payload + "." + a.commentGuardSign(r, "token", payload)
	return token, issuedAt + int64(commentGuardTokenTTL/time.Second), nil
}

func (a *App) consumeCommentGuard(r *http.Request, cid int64) bool {
	token := strings.TrimSpace(r.FormValue("_comment_guard"))
	valid := a.validateCommentGuardToken(r, cid, token)
	payload := plugin.CommentGuardPayload{
		Request: r,
		CID:     cid,
		Token:   token,
		Valid:   valid,
	}
	out, err := a.Plugins.ApplyActive(r.Context(), plugin.HookCommentGuard, payload)
	if err != nil {
		return false
	}
	next, ok := out.(plugin.CommentGuardPayload)
	if !ok {
		return valid
	}
	if !valid && next.Valid && !next.Handled {
		return false
	}
	return next.Valid
}

func (a *App) validateCommentGuardToken(r *http.Request, cid int64, token string) bool {
	if !strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest") || r.Header.Get("X-GopherInk-Comment") != "submit" {
		return false
	}
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
	expected := a.commentGuardSign(r, "token", payload)
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
	if a.Secrets == nil || !a.Secrets.Verify(r.Context(), "comment-guard:visitor", []byte(payload), parts[3]) {
		return "", false
	}
	return parts[2], true
}

func (a *App) setCommentGuardVisitor(w http.ResponseWriter, r *http.Request, visitorID string) {
	expiresAt := time.Now().Add(commentGuardVisitorTTL)
	payload := fmt.Sprintf("v1.%d.%s", expiresAt.Unix(), visitorID)
	if a.Secrets == nil {
		return
	}
	sig, err := a.Secrets.Sign(r.Context(), "comment-guard:visitor", []byte(payload))
	if err != nil {
		return
	}
	value := payload + "." + sig
	options := a.requestCookieOptions(r)
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

// commentGuardSign returns a purpose-scoped HMAC for the comment-guard cookie
// system. The old implementation used the raw auth_secret option and fell
// back to the string "gopherink" when it was missing, so a leaked or blank
// option would let attackers forge guard tokens. The new implementation
// routes signing through SecretManager.Sign so no fallback constant is ever
// used, and the purpose namespace ("comment-guard:token" or
// "comment-guard:visitor") is added into the derived key.
func (a *App) commentGuardSign(r *http.Request, purpose, payload string) string {
	if a.Secrets == nil {
		return ""
	}
	sig, err := a.Secrets.Sign(r.Context(), "comment-guard:"+purpose, []byte(payload))
	if err != nil {
		return ""
	}
	return sig
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
