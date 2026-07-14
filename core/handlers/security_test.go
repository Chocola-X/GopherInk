package handlers

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
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

	req = httptest.NewRequest(http.MethodGet, "/admin/management", nil)
	setSession(t, req, secret, editorID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor /admin/management status = %d, want 403", rec.Code)
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
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/search/Future" {
		t.Fatalf("search redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	req = httptest.NewRequest(http.MethodGet, "/search/Future", nil)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pretty search status = %d, want 200", rec.Code)
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

func TestNewPostAutosaveReusesDraftWhenPublishing(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()

	req := httptest.NewRequest(http.MethodGet, "/admin/posts/new", nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new post form status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="cid" value=""`) {
		t.Fatalf("new post form should render empty cid, got: %s", body)
	}
	if strings.Contains(body, `name="cid" value="0"`) {
		t.Fatal("new post form rendered cid=0, which prevents autosave from writing back the real id")
	}
	if strings.Contains(body, `id="status"`) {
		t.Fatal("new post form should not render top visibility selector")
	}
	for _, want := range []string{`name="status" value="draft"`, `name="status" value="publish"`, `name="discard" value="1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("new post form missing action button %q", want)
		}
	}

	form := url.Values{
		"_csrf":        {adminToken(secret, adminID)},
		"type":         {models.ContentTypePost},
		"cid":          {""},
		"title":        {"Autosave Draft"},
		"status":       {models.ContentStatusDraft},
		"text":         {"draft body"},
		"allowComment": {"1"},
		"allowFeed":    {"1"},
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/autosave", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("autosave status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK  bool  `json:"ok"`
		CID int64 `json:"cid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.CID <= 0 {
		t.Fatalf("autosave payload = %#v", payload)
	}
	draft, err := app.Contents.ByID(ctx, payload.CID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Status != models.ContentStatusDraft || draft.SlugID != 1 {
		t.Fatalf("autosaved draft = %#v, want draft slugID 1", draft)
	}

	form.Set("cid", itoa(payload.CID))
	form.Set("title", "Published From Autosave")
	form.Set("status", models.ContentStatusPost)
	form.Set("text", "published body")
	req = httptest.NewRequest(http.MethodPost, "/admin/posts/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("publish autosaved draft status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	published, err := app.Contents.ByID(ctx, payload.CID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != models.ContentStatusPost || published.Title != "Published From Autosave" || published.Text != "published body" || published.SlugID != draft.SlugID {
		t.Fatalf("published autosaved draft mismatch: draft=%#v published=%#v", draft, published)
	}
	posts, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePost, Status: "all", IncludeDrafts: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].CID != payload.CID {
		t.Fatalf("content list after publishing autosaved draft = %#v, want only published draft row", posts)
	}
}

func TestContentFormDiscardDeletesStandaloneDraft(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	draftID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:  "Discard Me",
		Text:   "draft body",
		Type:   models.ContentTypePost,
		Status: models.ContentStatusDraft,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf":   {adminToken(secret, adminID)},
		"type":    {models.ContentTypePost},
		"cid":     {itoa(draftID)},
		"discard": {"1"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/posts/"+itoa(draftID)+"/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("discard standalone draft status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if _, err := app.Contents.ByID(ctx, draftID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("discarded draft lookup err = %v, want sql.ErrNoRows", err)
	}
}

func TestNewPageAutosaveReusesDraftWhenPublishing(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()

	req := httptest.NewRequest(http.MethodGet, "/admin/pages/new", nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new page form status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="cid" value=""`) {
		t.Fatalf("new page form should render empty cid, got: %s", body)
	}
	if strings.Contains(body, `name="cid" value="0"`) {
		t.Fatal("new page form rendered cid=0, which prevents autosave from writing back the real id")
	}
	if strings.Contains(body, `id="status"`) {
		t.Fatal("new page form should not render top visibility selector")
	}
	for _, want := range []string{`name="status" value="draft"`, `name="status" value="publish"`, `name="discard" value="1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("new page form missing action button %q", want)
		}
	}

	form := url.Values{
		"_csrf":        {adminToken(secret, adminID)},
		"type":         {models.ContentTypePage},
		"cid":          {""},
		"title":        {"Autosave Page"},
		"status":       {models.ContentStatusDraft},
		"text":         {"draft page body"},
		"allowComment": {"1"},
		"allowFeed":    {"1"},
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/autosave", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("page autosave status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK  bool  `json:"ok"`
		CID int64 `json:"cid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.CID <= 0 {
		t.Fatalf("page autosave payload = %#v", payload)
	}
	draft, err := app.Contents.ByID(ctx, payload.CID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Type != models.ContentTypePage || draft.Status != models.ContentStatusDraft || draft.SlugID != 1 {
		t.Fatalf("autosaved page draft = %#v, want page draft slugID 1", draft)
	}

	form.Set("cid", itoa(payload.CID))
	form.Set("title", "Published Page From Autosave")
	form.Set("status", models.ContentStatusPost)
	form.Set("text", "published page body")
	req = httptest.NewRequest(http.MethodPost, "/admin/pages/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("publish autosaved page status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	published, err := app.Contents.ByID(ctx, payload.CID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Type != models.ContentTypePage || published.Status != models.ContentStatusPost || published.Title != "Published Page From Autosave" || published.Text != "published page body" || published.SlugID != draft.SlugID {
		t.Fatalf("published autosaved page mismatch: draft=%#v published=%#v", draft, published)
	}
	pages, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePage, Status: "all", IncludeDrafts: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].CID != payload.CID {
		t.Fatalf("page list after publishing autosaved draft = %#v, want only published draft row", pages)
	}
}

func TestPublishedPostEditUsesSeparateDraft(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	postID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:        "Original",
		Slug:         "published-edit",
		Text:         "published body",
		Type:         models.ContentTypePost,
		Status:       models.ContentStatusPost,
		AllowComment: true,
		AllowFeed:    true,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"_csrf":        {adminToken(secret, adminID)},
		"type":         {models.ContentTypePost},
		"cid":          {itoa(postID)},
		"title":        {"Edited"},
		"slug":         {"published-edit"},
		"status":       {models.ContentStatusDraft},
		"text":         {"draft body"},
		"allowComment": {"1"},
		"allowFeed":    {"1"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/posts/"+itoa(postID)+"/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save editing draft status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	published, err := app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Title != "Original" || published.Text != "published body" {
		t.Fatalf("published content changed after saving draft: %#v", published)
	}
	draft, err := app.Contents.DraftForContent(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Title != "Edited" || draft.Text != "draft body" || draft.DraftOf != postID {
		t.Fatalf("editing draft mismatch: %#v", draft)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/posts/"+itoa(postID)+"/edit", nil)
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit page status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">draft body</textarea>") {
		t.Fatal("edit page did not render editing draft body")
	}
	if !strings.Contains(body, `name="cid" value="`+itoa(postID)+`"`) {
		t.Fatal("edit page did not keep autosave cid on published content")
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/posts", nil)
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("posts list status = %d, want 200", rec.Code)
	}
	body = rec.Body.String()
	if strings.Contains(body, "/admin/posts/"+itoa(draft.CID)+"/edit") {
		t.Fatal("posts list rendered editing draft as a separate row")
	}
	if !strings.Contains(body, "/admin/posts/"+itoa(postID)+"/edit") || !strings.Contains(body, "/admin/posts/"+itoa(postID)+"/edit?source=published") {
		t.Fatal("posts list did not render both draft and published edit actions")
	}

	form = url.Values{"_csrf": {adminToken(secret, adminID)}}
	req = httptest.NewRequest(http.MethodPost, "/admin/posts/"+itoa(postID)+"/discard", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("discard draft status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if _, err := app.Contents.DraftForContent(ctx, postID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DraftForContent after discard err = %v, want sql.ErrNoRows", err)
	}
	published, err = app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Text != "published body" {
		t.Fatalf("discard changed published content: %#v", published)
	}

	form = url.Values{
		"_csrf":        {adminToken(secret, adminID)},
		"type":         {models.ContentTypePost},
		"cid":          {itoa(postID)},
		"title":        {"Final"},
		"slug":         {"published-edit"},
		"status":       {models.ContentStatusPost},
		"text":         {"final body"},
		"allowComment": {"1"},
		"allowFeed":    {"1"},
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/posts/"+itoa(postID)+"/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("publish editing draft status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	published, err = app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Title != "Final" || published.Text != "final body" {
		t.Fatalf("published content not updated after publishing draft: %#v", published)
	}
	if _, err := app.Contents.DraftForContent(ctx, postID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DraftForContent after publish err = %v, want sql.ErrNoRows", err)
	}
}

func TestPublishedPageEditUsesSeparateDraft(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	pageID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:        "Original Page",
		Slug:         "published-page-edit",
		Text:         "published page body",
		Type:         models.ContentTypePage,
		Status:       models.ContentStatusPost,
		AllowComment: true,
		AllowFeed:    true,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"_csrf":        {adminToken(secret, adminID)},
		"type":         {models.ContentTypePage},
		"cid":          {itoa(pageID)},
		"title":        {"Edited Page"},
		"slug":         {"published-page-edit"},
		"status":       {models.ContentStatusDraft},
		"text":         {"draft page body"},
		"allowComment": {"1"},
		"allowFeed":    {"1"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/pages/"+itoa(pageID)+"/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save page editing draft status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	published, err := app.Contents.ByID(ctx, pageID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Title != "Original Page" || published.Text != "published page body" {
		t.Fatalf("published page changed after saving draft: %#v", published)
	}
	draft, err := app.Contents.DraftForContent(ctx, pageID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Title != "Edited Page" || draft.Text != "draft page body" || draft.DraftOf != pageID || draft.SlugID != published.SlugID {
		t.Fatalf("page editing draft mismatch: published=%#v draft=%#v", published, draft)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/pages", nil)
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pages list status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "/admin/pages/"+itoa(draft.CID)+"/edit") {
		t.Fatal("pages list rendered editing draft as a separate row")
	}
	if !strings.Contains(body, "/admin/pages/"+itoa(pageID)+"/edit") || !strings.Contains(body, "/admin/pages/"+itoa(pageID)+"/edit?source=published") {
		t.Fatal("pages list did not render both draft and published edit actions")
	}
	if !strings.Contains(body, "有编辑草稿") {
		t.Fatal("pages list did not mark published page with editing draft")
	}

	form.Set("title", "Final Page")
	form.Set("status", models.ContentStatusPost)
	form.Set("text", "final page body")
	req = httptest.NewRequest(http.MethodPost, "/admin/pages/"+itoa(pageID)+"/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("publish page editing draft status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	published, err = app.Contents.ByID(ctx, pageID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Title != "Final Page" || published.Text != "final page body" || published.SlugID != draft.SlugID {
		t.Fatalf("published page not updated after publishing draft: %#v", published)
	}
	if _, err := app.Contents.DraftForContent(ctx, pageID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DraftForContent after page publish err = %v, want sql.ErrNoRows", err)
	}
}

func TestPublicCommentWhitelistAndStopWords(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "comments_whitelist", "1"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_post_interval_enable", "0"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_stop_words", "buy now"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "comment-white")
	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: postID, Author: "Known", Mail: "known@example.com", Text: "old", Status: "approved"}, 0); err != nil {
		t.Fatal(err)
	}

	rec := submitPublicComment(t, app, postID, url.Values{
		"author": {"Known"},
		"mail":   {"known@example.com"},
		"text":   {"normal comment"},
	}, "198.51.100.10")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("known commenter status = %d, want 303", rec.Code)
	}

	rec = submitPublicComment(t, app, postID, url.Values{
		"author": {"New"},
		"mail":   {"new@example.com"},
		"text":   {"normal comment"},
	}, "198.51.100.11")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("new commenter status = %d, want 303", rec.Code)
	}

	rec = submitPublicComment(t, app, postID, url.Values{
		"author": {"Spammer"},
		"mail":   {"spam@example.com"},
		"text":   {"please buy now"},
	}, "198.51.100.12")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("spam commenter status = %d, want 303", rec.Code)
	}

	all, err := app.Comments.List(ctx, "all", "", postID)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]int{}
	for _, comment := range all {
		statuses[comment.Status]++
	}
	if statuses["approved"] != 2 || statuses["waiting"] != 1 || statuses["spam"] != 1 {
		t.Fatalf("comment statuses = %#v, want approved=2 waiting=1 spam=1", statuses)
	}
}

func TestLoggedInAdminPublicCommentUsesAccountIdentity(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "comments_moderation_mode", "all"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_require_url", "1"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "admin-comment")
	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: postID, Author: "Guest", Mail: "guest@example.com", Text: "recent", Status: "approved", IP: "198.51.100.90"}, 0); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"cid": {itoa(postID)}, "text": {"administrator reply"}}
	tokenReq := httptest.NewRequest(http.MethodPost, "/comment", strings.NewReader(form.Encode()))
	setSession(t, tokenReq, secret, adminID)
	form.Set("_csrf", app.csrfTokenFor(tokenReq, "comment"))
	req := httptest.NewRequest(http.MethodPost, "/comment", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Real-IP", "198.51.100.90")
	req.Header.Set("Referer", "http://example.com/post/admin-comment.html")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_ok=1") {
		t.Fatalf("logged-in comment response = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	comments, err := app.Comments.List(ctx, "all", "administrator reply", postID)
	if err != nil || len(comments) != 1 {
		t.Fatalf("saved admin comment = %#v, err = %v", comments, err)
	}
	admin, err := app.Users.ByID(ctx, adminID)
	if err != nil {
		t.Fatal(err)
	}
	expectedName := admin.ScreenName
	if expectedName == "" {
		expectedName = admin.Name
	}
	comment := comments[0]
	if comment.AuthorID != adminID || comment.Author != expectedName || comment.Mail != admin.Mail || comment.Status != "approved" {
		t.Fatalf("admin comment identity/status = %#v, user = %#v", comment, admin)
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/post/admin-comment.html", nil)
	setSession(t, pageReq, secret, adminID)
	pageRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(pageRec, pageReq)
	if pageRec.Code != http.StatusOK || strings.Contains(pageRec.Body.String(), "name=\"author\"") || !strings.Contains(pageRec.Body.String(), expectedName) {
		t.Fatalf("logged-in comment form did not use account identity: status=%d body=%s", pageRec.Code, pageRec.Body.String())
	}
}

func TestPublicCommentModerationModesAndContentScopedInterval(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "comments_post_interval_enable", "0"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "moderation-modes")

	for _, test := range []struct {
		mode   string
		author string
		mail   string
		want   string
	}{
		{mode: "open", author: "Open", mail: "open@example.com", want: "approved"},
		{mode: "all", author: "Moderated", mail: "moderated@example.com", want: "waiting"},
		{mode: "approved_author", author: "Unknown", mail: "unknown@example.com", want: "waiting"},
	} {
		if err := app.Options.Set(ctx, "comments_moderation_mode", test.mode); err != nil {
			t.Fatal(err)
		}
		rec := submitPublicComment(t, app, postID, url.Values{"author": {test.author}, "mail": {test.mail}, "text": {"mode " + test.mode}}, "198.51.100."+strconv.Itoa(len(test.author)+20))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("mode %s response = %d", test.mode, rec.Code)
		}
		comments, err := app.Comments.List(ctx, "all", "mode "+test.mode, postID)
		if err != nil || len(comments) != 1 || comments[0].Status != test.want {
			t.Fatalf("mode %s comments = %#v, err = %v, want status %s", test.mode, comments, err, test.want)
		}
	}

	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: postID, Author: "Known", Mail: "known-mode@example.com", Text: "approved history", Status: "approved"}, 0); err != nil {
		t.Fatal(err)
	}
	rec := submitPublicComment(t, app, postID, url.Values{"author": {"Known"}, "mail": {"known-mode@example.com"}, "text": {"known followup"}}, "198.51.100.70")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("known commenter response = %d", rec.Code)
	}
	known, err := app.Comments.List(ctx, "all", "known followup", postID)
	if err != nil || len(known) != 1 || known[0].Status != "approved" {
		t.Fatalf("known commenter comments = %#v, err = %v", known, err)
	}

	if err := app.Options.Set(ctx, "comments_moderation_mode", "open"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_post_interval_enable", "1"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_post_interval", "60"); err != nil {
		t.Fatal(err)
	}
	otherPostID := createPublishedPost(t, app, adminID, "interval-other-post")
	ip := "198.51.100.88"
	first := submitPublicComment(t, app, postID, url.Values{"author": {"Interval"}, "mail": {"interval@example.com"}, "text": {"first content"}}, ip)
	second := submitPublicComment(t, app, otherPostID, url.Values{"author": {"Interval"}, "mail": {"interval@example.com"}, "text": {"other content"}}, ip)
	third := submitPublicComment(t, app, otherPostID, url.Values{"author": {"Interval"}, "mail": {"interval@example.com"}, "text": {"too soon"}}, ip)
	if !strings.Contains(first.Header().Get("Location"), "comment_ok=1") || !strings.Contains(second.Header().Get("Location"), "comment_ok=1") || !strings.Contains(third.Header().Get("Location"), "comment_error=frequent") {
		t.Fatalf("interval redirects = %q, %q, %q", first.Header().Get("Location"), second.Header().Get("Location"), third.Header().Get("Location"))
	}
}

