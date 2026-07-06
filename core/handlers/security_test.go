package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"goblog/core/models"
	"goblog/core/plugin"
	"goblog/core/services"
	"goblog/pkg/auth"

	_ "github.com/mattn/go-sqlite3"
	_ "goblog/themes/default"
)

func TestSafeNextRejectsExternalURL(t *testing.T) {
	for _, input := range []string{"http://evil.example", "https://evil.example", "//evil.example/path", "admin"} {
		if got := safeNext(input); got != "" {
			t.Fatalf("safeNext(%q) = %q, want empty", input, got)
		}
	}
	if got := safeNext("/admin/posts"); got != "/admin/posts" {
		t.Fatalf("safeNext relative path = %q", got)
	}
}

func TestCSRFTokensBindSubjectAndPurpose(t *testing.T) {
	now := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	admin := signCSRF("secret", "1", "admin", now)
	if admin == signCSRF("secret", "2", "admin", now) {
		t.Fatal("csrf token should differ by subject")
	}
	if admin == signCSRF("secret", "1", "comment", now) {
		t.Fatal("csrf token should differ by purpose")
	}
}

func TestAdminPostRequiresCSRF(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without csrf status = %d, want 403", rec.Code)
	}
}

func TestAdminPostRejectsWrongCSRF(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	form := url.Values{"_csrf": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST with wrong csrf status = %d, want 403", rec.Code)
	}
}

