package services

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"goblog/core/models"

	_ "github.com/mattn/go-sqlite3"
)

func TestPublishedQueriesExcludeFutureContent(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	future := time.Now().Add(24 * time.Hour).Unix()
	if _, err := service.Create(ctx, SaveContentInput{Title: "Future", Slug: "future", Type: models.ContentTypePost, Status: models.ContentStatusPost, Created: future}, 1); err != nil {
		t.Fatal(err)
	}

	posts, err := service.ListPublished(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 0 {
		t.Fatalf("future post leaked into ListPublished: %#v", posts)
	}

	if _, err := service.BySlug(ctx, "future"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("BySlug future error = %v, want sql.ErrNoRows", err)
	}
}

func TestArchiveMonthsGroupsPublishedPastPosts(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	loc := time.UTC
	posts := []SaveContentInput{
		{Title: "A", Slug: "a", Type: models.ContentTypePost, Status: models.ContentStatusPost, Created: time.Date(2026, 2, 10, 10, 0, 0, 0, loc).Unix()},
		{Title: "B", Slug: "b", Type: models.ContentTypePost, Status: models.ContentStatusPost, Created: time.Date(2026, 2, 2, 10, 0, 0, 0, loc).Unix()},
		{Title: "C", Slug: "c", Type: models.ContentTypePost, Status: models.ContentStatusPost, Created: time.Date(2026, 1, 2, 10, 0, 0, 0, loc).Unix()},
		{Title: "Draft", Slug: "draft", Type: models.ContentTypePost, Status: models.ContentStatusDraft, Created: time.Date(2026, 1, 3, 10, 0, 0, 0, loc).Unix()},
		{Title: "Future", Slug: "future", Type: models.ContentTypePost, Status: models.ContentStatusPost, Created: time.Now().Add(24 * time.Hour).Unix()},
	}
	for _, post := range posts {
		if _, err := service.Create(ctx, post, 1); err != nil {
			t.Fatal(err)
		}
	}

	archives, err := service.ArchiveMonths(ctx, loc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) != 2 {
		t.Fatalf("ArchiveMonths length = %d, want 2: %#v", len(archives), archives)
	}
	if archives[0].Date != "2026-02" || archives[0].Count != 2 {
		t.Fatalf("first archive = %#v, want 2026-02 count 2", archives[0])
	}
	if archives[1].Date != "2026-01" || archives[1].Count != 1 {
		t.Fatalf("second archive = %#v, want 2026-01 count 1", archives[1])
	}
}

func TestRevisionsAndRestore(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	id, err := service.Create(ctx, SaveContentInput{Title: "Original", Slug: "rev", Text: "one", Type: models.ContentTypePost, Status: models.ContentStatusPost}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Update(ctx, id, SaveContentInput{Title: "Changed", Slug: "rev", Text: "two", Type: models.ContentTypePost, Status: models.ContentStatusPost}); err != nil {
		t.Fatal(err)
	}
	revisions, err := service.Revisions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) == 0 || revisions[0].Title != "Original" {
		t.Fatalf("expected original revision, got %#v", revisions)
	}
	if _, err := service.RestoreRevision(ctx, id, revisions[0].RID); err != nil {
		t.Fatal(err)
	}
	restored, err := service.ByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Title != "Original" || restored.Text != "one" {
		t.Fatalf("restore mismatch: %#v", restored)
	}
}

func TestRestoreRevisionRequiresMatchingContentID(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	firstID, err := service.Create(ctx, SaveContentInput{Title: "First", Slug: "first", Text: "one", Type: models.ContentTypePost, Status: models.ContentStatusPost}, 1)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := service.Create(ctx, SaveContentInput{Title: "Second", Slug: "second", Text: "two", Type: models.ContentTypePost, Status: models.ContentStatusPost}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Update(ctx, secondID, SaveContentInput{Title: "Second changed", Slug: "second", Text: "changed", Type: models.ContentTypePost, Status: models.ContentStatusPost}); err != nil {
		t.Fatal(err)
	}
	revisions, err := service.Revisions(ctx, secondID)
	if err != nil || len(revisions) == 0 {
		t.Fatalf("expected revision, got %v %#v", err, revisions)
	}
	if _, err := service.RestoreRevision(ctx, firstID, revisions[0].RID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("RestoreRevision wrong cid err = %v, want sql.ErrNoRows", err)
	}
	second, err := service.ByID(ctx, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Title != "Second changed" || second.Text != "changed" {
		t.Fatalf("second content changed after rejected restore: %#v", second)
	}
}

func TestCustomFields(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	id, err := service.Create(ctx, SaveContentInput{
		Title:  "Fields",
		Type:   models.ContentTypePost,
		Status: models.ContentStatusDraft,
		Fields: []SaveFieldInput{
			{Name: "subtitle", Type: "str", StrValue: "Hello"},
			{Name: "views", Type: "int", IntValue: 42},
			{Name: "score", Type: "float", FloatValue: 9.5},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := service.FieldMap(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if fields["subtitle"] != "Hello" || fields["views"] != int64(42) || fields["score"] != 9.5 {
		t.Fatalf("field map mismatch: %#v", fields)
	}
}

func newContentTestService(t *testing.T) *ContentService {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := models.Migrate(context.Background(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	return NewContentService(db)
}