func TestAdminCommentsDefaultToAllAndUseAvatarTemplate(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	templateURL := "https://weavatar.example/avatar/{hash}?s={size}"
	if err := app.Options.Set(ctx, "avatar_url_template", templateURL); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "admin-comment-list")
	for _, input := range []services.SaveCommentInput{
		{CID: postID, Author: "Approved", Mail: "approved@example.com", Text: "approved-visible", Status: "approved"},
		{CID: postID, Author: "Waiting", Mail: "waiting@example.com", Text: "waiting-visible", Status: "waiting"},
	} {
		if err := app.Comments.Save(ctx, input, 0); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/comments", nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	hash := md5.Sum([]byte("waiting@example.com"))
	wantAvatar := "https://weavatar.example/avatar/" + hex.EncodeToString(hash[:]) + "?s=48"
	if rec.Code != http.StatusOK || !strings.Contains(body, "approved-visible") || !strings.Contains(body, "waiting-visible") || !strings.Contains(body, wantAvatar) || !strings.Contains(body, "name=\"status\" label=\"状态\" value=\"all\"") {
		t.Fatalf("default comments page missing all statuses/avatar: status=%d body=%s", rec.Code, body)
	}
}

func TestDiscussionSettingsSynchronizeModerationCompatibilityOptions(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	form := url.Values{
		"_csrf":                         {adminToken(secret, adminID)},
		"comments_moderation_mode":      {"approved_author"},
		"comments_post_interval_enable": {"1"},
		"comments_post_interval":        {"90"},
		"comments_list_size":            {"10"},
		"comments_page_size":            {"20"},
		"comments_max_nesting_levels":   {"3"},
		"avatar_url_template":           {"https://weavatar.example/avatar/{hash}?s={size}"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/options/discussion", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("discussion settings status = %d, want 303: %s", rec.Code, rec.Body.String())
	}

	for key, want := range map[string]string{
		"comments_moderation_mode":    "approved_author",
		"comments_require_moderation": "0",
		"comments_whitelist":          "1",
		"comments_post_interval":      "90",
	} {
		got, err := app.Options.Get(context.Background(), key)
		if err != nil || got != want {
			t.Fatalf("option %s = %q, err = %v, want %q", key, got, err, want)
		}
	}
}

func TestContentLifecycleAndRenderingPluginHooks(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	manager := plugin.NewManager()
	app.Plugins = manager
	ctx := context.Background()
	var events []string
	manager.RegisterHook(plugin.HookContentBeforeSave, func(ctx context.Context, value any) (any, error) {
		events = append(events, "before-save")
		payload := value.(plugin.ContentSavePayload)
		input := payload.Input.(services.SaveContentInput)
		input.Title += " filtered"
		payload.Input = input
		return payload, nil
	})
	manager.RegisterHook(plugin.HookContentAfterSave, func(ctx context.Context, value any) (any, error) {
		events = append(events, "after-save")
		return value, nil
	})
	manager.RegisterHook(plugin.HookContentAfterDraftSave, func(ctx context.Context, value any) (any, error) {
		events = append(events, "after-draft")
		return value, nil
	})
	manager.RegisterHook(plugin.HookContentBeforeStatus, func(ctx context.Context, value any) (any, error) {
		events = append(events, "before-status")
		return value, nil
	})
	manager.RegisterHook(plugin.HookContentAfterStatus, func(ctx context.Context, value any) (any, error) {
		events = append(events, "after-status")
		return value, nil
	})
	manager.RegisterHook(plugin.HookContentBeforeDelete, func(ctx context.Context, value any) (any, error) {
		events = append(events, "before-delete")
		return value, nil
	})
	manager.RegisterHook(plugin.HookContentAfterDelete, func(ctx context.Context, value any) (any, error) {
		events = append(events, "after-delete")
		return value, nil
	})

	id, err := app.saveContentWithHooks(ctx, 0, services.SaveContentInput{Title: "Hooked", Type: models.ContentTypePost, Status: models.ContentStatusDraft}, adminID, "draft")
	if err != nil {
		t.Fatal(err)
	}
	content, err := app.Contents.ByID(ctx, id)
	if err != nil || content.Title != "Hooked filtered" {
		t.Fatalf("filtered content = %#v, err = %v", content, err)
	}
	if err := app.markContentStatus(ctx, id, "hidden"); err != nil {
		t.Fatal(err)
	}
	if err := app.deleteContentWithAttachmentPolicy(ctx, id); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"before-save", "after-save", "after-draft", "before-status", "after-status", "before-delete", "after-delete"}
	if strings.Join(events, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("hook events = %#v, want %#v", events, wantEvents)
	}

	manager.RegisterHook(plugin.HookContentMarkdown, func(ctx context.Context, value any) (any, error) {
		payload := value.(plugin.ContentParserPayload)
		payload.HTML = template.HTML("<strong>plugin markdown</strong>")
		payload.Handled = true
		return payload, nil
	})
	manager.RegisterHook(plugin.HookContentTitle, func(ctx context.Context, value any) (any, error) {
		payload := value.(plugin.ContentTitlePayload)
		payload.Title = "[plugin] " + payload.Title
		return payload, nil
	})
	html, err := app.renderContentHTML(ctx, models.Content{Title: "Render", Text: "**default**", Type: models.ContentTypePost}, nil)
	if err != nil || string(html) != "<strong>plugin markdown</strong>" {
		t.Fatalf("plugin markdown = %q, err = %v", html, err)
	}
	filtered, err := app.filterContentTitle(ctx, models.Content{Title: "Render"})
	if err != nil || filtered.Title != "[plugin] Render" {
		t.Fatalf("plugin title = %#v, err = %v", filtered, err)
	}
}

func TestContentSearchAndReadOnlyFieldPluginHooks(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	manager := plugin.NewManager()
	app.Plugins = manager
	ctx := context.Background()
	id, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:  "Fields",
		Type:   models.ContentTypePost,
		Status: models.ContentStatusDraft,
		Fields: []services.SaveFieldInput{{Name: "protected_value", Type: "str", StrValue: "original"}},
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	manager.RegisterHook(plugin.HookContentFields, func(ctx context.Context, value any) (any, error) {
		payload := value.(plugin.ContentFieldsPayload)
		payload.Fields = append(payload.Fields, plugin.FieldSchema{Name: "plugin_field", Label: "Plugin field", Type: plugin.FieldText, Default: "default"})
		return payload, nil
	})
	manager.RegisterHook(plugin.HookContentFieldReadOnly, func(ctx context.Context, value any) (any, error) {
		payload := value.(plugin.ContentFieldReadOnlyPayload)
		if payload.Name == "protected_value" {
			payload.ReadOnly = true
		}
		return payload, nil
	})
	preserved, err := app.preserveReadOnlyFields(ctx, id, models.ContentTypePost, []services.SaveFieldInput{{Name: "protected_value", Type: "str", StrValue: "changed"}})
	if err != nil || len(preserved) != 1 || preserved[0].StrValue != "original" {
		t.Fatalf("preserved fields = %#v, err = %v", preserved, err)
	}
	existingFields, err := app.Contents.FieldsForContent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	formFields, err := app.contentFormFields(ctx, models.ContentTypePost, id, existingFields)
	if err != nil {
		t.Fatal(err)
	}
	fieldState := map[string]models.Field{}
	for _, field := range formFields {
		fieldState[field.Name] = field
	}
	if !fieldState["protected_value"].ReadOnly || fieldState["plugin_field"].StrValue != "default" {
		t.Fatalf("plugin form fields = %#v", fieldState)
	}

	manager.RegisterHook(plugin.HookContentSearch, func(ctx context.Context, value any) (any, error) {
		payload := value.(plugin.ContentSearchPayload)
		if payload.Stage == "before" {
			payload.Handled = true
			payload.Results = []models.Content{{CID: 99, Title: "External result", Type: models.ContentTypePost, Status: models.ContentStatusPost}}
			payload.Total = 1
		}
		return payload, nil
	})
	results, err := app.listContentsWithSearchHook(ctx, services.ContentQuery{Type: models.ContentTypePost, Keywords: "external"})
	if err != nil || len(results) != 1 || results[0].CID != 99 {
		t.Fatalf("plugin search results = %#v, err = %v", results, err)
	}
}

func TestContributorPublishIsForcedToWaitingAndCannotSetPassword(t *testing.T) {
	app, secret, _ := newSecurityTestApp(t)
	ctx := context.Background()
	contributorID, err := app.Users.Save(ctx, services.SaveUserInput{Name: "writer", Password: "secret123", Mail: "writer@example.com", Role: "contributor"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf":        {adminToken(secret, contributorID)},
		"title":        {"Contributor post"},
		"text":         {"body"},
		"status":       {models.ContentStatusPost},
		"password":     {"should-not-be-stored"},
		"allowComment": {"1"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/posts/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, contributorID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("contributor publish status = %d: %s", rec.Code, rec.Body.String())
	}
	posts, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePost, Status: "all", AuthorID: contributorID, Limit: 10})
	if err != nil || len(posts) != 1 {
		t.Fatalf("contributor posts = %#v, err = %v", posts, err)
	}
	if posts[0].Status != "waiting" || posts[0].Password != "" {
		t.Fatalf("contributor post = %#v, want waiting without password", posts[0])
	}
}