func TestLoginNextCannotRedirectOffsite(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	form := url.Values{
		"name":     {"admin"},
		"password": {"admin123"},
		"next":     {"http://evil.example"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form.Set("_csrf", app.csrfTokenFor(req, "login"))
	req = httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin" {
		t.Fatalf("login redirect = %q, want /admin", loc)
	}
}

func TestPermissionMatrixAndAuthorBoundary(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	contributorID, err := app.Users.Save(ctx, services.SaveUserInput{Name: "contrib", Password: "secret123", Mail: "c@example.com", Role: "contributor"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	editorID, err := app.Users.Save(ctx, services.SaveUserInput{Name: "editor", Password: "secret123", Mail: "e@example.com", Role: "editor"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	subscriberID, err := app.Users.Save(ctx, services.SaveUserInput{Name: "sub", Password: "secret123", Mail: "s@example.com", Role: "subscriber"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Admin Post", Type: models.ContentTypePost, Status: models.ContentStatusPost, AllowComment: true}, adminID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	setSession(t, req, secret, contributorID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("contributor /admin/users status = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/posts/"+itoa(postID)+"/edit", nil)
	setSession(t, req, secret, contributorID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("contributor editing another author's post status = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/options/general", nil)
	setSession(t, req, secret, editorID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor /admin/options/general status = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin", nil)
	setSession(t, req, secret, subscriberID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("subscriber /admin status = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/profile", nil)
	setSession(t, req, secret, subscriberID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscriber /admin/profile status = %d, want 200", rec.Code)
	}
}

func TestContentRoutesRejectTypeMismatch(t *testing.T) {
	app, secret, _ := newSecurityTestApp(t)
	ctx := context.Background()
	contributorID, err := app.Users.Save(ctx, services.SaveUserInput{Name: "contrib", Password: "secret123", Mail: "c@example.com", Role: "contributor"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pageID, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Own Page", Type: models.ContentTypePage, Status: models.ContentStatusPost}, contributorID)
	if err != nil {
		t.Fatal(err)
	}
	attachmentID, err := app.Contents.CreateAttachment(ctx, "asset.txt", "asset", "/uploads/asset.txt", contributorID, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{pageID, attachmentID} {
		req := httptest.NewRequest(http.MethodGet, "/admin/posts/"+itoa(id)+"/edit", nil)
		setSession(t, req, secret, contributorID)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("cross-type /admin/posts/%d/edit status = %d, want 404", id, rec.Code)
		}
	}
}

func TestContributorAttachmentUploadsMustTargetOwnPost(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	contributorID, err := app.Users.Save(ctx, services.SaveUserInput{Name: "contrib", Password: "secret123", Mail: "c@example.com", Role: "contributor"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	ownPostID, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Own Post", Type: models.ContentTypePost, Status: models.ContentStatusPost}, contributorID)
	if err != nil {
		t.Fatal(err)
	}
	otherPostID, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Other Post", Type: models.ContentTypePost, Status: models.ContentStatusPost}, adminID)
	if err != nil {
		t.Fatal(err)
	}

	req := multipartUploadRequest(t, "/admin/medias", map[string]string{"_csrf": adminToken(secret, contributorID)}, "file", "a.txt", "hello")
	setSession(t, req, secret, contributorID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("contributor upload without cid status = %d, want 403", rec.Code)
	}

	req = multipartUploadRequest(t, "/admin/medias", map[string]string{"_csrf": adminToken(secret, contributorID), "cid": itoa(otherPostID)}, "file", "a.txt", "hello")
	setSession(t, req, secret, contributorID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("contributor upload to another post status = %d, want 403", rec.Code)
	}

	req = multipartUploadRequest(t, "/admin/medias", map[string]string{"_csrf": adminToken(secret, contributorID), "cid": itoa(ownPostID)}, "file", "a.txt", "hello")
	setSession(t, req, secret, contributorID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("contributor upload to own post status = %d, want 303", rec.Code)
	}
}

func TestDraftPreviewRequiresSignedToken(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	id, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Draft Preview", Slug: "draft-preview", Text: "draft body", Type: models.ContentTypePost, Status: models.ContentStatusDraft}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := app.Contents.ByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/preview/"+itoa(id), nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preview without token status = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, app.previewURL(req, item), nil)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview with token status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestFuturePostDoesNotLeakInSearchPage(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	_, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:   "Future Secret",
		Slug:    "future-secret",
		Text:    "hidden",
		Type:    models.ContentTypePost,
		Status:  models.ContentStatusPost,
		Created: time.Now().Add(24 * time.Hour).Unix(),
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/search?q=Future", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Future Secret") {
		t.Fatal("future post leaked in search page")
	}
}

func TestRestoreRevisionMustBelongToRouteContent(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	contributorID, err := app.Users.Save(ctx, services.SaveUserInput{Name: "contrib2", Password: "secret123", Mail: "c2@example.com", Role: "contributor"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	ownID, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Own", Slug: "own", Text: "own", Type: models.ContentTypePost, Status: models.ContentStatusPost}, contributorID)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Other v1", Slug: "other", Text: "v1", Type: models.ContentTypePost, Status: models.ContentStatusPost}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Contents.Update(ctx, otherID, services.SaveContentInput{Title: "Other v2", Slug: "other", Text: "v2", Type: models.ContentTypePost, Status: models.ContentStatusPost}); err != nil {
		t.Fatal(err)
	}
	revisions, err := app.Contents.Revisions(ctx, otherID)
	if err != nil || len(revisions) == 0 {
		t.Fatalf("expected other revision, got %v %#v", err, revisions)
	}

	form := url.Values{"_csrf": {adminToken(secret, contributorID)}, "rid": {itoa(revisions[0].RID)}}
	req := httptest.NewRequest(http.MethodPost, "/admin/posts/"+itoa(ownID)+"/restore", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, contributorID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-content restore status = %d, want 403", rec.Code)
	}
	other, err := app.Contents.ByID(ctx, otherID)
	if err != nil {
		t.Fatal(err)
	}
	if other.Title != "Other v2" || other.Text != "v2" {
		t.Fatalf("other post changed after forbidden restore: %#v", other)
	}
}

func TestAutosaveAllowsOnlyPostOrPageAndChecksAuthor(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	contributorID, err := app.Users.Save(ctx, services.SaveUserInput{Name: "contrib3", Password: "secret123", Mail: "c3@example.com", Role: "contributor"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	otherPostID, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Other", Slug: "other-auto", Text: "v1", Type: models.ContentTypePost, Status: models.ContentStatusPost}, adminID)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"_csrf": {adminToken(secret, contributorID)}, "type": {models.ContentTypeAttach}, "title": {"Bad"}, "status": {models.ContentStatusDraft}}
	req := httptest.NewRequest(http.MethodPost, "/admin/autosave", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, contributorID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("autosave attachment status = %d, want 400", rec.Code)
	}
	attachments, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 0 {
		t.Fatalf("autosave created attachment content: %#v", attachments)
	}

	form = url.Values{"_csrf": {adminToken(secret, contributorID)}, "type": {models.ContentTypePost}, "cid": {itoa(otherPostID)}, "title": {"Hijack"}, "status": {models.ContentStatusDraft}}
	req = httptest.NewRequest(http.MethodPost, "/admin/autosave", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, contributorID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("autosave another author's post status = %d, want 403", rec.Code)
	}
}

func newSecurityTestApp(t *testing.T) (*App, string, int64) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := models.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	options := services.NewOptionService(db)
	if err := options.EnsureDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	users := services.NewUserService(db)
	if err := users.EnsureDefaultAdmin(ctx, "admin", "admin123", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	metas := services.NewMetaService(db)
	if err := metas.EnsureDefaultCategory(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := users.ByName(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := options.Get(ctx, "auth_secret")
	if err != nil {
		t.Fatal(err)
	}
	return New(services.NewContentService(db), metas, services.NewCommentService(db), users, options, plugin.Default), secret, admin.UID
}

func setSession(t *testing.T, req *http.Request, secret string, uid int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	auth.SetSession(rec, secret, uid)
	for _, cookie := range rec.Result().Cookies() {
		req.AddCookie(cookie)
	}
}

func multipartUploadRequest(t *testing.T, target string, fields map[string]string, fileField, filename, content string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile(fileField, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func adminToken(secret string, uid int64) string {
	return signCSRF(secret, strconv.FormatInt(uid, 10), "admin", time.Now().UTC())
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
