package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

func TestEditingDraftLifecyclePreservesPublishedUntilPublish(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	publishedID, err := service.Create(ctx, SaveContentInput{
		Title:        "Original",
		Slug:         "same-slug",
		Text:         "published body",
		Type:         models.ContentTypePost,
		Status:       models.ContentStatusPost,
		AllowComment: true,
		AllowFeed:    true,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	draftID, err := service.SaveEditingDraft(ctx, publishedID, SaveContentInput{
		Title:        "Edited",
		Slug:         "same-slug",
		Text:         "draft body",
		Type:         models.ContentTypePost,
		Status:       models.ContentStatusDraft,
		AllowComment: true,
		AllowFeed:    true,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := service.ByID(ctx, draftID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.DraftOf != publishedID || draft.Slug != "same-slug" || draft.Text != "draft body" {
		t.Fatalf("editing draft mismatch: %#v", draft)
	}
	publishedBeforeDraft, err := service.ByID(ctx, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.SlugID <= 0 || draft.SlugID != publishedBeforeDraft.SlugID {
		t.Fatalf("editing draft slugID = %d, want same as published slugID %d", draft.SlugID, publishedBeforeDraft.SlugID)
	}
	published, err := service.BySlug(ctx, "same-slug")
	if err != nil {
		t.Fatal(err)
	}
	if published.CID != publishedID || published.Text != "published body" {
		t.Fatalf("published content changed before publish: %#v", published)
	}
	draftMap, err := service.DraftMapForContents(ctx, []int64{publishedID})
	if err != nil {
		t.Fatal(err)
	}
	if !draftMap[publishedID] {
		t.Fatalf("editing draft not reported for published content: %#v", draftMap)
	}

	if err := service.DeleteDraft(ctx, draftID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DraftForContent(ctx, publishedID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DraftForContent after delete err = %v, want sql.ErrNoRows", err)
	}
	published, err = service.ByID(ctx, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Text != "published body" {
		t.Fatalf("DeleteDraft changed published content: %#v", published)
	}

	draftID, err = service.SaveEditingDraft(ctx, publishedID, SaveContentInput{
		Title:        "Final",
		Slug:         "same-slug",
		Text:         "final body",
		Type:         models.ContentTypePost,
		Status:       models.ContentStatusDraft,
		AllowComment: true,
		AllowFeed:    true,
		Tags:         []string{"alpha", "beta"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	finalDraft, err := service.ByID(ctx, draftID)
	if err != nil {
		t.Fatal(err)
	}
	if finalDraft.SlugID != publishedBeforeDraft.SlugID {
		t.Fatalf("final editing draft slugID = %d, want same as published slugID %d", finalDraft.SlugID, publishedBeforeDraft.SlugID)
	}
	if err := service.PublishDraft(ctx, draftID); err != nil {
		t.Fatal(err)
	}
	published, err = service.ByID(ctx, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Title != "Final" || published.Text != "final body" || published.Slug != "same-slug" || published.DraftOf != 0 {
		t.Fatalf("published content not updated from draft: %#v", published)
	}
	if published.SlugID != finalDraft.SlugID {
		t.Fatalf("published slugID after draft publish = %d, want draft slugID %d", published.SlugID, finalDraft.SlugID)
	}
	tags, err := service.TagsForContentNames(ctx, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	tagSet := map[string]bool{}
	for _, tag := range tags {
		tagSet[tag] = true
	}
	if len(tagSet) != 2 || !tagSet["alpha"] || !tagSet["beta"] {
		t.Fatalf("published tags not copied from draft: %#v", tags)
	}
	if _, err := service.DraftForContent(ctx, publishedID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DraftForContent after publish err = %v, want sql.ErrNoRows", err)
	}
	revisions, err := service.Revisions(ctx, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) == 0 || revisions[0].Title != "Original" || revisions[0].Text != "published body" {
		t.Fatalf("published snapshot missing before draft publish: %#v", revisions)
	}
}

func TestDeleteRevisionRequiresMatchingContentID(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	firstID, err := service.Create(ctx, SaveContentInput{Title: "First", Slug: "first-delete-revision", Text: "one", Type: models.ContentTypePost, Status: models.ContentStatusPost}, 1)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := service.Create(ctx, SaveContentInput{Title: "Second", Slug: "second-delete-revision", Text: "two", Type: models.ContentTypePost, Status: models.ContentStatusPost}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Update(ctx, secondID, SaveContentInput{Title: "Second changed", Slug: "second-delete-revision", Text: "changed", Type: models.ContentTypePost, Status: models.ContentStatusPost}); err != nil {
		t.Fatal(err)
	}
	revisions, err := service.Revisions(ctx, secondID)
	if err != nil || len(revisions) == 0 {
		t.Fatalf("expected revision, got %v %#v", err, revisions)
	}
	if err := service.DeleteRevision(ctx, firstID, revisions[0].RID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DeleteRevision wrong cid err = %v, want sql.ErrNoRows", err)
	}
	if err := service.DeleteRevision(ctx, secondID, revisions[0].RID); err != nil {
		t.Fatal(err)
	}
	revisions, err = service.Revisions(ctx, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 0 {
		t.Fatalf("revision was not deleted: %#v", revisions)
	}
}

func TestContentFieldValidationJSONIncrementAndRevisionLimit(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	if _, err := service.Create(ctx, SaveContentInput{
		Title:  "Invalid field",
		Type:   models.ContentTypePost,
		Status: models.ContentStatusDraft,
		Fields: []SaveFieldInput{{Name: "invalid-name", Type: "str", StrValue: "value"}},
	}, 1); err == nil {
		t.Fatal("invalid custom field name should be rejected")
	}
	if _, err := service.Create(ctx, SaveContentInput{
		Title:       "Invalid category",
		Type:        models.ContentTypePost,
		Status:      models.ContentStatusDraft,
		CategoryIDs: []int64{9999},
	}, 1); err == nil {
		t.Fatal("missing category should be rejected")
	}

	id, err := service.Create(ctx, SaveContentInput{
		Title:  "Fields",
		Type:   models.ContentTypePost,
		Status: models.ContentStatusPost,
		Fields: []SaveFieldInput{{Name: "metadata", Type: "json", StrValue: `{"enabled":true,"count":2}`}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := service.FieldMap(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	metadata, ok := fields["metadata"].(map[string]any)
	if !ok || metadata["enabled"] != true || metadata["count"] != float64(2) {
		t.Fatalf("decoded json field = %#v", fields["metadata"])
	}
	if value, err := service.IncrementIntField(ctx, id, "views", 2); err != nil || value != 2 {
		t.Fatalf("first increment = %d, err = %v", value, err)
	}
	if value, err := service.IncrementIntField(ctx, id, "views", 3); err != nil || value != 5 {
		t.Fatalf("second increment = %d, err = %v", value, err)
	}
	if err := service.SaveFields(ctx, id, []SaveFieldInput{{Name: "stable", Type: "str", StrValue: "kept"}}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveFields(ctx, id, []SaveFieldInput{{Name: "invalid-name", Type: "str", StrValue: "bad"}}); err == nil {
		t.Fatal("invalid SaveFields call should fail")
	}
	fields, err = service.FieldMap(ctx, id)
	if err != nil || fields["stable"] != "kept" {
		t.Fatalf("failed SaveFields call changed existing fields: %#v, err = %v", fields, err)
	}

	service.SetRevisionLimit(2)
	for i := 0; i < 4; i++ {
		if err := service.Update(ctx, id, SaveContentInput{Title: fmt.Sprintf("Revision %d", i), Type: models.ContentTypePost, Status: models.ContentStatusPost}); err != nil {
			t.Fatal(err)
		}
	}
	revisions, err := service.Revisions(ctx, id)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("revisions = %d, err = %v, want 2", len(revisions), err)
	}
}

func TestDeletingContentCleansOrphanTags(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	id, err := service.Create(ctx, SaveContentInput{Title: "Tagged", Type: models.ContentTypePost, Status: models.ContentStatusPost, Tags: []string{"temporary-tag"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := service.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_metas WHERE type = 'tag' AND name = 'temporary-tag'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("orphan tag count = %d, want 0", count)
	}
}

func TestDeleteDraftRejectsPublishedContent(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	publishedID, err := service.Create(ctx, SaveContentInput{Title: "Published", Slug: "published", Text: "body", Type: models.ContentTypePost, Status: models.ContentStatusPost}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteDraft(ctx, publishedID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DeleteDraft published err = %v, want sql.ErrNoRows", err)
	}
	if _, err := service.ByID(ctx, publishedID); err != nil {
		t.Fatalf("published content deleted by DeleteDraft: %v", err)
	}
}

func TestRepairOrphanEditingDraftsFoldsSlugCopies(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	publishedID, err := service.Create(ctx, SaveContentInput{
		Title:  "Repair",
		Slug:   "repair-post",
		Text:   "published",
		Type:   models.ContentTypePost,
		Status: models.ContentStatusPost,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, SaveContentInput{Title: "Repair", Slug: "repair-post", Text: "old draft", Type: models.ContentTypePost, Status: models.ContentStatusDraft}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, SaveContentInput{Title: "Repair", Slug: "repair-post", Text: "new draft", Type: models.ContentTypePost, Status: models.ContentStatusDraft}, 1); err != nil {
		t.Fatal(err)
	}
	before, err := service.List(ctx, ContentQuery{Type: models.ContentTypePost, Status: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 {
		t.Fatalf("before repair list length = %d, want 3", len(before))
	}
	if err := service.RepairOrphanEditingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := service.List(ctx, ContentQuery{Type: models.ContentTypePost, Status: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].CID != publishedID {
		t.Fatalf("after repair list = %#v, want only published post", after)
	}
	draft, err := service.DraftForContent(ctx, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.DraftOf != publishedID || draft.Slug != "repair-post" || draft.Text != "new draft" {
		t.Fatalf("repaired draft mismatch: %#v", draft)
	}
	all, err := service.List(ctx, ContentQuery{Type: models.ContentTypePost, Status: "all", IncludeDrafts: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all contents after repair = %d, want published + one draft: %#v", len(all), all)
	}
	count, err := service.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("Count = %d, want 1 published post", count)
	}
}

func TestNumericSlugIDAllocationRecyclesOnlyUnpublishedDrafts(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	var publishedIDs []int64
	for i := 1; i <= 4; i++ {
		id, err := service.Create(ctx, SaveContentInput{Title: "Post", Type: models.ContentTypePost, Status: models.ContentStatusPost}, 1)
		if err != nil {
			t.Fatal(err)
		}
		post, err := service.ByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if post.Slug != "" || post.SlugID != int64(i) {
			t.Fatalf("published post %d slug state = %#v, want empty custom slug and slugID %d", i, post, i)
		}
		publishedIDs = append(publishedIDs, id)
	}

	draftID, err := service.Create(ctx, SaveContentInput{Title: "Draft", Type: models.ContentTypePost, Status: models.ContentStatusDraft}, 1)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := service.ByID(ctx, draftID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.SlugID != 5 {
		t.Fatalf("draft slugID = %d, want 5", draft.SlugID)
	}
	if err := service.Delete(ctx, draftID); err != nil {
		t.Fatal(err)
	}

	reusedID, err := service.Create(ctx, SaveContentInput{Title: "Reused", Type: models.ContentTypePost, Status: models.ContentStatusDraft}, 1)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := service.ByID(ctx, reusedID)
	if err != nil {
		t.Fatal(err)
	}
	if reused.SlugID != 5 {
		t.Fatalf("reused draft slugID = %d, want recycled 5", reused.SlugID)
	}
	if err := service.MarkStatus(ctx, reusedID, models.ContentStatusPost); err != nil {
		t.Fatal(err)
	}
	byNumericSlug, err := service.BySlug(ctx, "5")
	if err != nil {
		t.Fatal(err)
	}
	if byNumericSlug.CID != reusedID {
		t.Fatalf("BySlug(5) = %#v, want cid %d", byNumericSlug, reusedID)
	}

	if err := service.Delete(ctx, publishedIDs[1]); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, publishedIDs[2]); err != nil {
		t.Fatal(err)
	}
	nextID, err := service.Create(ctx, SaveContentInput{Title: "Next", Type: models.ContentTypePost, Status: models.ContentStatusDraft}, 1)
	if err != nil {
		t.Fatal(err)
	}
	next, err := service.ByID(ctx, nextID)
	if err != nil {
		t.Fatal(err)
	}
	if next.SlugID != 6 {
		t.Fatalf("next draft slugID = %d, want 6 after published IDs are reserved", next.SlugID)
	}
}

func TestCustomSlugKeepsDisplaySlugWhileStoringSlugID(t *testing.T) {
	service := newContentTestService(t)
	ctx := context.Background()
	customID, err := service.Create(ctx, SaveContentInput{Title: "Custom", Slug: "custom-path", Type: models.ContentTypePost, Status: models.ContentStatusPost}, 1)
	if err != nil {
		t.Fatal(err)
	}
	custom, err := service.ByID(ctx, customID)
	if err != nil {
		t.Fatal(err)
	}
	if custom.Slug != "custom-path" || custom.SlugID != 1 {
		t.Fatalf("custom slug state = %#v, want custom-path with slugID 1", custom)
	}
	byCustom, err := service.BySlug(ctx, "custom-path")
	if err != nil {
		t.Fatal(err)
	}
	if byCustom.CID != customID {
		t.Fatalf("BySlug(custom-path) = %#v, want cid %d", byCustom, customID)
	}
	numericID, err := service.Create(ctx, SaveContentInput{Title: "Numeric", Type: models.ContentTypePost, Status: models.ContentStatusPost}, 1)
	if err != nil {
		t.Fatal(err)
	}
	numeric, err := service.ByID(ctx, numericID)
	if err != nil {
		t.Fatal(err)
	}
	if numeric.Slug != "" || numeric.SlugID != 2 {
		t.Fatalf("numeric slug state = %#v, want empty custom slug and slugID 2", numeric)
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