func TestPrivateContentIsVisibleOnlyToItsAuthor(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	id, err := app.Contents.Create(context.Background(), services.SaveContentInput{Title: "Private post", Slug: "private-post", Text: "private body", Type: models.ContentTypePost, Status: "private"}, adminID)
	if err != nil || id <= 0 {
		t.Fatal(err)
	}
	guestReq := httptest.NewRequest(http.MethodGet, "/post/private-post.html", nil)
	guestRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(guestRec, guestReq)
	if guestRec.Code != http.StatusNotFound {
		t.Fatalf("guest private post status = %d, want 404", guestRec.Code)
	}
	authorReq := httptest.NewRequest(http.MethodGet, "/post/private-post.html", nil)
	setSession(t, authorReq, secret, adminID)
	authorRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(authorRec, authorReq)
	if authorRec.Code != http.StatusOK || !strings.Contains(authorRec.Body.String(), "private body") {
		t.Fatalf("author private post response = %d: %s", authorRec.Code, authorRec.Body.String())
	}
}

func TestPublicCommentRefererCheckRequiresCurrentContent(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "comments_post_interval_enable", "0"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "comment-referer")
	form := url.Values{
		"author": {"Ref"},
		"mail":   {"ref@example.com"},
		"text":   {"referer check"},
	}

	rec := submitPublicCommentWithReferer(t, app, postID, form, "198.51.100.40", "")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_error=referer") {
		t.Fatalf("empty referer response = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = submitPublicCommentWithReferer(t, app, postID, form, "198.51.100.41", "http://evil.example/post/comment-referer")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_error=referer") {
		t.Fatalf("wrong host referer response = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = submitPublicCommentWithReferer(t, app, postID, form, "198.51.100.42", "http://example.com/post/other")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_error=referer") {
		t.Fatalf("wrong path referer response = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = submitPublicCommentWithReferer(t, app, postID, form, "198.51.100.43", "http://example.com/post/comment-referer.html?comments_page=1")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_ok=1") {
		t.Fatalf("valid referer response = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPublicCommentIPBlacklistAndParentValidation(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "comments_ip_blacklist", "203.0.113.*"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_post_interval_enable", "0"); err != nil {
		t.Fatal(err)
	}
	firstID := createPublishedPost(t, app, adminID, "first-parent")
	secondID := createPublishedPost(t, app, adminID, "second-parent")
	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: firstID, Author: "Parent", Mail: "p@example.com", Text: "parent", Status: "approved"}, 0); err != nil {
		t.Fatal(err)
	}
	parentComments, err := app.Comments.List(ctx, "approved", "", firstID)
	if err != nil || len(parentComments) == 0 {
		t.Fatalf("parent comment missing: %v %#v", err, parentComments)
	}

	rec := submitPublicComment(t, app, secondID, url.Values{
		"author": {"Blocked"},
		"mail":   {"b@example.com"},
		"text":   {"blocked"},
	}, "203.0.113.9")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_error=blocked") {
		t.Fatalf("blacklist response = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = submitPublicComment(t, app, secondID, url.Values{
		"author": {"Child"},
		"mail":   {"c@example.com"},
		"text":   {"cross parent"},
		"parent": {itoa(parentComments[0].COID)},
	}, "198.51.100.20")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_error=parent") {
		t.Fatalf("cross-parent response = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	secondComments, err := app.Comments.List(ctx, "all", "", secondID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondComments) != 0 {
		t.Fatalf("unexpected second post comments: %#v", secondComments)
	}
}

func TestCommentPaginationKeepsThreadReplies(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "comments_page_size", "1"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_page_display", "first"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "comment-thread-page")
	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: postID, Author: "Root One", Mail: "r1@example.com", Text: "root one", Status: "approved"}, 0); err != nil {
		t.Fatal(err)
	}
	roots, err := app.Comments.List(ctx, "approved", "", postID)
	if err != nil || len(roots) == 0 {
		t.Fatalf("root comment missing: %v %#v", err, roots)
	}
	rootOneID := roots[len(roots)-1].COID
	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: postID, Author: "Reply", Mail: "reply@example.com", Text: "reply", Status: "approved", Parent: rootOneID}, 0); err != nil {
		t.Fatal(err)
	}
	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: postID, Author: "Root Two", Mail: "r2@example.com", Text: "root two", Status: "approved"}, 0); err != nil {
		t.Fatal(err)
	}
	post, err := app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/post/comment-thread-page?comments_page=1", nil)
	views, pager, err := app.commentsForPost(req, post)
	if err != nil {
		t.Fatal(err)
	}
	if pager.Total != 2 || pager.TotalPages != 2 {
		t.Fatalf("pager = %#v, want 2 top-level threads over 2 pages", pager)
	}
	if len(views) != 2 || views[0].Author != "Root One" || views[1].Author != "Reply" || views[1].Parent != rootOneID {
		t.Fatalf("page 1 views = %#v, want root one with reply", views)
	}

	req = httptest.NewRequest(http.MethodGet, "/post/comment-thread-page?comments_page=2", nil)
	views, _, err = app.commentsForPost(req, post)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Author != "Root Two" {
		t.Fatalf("page 2 views = %#v, want only root two", views)
	}
}

func TestPublicCommentNestingDepth(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "comments_max_nesting_levels", "2"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_post_interval_enable", "0"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "comment-depth")
	rootID, err := app.Comments.SaveReturningID(ctx, services.SaveCommentInput{CID: postID, Author: "Root", Mail: "root@example.com", Text: "root", Status: "approved"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	parentID, err := app.Comments.SaveReturningID(ctx, services.SaveCommentInput{CID: postID, Author: "Parent", Mail: "p@example.com", Text: "parent", Status: "approved", Parent: rootID}, 0)
	if err != nil {
		t.Fatal(err)
	}

	rec := submitPublicComment(t, app, postID, url.Values{
		"author": {"Child"},
		"mail":   {"c@example.com"},
		"text":   {"normalized child"},
		"parent": {itoa(parentID)},
	}, "198.51.100.30")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_ok=1") {
		t.Fatalf("depth response = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	comments, err := app.Comments.List(ctx, "approved", "", postID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, comment := range comments {
		if comment.Text == "normalized child" && comment.Parent != rootID {
			t.Fatalf("normalized parent = %d, want root %d", comment.Parent, rootID)
		}
		if comment.Text == "normalized child" {
			found = true
		}
	}
	if !found {
		t.Fatal("normalized child comment was not saved")
	}
}

func TestWaitingCommentIsVisibleOnlyToSubmitter(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "comments_moderation_mode", "all"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_post_interval_enable", "0"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "waiting-self-visible")
	rec := submitPublicComment(t, app, postID, url.Values{
		"author": {"Pending Reader"},
		"mail":   {"pending@example.com"},
		"text":   {"private pending text"},
	}, "198.51.100.31")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_status=waiting") {
		t.Fatalf("waiting response = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	post, err := app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/post/waiting-self-visible.html", nil)
	var visibilityCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		request.AddCookie(cookie)
		if strings.HasSuffix(cookie.Name, "unapproved_comment") {
			copy := *cookie
			visibilityCookie = &copy
		}
	}
	if visibilityCookie == nil {
		t.Fatal("signed unapproved comment cookie was not set")
	}
	views, _, err := app.commentsForPost(request, post)
	if err != nil || len(views) != 1 || views[0].Text != "private pending text" || !views[0].Pending {
		t.Fatalf("submitter views = %#v, err = %v", views, err)
	}

	request = httptest.NewRequest(http.MethodGet, "/post/waiting-self-visible.html", nil)
	views, _, err = app.commentsForPost(request, post)
	if err != nil || len(views) != 0 {
		t.Fatalf("anonymous outsider views = %#v, err = %v", views, err)
	}

	visibilityCookie.Value += "tampered"
	request = httptest.NewRequest(http.MethodGet, "/post/waiting-self-visible.html", nil)
	request.AddCookie(visibilityCookie)
	views, _, err = app.commentsForPost(request, post)
	if err != nil || len(views) != 0 {
		t.Fatalf("tampered cookie views = %#v, err = %v", views, err)
	}
}

func TestCommentPluginLifecycleAndRenderHooks(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	manager := plugin.NewManager()
	app.Plugins = manager
	ctx := context.Background()
	postID := createPublishedPost(t, app, adminID, "comment-hooks")
	var events []string
	manager.RegisterHook(plugin.HookCommentBeforeSave, func(ctx context.Context, value any) (any, error) {
		events = append(events, "before-save")
		payload := value.(plugin.CommentSavePayload)
		input := payload.Input.(services.SaveCommentInput)
		input.Text += " filtered"
		payload.Input = input
		return payload, nil
	})
	manager.RegisterHook(plugin.HookCommentAfterSave, func(ctx context.Context, value any) (any, error) {
		events = append(events, "after-save")
		if value.(plugin.CommentSavePayload).Comment == nil {
			t.Fatal("after-save hook did not receive saved comment")
		}
		return value, nil
	})
	manager.RegisterHook(plugin.HookCommentBeforeReply, func(ctx context.Context, value any) (any, error) {
		events = append(events, "before-reply")
		return value, nil
	})
	manager.RegisterHook(plugin.HookCommentAfterReply, func(ctx context.Context, value any) (any, error) {
		events = append(events, "after-reply")
		return value, nil
	})
	manager.RegisterHook(plugin.HookCommentBeforeMark, func(ctx context.Context, value any) (any, error) {
		events = append(events, "before-mark")
		return value, nil
	})
	manager.RegisterHook(plugin.HookCommentAfterMark, func(ctx context.Context, value any) (any, error) {
		events = append(events, "after-mark")
		return value, nil
	})
	manager.RegisterHook(plugin.HookCommentBeforeDelete, func(ctx context.Context, value any) (any, error) {
		events = append(events, "before-delete")
		return value, nil
	})
	manager.RegisterHook(plugin.HookCommentAfterDelete, func(ctx context.Context, value any) (any, error) {
		events = append(events, "after-delete")
		return value, nil
	})

	payload, err := app.saveCommentWithHooks(ctx, services.SaveCommentInput{CID: postID, Author: "Admin", Text: "reply", Status: "waiting", Agent: strings.Repeat("界", 600)}, 0, "reply", nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := app.Comments.ByID(ctx, payload.ID)
	if err != nil || saved.Text != "reply filtered" || len([]rune(saved.Agent)) != 511 {
		t.Fatalf("saved hooked comment = %#v, err = %v", saved, err)
	}
	if err := app.markCommentWithHooks(ctx, saved.COID, "approved"); err != nil {
		t.Fatal(err)
	}
	content, err := app.Contents.ByID(ctx, postID)
	if err != nil || content.CommentsNum != 1 {
		t.Fatalf("incremental comment count = %d, err = %v", content.CommentsNum, err)
	}

	manager.RegisterHook(plugin.HookCommentFilter, func(ctx context.Context, value any) (any, error) {
		payload := value.(plugin.CommentFilterPayload)
		comment := payload.Comment.(models.Comment)
		comment.Author = "Filtered Author"
		payload.Comment = comment
		return payload, nil
	})
	manager.RegisterHook(plugin.HookCommentMarkdown, func(ctx context.Context, value any) (any, error) {
		payload := value.(plugin.CommentParserPayload)
		payload.HTML = template.HTML("<strong>plugin comment</strong>")
		payload.Handled = true
		return payload, nil
	})
	manager.RegisterHook(plugin.HookCommentAvatar, func(ctx context.Context, value any) (any, error) {
		payload := value.(plugin.CommentAvatarPayload)
		payload.URL = "/plugin-avatar.png"
		return payload, nil
	})
	if err := app.Options.Set(ctx, "comments_markdown", "1"); err != nil {
		t.Fatal(err)
	}
	view := app.commentView(httptest.NewRequest(http.MethodGet, "/", nil), saved, 0)
	if view.Author != "Filtered Author" || string(view.BodyHTML) != "<strong>plugin comment</strong>" || view.AvatarURL != "/plugin-avatar.png" {
		t.Fatalf("hooked comment view = %#v", view)
	}
	if err := app.deleteCommentWithHooks(ctx, saved.COID); err != nil {
		t.Fatal(err)
	}
	want := "before-save,before-reply,after-save,after-reply,before-mark,after-mark,before-delete,after-delete"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("comment hook events = %q, want %q", got, want)
	}
}

func TestAdminCommentsPagination(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "comments_list_size", "2"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "admin-comment-pages")
	for _, text := range []string{"oldest-comment", "middle-comment", "newest-comment"} {
		if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: postID, Author: "Reader", Text: text, Status: "approved"}, 0); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/comments?page=1", nil)
	setSession(t, request, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, request)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "newest-comment") || !strings.Contains(body, "middle-comment") || strings.Contains(body, "oldest-comment") || !strings.Contains(body, "共 3 条") {
		t.Fatalf("admin comments page 1 status=%d body=%s", rec.Code, body)
	}
	request = httptest.NewRequest(http.MethodGet, "/admin/comments?page=2", nil)
	setSession(t, request, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, request)
	if body = rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "oldest-comment") || strings.Contains(body, "newest-comment") {
		t.Fatalf("admin comments page 2 status=%d body=%s", rec.Code, body)
	}
}

