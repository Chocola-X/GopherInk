package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/xml"
	"errors"
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

	rec = submitPublicCommentWithReferer(t, app, postID, form, "198.51.100.43", "http://example.com/post/comment-referer?comments_page=1")
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
	if err := app.Options.Set(ctx, "comments_max_nesting_levels", "1"); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "comments_post_interval_enable", "0"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "comment-depth")
	if err := app.Comments.Save(ctx, services.SaveCommentInput{CID: postID, Author: "Parent", Mail: "p@example.com", Text: "parent", Status: "approved"}, 0); err != nil {
		t.Fatal(err)
	}
	parentComments, err := app.Comments.List(ctx, "approved", "", postID)
	if err != nil || len(parentComments) == 0 {
		t.Fatalf("parent comment missing: %v %#v", err, parentComments)
	}

	rec := submitPublicComment(t, app, postID, url.Values{
		"author": {"Child"},
		"mail":   {"c@example.com"},
		"text":   {"too deep"},
		"parent": {itoa(parentComments[0].COID)},
	}, "198.51.100.30")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "comment_error=depth") {
		t.Fatalf("depth response = %d %q", rec.Code, rec.Header().Get("Location"))
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
	if !strings.HasPrefix(meta.Path, itoa(postID)+"/") || !strings.HasPrefix(meta.URL, "/uploads/"+itoa(postID)+"/") {
		t.Fatalf("attachment path/url = %#v, want content-id bucket", meta)
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
	if strings.Contains(meta.Path, "..") || !strings.HasPrefix(meta.Path, itoa(postID)+"/") || meta.Name != "evil.png" {
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
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old file still exists after successful replace: %v", err)
	}
	updated, err := app.Contents.ByID(ctx, attachment.CID)
	if err != nil {
		t.Fatal(err)
	}
	updatedMeta := parseAttachmentMeta(updated)
	if updatedMeta.Path == meta.Path || updatedMeta.Type != "png" {
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
		deleteContentRequest(t, app, secret, adminID, postID)
		if _, err := app.Contents.ByID(ctx, attachment.CID); err != nil {
			t.Fatalf("attachment record should be kept: %v", err)
		}
		if _, err := os.Stat(filepath.Join(app.UploadDir, filepath.FromSlash(meta.Path))); err != nil {
			t.Fatalf("attachment file should be kept: %v", err)
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

	req := httptest.NewRequest(http.MethodGet, "/post/image-post", nil)
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