func TestAdminCommentBatchAndClearSpam(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	postID := createPublishedPost(t, app, adminID, "comment-batch")
	for _, input := range []services.SaveCommentInput{
		{CID: postID, Author: "One", Mail: "one@example.com", Text: "one", Status: "waiting"},
		{CID: postID, Author: "Two", Mail: "two@example.com", Text: "two", Status: "waiting"},
		{CID: postID, Author: "Spam", Mail: "spam@example.com", Text: "spam", Status: "spam"},
	} {
		if err := app.Comments.Save(ctx, input, 0); err != nil {
			t.Fatal(err)
		}
	}
	all, err := app.Comments.List(ctx, "all", "", postID)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {adminToken(secret, adminID)}, "action": {"approved"}}
	for _, comment := range all {
		if comment.Status == "waiting" {
			form.Add("id", itoa(comment.COID))
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/comments/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("batch approve status = %d, want 303", rec.Code)
	}
	approved, err := app.Comments.List(ctx, "approved", "", postID)
	if err != nil {
		t.Fatal(err)
	}
	if len(approved) != 2 {
		t.Fatalf("approved comments = %d, want 2", len(approved))
	}

	form = url.Values{"_csrf": {adminToken(secret, adminID)}, "action": {"spam"}}
	for _, comment := range approved {
		form.Add("id", itoa(comment.COID))
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/comments/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("batch spam status = %d, want 303", rec.Code)
	}
	spam, err := app.Comments.List(ctx, "spam", "", postID)
	if err != nil {
		t.Fatal(err)
	}
	if len(spam) != 3 {
		t.Fatalf("spam comments = %d, want 3", len(spam))
	}

	form = url.Values{"_csrf": {adminToken(secret, adminID)}, "action": {"delete"}}
	form.Add("id", itoa(spam[0].COID))
	req = httptest.NewRequest(http.MethodPost, "/admin/comments/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("batch delete status = %d, want 303", rec.Code)
	}
	spam, err = app.Comments.List(ctx, "spam", "", postID)
	if err != nil {
		t.Fatal(err)
	}
	if len(spam) != 2 {
		t.Fatalf("spam comments after delete = %d, want 2", len(spam))
	}

	form = url.Values{"_csrf": {adminToken(secret, adminID)}}
	req = httptest.NewRequest(http.MethodPost, "/admin/comments/clear-spam", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear spam status = %d, want 303", rec.Code)
	}
	spam, err = app.Comments.List(ctx, "spam", "", postID)
	if err != nil {
		t.Fatal(err)
	}
	if len(spam) != 0 {
		t.Fatalf("spam comments remain: %#v", spam)
	}
}

func TestCommentHTMLAllowListSanitizesTags(t *testing.T) {
	got := string(sanitizeCommentHTML(`<strong>ok</strong><script>alert(1)</script><a href="example.com" onclick="x">site</a>`, "strong,a", true))
	if !strings.Contains(got, "<strong>ok</strong>") {
		t.Fatalf("strong tag not preserved: %s", got)
	}
	if strings.Contains(got, "<script>") || strings.Contains(got, "onclick") {
		t.Fatalf("unsafe html preserved: %s", got)
	}
	if !strings.Contains(got, `<a href="https://example.com" rel="nofollow">site</a>`) {
		t.Fatalf("safe link not normalized: %s", got)
	}
	for _, raw := range []string{`<a href="javascript:alert(1)">x</a>`, `<a href="data:text/html,x">x</a>`} {
		got = string(sanitizeCommentHTML(raw, "a", true))
		if strings.Contains(got, "javascript:") || strings.Contains(got, "data:text") || strings.Contains(got, "href=") {
			t.Fatalf("dangerous href preserved for %q: %s", raw, got)
		}
	}
}

func TestMediaUploadStoresMetadataUnderContentIDAndDeleteRemovesFile(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	postID := createPublishedPost(t, app, adminID, "media-parent")
	png := tinyPNG(t)
	req := multipartUploadRequestBytes(t, "/admin/medias", map[string]string{"_csrf": adminToken(secret, adminID), "cid": itoa(postID)}, "file", "photo.png", png)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("media upload status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	attachments, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(attachments))
	}
	meta := parseAttachmentMeta(attachments[0])
	wantBucket := "posts/" + itoa(postID) + "/"
	if !strings.HasPrefix(meta.Path, wantBucket) || !strings.HasPrefix(meta.URL, "/uploads/"+wantBucket) {
		t.Fatalf("attachment path/url = %#v, want post bucket %q", meta, wantBucket)
	}
	if !meta.IsImage || meta.Width != 1 || meta.Height != 1 || meta.MIME != "image/png" {
		t.Fatalf("image metadata = %#v, want 1x1 image/png", meta)
	}
	fullPath := filepath.Join(app.UploadDir, filepath.FromSlash(meta.Path))
	if _, err := os.Stat(fullPath); err != nil {
		t.Fatalf("uploaded file missing: %v", err)
	}

	form := url.Values{"_csrf": {adminToken(secret, adminID)}}
	req = httptest.NewRequest(http.MethodPost, "/admin/medias/"+itoa(attachments[0].CID)+"/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("media delete status = %d, want 303", rec.Code)
	}
	if _, err := os.Stat(fullPath); !os.IsNotExist(err) {
		t.Fatalf("uploaded file still exists after delete: %v", err)
	}
}

func TestPageMediaUploadUsesPagesBucket(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	pageID := createPublishedPage(t, app, adminID, "media-page")
	_, meta := uploadMedia(t, app, secret, adminID, pageID, "cover.png", tinyPNG(t))
	wantBucket := "pages/" + itoa(pageID) + "/"
	if !strings.HasPrefix(meta.Path, wantBucket) || !strings.HasPrefix(meta.URL, "/uploads/"+wantBucket) {
		t.Fatalf("page attachment path/url = %#v, want page bucket %q", meta, wantBucket)
	}
	if _, err := os.Stat(filepath.Join(app.UploadDir, filepath.FromSlash(meta.Path))); err != nil {
		t.Fatalf("page attachment file missing: %v", err)
	}
}

func TestAdminManagementUploadUsesSeparateSettingsBucket(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	before, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	upload := func(filename string) string {
		t.Helper()
		req := multipartUploadRequestBytes(t, "/admin/management/upload", map[string]string{"_csrf": adminToken(secret, adminID)}, "file", filename, tinyPNG(t))
		setSession(t, req, secret, adminID)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("admin management upload status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var payload struct {
			OK  bool   `json:"ok"`
			URL string `json:"url"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if !payload.OK {
			t.Fatalf("admin management upload payload = %#v, want ok", payload)
		}
		return payload.URL
	}
	usedURL := upload("background.png")
	unusedURL := upload("old-background.png")
	prefix := "/uploads/" + adminSettingsUploadBucket + "/"
	if !strings.HasPrefix(usedURL, prefix) || !strings.HasPrefix(unusedURL, prefix) {
		t.Fatalf("admin management upload URLs = %q %q, want %s bucket", usedURL, unusedURL, prefix)
	}
	relPath := strings.TrimPrefix(usedURL, "/uploads/")
	if _, err := os.Stat(filepath.Join(app.UploadDir, filepath.FromSlash(relPath))); err != nil {
		t.Fatalf("admin settings upload file missing: %v", err)
	}
	after, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("admin settings upload created attachment rows: before=%d after=%d", len(before), len(after))
	}
	if err := app.setOptionJSONForUser(ctx, adminAppearanceOptionKey, map[string]string{"desktop_background": usedURL}, 0); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {adminToken(secret, adminID)}}
	req := httptest.NewRequest(http.MethodPost, "/admin/management/assets/clean", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin asset clean status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	usedPath := filepath.Join(app.UploadDir, filepath.FromSlash(strings.TrimPrefix(usedURL, "/uploads/")))
	unusedPath := filepath.Join(app.UploadDir, filepath.FromSlash(strings.TrimPrefix(unusedURL, "/uploads/")))
	if _, err := os.Stat(usedPath); err != nil {
		t.Fatalf("used admin asset should remain: %v", err)
	}
	if _, err := os.Stat(unusedPath); !os.IsNotExist(err) {
		t.Fatalf("unused admin asset should be removed: %v", err)
	}
	form = url.Values{"_csrf": {adminToken(secret, adminID)}, "name": {strings.TrimPrefix(usedURL, prefix)}}
	req = httptest.NewRequest(http.MethodPost, "/admin/management/assets/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin asset delete status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(usedPath); !os.IsNotExist(err) {
		t.Fatalf("admin asset should be deleted: %v", err)
	}
}

func TestDefaultThemeUploadUsesSeparateSettingsBucket(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	before, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	upload := func(filename string) string {
		t.Helper()
		req := multipartUploadRequestBytes(t, "/admin/themes/default/upload", map[string]string{"_csrf": adminToken(secret, adminID)}, "file", filename, tinyPNG(t))
		setSession(t, req, secret, adminID)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("default theme upload status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var payload struct {
			OK  bool   `json:"ok"`
			URL string `json:"url"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if !payload.OK {
			t.Fatalf("default theme upload payload = %#v, want ok", payload)
		}
		return payload.URL
	}
	usedURL := upload("theme-cover.png")
	unusedURL := upload("theme-unused.png")
	prefix := "/uploads/" + themeSettingsUploadBucket + "/"
	if !strings.HasPrefix(usedURL, prefix) || !strings.HasPrefix(unusedURL, prefix) {
		t.Fatalf("theme upload URLs = %q %q, want %s bucket", usedURL, unusedURL, prefix)
	}
	after, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("theme settings upload created attachment rows: before=%d after=%d", len(before), len(after))
	}
	if err := app.setOptionJSONForUser(ctx, themeOptionKey("default"), map[string]string{"background_image": usedURL}, 0); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {adminToken(secret, adminID)}}
	req := httptest.NewRequest(http.MethodPost, "/admin/themes/default/assets/clean", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("theme asset clean status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	usedPath := filepath.Join(app.UploadDir, filepath.FromSlash(strings.TrimPrefix(usedURL, "/uploads/")))
	unusedPath := filepath.Join(app.UploadDir, filepath.FromSlash(strings.TrimPrefix(unusedURL, "/uploads/")))
	if _, err := os.Stat(usedPath); err != nil {
		t.Fatalf("used theme asset should remain: %v", err)
	}
	if _, err := os.Stat(unusedPath); !os.IsNotExist(err) {
		t.Fatalf("unused theme asset should be removed: %v", err)
	}
	form = url.Values{"_csrf": {adminToken(secret, adminID)}, "name": {strings.TrimPrefix(usedURL, prefix)}}
	req = httptest.NewRequest(http.MethodPost, "/admin/themes/default/assets/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("theme asset delete status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(usedPath); !os.IsNotExist(err) {
		t.Fatalf("theme asset should be deleted: %v", err)
	}
}

func TestEditorMediaSourceFiltering(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	postA := createPublishedPost(t, app, adminID, "media-source-a")
	postB := createPublishedPost(t, app, adminID, "media-source-b")
	createAttachmentMeta(t, app, adminID, postA, models.AttachmentMeta{Name: "a.png", URL: "/uploads/posts/a/a.png", Path: "posts/a/a.png", Type: "png", MIME: "image/png", IsImage: true, Size: 12})
	createAttachmentMeta(t, app, adminID, postB, models.AttachmentMeta{Name: "b.pdf", URL: "/uploads/posts/b/b.pdf", Path: "posts/b/b.pdf", Type: "pdf", MIME: "application/pdf", Size: 34})
	createAttachmentMeta(t, app, adminID, 0, models.AttachmentMeta{Name: "loose.zip", URL: "/uploads/unattached/loose.zip", Path: "unattached/loose.zip", Type: "zip", MIME: "application/zip", Size: 56})

	getItems := func(target string) []editorMediaItem {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		setSession(t, req, secret, adminID)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("editor media status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var payload struct {
			OK    bool              `json:"ok"`
			Items []editorMediaItem `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if !payload.OK {
			t.Fatalf("editor media payload not ok: %#v", payload)
		}
		return payload.Items
	}

	if items := getItems("/admin/medias/editor?source=__none"); len(items) != 0 {
		t.Fatalf("none source items = %#v, want empty", items)
	}
	items := getItems("/admin/medias/editor?source=__unattached")
	if len(items) != 1 || items[0].Name != "loose.zip" || items[0].Icon != "folder_zip" {
		t.Fatalf("unattached source items = %#v, want loose zip", items)
	}
	items = getItems("/admin/medias/editor?source=content:" + itoa(postB))
	if len(items) != 1 || items[0].Name != "b.pdf" || items[0].Icon != "picture_as_pdf" || !strings.Contains(items[0].Markdown, "[b.pdf]") {
		t.Fatalf("post source items = %#v, want pdf markdown item", items)
	}
	if items[0].RelativeURL != "/uploads/posts/b/b.pdf" || items[0].AbsoluteURL != "http://localhost:8080/uploads/posts/b/b.pdf" {
		t.Fatalf("post source URLs = relative %q absolute %q", items[0].RelativeURL, items[0].AbsoluteURL)
	}
	items = getItems("/admin/medias/editor?source=current&parent=" + itoa(postA))
	if len(items) != 1 || items[0].Name != "a.png" || !items[0].IsImage {
		t.Fatalf("current source items = %#v, want image item", items)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/posts/new", nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new post form status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "loose.zip") || strings.Contains(body, "b.pdf") {
		t.Fatalf("new post form should not render existing media by default: %s", body)
	}
	if !strings.Contains(body, "孤立附件") || !strings.Contains(body, "文章 #"+itoa(postA)) {
		t.Fatalf("new post form missing media source options: %s", body)
	}
	if !strings.Contains(body, `data-markdown-editor`) || !strings.Contains(body, `data-md-action="bold"`) || !strings.Contains(body, `markdown-preview-toggle`) {
		t.Fatalf("new post form missing markdown editor controls: %s", body)
	}
}

func TestAdminMarkdownPreviewRendersMarkdown(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	form := url.Values{
		"_csrf": {adminToken(secret, adminID)},
		"type":  {models.ContentTypePost},
		"title": {"Preview"},
		"text":  {"# Preview\n\n<script>alert(1)</script>\n\n**bold**"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/markdown/preview", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("markdown preview status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK   bool   `json:"ok"`
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || !strings.Contains(payload.HTML, "<h1") || !strings.Contains(payload.HTML, "<strong>bold</strong>") {
		t.Fatalf("markdown preview payload = %#v", payload)
	}
	if strings.Contains(payload.HTML, "<script>") {
		t.Fatalf("markdown preview rendered unsafe script: %s", payload.HTML)
	}
}

func TestMediaUploadRejectsDangerousExtension(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	postID := createPublishedPost(t, app, adminID, "media-danger")
	req := multipartUploadRequestBytes(t, "/admin/medias", map[string]string{"_csrf": adminToken(secret, adminID), "cid": itoa(postID)}, "file", "avatar.jpg.php", []byte("<?php echo 1;"))
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("dangerous upload status = %d, want 400", rec.Code)
	}
}

func TestMediaUploadSanitizesTraversalNameAndEnforcesSize(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	postID := createPublishedPost(t, app, adminID, "media-traversal")
	if err := app.Options.Set(ctx, "upload_max_size", "8"); err != nil {
		t.Fatal(err)
	}
	req := multipartUploadRequestBytes(t, "/admin/medias", map[string]string{"_csrf": adminToken(secret, adminID), "cid": itoa(postID)}, "file", "big.txt", []byte("this is too large"))
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized upload status = %d, want 400", rec.Code)
	}
	if err := app.Options.Set(ctx, "upload_max_size", "10485760"); err != nil {
		t.Fatal(err)
	}
	req = multipartUploadRequestBytes(t, "/admin/medias", map[string]string{"_csrf": adminToken(secret, adminID), "cid": itoa(postID)}, "file", "../../evil.png", tinyPNG(t))
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("traversal upload status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	attachments, err := app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(attachments))
	}
	meta := parseAttachmentMeta(attachments[0])
	if strings.Contains(meta.Path, "..") || !strings.HasPrefix(meta.Path, "posts/"+itoa(postID)+"/") || meta.Name != "evil.png" {
		t.Fatalf("unsafe sanitized metadata: %#v", meta)
	}
}

func TestMediaReplaceSameExtensionPolicy(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	postID := createPublishedPost(t, app, adminID, "media-replace")
	attachment, meta := uploadMedia(t, app, secret, adminID, postID, "photo.png", tinyPNG(t))
	oldPath := filepath.Join(app.UploadDir, filepath.FromSlash(meta.Path))

	req := multipartUploadRequestBytes(t, "/admin/medias/"+itoa(attachment.CID)+"/replace", map[string]string{"_csrf": adminToken(secret, adminID)}, "file", "note.txt", []byte("plain text"))
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("replace different ext status = %d, want 400", rec.Code)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old file removed after failed replace: %v", err)
	}

	req = multipartUploadRequestBytes(t, "/admin/medias/"+itoa(attachment.CID)+"/replace", map[string]string{"_csrf": adminToken(secret, adminID)}, "file", "new.png", tinyPNG(t))
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("replace same ext status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("stable replacement path missing after successful replace: %v", err)
	}
	updated, err := app.Contents.ByID(ctx, attachment.CID)
	if err != nil {
		t.Fatal(err)
	}
	updatedMeta := parseAttachmentMeta(updated)
	if updatedMeta.Path != meta.Path || updatedMeta.URL != meta.URL || updatedMeta.Type != "png" {
		t.Fatalf("updated metadata = %#v, old = %#v", updatedMeta, meta)
	}
}

func TestContentDeleteAttachmentPolicy(t *testing.T) {
	t.Run("keep", func(t *testing.T) {
		app, secret, adminID := newSecurityTestApp(t)
		app.UploadDir = t.TempDir()
		ctx := context.Background()
		postID := createPublishedPost(t, app, adminID, "delete-keep")
		attachment, meta := uploadMedia(t, app, secret, adminID, postID, "photo.png", tinyPNG(t))
		oldPath := filepath.Join(app.UploadDir, filepath.FromSlash(meta.Path))
		deleteContentRequest(t, app, secret, adminID, postID)
		updated, err := app.Contents.ByID(ctx, attachment.CID)
		if err != nil {
			t.Fatalf("attachment record should be kept: %v", err)
		}
		if updated.Parent != 0 {
			t.Fatalf("attachment parent = %d, want detached", updated.Parent)
		}
		updatedMeta := parseAttachmentMeta(updated)
		wantBucket := "unattached/" + itoa(postID) + "-post/"
		if !strings.HasPrefix(updatedMeta.Path, wantBucket) || !strings.HasPrefix(updatedMeta.URL, "/uploads/"+wantBucket) {
			t.Fatalf("detached attachment metadata = %#v, want bucket %q", updatedMeta, wantBucket)
		}
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Fatalf("old attachment file should move away: %v", err)
		}
		if _, err := os.Stat(filepath.Join(app.UploadDir, filepath.FromSlash(updatedMeta.Path))); err != nil {
			t.Fatalf("detached attachment file should be kept: %v", err)
		}
	})
	t.Run("keep-page", func(t *testing.T) {
		app, secret, adminID := newSecurityTestApp(t)
		app.UploadDir = t.TempDir()
		ctx := context.Background()
		pageID := createPublishedPage(t, app, adminID, "delete-page-keep")
		attachment, meta := uploadMedia(t, app, secret, adminID, pageID, "cover.png", tinyPNG(t))
		oldPath := filepath.Join(app.UploadDir, filepath.FromSlash(meta.Path))
		deletePageRequest(t, app, secret, adminID, pageID)
		updated, err := app.Contents.ByID(ctx, attachment.CID)
		if err != nil {
			t.Fatalf("page attachment record should be kept: %v", err)
		}
		if updated.Parent != 0 {
			t.Fatalf("page attachment parent = %d, want detached", updated.Parent)
		}
		updatedMeta := parseAttachmentMeta(updated)
		wantBucket := "unattached/" + itoa(pageID) + "-pages/"
		if !strings.HasPrefix(updatedMeta.Path, wantBucket) || !strings.HasPrefix(updatedMeta.URL, "/uploads/"+wantBucket) {
			t.Fatalf("detached page attachment metadata = %#v, want bucket %q", updatedMeta, wantBucket)
		}
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Fatalf("old page attachment file should move away: %v", err)
		}
		if _, err := os.Stat(filepath.Join(app.UploadDir, filepath.FromSlash(updatedMeta.Path))); err != nil {
			t.Fatalf("detached page attachment file should be kept: %v", err)
		}
	})
	t.Run("file", func(t *testing.T) {
		app, secret, adminID := newSecurityTestApp(t)
		app.UploadDir = t.TempDir()
		ctx := context.Background()
		if err := app.Options.Set(ctx, "attachment_delete_policy", "file"); err != nil {
			t.Fatal(err)
		}
		postID := createPublishedPost(t, app, adminID, "delete-file")
		attachment, meta := uploadMedia(t, app, secret, adminID, postID, "photo.png", tinyPNG(t))
		deleteContentRequest(t, app, secret, adminID, postID)
		if _, err := app.Contents.ByID(ctx, attachment.CID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("attachment record should be deleted, err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(app.UploadDir, filepath.FromSlash(meta.Path))); !os.IsNotExist(err) {
			t.Fatalf("attachment file should be deleted: %v", err)
		}
	})
}

func TestBackupExportImportRoundTrip(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	postID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:        "Backup Post",
		Slug:         "backup-post",
		Text:         "body",
		Type:         models.ContentTypePost,
		Status:       models.ContentStatusPost,
		AllowComment: true,
		CategoryIDs:  []int64{1},
		Fields:       []services.SaveFieldInput{{Name: "source", Type: "str", StrValue: "backup"}},
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if postID <= 0 {
		t.Fatal("expected ids")
	}
	draftID, err := app.Contents.SaveEditingDraft(ctx, postID, services.SaveContentInput{
		Title:        "Backup Post Edited",
		Slug:         "backup-post",
		Text:         "draft body",
		Type:         models.ContentTypePost,
		Status:       models.ContentStatusDraft,
		AllowComment: true,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	published, err := app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.Contents.ByID(ctx, draftID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.DraftOf != postID || draft.SlugID != published.SlugID {
		t.Fatalf("backup draft relation mismatch: published=%#v draft=%#v", published, draft)
	}
	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: postID, Author: "Reader", Mail: "r@example.com", Text: "hello", Status: "approved"}, 0); err != nil {
		t.Fatal(err)
	}
	payload, err := app.backupPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Version != 1 || len(payload.Users) == 0 || len(payload.Relationships) == 0 || len(payload.Fields) == 0 {
		t.Fatalf("incomplete backup payload")
	}
	foundDraft := false
	for _, item := range payload.Contents {
		if item.CID == draftID && item.DraftOf == postID && item.SlugID == published.SlugID {
			foundDraft = true
			break
		}
	}
	if !foundDraft {
		t.Fatalf("backup payload missing editing draft with shared slugID: %#v", payload.Contents)
	}

	target, _, _ := newSecurityTestApp(t)
	if err := target.importBackupPayload(ctx, payload, importSectionSet{Options: true, Users: true, Contents: true, Metas: true, Comments: true, Fields: true, Media: true}); err != nil {
		t.Fatal(err)
	}
	imported, err := target.Contents.BySlug(ctx, "backup-post")
	if err != nil {
		t.Fatal(err)
	}
	fields, err := target.Contents.FieldMap(ctx, imported.CID)
	if err != nil {
		t.Fatal(err)
	}
	if fields["source"] != "backup" {
		t.Fatalf("imported fields = %#v", fields)
	}
	importedDraft, err := target.Contents.DraftForContent(ctx, imported.CID)
	if err != nil {
		t.Fatal(err)
	}
	if importedDraft.Text != "draft body" || importedDraft.SlugID != imported.SlugID {
		t.Fatalf("imported editing draft mismatch: published=%#v draft=%#v", imported, importedDraft)
	}
	comments, err := target.Comments.List(ctx, "approved", "", imported.CID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Author != "Reader" {
		t.Fatalf("imported comments = %#v", comments)
	}
}

func TestBackupImportUnsupportedVersionDoesNotWrite(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	ctx := context.Background()
	payload := backupData{Version: 99, Options: map[string]string{"site_title": "Broken"}}
	if err := app.importBackupPayload(ctx, payload, importSectionSet{Options: true}); err == nil {
		t.Fatal("unsupported backup version should fail")
	}
	value, err := app.Options.Get(ctx, "site_title")
	if err != nil {
		t.Fatal(err)
	}
	if value == "Broken" {
		t.Fatal("unsupported backup version changed options")
	}
}

func TestBackupDryRunCountsAddUpdateSkip(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	postID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:        "dry-run-existing",
		Slug:         "dry-run-existing",
		Text:         "body",
		Type:         models.ContentTypePost,
		Status:       models.ContentStatusPost,
		AllowComment: true,
		CategoryIDs:  []int64{1},
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	payload := backupData{
		Version: 1,
		Options: map[string]string{
			"site_title": "Updated",
			"new_option": "New",
		},
		Users: []models.User{
			{UID: adminID, Name: "admin"},
			{UID: 99, Name: "new", Password: "hash"},
			{UID: 0, Name: "bad"},
		},
		Contents: []models.Content{
			{CID: postID, Type: models.ContentTypePost, Title: "Existing"},
			{CID: 99, Type: models.ContentTypePost, Title: "New"},
			{CID: 100, Type: models.ContentTypeAttach, Title: "Media"},
		},
		Metas: []models.Meta{
			{MID: 1, Type: "category", Name: "默认分类"},
			{MID: 99, Type: "tag", Name: "new"},
		},
		Relationships: []models.Relationship{{CID: postID, MID: 1}, {CID: 99, MID: 99}},
	}
	plan, err := app.backupPlan(ctx, payload, importSectionSet{Options: true, Users: true, Contents: true, Metas: true, Media: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Options.Update != 1 || plan.Options.Add != 1 {
		t.Fatalf("option plan = %#v", plan.Options)
	}
	if plan.Users.Skip != 2 || plan.Users.Add != 1 {
		t.Fatalf("user plan = %#v", plan.Users)
	}
	if plan.Contents.Skip != 1 || plan.Contents.Add != 1 || plan.Media.Add != 1 {
		t.Fatalf("content/media plan = %#v %#v", plan.Contents, plan.Media)
	}
	if plan.Metas.Skip != 1 || plan.Metas.Add != 1 || plan.Relationships.Skip != 1 || plan.Relationships.Add != 1 {
		t.Fatalf("meta/rel plan = %#v %#v", plan.Metas, plan.Relationships)
	}
	title, err := app.Options.Get(ctx, "site_title")
	if err != nil {
		t.Fatal(err)
	}
	if title == "Updated" {
		t.Fatal("dry-run changed database")
	}
}

func TestBackupImportMidFailureRollsBack(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	ctx := context.Background()
	payload := backupData{
		Version: 1,
		Options: map[string]string{"site_title": "Should Rollback"},
		Contents: []models.Content{
			{CID: 999, Title: "Bad Content"},
		},
	}
	if err := app.importBackupPayload(ctx, payload, importSectionSet{Options: true, Contents: true}); err == nil {
		t.Fatal("import should fail on content without type")
	}
	title, err := app.Options.Get(ctx, "site_title")
	if err != nil {
		t.Fatal(err)
	}
	if title == "Should Rollback" {
		t.Fatal("option write was not rolled back")
	}
}

func TestPermalinkGeneratesMatchesAndRedirectsCanonical(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "permalink_post", "/{year}/{month}/{slug}"); err != nil {
		t.Fatal(err)
	}
	postID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:   "Permalink Post",
		Slug:    "permalink-post",
		Text:    "body",
		Type:    models.ContentTypePost,
		Status:  models.ContentStatusPost,
		Created: time.Date(2026, 7, 6, 9, 0, 0, 0, time.Local).Unix(),
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	post, err := app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if got := app.contentURL(ctx, post); got != "/2026/07/permalink-post" {
		t.Fatalf("contentURL = %q", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/2026/07/permalink-post", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dynamic permalink status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/post/permalink-post", nil)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/2026/07/permalink-post" {
		t.Fatalf("canonical redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDefaultPostURLUsesNumericSlugIDWithHTMLSuffix(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	postID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:  "数字链接",
		Text:   "body",
		Type:   models.ContentTypePost,
		Status: models.ContentStatusPost,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	post, err := app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if post.Slug != "" || post.SlugID != 1 {
		t.Fatalf("post slug state = %#v, want empty custom slug and slugID 1", post)
	}
	if got := contentPublicURL(post); got != "/post/1.html" {
		t.Fatalf("contentPublicURL = %q, want /post/1.html", got)
	}
	if got := app.contentURL(ctx, post); got != "/post/1.html" {
		t.Fatalf("contentURL = %q, want /post/1.html", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/post/1.html", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("numeric slug route status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/post/1", nil)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/post/1.html" {
		t.Fatalf("numeric slug canonical redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	customID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:  "Custom",
		Slug:   "custom-path",
		Text:   "body",
		Type:   models.ContentTypePost,
		Status: models.ContentStatusPost,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	custom, err := app.Contents.ByID(ctx, customID)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentPublicURL(custom); got != "/post/custom-path.html" {
		t.Fatalf("custom contentPublicURL = %q, want /post/custom-path.html", got)
	}
}

func TestDefaultPageURLUsesNumericSlugIDWithHTMLSuffix(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	pageID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:  "数字页面",
		Text:   "body",
		Type:   models.ContentTypePage,
		Status: models.ContentStatusPost,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	pageData, err := app.Contents.ByID(ctx, pageID)
	if err != nil {
		t.Fatal(err)
	}
	if pageData.Slug != "" || pageData.SlugID != 1 {
		t.Fatalf("page slug state = %#v, want empty custom slug and slugID 1", pageData)
	}
	if got := contentPublicURL(pageData); got != "/page/1.html" {
		t.Fatalf("contentPublicURL = %q, want /page/1.html", got)
	}
	if got := app.contentURL(ctx, pageData); got != "/page/1.html" {
		t.Fatalf("contentURL = %q, want /page/1.html", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/page/1.html", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("numeric page route status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/page/1", nil)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/page/1.html" {
		t.Fatalf("numeric page canonical redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	customID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:  "Custom Page",
		Slug:   "custom-page",
		Text:   "body",
		Type:   models.ContentTypePage,
		Status: models.ContentStatusPost,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	custom, err := app.Contents.ByID(ctx, customID)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentPublicURL(custom); got != "/page/custom-page.html" {
		t.Fatalf("custom contentPublicURL = %q, want /page/custom-page.html", got)
	}
}

func TestWAFInvalidPathBanAndDisableSwitch(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	ctx := context.Background()
	for key, value := range map[string]string{
		"waf_cache_enabled":            "0",
		"waf_dynamic_rate_enabled":     "1",
		"waf_dynamic_rate_window":      "60",
		"waf_dynamic_rate_limit":       "100",
		"waf_invalid_path_enabled":     "1",
		"waf_invalid_path_window":      "60",
		"waf_invalid_path_limit":       "1",
		"waf_invalid_path_ban_seconds": "60",
		"waf_attachment_ban_enabled":   "0",
		"waf_search_rate_enabled":      "0",
		"waf_login_ban_enabled":        "0",
		"waf_url_index_enabled":        "1",
		"waf_url_index_ttl":            "60",
		"waf_static_rate_enabled":      "0",
		"waf_upload_rate_enabled":      "0",
	} {
		if err := app.Options.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	handler := app.Handler()
	req := httptest.NewRequest(http.MethodGet, "/missing-a", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("first missing status = %d, want 404", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/missing-b", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("second missing status = %d, want 403", rec.Code)
	}

	app2, _, _ := newSecurityTestApp(t)
	for key, value := range map[string]string{
		"waf_cache_enabled":          "0",
		"waf_invalid_path_enabled":   "0",
		"waf_dynamic_rate_enabled":   "0",
		"waf_search_rate_enabled":    "0",
		"waf_login_ban_enabled":      "0",
		"waf_attachment_ban_enabled": "0",
	} {
		if err := app2.Options.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	handler = app2.Handler()
	for i := 0; i < 3; i++ {
		req = httptest.NewRequest(http.MethodGet, "/missing-disabled-"+strconv.Itoa(i), nil)
		req.Header.Set("X-Forwarded-For", "198.51.100.11")
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("disabled invalid path status %d = %d, want 404", i, rec.Code)
		}
	}
}

func TestWAFPublicPageCache(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	for key, value := range map[string]string{
		"waf_cache_enabled":          "1",
		"waf_cache_ttl":              "60",
		"waf_cache_max_entries":      "10",
		"waf_dynamic_rate_enabled":   "0",
		"waf_search_rate_enabled":    "0",
		"waf_login_ban_enabled":      "0",
		"waf_attachment_ban_enabled": "0",
	} {
		if err := app.Options.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	postID := createPublishedPost(t, app, adminID, "cache-target")
	post, err := app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	req := httptest.NewRequest(http.MethodGet, app.contentURL(ctx, post), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first cache status = %d: %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, app.contentURL(ctx, post), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-GoBlog-Cache") != "HIT" {
		t.Fatalf("second cache response = %d cache %q", rec.Code, rec.Header().Get("X-GoBlog-Cache"))
	}
}

func TestWAFSearchRateLimit(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	ctx := context.Background()
	for key, value := range map[string]string{
		"waf_cache_enabled":          "0",
		"waf_dynamic_rate_enabled":   "0",
		"waf_search_rate_enabled":    "1",
		"waf_search_rate_window":     "60",
		"waf_search_rate_limit":      "1",
		"waf_login_ban_enabled":      "0",
		"waf_attachment_ban_enabled": "0",
	} {
		if err := app.Options.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	handler := app.Handler()
	req := httptest.NewRequest(http.MethodGet, "/search/one", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.20")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first search status = %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/search/two", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.20")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second search status = %d, want 429", rec.Code)
	}
}

func TestWAFLoginFailureBan(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	ctx := context.Background()
	for key, value := range map[string]string{
		"waf_dynamic_rate_enabled":   "0",
		"waf_search_rate_enabled":    "0",
		"waf_login_ban_enabled":      "1",
		"waf_login_window":           "60",
		"waf_login_failures":         "1",
		"waf_login_ban_seconds":      "60",
		"waf_attachment_ban_enabled": "0",
	} {
		if err := app.Options.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	handler := app.Handler()
	form := url.Values{"name": {"admin"}, "password": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "198.51.100.30")
	form.Set("_csrf", app.csrfTokenFor(req, "login"))
	req = httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "198.51.100.30")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "用户名或密码不正确") {
		t.Fatalf("first login failure = %d: %s", rec.Code, rec.Body.String())
	}

	form = url.Values{"name": {"admin"}, "password": {"admin123"}}
	req = httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "198.51.100.30")
	form.Set("_csrf", app.csrfTokenFor(req, "login"))
	req = httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "198.51.100.30")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusSeeOther || !strings.Contains(rec.Body.String(), "尝试过于频繁") {
		t.Fatalf("banned login response = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWAFAttachmentDownloadBan(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	ctx := context.Background()
	uploadDir := t.TempDir()
	app.UploadDir = uploadDir
	if err := os.WriteFile(filepath.Join(uploadDir, "asset.txt"), []byte("asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"waf_cache_enabled":          "0",
		"waf_dynamic_rate_enabled":   "0",
		"waf_static_rate_enabled":    "0",
		"waf_upload_rate_enabled":    "0",
		"waf_search_rate_enabled":    "0",
		"waf_login_ban_enabled":      "0",
		"waf_attachment_ban_enabled": "1",
		"waf_attachment_ban_window":  "60",
		"waf_attachment_ban_limit":   "1",
		"waf_attachment_ban_seconds": "60",
		"waf_invalid_path_enabled":   "0",
		"waf_url_index_enabled":      "0",
	} {
		if err := app.Options.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	handler := app.Handler()
	req := httptest.NewRequest(http.MethodGet, "/uploads/asset.txt", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.40")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first attachment status = %d: %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/uploads/asset.txt", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.40")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("second attachment status = %d, want 403", rec.Code)
	}
}

func TestAdminOptionsWAFPageRenders(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/options/waf", nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("waf options status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"URL 快速索引", "公开页缓存", "附件下载封禁", "后台登录爆破封禁"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("waf options page missing %q", want)
		}
	}
}

func TestAdminOptionsWAFSavesCheckedValuesWithHiddenFallback(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	form := url.Values{
		"waf_enabled":                  {"0", "1"},
		"waf_url_index_enabled":        {"0", "1"},
		"waf_url_index_ttl":            {"60"},
		"waf_index_max_items":          {"10000"},
		"waf_cache_enabled":            {"0"},
		"waf_cache_ttl":                {"30"},
		"waf_cache_max_entries":        {"512"},
		"waf_dynamic_rate_enabled":     {"0", "1"},
		"waf_dynamic_rate_window":      {"60"},
		"waf_dynamic_rate_limit":       {"2"},
		"waf_static_rate_enabled":      {"0", "1"},
		"waf_static_rate_window":       {"60"},
		"waf_static_rate_limit":        {"1200"},
		"waf_upload_rate_enabled":      {"0", "1"},
		"waf_upload_rate_window":       {"60"},
		"waf_upload_rate_limit":        {"600"},
		"waf_attachment_ban_enabled":   {"0", "1"},
		"waf_attachment_ban_window":    {"60"},
		"waf_attachment_ban_limit":     {"120"},
		"waf_attachment_ban_seconds":   {"600"},
		"waf_invalid_path_enabled":     {"0", "1"},
		"waf_invalid_path_window":      {"60"},
		"waf_invalid_path_limit":       {"20"},
		"waf_invalid_path_ban_seconds": {"600"},
		"waf_search_rate_enabled":      {"0", "1"},
		"waf_search_rate_window":       {"60"},
		"waf_search_rate_limit":        {"20"},
		"waf_login_ban_enabled":        {"0", "1"},
		"waf_login_window":             {"300"},
		"waf_login_failures":           {"5"},
		"waf_login_ban_seconds":        {"900"},
	}
	tokenReq := httptest.NewRequest(http.MethodPost, "/admin/options/waf", nil)
	setSession(t, tokenReq, secret, adminID)
	form.Set("_csrf", app.csrfTokenFor(tokenReq, "admin"))
	req := httptest.NewRequest(http.MethodPost, "/admin/options/waf", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()

	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save waf status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, key := range []string{"waf_enabled", "waf_dynamic_rate_enabled", "waf_search_rate_enabled", "waf_login_ban_enabled"} {
		value, err := app.Options.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if value != "1" {
			t.Fatalf("%s = %q, want 1", key, value)
		}
	}
	value, err := app.Options.Get(context.Background(), "waf_cache_enabled")
	if err != nil {
		t.Fatal(err)
	}
	if value != "0" {
		t.Fatalf("waf_cache_enabled = %q, want 0", value)
	}
}

func TestWAFDynamicRateLimit(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	postID := createPublishedPost(t, app, adminID, "dynamic-limit")
	post, err := app.Contents.ByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"waf_enabled":                  "1",
		"waf_cache_enabled":            "0",
		"waf_url_index_enabled":        "1",
		"waf_dynamic_rate_enabled":     "1",
		"waf_dynamic_rate_window":      "60",
		"waf_dynamic_rate_limit":       "2",
		"waf_static_rate_enabled":      "0",
		"waf_upload_rate_enabled":      "0",
		"waf_attachment_ban_enabled":   "0",
		"waf_invalid_path_enabled":     "0",
		"waf_search_rate_enabled":      "0",
		"waf_login_ban_enabled":        "0",
		"waf_invalid_path_ban_seconds": "60",
	} {
		if err := app.Options.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	handler := app.Handler()
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, app.contentURL(ctx, post), nil)
		req.Header.Set("X-Forwarded-For", "198.51.100.50")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("dynamic request %d status = %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, app.contentURL(ctx, post), nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.50")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third dynamic request status = %d, want 429", rec.Code)
	}
}

func TestWAFDoesNotBlockAuthenticatedAdministratorBackend(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	editorID, err := app.Users.Save(ctx, services.SaveUserInput{Name: "waf-editor", Password: "secret123", Mail: "waf-editor@example.com", Role: "editor"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"waf_enabled":                  "1",
		"waf_cache_enabled":            "0",
		"waf_url_index_enabled":        "1",
		"waf_dynamic_rate_enabled":     "0",
		"waf_static_rate_enabled":      "0",
		"waf_upload_rate_enabled":      "0",
		"waf_attachment_ban_enabled":   "0",
		"waf_search_rate_enabled":      "0",
		"waf_login_ban_enabled":        "0",
		"waf_invalid_path_enabled":     "1",
		"waf_invalid_path_window":      "60",
		"waf_invalid_path_limit":       "1",
		"waf_invalid_path_ban_seconds": "60",
	} {
		if err := app.Options.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	handler := app.Handler()
	for _, path := range []string{"/missing-one", "/missing-two"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Forwarded-For", "198.51.100.60")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
	blockedReq := httptest.NewRequest(http.MethodGet, "/", nil)
	blockedReq.Header.Set("X-Forwarded-For", "198.51.100.60")
	blockedRec := httptest.NewRecorder()
	handler.ServeHTTP(blockedRec, blockedReq)
	if blockedRec.Code != http.StatusForbidden {
		t.Fatalf("banned public request status = %d, want 403", blockedRec.Code)
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/admin", nil)
	adminReq.Header.Set("X-Forwarded-For", "198.51.100.60")
	setSession(t, adminReq, secret, adminID)
	adminRec := httptest.NewRecorder()
	handler.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("authenticated admin backend status = %d: %s", adminRec.Code, adminRec.Body.String())
	}

	adminPublicReq := httptest.NewRequest(http.MethodGet, "/", nil)
	adminPublicReq.Header.Set("X-Forwarded-For", "198.51.100.60")
	setSession(t, adminPublicReq, secret, adminID)
	adminPublicRec := httptest.NewRecorder()
	handler.ServeHTTP(adminPublicRec, adminPublicReq)
	if adminPublicRec.Code != http.StatusForbidden {
		t.Fatalf("authenticated admin public status = %d, want 403", adminPublicRec.Code)
	}

	editorReq := httptest.NewRequest(http.MethodGet, "/admin", nil)
	editorReq.Header.Set("X-Forwarded-For", "198.51.100.60")
	setSession(t, editorReq, secret, editorID)
	editorRec := httptest.NewRecorder()
	handler.ServeHTTP(editorRec, editorReq)
	if editorRec.Code != http.StatusForbidden {
		t.Fatalf("authenticated non-admin backend status = %d, want 403", editorRec.Code)
	}
}

func TestCategoryDirectoryPermalink(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "permalink_category", "/category/{directory}"); err != nil {
		t.Fatal(err)
	}
	parentID, err := app.Metas.Save(ctx, services.SaveMetaInput{Name: "Parent", Slug: "parent", Type: "category"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	childID, err := app.Metas.Save(ctx, services.SaveMetaInput{Name: "Child", Slug: "child", Type: "category", Parent: parentID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Nested Category", Slug: "nested-category", Text: "body", Type: models.ContentTypePost, Status: models.ContentStatusPost, CategoryIDs: []int64{childID}}, adminID); err != nil {
		t.Fatal(err)
	}
	child, err := app.Metas.ByID(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if got := app.metaURL(ctx, child); got != "/category/parent/child" {
		t.Fatalf("category URL = %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/category/parent/child", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("category directory status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestAtomFeedIsAtomXML(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	createPublishedPost(t, app, adminID, "atom-post")
	req := httptest.NewRequest(http.MethodGet, "/atom.xml", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("atom status = %d, want 200", rec.Code)
	}
	var feed atomFeed
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.XMLName.Local != "feed" || feed.Xmlns != "http://www.w3.org/2005/Atom" || len(feed.Entries) == 0 {
		t.Fatalf("atom feed = %#v", feed)
	}
}

func TestSearchQueryRedirectsToPrettyURL(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	createPublishedPost(t, app, adminID, "search-post")

	req := httptest.NewRequest(http.MethodGet, "/search?q=hello", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/search/hello" {
		t.Fatalf("search query redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	req = httptest.NewRequest(http.MethodGet, "/search/hello", nil)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pretty search status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestCommentFeedLinksPageCommentsToPageURL(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "permalink_page", "/docs/{slug}"); err != nil {
		t.Fatal(err)
	}
	pageID, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:        "About",
		Slug:         "about",
		Text:         "page body",
		Type:         models.ContentTypePage,
		Status:       models.ContentStatusPost,
		AllowComment: true,
	}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: pageID, Author: "Reader", Mail: "r@example.com", Text: "page comment", Status: "approved"}, 0); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/comments/feed.xml", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("comment feed status = %d, want 200", rec.Code)
	}
	var feed rssFeed
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Channel.Items) == 0 {
		t.Fatal("comment feed has no items")
	}
	if link := feed.Channel.Items[0].Link; !strings.HasPrefix(link, "http://localhost:8080/docs/about#comment-") {
		t.Fatalf("page comment link = %q", link)
	}
}

func TestPostSEOImageUsesFirstContentImage(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	if _, err := app.Contents.Create(ctx, services.SaveContentInput{
		Title:  "Image Post",
		Slug:   "image-post",
		Text:   "intro\n![cover](/uploads/42/cover.jpg)\n<img src=\"/uploads/42/second.jpg\">",
		Type:   models.ContentTypePost,
		Status: models.ContentStatusPost,
	}, adminID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/post/image-post.html", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("post status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<meta property="og:image" content="http://localhost:8080/uploads/42/cover.jpg">`,
		`<meta name="twitter:image" content="http://localhost:8080/uploads/42/cover.jpg">`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing SEO image meta %q in body: %s", want, body)
		}
	}
}

func TestCustomFrontPageRendersPage(t *testing.T) {
	app, _, adminID := newSecurityTestApp(t)
	ctx := context.Background()
	pageID, err := app.Contents.Create(ctx, services.SaveContentInput{Title: "Landing Page", Slug: "landing", Text: "front body", Type: models.ContentTypePage, Status: models.ContentStatusPost}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "front_page_type", "page"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "front_page_cid", itoa(pageID)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("front page status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Landing Page") {
		t.Fatalf("front page did not render selected page: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `<link rel="canonical" href="http://localhost:8080/">`) {
		t.Fatalf("front page canonical should point to site root: %s", rec.Body.String())
	}
}

func TestPermalinkConflictValidation(t *testing.T) {
	form := url.Values{
		"permalink_post":     {"/archive/{slug}"},
		"permalink_page":     {"/page/{slug}"},
		"permalink_category": {"/category/{slug}"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/options/permalink", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if err := validatePermalinkOptions(req); err == nil {
		t.Fatal("expected conflict with archive route")
	}
}

func TestPluginActivationFiltersRoutesAndHooks(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	mgr := plugin.NewManager()
	mgr.Register(phase6Plugin{})
	app.Plugins = mgr
	ctx := context.Background()
	if err := app.Options.Set(ctx, "active_plugins", `[]`); err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/phase6", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("inactive route status = %d, want 404", rec.Code)
	}
	app.syncActivePlugins(ctx)
	payload, err := app.Plugins.ApplyActive(ctx, plugin.HookExcerpt, plugin.ExcerptPayload{Text: "body", Limit: 10, Output: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if got := payload.(plugin.ExcerptPayload).Output; got != "body" {
		t.Fatalf("inactive hook output = %q", got)
	}

	form := url.Values{"_csrf": {adminToken(secret, adminID)}}
	req = httptest.NewRequest(http.MethodPost, "/admin/plugins/phase6/activate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("activate status = %d, want 303: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/phase6", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "phase6" {
		t.Fatalf("active route = %d %q", rec.Code, rec.Body.String())
	}
	payload, err = app.Plugins.ApplyActive(ctx, plugin.HookExcerpt, plugin.ExcerptPayload{Text: "body", Limit: 10, Output: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if got := payload.(plugin.ExcerptPayload).Output; got != "phase6:body" {
		t.Fatalf("active hook output = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/plugins/phase6/deactivate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("deactivate status = %d, want 303", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/phase6", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deactivated route status = %d, want 404", rec.Code)
	}
}

func TestPluginRuntimeProvidesConsistentCapabilities(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	runtime := app.pluginRuntime()
	if runtime.ListPublished == nil || runtime.ContentByID == nil || runtime.IncrementIntField == nil ||
		runtime.Option == nil || runtime.Config == nil || runtime.PersonalConfig == nil || runtime.DispatchHook == nil {
		t.Fatalf("incomplete plugin runtime: %#v", runtime)
	}
	app.Plugins.RegisterHook("test.runtime", func(_ context.Context, payload any) (any, error) {
		return payload.(string) + ":hooked", nil
	})
	dispatch, err := runtime.DispatchHook(context.Background(), "test.runtime", "value")
	if err != nil {
		t.Fatal(err)
	}
	if !dispatch.Triggered || dispatch.Payload != "value:hooked" {
		t.Fatalf("runtime dispatch = %#v", dispatch)
	}
}

func TestPluginConfigSavesJSON(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	mgr := plugin.NewManager()
	mgr.Register(phase6Plugin{})
	app.Plugins = mgr

	form := url.Values{"_csrf": {adminToken(secret, adminID)}, "message": {"hello"}, "enabled": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/plugins/phase6/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("plugin config status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	values, err := app.pluginConfig(context.Background(), "phase6")
	if err != nil {
		t.Fatal(err)
	}
	if values["message"] != "hello" || values["enabled"] != "1" {
		t.Fatalf("plugin config = %#v", values)
	}
}

func TestPluginPersonalConfigAvailableFromProfileAndRuntime(t *testing.T) {
	app, secret, _ := newSecurityTestApp(t)
	mgr := plugin.NewManager()
	mgr.Register(phase6Plugin{})
	app.Plugins = mgr
	ctx := context.Background()
	if err := app.Options.Set(ctx, "active_plugins", `["phase6"]`); err != nil {
		t.Fatal(err)
	}
	visitorID, err := app.Users.Save(ctx, services.SaveUserInput{
		Name: "visitor", Password: "visitor123", Mail: "visitor@example.com", Role: "visitor",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.setOptionJSONForUser(ctx, pluginOptionKey("phase6"), map[string]string{"message": "global"}, 0); err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin/profile", nil)
	setSession(t, req, secret, visitorID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/admin/profile/plugins/phase6") {
		t.Fatalf("visitor profile personal plugin entry = %d: %s", rec.Code, rec.Body.String())
	}

	form := url.Values{
		"_csrf":        {adminToken(secret, visitorID)},
		"display_name": {"Reader"},
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/profile/plugins/phase6", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, visitorID)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("personal plugin config status = %d: %s", rec.Code, rec.Body.String())
	}
	values, err := app.pluginPersonalConfig(ctx, "phase6", visitorID)
	if err != nil {
		t.Fatal(err)
	}
	if values["display_name"] != "Reader" || values["message"] != "global" {
		t.Fatalf("merged personal config = %#v", values)
	}
}

func TestThemeConfigInjectedIntoTemplates(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	mgr := plugin.NewManager()
	mgr.RegisterTheme(plugin.Theme{
		Name: "custom",
		Templates: fstest.MapFS{
			"templates/base.html":  {Data: []byte(`{{define "base"}}{{index .ThemeConfig "headline"}}{{template "content" .}}{{end}}`)},
			"templates/index.html": {Data: []byte(`{{define "content"}} index{{end}}`)},
		},
		ConfigSchema: []plugin.FieldSchema{{Name: "headline", Label: "Headline", Type: plugin.FieldText}},
	})
	app.Plugins = mgr
	ctx := context.Background()
	if err := app.Options.Set(ctx, "active_theme", "custom"); err != nil {
		t.Fatal(err)
	}
	if err := app.setOptionJSONForUser(ctx, themeOptionKey("custom"), map[string]string{"headline": "Configured"}, 0); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("theme render status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "Configured index" {
		t.Fatalf("theme output = %q", rec.Body.String())
	}
}

func TestThemeFileEditorRejectsPathTraversal(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "theme.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := plugin.NewManager()
	mgr.RegisterTheme(plugin.Theme{Name: "editable", EditableDir: root})
	app.Plugins = mgr

	req := httptest.NewRequest(http.MethodGet, "/admin/themes/editable/files?file=../secret.txt", nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("path traversal status = %d, want 400", rec.Code)
	}
}

func TestRegistrationToggleUniqueAndDefaultRole(t *testing.T) {
	app, secret, _ := newSecurityTestApp(t)
	ctx := context.Background()

	req := httptest.NewRequest(http.MethodGet, "/register", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled register status = %d, want 404", rec.Code)
	}

	if err := app.Options.Set(ctx, "allow_register", "1"); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf":      {signCSRF(secret, "anon", "register", time.Now().UTC())},
		"name":       {"reader"},
		"mail":       {"reader@example.com"},
		"password":   {"secret123"},
		"confirm":    {"secret123"},
		"screenName": {"Reader"},
	}
	req = httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("register redirect = %d %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var flash *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "flash" {
			flash = cookie
		}
	}
	if flash == nil {
		t.Fatal("register success did not set flash")
	}
	req = httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	req.AddCookie(flash)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "注册成功，请登录。") {
		t.Fatalf("login page missing register flash: %s", rec.Body.String())
	}
	user, err := app.Users.ByName(ctx, "reader")
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != "subscriber" {
		t.Fatalf("registered role = %q, want subscriber", user.Role)
	}

	form.Set("name", "reader2")
	req = httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "邮箱已存在") {
		t.Fatalf("duplicate register = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestRegistrationRejectsMismatchedPasswordConfirmation(t *testing.T) {
	app, secret, _ := newSecurityTestApp(t)
	if err := app.Options.Set(context.Background(), "allow_register", "1"); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf":    {signCSRF(secret, "anon", "register", time.Now().UTC())},
		"name":     {"mismatch"},
		"mail":     {"mismatch@example.com"},
		"password": {"secret123"},
		"confirm":  {"different"},
	}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "两次输入的密码不一致") {
		t.Fatalf("mismatched registration = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := app.Users.ByName(context.Background(), "mismatch"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mismatched registration created user: %v", err)
	}
}

func TestRevokeCurrentUserSessionsReissuesCurrentSession(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	oldRecorder := httptest.NewRecorder()
	auth.SetSession(oldRecorder, secret, adminID)
	oldCookie := oldRecorder.Result().Cookies()[0]
	form := url.Values{"_csrf": {adminToken(secret, adminID)}}
	req := httptest.NewRequest(http.MethodPost, "/admin/users/"+itoa(adminID)+"/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(oldCookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("revoke current sessions = %d: %s", rec.Code, rec.Body.String())
	}
	var currentCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.CookieName {
			currentCookie = cookie
			break
		}
	}
	if currentCookie == nil {
		t.Fatal("revoke current sessions did not issue a replacement session")
	}

	oldRequest := httptest.NewRequest(http.MethodGet, "/admin", nil)
	oldRequest.AddCookie(oldCookie)
	oldResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(oldResponse, oldRequest)
	if oldResponse.Code != http.StatusSeeOther || oldResponse.Header().Get("Location") != "/admin/login" {
		t.Fatalf("old session remained valid: %d %q", oldResponse.Code, oldResponse.Header().Get("Location"))
	}
	currentRequest := httptest.NewRequest(http.MethodGet, "/admin", nil)
	currentRequest.AddCookie(currentCookie)
	currentResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(currentResponse, currentRequest)
	if currentResponse.Code != http.StatusOK {
		t.Fatalf("replacement session status = %d: %s", currentResponse.Code, currentResponse.Body.String())
	}
}

func TestInstallWizardAvailableOnEmptyDatabaseThenLocks(t *testing.T) {
	app, secret := newInstallTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/install", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("install get status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	form := url.Values{
		"_csrf":      {signCSRF(secret, "anon", "install", time.Now().UTC())},
		"site_title": {"Installed"},
		"base_url":   {"http://example.com"},
		"name":       {"owner"},
		"mail":       {"owner@example.com"},
		"password":   {"secret123"},
		"confirm":    {"secret123"},
	}
	req = httptest.NewRequest(http.MethodPost, "/install", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("install post redirect = %d %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	user, err := app.Users.ByName(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != "administrator" {
		t.Fatalf("installed user role = %q", user.Role)
	}

	req = httptest.NewRequest(http.MethodGet, "/install", nil)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("install after setup status = %d, want 404", rec.Code)
	}
}

func TestAdminI18nEnglishGeneralSettings(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	if err := app.Options.Set(context.Background(), "site_language", "en-US"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/options/general", nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("general options status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"General Settings", "Site title", "Allow registration", "Save settings"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing english text %q in %s", want, body)
		}
	}
	for _, unwanted := range []string{"基本设置", "站点名称", "是否允许注册", "保存设置"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("unexpected chinese text %q in %s", unwanted, body)
		}
	}
}

func TestFlashNoticeSurvivesRedirectAndClears(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	form := url.Values{
		"_csrf":                        {adminToken(secret, adminID)},
		"site_title":                   {"GoBlog"},
		"base_url":                     {"http://localhost:8080"},
		"site_language":                {"zh-CN"},
		"site_timezone":                {"Local"},
		"allow_register":               {"0"},
		"register_default_role":        {"subscriber"},
		"cookie_secure":                {"0"},
		"cookie_samesite":              {"Lax"},
		"upload_replace_same_ext_only": {"1"},
		"attachment_delete_policy":     {"keep"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/options/general", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("options save = %d, want 303", rec.Code)
	}
	var flash *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "flash" {
			flash = cookie
		}
	}
	if flash == nil {
		t.Fatal("expected flash cookie")
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/options/general", nil)
	req.AddCookie(flash)
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "设置已保存") {
		t.Fatalf("missing flash notice: %s", rec.Body.String())
	}
	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "flash" && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("flash cookie was not cleared")
	}
}

func TestSiteTimezoneFormatsDates(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	ctx := context.Background()
	ts := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC).Unix()
	if err := app.Options.Set(ctx, "post_date_format", "2006-01-02 15:04 MST"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "site_timezone", "UTC"); err != nil {
		t.Fatal(err)
	}
	utc := app.formatDate(ctx, ts, "post_date_format")
	if err := app.Options.Set(ctx, "site_timezone", "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	shanghai := app.formatDate(ctx, ts, "post_date_format")
	if utc == shanghai || !strings.Contains(shanghai, "08:00") {
		t.Fatalf("timezone formatting utc=%q shanghai=%q", utc, shanghai)
	}
}

func TestCookiePrefixAndSecurityOptions(t *testing.T) {
	app, secret, _ := newSecurityTestApp(t)
	ctx := context.Background()
	if err := app.Options.Set(ctx, "cookie_prefix", "gb_"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "cookie_secure", "1"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "cookie_samesite", "Strict"); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf":    {signCSRF(secret, "anon", "login", time.Now().UTC())},
		"name":     {"admin"},
		"password": {"admin123"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "gb_goblog_session" {
			found = true
			if !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || !cookie.HttpOnly {
				t.Fatalf("session cookie security = secure:%v samesite:%v httponly:%v", cookie.Secure, cookie.SameSite, cookie.HttpOnly)
			}
		}
	}
	if !found {
		t.Fatal("prefixed session cookie missing")
	}
}

func TestSchemaVersionUpgrade(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := models.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE gb_options SET value = '0' WHERE name = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := models.RunVersionedMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version string
	if err := db.QueryRowContext(ctx, `SELECT value FROM gb_options WHERE name = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != strconv.Itoa(models.CurrentSchemaVersion) {
		t.Fatalf("schema_version = %q", version)
	}
}

type phase6Plugin struct{}

func (phase6Plugin) Name() string        { return "phase6" }
func (phase6Plugin) Version() string     { return "1.0.0" }
func (phase6Plugin) Description() string { return "phase 06 test plugin" }
func (phase6Plugin) Info() plugin.PluginInfo {
	return plugin.PluginInfo{Name: "phase6", Version: "1.0.0", Author: "test", Description: "phase 06 test plugin"}
}
func (phase6Plugin) ConfigSchema() []plugin.FieldSchema {
	return []plugin.FieldSchema{
		{Name: "message", Label: "Message", Type: plugin.FieldText, Default: "default"},
		{Name: "enabled", Label: "Enabled", Type: plugin.FieldCheckbox, Default: "0"},
	}
}
func (phase6Plugin) PersonalConfigSchema() []plugin.FieldSchema {
	return []plugin.FieldSchema{{Name: "display_name", Label: "Display name", Type: plugin.FieldText}}
}
func (phase6Plugin) Init(m *plugin.Manager) {
	m.RegisterRoute(http.MethodGet, "/phase6", func(_ *plugin.Runtime, w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("phase6"))
	})
	m.RegisterHook(plugin.HookExcerpt, func(ctx context.Context, payload any) (any, error) {
		value := payload.(plugin.ExcerptPayload)
		value.Output = "phase6:" + value.Output
		return value, nil
	})
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

func newInstallTestApp(t *testing.T) (*App, string) {
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
	metas := services.NewMetaService(db)
	if err := metas.EnsureDefaultCategory(ctx); err != nil {
		t.Fatal(err)
	}
	secret, err := options.Get(ctx, "auth_secret")
	if err != nil {
		t.Fatal(err)
	}
	return New(services.NewContentService(db), metas, services.NewCommentService(db), services.NewUserService(db), options, plugin.Default), secret
}

func createPublishedPost(t *testing.T, app *App, authorID int64, slug string) int64 {
	t.Helper()
	id, err := app.Contents.Create(context.Background(), services.SaveContentInput{
		Title:        slug,
		Slug:         slug,
		Text:         "body",
		Type:         models.ContentTypePost,
		Status:       models.ContentStatusPost,
		AllowComment: true,
	}, authorID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func createPublishedPage(t *testing.T, app *App, authorID int64, slug string) int64 {
	t.Helper()
	id, err := app.Contents.Create(context.Background(), services.SaveContentInput{
		Title:        slug,
		Slug:         slug,
		Text:         "body",
		Type:         models.ContentTypePage,
		Status:       models.ContentStatusPost,
		AllowComment: true,
	}, authorID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func uploadMedia(t *testing.T, app *App, secret string, adminID, parentID int64, filename string, content []byte) (models.Content, models.AttachmentMeta) {
	t.Helper()
	req := multipartUploadRequestBytes(t, "/admin/medias", map[string]string{"_csrf": adminToken(secret, adminID), "cid": itoa(parentID)}, "file", filename, content)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("media upload status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	attachments, err := app.Contents.List(context.Background(), services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Parent: parentID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) == 0 {
		t.Fatal("expected uploaded attachment")
	}
	item := attachments[0]
	return item, parseAttachmentMeta(item)
}

func createAttachmentMeta(t *testing.T, app *App, authorID, parentID int64, meta models.AttachmentMeta) int64 {
	t.Helper()
	text, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	slug := strings.TrimSuffix(filepath.Base(meta.Name), filepath.Ext(meta.Name))
	id, err := app.Contents.CreateAttachmentMeta(context.Background(), meta.Name, slug, string(text), authorID, parentID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func deleteContentRequest(t *testing.T, app *App, secret string, adminID, cid int64) {
	t.Helper()
	form := url.Values{"_csrf": {adminToken(secret, adminID)}}
	req := httptest.NewRequest(http.MethodPost, "/admin/posts/"+itoa(cid)+"/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete content status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
}

func deletePageRequest(t *testing.T, app *App, secret string, adminID, cid int64) {
	t.Helper()
	form := url.Values{"_csrf": {adminToken(secret, adminID)}}
	req := httptest.NewRequest(http.MethodPost, "/admin/pages/"+itoa(cid)+"/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete page status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
}

func submitPublicComment(t *testing.T, app *App, cid int64, form url.Values, ip string) *httptest.ResponseRecorder {
	t.Helper()
	post, err := app.Contents.ByID(context.Background(), cid)
	if err != nil {
		t.Fatal(err)
	}
	return submitPublicCommentWithReferer(t, app, cid, form, ip, "http://example.com"+contentPublicURL(post))
}

func submitPublicCommentWithReferer(t *testing.T, app *App, cid int64, form url.Values, ip, referer string) *httptest.ResponseRecorder {
	t.Helper()
	form = cloneValues(form)
	form.Set("cid", itoa(cid))
	req := httptest.NewRequest(http.MethodPost, "/comment", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Real-IP", ip)
	form.Set("_csrf", app.csrfTokenFor(req, "comment"))
	req = httptest.NewRequest(http.MethodPost, "/comment", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Real-IP", ip)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for key, items := range values {
		for _, item := range items {
			out.Add(key, item)
		}
	}
	return out
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
	return multipartUploadRequestBytes(t, target, fields, fileField, filename, []byte(content))
}

func multipartUploadRequestBytes(t *testing.T, target string, fields map[string]string, fileField, filename string, content []byte) *http.Request {
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
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func adminToken(secret string, uid int64) string {
	return signCSRF(secret, strconv.FormatInt(uid, 10), "admin", time.Now().UTC())
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
