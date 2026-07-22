package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
	"github.com/Chocola-X/GopherInk/pkg/slug"
)

type ContentService struct {
	db              DB
	revisionEnabled atomic.Bool
	revisionLimit   atomic.Int64
}

type SaveContentInput struct {
	Title        string
	Slug         string
	SlugID       int64
	Text         string
	Type         string
	Status       string
	Password     string
	Created      int64
	SortOrder    int64
	Template     string
	Parent       int64
	AllowComment bool
	AllowPing    bool
	AllowFeed    bool
	CategoryIDs  []int64
	Tags         []string
	Fields       []SaveFieldInput
	DraftOf      int64
}

type ContentQuery struct {
	CID           int64
	Slug          string
	SlugID        int64
	Type          string
	Status        string
	Keywords      string
	Category      int64
	Tag           int64
	AuthorID      int64
	Parent        int64
	Year          int
	Month         int
	Day           int
	ExcludeFuture bool
	IncludeDrafts bool
	Limit         int
	Offset        int
}

type ArchivePeriod struct {
	Year  int
	Month int
	Day   int
	Date  string
	Count int
}

type SaveFieldInput struct {
	Name       string
	Type       string
	StrValue   string
	IntValue   int64
	FloatValue float64
}

func NewContentService(db any) *ContentService {
	service := &ContentService{db: WrapDB(db)}
	service.revisionEnabled.Store(true)
	service.revisionLimit.Store(20)
	return service
}

func (s *ContentService) SetRevisionLimit(limit int) {
	s.SetRevisionConfig(true, limit)
}

func (s *ContentService) SetRevisionConfig(enabled bool, limit int) {
	if limit < 0 {
		limit = 20
	}
	if limit > 10000 {
		limit = 10000
	}
	s.revisionEnabled.Store(enabled)
	s.revisionLimit.Store(int64(limit))
}

func (s *ContentService) DB() *sql.DB {
	return s.db.RawWriter()
}

func (s *ContentService) Dialect() models.Dialect {
	return s.db.Dialect()
}

func (s *ContentService) ListPublished(ctx context.Context, limit, offset int) ([]models.Content, error) {
	return s.List(ctx, ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, ExcludeFuture: true, Limit: limit, Offset: offset})
}

func (s *ContentService) ArchiveMonths(ctx context.Context, loc *time.Location, limit int) ([]ArchivePeriod, error) {
	if loc == nil {
		loc = time.Local
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT created
		FROM gb_contents
		WHERE type = ? AND status = ? AND created <= ? AND COALESCE(draftOf, 0) = 0
		ORDER BY created DESC
	`, models.ContentTypePost, models.ContentStatusPost, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchivePeriod
	seen := map[string]int{}
	for rows.Next() {
		var created int64
		if err := rows.Scan(&created); err != nil {
			return nil, err
		}
		t := time.Unix(created, 0).In(loc)
		key := t.Format("2006-01")
		if idx, ok := seen[key]; ok {
			out[idx].Count++
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		seen[key] = len(out)
		out = append(out, ArchivePeriod{
			Year:  t.Year(),
			Month: int(t.Month()),
			Day:   t.Day(),
			Date:  key,
			Count: 1,
		})
	}
	return out, rows.Err()
}

func (s *ContentService) ListContentsPlugin(ctx context.Context, query plugin.PublicContentQuery) ([]plugin.PublicContent, int64, error) {
	q := ContentQuery{
		CID:           query.CID,
		Slug:          query.Slug,
		SlugID:        query.SlugID,
		Type:          query.Type,
		Status:        query.Status,
		Keywords:      query.Keywords,
		Category:      query.Category,
		Tag:           query.Tag,
		AuthorID:      query.AuthorID,
		Year:          query.Year,
		Month:         query.Month,
		Day:           query.Day,
		Limit:         query.Limit,
		Offset:        query.Offset,
		IncludeDrafts: query.IncludeDrafts,
		ExcludeFuture: query.ExcludeFuture,
	}
	total, err := s.CountList(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	contents, err := s.List(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	out := make([]plugin.PublicContent, 0, len(contents))
	for _, content := range contents {
		out = append(out, publicContentForPlugin(content))
	}
	return out, total, nil
}

func publicContentForPlugin(content models.Content) plugin.PublicContent {
	return plugin.PublicContent{
		CID: content.CID, Title: content.Title, Slug: content.Slug, SlugID: content.SlugID,
		Created: content.Created, Modified: content.Modified, Text: content.Text,
		Type: content.Type, Status: content.Status, AuthorID: content.AuthorID,
		Password: content.Password, CommentsNum: content.CommentsNum,
		AllowComment: content.AllowComment, AllowPing: content.AllowPing, AllowFeed: content.AllowFeed,
		Template: content.Template, Parent: content.Parent, SortOrder: content.SortOrder,
		DraftOf: content.DraftOf,
	}
}

func (s *ContentService) ListAll(ctx context.Context, limit, offset int) ([]models.Content, error) {
	return s.List(ctx, ContentQuery{Type: models.ContentTypePost, Limit: limit, Offset: offset})
}

func (s *ContentService) List(ctx context.Context, q ContentQuery) ([]models.Content, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	where, args := buildContentWhere(q)
	args = append(args, q.Limit, q.Offset)

	orderBy := "c.modified DESC, c.cid DESC"
	if q.Type == models.ContentTypePost && q.Status == models.ContentStatusPost {
		orderBy = "c.created DESC, c.cid DESC"
	} else if q.Type == models.ContentTypePage {
		orderBy = "c.sortOrder ASC, c.created DESC, c.cid DESC"
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.cid, c.title, c.slug, COALESCE(c.slugId,0), c.created, c.modified, c.text, c.sortOrder, c.authorId, COALESCE(c.template,''), c.type, c.status,
			COALESCE(c.password,''), c.commentsNum, c.allowComment, c.allowPing, c.allowFeed, c.parent, COALESCE(c.draftOf,0)
		FROM gb_contents c
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY `+orderBy+`
		LIMIT ? OFFSET ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanContents(rows)
}

func (s *ContentService) Count(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_contents WHERE type = ? AND status = ? AND COALESCE(draftOf, 0) = 0`, models.ContentTypePost, models.ContentStatusPost).Scan(&count)
	return count, err
}

func (s *ContentService) CountList(ctx context.Context, q ContentQuery) (int64, error) {
	where, args := buildContentWhere(q)
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_contents c WHERE `+strings.Join(where, " AND "), args...).Scan(&count)
	return count, err
}

func (s *ContentService) Stats(ctx context.Context) (models.Stats, error) {
	var stats models.Stats
	queries := []struct {
		sql string
		dst *int64
	}{
		{`SELECT COUNT(*) FROM gb_contents WHERE type = 'post' AND status = 'publish' AND COALESCE(draftOf, 0) = 0`, &stats.Posts},
		{`SELECT COUNT(*) FROM gb_contents WHERE type = 'page' AND status = 'publish' AND COALESCE(draftOf, 0) = 0`, &stats.Pages},
		{`SELECT COUNT(*) FROM gb_comments`, &stats.Comments},
		{`SELECT COUNT(*) FROM gb_metas WHERE type = 'category'`, &stats.Categories},
		{`SELECT COUNT(*) FROM gb_metas WHERE type = 'tag'`, &stats.Tags},
		{`SELECT COUNT(*) FROM gb_users`, &stats.Users},
		{`SELECT COUNT(*) FROM gb_comments WHERE status = 'waiting'`, &stats.Waiting},
	}
	for _, q := range queries {
		if err := s.db.QueryRowContext(ctx, q.sql).Scan(q.dst); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (s *ContentService) BySlug(ctx context.Context, postSlug string) (models.Content, error) {
	if c, err := s.one(ctx, `WHERE slug = ? AND slug <> '' AND type = ? AND status = ? AND created <= ? AND COALESCE(draftOf, 0) = 0`, postSlug, models.ContentTypePost, models.ContentStatusPost, time.Now().Unix()); err == nil {
		return c, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return models.Content{}, err
	}
	if slugID, err := strconv.ParseInt(postSlug, 10, 64); err == nil && slugID > 0 {
		return s.one(ctx, `WHERE slugId = ? AND type = ? AND status = ? AND created <= ? AND COALESCE(draftOf, 0) = 0`, slugID, models.ContentTypePost, models.ContentStatusPost, time.Now().Unix())
	}
	return models.Content{}, sql.ErrNoRows
}

func (s *ContentService) PageBySlug(ctx context.Context, pageSlug string) (models.Content, error) {
	if c, err := s.one(ctx, `WHERE slug = ? AND slug <> '' AND type = ? AND status = ? AND created <= ? AND COALESCE(draftOf, 0) = 0`, pageSlug, models.ContentTypePage, models.ContentStatusPost, time.Now().Unix()); err == nil {
		return c, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return models.Content{}, err
	}
	if slugID, err := strconv.ParseInt(pageSlug, 10, 64); err == nil && slugID > 0 {
		return s.one(ctx, `WHERE slugId = ? AND type = ? AND status = ? AND created <= ? AND COALESCE(draftOf, 0) = 0`, slugID, models.ContentTypePage, models.ContentStatusPost, time.Now().Unix())
	}
	return models.Content{}, sql.ErrNoRows
}

func (s *ContentService) PrivateBySlugForAuthor(ctx context.Context, contentSlug, typ string, authorID int64) (models.Content, error) {
	if authorID <= 0 || (typ != models.ContentTypePost && typ != models.ContentTypePage) {
		return models.Content{}, sql.ErrNoRows
	}
	if content, err := s.one(ctx, `WHERE slug = ? AND slug <> '' AND type = ? AND status = 'private' AND authorId = ? AND COALESCE(draftOf, 0) = 0`, contentSlug, typ, authorID); err == nil {
		return content, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return models.Content{}, err
	}
	if slugID, err := strconv.ParseInt(contentSlug, 10, 64); err == nil && slugID > 0 {
		return s.one(ctx, `WHERE slugId = ? AND type = ? AND status = 'private' AND authorId = ? AND COALESCE(draftOf, 0) = 0`, slugID, typ, authorID)
	}
	return models.Content{}, sql.ErrNoRows
}

func (s *ContentService) AttachmentBySlug(ctx context.Context, attachSlug string) (models.Content, error) {
	return s.one(ctx, `WHERE slug = ? AND type = ?`, attachSlug, models.ContentTypeAttach)
}

func (s *ContentService) ByID(ctx context.Context, id int64) (models.Content, error) {
	return s.one(ctx, `WHERE cid = ?`, id)
}

func (s *ContentService) Create(ctx context.Context, input SaveContentInput, authorID int64) (int64, error) {
	ctx = WithWriter(ctx)
	if input.Type == "" {
		input.Type = models.ContentTypePost
	}
	status := normalizeStatus(input.Status)
	now := time.Now().Unix()
	created := input.Created
	if created <= 0 {
		created = now
	}
	slugID, err := s.contentSlugID(ctx, input, models.Content{})
	if err != nil {
		return 0, err
	}
	postSlug, err := s.contentSlug(ctx, input, 0)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	insertSQL := `
		INSERT INTO gb_contents (title, slug, slugId, created, modified, text, sortOrder, authorId, template, type, status, password, commentsNum, allowComment, allowPing, allowFeed, parent, draftOf)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`
	insertArgs := []any{input.Title, postSlug, slugID, created, now, input.Text, input.SortOrder, authorID, input.Template, input.Type, status, input.Password, boolChar(input.AllowComment), boolChar(input.AllowPing), boolChar(input.AllowFeed), input.Parent, input.DraftOf}
	if s.db.Dialect() == models.DialectPostgres {
		err = tx.QueryRowContext(ctx, models.Rebind(s.db.Dialect(), insertSQL+" RETURNING cid"), insertArgs...).Scan(&id)
	} else {
		var res sql.Result
		res, err = tx.ExecContext(ctx, insertSQL, insertArgs...)
		if err == nil {
			id, err = res.LastInsertId()
		}
	}
	if err != nil {
		return 0, err
	}
	if err := s.syncRelationshipsTx(ctx, tx, id, input); err != nil {
		return 0, err
	}
	if input.Fields != nil {
		if err := s.saveFieldsTx(ctx, tx, id, input.Fields); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if err := s.commitContentSlugID(ctx, input.Type, status, input.DraftOf, slugID); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *ContentService) CreateAttachment(ctx context.Context, title, slugValue, filePath string, authorID, parent int64) (int64, error) {
	return s.Create(ctx, SaveContentInput{
		Title:     title,
		Slug:      slugValue,
		Text:      filePath,
		Type:      models.ContentTypeAttach,
		Status:    models.ContentStatusPost,
		AllowFeed: true,
		Parent:    parent,
	}, authorID)
}

func (s *ContentService) CreateAttachmentMeta(ctx context.Context, title, slugValue, text string, authorID, parent int64) (int64, error) {
	return s.Create(ctx, SaveContentInput{
		Title:     title,
		Slug:      slugValue,
		Text:      text,
		Type:      models.ContentTypeAttach,
		Status:    models.ContentStatusPost,
		AllowFeed: true,
		Parent:    parent,
	}, authorID)
}

func (s *ContentService) UpdateAttachmentMeta(ctx context.Context, id int64, title, slugValue, text string, parent int64) error {
	return s.Update(ctx, id, SaveContentInput{
		Title:     title,
		Slug:      slugValue,
		Text:      text,
		Type:      models.ContentTypeAttach,
		Status:    models.ContentStatusPost,
		AllowFeed: true,
		Parent:    parent,
	})
}

func (s *ContentService) Update(ctx context.Context, id int64, input SaveContentInput) error {
	ctx = WithWriter(ctx)
	current, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	if input.Type == "" {
		input.Type = current.Type
	}
	status := normalizeStatus(input.Status)
	slugID, err := s.contentSlugID(ctx, input, current)
	if err != nil {
		return err
	}
	postSlug, err := s.contentSlug(ctx, input, id)
	if err != nil {
		return err
	}
	created := input.Created
	if created <= 0 {
		created = current.Created
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if shouldSnapshotOnUpdate(current, input) {
		if err := s.saveRevisionTx(ctx, tx, current); err != nil {
			return err
		}
	}
	_, err = txExec(ctx, tx, s.db.Dialect(), `
		UPDATE gb_contents
		SET title = ?, slug = ?, slugId = ?, created = ?, modified = ?, text = ?, status = ?, password = ?, sortOrder = ?, template = ?, parent = ?, allowComment = ?, allowPing = ?, allowFeed = ?, draftOf = ?
		WHERE cid = ?
	`, input.Title, postSlug, slugID, created, time.Now().Unix(), input.Text, status, input.Password, input.SortOrder, input.Template, input.Parent, boolChar(input.AllowComment), boolChar(input.AllowPing), boolChar(input.AllowFeed), input.DraftOf, id)
	if err != nil {
		return err
	}
	if err := s.syncRelationshipsTx(ctx, tx, id, input); err != nil {
		return err
	}
	if input.Fields != nil {
		if err := s.saveFieldsTx(ctx, tx, id, input.Fields); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.commitContentSlugID(ctx, input.Type, status, input.DraftOf, slugID)
}

func (s *ContentService) MarkStatus(ctx context.Context, id int64, status string) error {
	ctx = WithWriter(ctx)
	current, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	nextStatus := normalizeStatus(status)
	if current.DraftOf > 0 && nextStatus == models.ContentStatusPost {
		return s.PublishDraft(ctx, id)
	}
	slugID := current.SlugID
	if needsSlugID(current.Type) && current.DraftOf == 0 && slugID <= 0 {
		slugID, err = s.allocateSlugID(ctx, current.Type, current.CID)
		if err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE gb_contents SET status = ?, modified = ?, slugId = ? WHERE cid = ?`, nextStatus, time.Now().Unix(), slugID, id); err != nil {
		return err
	}
	return s.commitContentSlugID(ctx, current.Type, nextStatus, current.DraftOf, slugID)
}

func (s *ContentService) Delete(ctx context.Context, id int64) error {
	ctx = WithWriter(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	txDB := txExecer{tx: tx, dialect: s.db.Dialect()}
	affected, err := metaIDsForContent(ctx, txDB, id)
	if err != nil {
		return err
	}
	var draftID int64
	err = tx.QueryRowContext(ctx, models.Rebind(s.db.Dialect(), `SELECT cid FROM gb_contents WHERE draftOf = ? AND status = ? ORDER BY modified DESC, cid DESC LIMIT 1`), id, models.ContentStatusDraft).Scan(&draftID)
	if err == nil {
		draftMetaIDs, metaErr := metaIDsForContent(ctx, txDB, draftID)
		if metaErr != nil {
			return metaErr
		}
		affected = append(affected, draftMetaIDs...)
		if err := s.deleteDraftTx(ctx, tx, draftID); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_relationships WHERE cid = ?`, id); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_comments WHERE cid = ?`, id); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_fields WHERE cid = ?`, id); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_revisions WHERE cid = ?`, id); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_contents WHERE cid = ?`, id); err != nil {
		return err
	}
	if err := s.cleanupMetasTx(ctx, txDB, affected); err != nil {
		return err
	}
	return tx.Commit()
}

// DraftForContent returns the editing draft for a given published content ID.
// Returns sql.ErrNoRows if no draft exists.
func (s *ContentService) DraftForContent(ctx context.Context, contentID int64) (models.Content, error) {
	return s.one(ctx, `WHERE draftOf = ? AND status = ? ORDER BY modified DESC, cid DESC`, contentID, models.ContentStatusDraft)
}

// DraftMapForContents returns a map of contentID -> true for published contents that have editing drafts.
func (s *ContentService) DraftMapForContents(ctx context.Context, contentIDs []int64) (map[int64]bool, error) {
	if len(contentIDs) == 0 {
		return map[int64]bool{}, nil
	}
	placeholders := make([]string, len(contentIDs))
	args := make([]any, len(contentIDs))
	for i, id := range contentIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT DISTINCT draftOf FROM gb_contents WHERE draftOf IN (` + strings.Join(placeholders, ",") + `) AND draftOf > 0 AND status = ?`
	args = append(args, models.ContentStatusDraft)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]bool{}
	for rows.Next() {
		var draftOf int64
		if err := rows.Scan(&draftOf); err != nil {
			return nil, err
		}
		result[draftOf] = true
	}
	return result, rows.Err()
}

// DeleteDraft deletes an editing draft (draftOf > 0) without affecting the published content.
func (s *ContentService) DeleteDraft(ctx context.Context, draftID int64) error {
	ctx = WithWriter(ctx)
	draft, err := s.ByID(ctx, draftID)
	if err != nil {
		return err
	}
	if draft.DraftOf <= 0 {
		return sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	txDB := txExecer{tx: tx, dialect: s.db.Dialect()}
	affected, err := metaIDsForContent(ctx, txDB, draftID)
	if err != nil {
		return err
	}
	if err := s.deleteDraftTx(ctx, tx, draftID); err != nil {
		return err
	}
	if err := s.cleanupMetasTx(ctx, txDB, affected); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ContentService) SaveEditingDraft(ctx context.Context, publishedID int64, input SaveContentInput, authorID int64) (int64, error) {
	ctx = WithWriter(ctx)
	published, err := s.ByID(ctx, publishedID)
	if err != nil {
		return 0, err
	}
	if published.DraftOf > 0 {
		return 0, sql.ErrNoRows
	}
	if needsSlugID(published.Type) && published.SlugID <= 0 {
		slugID, err := s.allocateSlugID(ctx, published.Type, published.CID)
		if err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE gb_contents SET slugId = ? WHERE cid = ?`, slugID, published.CID); err != nil {
			return 0, err
		}
		published.SlugID = slugID
	}
	input.Type = published.Type
	input.Status = models.ContentStatusDraft
	input.DraftOf = publishedID
	input.SlugID = published.SlugID
	if input.Created <= 0 {
		input.Created = published.Created
	}
	if draft, err := s.DraftForContent(ctx, publishedID); err == nil {
		if err := s.Update(ctx, draft.CID, input); err != nil {
			return 0, err
		}
		if err := s.PruneEditingDrafts(ctx, publishedID, draft.CID); err != nil {
			return 0, err
		}
		return draft.CID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	id, err := s.Create(ctx, input, authorID)
	if err != nil {
		return 0, err
	}
	if err := s.PruneEditingDrafts(ctx, publishedID, id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *ContentService) deleteDraftTx(ctx context.Context, tx *sql.Tx, draftID int64) error {
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_relationships WHERE cid = ?`, draftID); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_fields WHERE cid = ?`, draftID); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_revisions WHERE cid = ?`, draftID); err != nil {
		return err
	}
	_, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_contents WHERE cid = ?`, draftID)
	return err
}

func (s *ContentService) PruneEditingDrafts(ctx context.Context, publishedID, keepID int64) error {
	ctx = WithWriter(ctx)
	rows, err := s.db.QueryContext(ctx, `
		SELECT cid FROM gb_contents
		WHERE draftOf = ? AND status = ? AND cid <> ?
		ORDER BY modified DESC
	`, publishedID, models.ContentStatusDraft, keepID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if err := s.DeleteDraft(ctx, id); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func (s *ContentService) RepairOrphanEditingDrafts(ctx context.Context) error {
	ctx = WithWriter(ctx)
	for _, typ := range []string{models.ContentTypePost, models.ContentTypePage} {
		items, err := s.List(ctx, ContentQuery{Type: typ, Status: "all", IncludeDrafts: true, Limit: 10000})
		if err != nil {
			return err
		}
		publishedBySlug := make(map[string]models.Content)
		for _, item := range items {
			if item.DraftOf == 0 && item.Status == models.ContentStatusPost && item.Slug != "" {
				publishedBySlug[item.Slug] = item
			}
		}
		touched := map[int64]bool{}
		for _, item := range items {
			if item.DraftOf != 0 || item.Status != models.ContentStatusDraft {
				continue
			}
			base, ok := stripNumericSlugSuffix(item.Slug)
			if !ok {
				continue
			}
			published, ok := publishedBySlug[base]
			if !ok || published.AuthorID != item.AuthorID {
				continue
			}
			if _, err := s.db.ExecContext(ctx, `
				UPDATE gb_contents
				SET draftOf = ?, slug = ?, slugId = ?, modified = ?
				WHERE cid = ?
			`, published.CID, published.Slug, published.SlugID, time.Now().Unix(), item.CID); err != nil {
				return err
			}
			touched[published.CID] = true
		}
		for _, item := range items {
			if item.DraftOf == 0 && item.Status == models.ContentStatusPost {
				touched[item.CID] = true
			}
		}
		for publishedID := range touched {
			draft, err := s.DraftForContent(ctx, publishedID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if err := s.PruneEditingDrafts(ctx, publishedID, draft.CID); err != nil {
				return err
			}
		}
	}
	return nil
}

// PublishDraft merges the editing draft content into the published article and deletes the draft.
func (s *ContentService) PublishDraft(ctx context.Context, draftID int64) error {
	ctx = WithWriter(ctx)
	draft, err := s.ByID(ctx, draftID)
	if err != nil {
		return err
	}
	if draft.DraftOf <= 0 {
		return sql.ErrNoRows
	}
	publishedID := draft.DraftOf
	published, err := s.ByID(ctx, publishedID)
	if err != nil {
		return err
	}
	draftCategoryIDs, err := s.CategoriesForContentIDs(ctx, draftID)
	if err != nil {
		return err
	}
	draftTags, err := s.TagsForContentNames(ctx, draftID)
	if err != nil {
		return err
	}
	draftFields, err := s.FieldsForContent(ctx, draftID)
	if err != nil {
		return err
	}
	slugID := published.SlugID
	if slugID <= 0 {
		slugID = draft.SlugID
	}
	if needsSlugID(published.Type) && slugID <= 0 {
		slugID, err = s.allocateSlugID(ctx, published.Type, publishedID)
		if err != nil {
			return err
		}
	}
	postSlug, err := s.contentSlug(ctx, SaveContentInput{Title: draft.Title, Slug: draft.Slug, Type: published.Type}, publishedID)
	if err != nil {
		return err
	}
	created := draft.Created
	if created <= 0 {
		created = published.Created
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.saveRevisionTx(ctx, tx, published); err != nil {
		return err
	}
	_, err = txExec(ctx, tx, s.db.Dialect(), `
		UPDATE gb_contents
		SET title = ?, slug = ?, slugId = ?, created = ?, modified = ?, text = ?, status = ?, password = ?, sortOrder = ?, template = ?, parent = ?, allowComment = ?, allowPing = ?, allowFeed = ?, draftOf = 0
		WHERE cid = ?
	`, draft.Title, postSlug, slugID, created, time.Now().Unix(), draft.Text, models.ContentStatusPost, draft.Password, draft.SortOrder, draft.Template, draft.Parent, draft.AllowComment, draft.AllowPing, draft.AllowFeed, publishedID)
	if err != nil {
		return err
	}
	relInput := SaveContentInput{
		Type:        published.Type,
		CategoryIDs: draftCategoryIDs,
		Tags:        draftTags,
	}
	if err := s.syncRelationshipsTx(ctx, tx, publishedID, relInput); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_fields WHERE cid = ?`, publishedID); err != nil {
		return err
	}
	for _, f := range draftFields {
		if _, err := txExec(ctx, tx, s.db.Dialect(), `
			INSERT INTO gb_fields (cid, name, type, strValue, intValue, floatValue)
			VALUES (?, ?, ?, ?, ?, ?)
		`, publishedID, f.Name, f.Type, f.StrValue, f.IntValue, f.FloatValue); err != nil {
			return err
		}
	}
	if err := s.deleteDraftTx(ctx, tx, draftID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.commitContentSlugID(ctx, published.Type, models.ContentStatusPost, 0, slugID)
}

// CategoriesForContentIDs returns category IDs for a content item.
func (s *ContentService) CategoriesForContentIDs(ctx context.Context, cid int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.mid FROM gb_relationships r
		JOIN gb_metas m ON m.mid = r.mid
		WHERE r.cid = ? AND m.type = 'category'
	`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// TagsForContentNames returns tag names for a content item.
func (s *ContentService) TagsForContentNames(ctx context.Context, cid int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.name FROM gb_relationships r
		JOIN gb_metas m ON m.mid = r.mid
		WHERE r.cid = ? AND m.type = 'tag'
	`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (s *ContentService) MetaIDsForContent(ctx context.Context, cid int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mid FROM gb_relationships WHERE cid = ?`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *ContentService) SaveFields(ctx context.Context, cid int64, fields []SaveFieldInput) error {
	ctx = WithWriter(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.saveFieldsTx(ctx, tx, cid, fields); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ContentService) saveFieldsTx(ctx context.Context, tx *sql.Tx, cid int64, fields []SaveFieldInput) error {
	return s.saveFieldsTxLike(ctx, txExecer{tx: tx, dialect: s.db.Dialect()}, cid, fields)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type txExecer struct {
	tx      *sql.Tx
	dialect models.Dialect
}

func (e txExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return e.tx.ExecContext(ctx, models.Rebind(e.dialect, query), args...)
}

func (e txExecer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return e.tx.QueryContext(ctx, models.Rebind(e.dialect, query), args...)
}

func (e txExecer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return e.tx.QueryRowContext(ctx, models.Rebind(e.dialect, query), args...)
}

func txExec(ctx context.Context, tx *sql.Tx, dialect models.Dialect, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, models.Rebind(dialect, query), args...)
}

func (s *ContentService) saveFieldsTxLike(ctx context.Context, db execer, cid int64, fields []SaveFieldInput) error {
	seen := map[string]bool{}
	for _, field := range fields {
		name := strings.TrimSpace(field.Name)
		if name == "" {
			continue
		}
		if !validFieldName.MatchString(name) {
			return fmt.Errorf("invalid custom field name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate custom field name %q", name)
		}
		seen[name] = true
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM gb_fields WHERE cid = ?`, cid); err != nil {
		return err
	}
	for _, field := range fields {
		field.Name = strings.TrimSpace(field.Name)
		field.Type = normalizeFieldType(field.Type)
		if field.Name == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO gb_fields (cid, name, type, strValue, intValue, floatValue)
			VALUES (?, ?, ?, ?, ?, ?)
		`, cid, field.Name, field.Type, field.StrValue, field.IntValue, field.FloatValue); err != nil {
			return err
		}
	}
	return nil
}

func (s *ContentService) FieldsForContent(ctx context.Context, cid int64) ([]models.Field, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT fid, cid, COALESCE(name,''), type, COALESCE(strValue,''), intValue, floatValue
		FROM gb_fields WHERE cid = ? ORDER BY fid ASC
	`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fields []models.Field
	for rows.Next() {
		var f models.Field
		if err := rows.Scan(&f.FID, &f.CID, &f.Name, &f.Type, &f.StrValue, &f.IntValue, &f.FloatValue); err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return fields, rows.Err()
}

func (s *ContentService) FieldMap(ctx context.Context, cid int64) (map[string]any, error) {
	fields, err := s.FieldsForContent(ctx, cid)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		out[f.Name] = fieldMapValue(f)
	}
	return out, nil
}

func (s *ContentService) FieldMapsForContents(ctx context.Context, cids []int64) (map[int64]map[string]any, error) {
	out := make(map[int64]map[string]any, len(cids))
	args := make([]any, 0, len(cids))
	placeholders := make([]string, 0, len(cids))
	seen := make(map[int64]bool, len(cids))
	for _, cid := range cids {
		if cid <= 0 || seen[cid] {
			continue
		}
		seen[cid] = true
		out[cid] = map[string]any{}
		args = append(args, cid)
		placeholders = append(placeholders, "?")
	}
	if len(args) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT fid, cid, COALESCE(name,''), type, COALESCE(strValue,''), intValue, floatValue
		FROM gb_fields WHERE cid IN (`+strings.Join(placeholders, ",")+`) ORDER BY fid ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var field models.Field
		if err := rows.Scan(&field.FID, &field.CID, &field.Name, &field.Type, &field.StrValue, &field.IntValue, &field.FloatValue); err != nil {
			return nil, err
		}
		out[field.CID][field.Name] = fieldMapValue(field)
	}
	return out, rows.Err()
}

func fieldMapValue(field models.Field) any {
	switch field.Type {
	case "int":
		return field.IntValue
	case "float":
		return field.FloatValue
	case "json":
		var value any
		if err := json.Unmarshal([]byte(field.StrValue), &value); err == nil {
			return value
		}
	}
	return field.StrValue
}

func (s *ContentService) AllFields(ctx context.Context) ([]models.Field, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT fid, cid, COALESCE(name,''), type, COALESCE(strValue,''), intValue, floatValue
		FROM gb_fields ORDER BY fid ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fields []models.Field
	for rows.Next() {
		var f models.Field
		if err := rows.Scan(&f.FID, &f.CID, &f.Name, &f.Type, &f.StrValue, &f.IntValue, &f.FloatValue); err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return fields, rows.Err()
}

func (s *ContentService) AllRelationships(ctx context.Context) ([]models.Relationship, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cid, mid FROM gb_relationships ORDER BY cid ASC, mid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var relationships []models.Relationship
	for rows.Next() {
		var rel models.Relationship
		if err := rows.Scan(&rel.CID, &rel.MID); err != nil {
			return nil, err
		}
		relationships = append(relationships, rel)
	}
	return relationships, rows.Err()
}

func (s *ContentService) SaveRevision(ctx context.Context, c models.Content) error {
	ctx = WithWriter(ctx)
	return s.saveRevisionTxLike(ctx, s.db, c)
}

func (s *ContentService) saveRevisionTx(ctx context.Context, tx *sql.Tx, c models.Content) error {
	return s.saveRevisionTxLike(ctx, txExecer{tx: tx, dialect: s.db.Dialect()}, c)
}

func (s *ContentService) saveRevisionTxLike(ctx context.Context, db execer, c models.Content) error {
	if !s.revisionEnabled.Load() || c.CID <= 0 || c.Type == models.ContentTypeAttach {
		return nil
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO gb_revisions (cid, created, authorId, title, slug, text, status, password, sortOrder, template, parent, allowComment, allowPing, allowFeed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, c.CID, time.Now().Unix(), c.AuthorID, c.Title, c.Slug, c.Text, c.Status, c.Password, c.SortOrder, c.Template, c.Parent, c.AllowComment, c.AllowPing, c.AllowFeed)
	if err != nil {
		return err
	}
	limit := s.revisionLimit.Load()
	if limit <= 0 {
		return nil
	}
	_, _ = db.ExecContext(ctx, `
		DELETE FROM gb_revisions
		WHERE cid = ? AND rid NOT IN (
			SELECT rid FROM (
				SELECT rid FROM gb_revisions WHERE cid = ? ORDER BY rid DESC LIMIT ?
			) AS kept_revisions
		)
	`, c.CID, c.CID, limit)
	return nil
}

func (s *ContentService) Revisions(ctx context.Context, cid int64) ([]models.Revision, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rid, cid, created, authorId, COALESCE(title,''), COALESCE(slug,''), COALESCE(text,''), status, COALESCE(password,''), sortOrder, COALESCE(template,''), parent, allowComment, allowPing, allowFeed
		FROM gb_revisions WHERE cid = ? ORDER BY rid DESC
	`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revisions []models.Revision
	for rows.Next() {
		var r models.Revision
		if err := rows.Scan(&r.RID, &r.CID, &r.Created, &r.AuthorID, &r.Title, &r.Slug, &r.Text, &r.Status, &r.Password, &r.SortOrder, &r.Template, &r.Parent, &r.AllowComment, &r.AllowPing, &r.AllowFeed); err != nil {
			return nil, err
		}
		revisions = append(revisions, r)
	}
	return revisions, rows.Err()
}

func (s *ContentService) RevisionByID(ctx context.Context, rid int64) (models.Revision, error) {
	var r models.Revision
	err := s.db.QueryRowContext(ctx, `
		SELECT rid, cid, created, authorId, COALESCE(title,''), COALESCE(slug,''), COALESCE(text,''), status, COALESCE(password,''), sortOrder, COALESCE(template,''), parent, allowComment, allowPing, allowFeed
		FROM gb_revisions WHERE rid = ?
	`, rid).Scan(&r.RID, &r.CID, &r.Created, &r.AuthorID, &r.Title, &r.Slug, &r.Text, &r.Status, &r.Password, &r.SortOrder, &r.Template, &r.Parent, &r.AllowComment, &r.AllowPing, &r.AllowFeed)
	return r, err
}

func (s *ContentService) DeleteRevision(ctx context.Context, cid, rid int64) error {
	ctx = WithWriter(ctx)
	res, err := s.db.ExecContext(ctx, `DELETE FROM gb_revisions WHERE cid = ? AND rid = ?`, cid, rid)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *ContentService) RestoreRevision(ctx context.Context, cid, rid int64) (int64, error) {
	ctx = WithWriter(ctx)
	revision, err := s.RevisionByID(ctx, rid)
	if err != nil {
		return 0, err
	}
	if revision.CID != cid {
		return 0, sql.ErrNoRows
	}
	current, err := s.ByID(ctx, revision.CID)
	if err != nil {
		return 0, err
	}
	if err := s.SaveRevision(ctx, current); err != nil {
		return 0, err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE gb_contents
		SET title = ?, slug = ?, modified = ?, text = ?, status = ?, password = ?, sortOrder = ?, template = ?, parent = ?, allowComment = ?, allowPing = ?, allowFeed = ?
		WHERE cid = ?
	`, revision.Title, revision.Slug, time.Now().Unix(), revision.Text, revision.Status, revision.Password, revision.SortOrder, revision.Template, revision.Parent, revision.AllowComment, revision.AllowPing, revision.AllowFeed, revision.CID)
	return revision.CID, err
}

func (s *ContentService) Adjacent(ctx context.Context, c models.Content) (prev, next models.Content, err error) {
	now := time.Now().Unix()
	prev, prevErr := s.one(ctx, `WHERE type = ? AND status = ? AND created < ? AND created <= ? AND COALESCE(draftOf, 0) = 0 ORDER BY created DESC`, c.Type, models.ContentStatusPost, c.Created, now)
	if prevErr != nil && !errors.Is(prevErr, sql.ErrNoRows) {
		return models.Content{}, models.Content{}, prevErr
	}
	next, nextErr := s.one(ctx, `WHERE type = ? AND status = ? AND created > ? AND created <= ? AND COALESCE(draftOf, 0) = 0 ORDER BY created ASC`, c.Type, models.ContentStatusPost, c.Created, now)
	if nextErr != nil && !errors.Is(nextErr, sql.ErrNoRows) {
		return models.Content{}, models.Content{}, nextErr
	}
	return prev, next, nil
}

func (s *ContentService) one(ctx context.Context, where string, args ...any) (models.Content, error) {
	query := `
		SELECT cid, title, slug, COALESCE(slugId,0), created, modified, text, sortOrder, authorId, COALESCE(template,''), type, status,
			COALESCE(password,''), commentsNum, allowComment, allowPing, allowFeed, parent, COALESCE(draftOf,0)
		FROM gb_contents ` + where + ` LIMIT 1`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return models.Content{}, err
	}
	defer rows.Close()
	contents, err := scanContents(rows)
	if err != nil {
		return models.Content{}, err
	}
	if len(contents) == 0 {
		return models.Content{}, sql.ErrNoRows
	}
	return contents[0], nil
}

func buildContentWhere(q ContentQuery) ([]string, []any) {
	if q.Type == "" {
		q.Type = models.ContentTypePost
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	var args []any
	var where []string
	if q.Type != "all" {
		where = append(where, "c.type = ?")
		args = append(args, q.Type)
	}
	if !q.IncludeDrafts {
		where = append(where, "COALESCE(c.draftOf, 0) = 0")
	}
	if q.CID > 0 {
		where = append(where, "c.cid = ?")
		args = append(args, q.CID)
	}
	if q.Slug != "" {
		where = append(where, "c.slug = ?")
		args = append(args, q.Slug)
	}
	if q.SlugID > 0 {
		where = append(where, "c.slugId = ?")
		args = append(args, q.SlugID)
	}
	if q.Status != "" && q.Status != "all" {
		where = append(where, "c.status = ?")
		args = append(args, q.Status)
	}
	if q.Keywords != "" {
		where = append(where, "(c.title LIKE ? OR c.text LIKE ? OR c.slug LIKE ?)")
		kw := "%" + q.Keywords + "%"
		args = append(args, kw, kw, kw)
	}
	if q.Category > 0 {
		where = append(where, `EXISTS (SELECT 1 FROM gb_relationships r JOIN gb_metas m ON m.mid = r.mid WHERE r.cid = c.cid AND m.type = 'category' AND m.mid = ?)`)
		args = append(args, q.Category)
	}
	if q.Tag > 0 {
		where = append(where, `EXISTS (SELECT 1 FROM gb_relationships r JOIN gb_metas m ON m.mid = r.mid WHERE r.cid = c.cid AND m.type = 'tag' AND m.mid = ?)`)
		args = append(args, q.Tag)
	}
	if q.AuthorID > 0 {
		where = append(where, "c.authorId = ?")
		args = append(args, q.AuthorID)
	}
	if q.Parent > 0 {
		where = append(where, "c.parent = ?")
		args = append(args, q.Parent)
	}
	if q.ExcludeFuture {
		where = append(where, "c.created <= ?")
		args = append(args, time.Now().Unix())
	}
	if q.Year > 0 {
		start := time.Date(q.Year, time.Month(maxInt(q.Month, 1)), maxInt(q.Day, 1), 0, 0, 0, 0, time.Local)
		end := start.AddDate(1, 0, 0)
		if q.Month > 0 {
			end = start.AddDate(0, 1, 0)
		}
		if q.Day > 0 {
			end = start.AddDate(0, 0, 1)
		}
		where = append(where, "c.created >= ? AND c.created < ?")
		args = append(args, start.Unix(), end.Unix())
	}
	if len(where) == 0 {
		where = append(where, "1 = 1")
	}
	return where, args
}

func (s *ContentService) syncRelationships(ctx context.Context, cid int64, input SaveContentInput) error {
	return s.syncRelationshipsTxLike(ctx, s.db, cid, input)
}

func (s *ContentService) syncRelationshipsTx(ctx context.Context, tx *sql.Tx, cid int64, input SaveContentInput) error {
	return s.syncRelationshipsTxLike(ctx, txExecer{tx: tx, dialect: s.db.Dialect()}, cid, input)
}

func (s *ContentService) syncRelationshipsTxLike(ctx context.Context, db execer, cid int64, input SaveContentInput) error {
	affected, err := metaIDsForContent(ctx, db, cid)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM gb_relationships WHERE cid = ?`, cid); err != nil {
		return err
	}
	if input.Type == models.ContentTypePost {
		for _, mid := range input.CategoryIDs {
			if mid > 0 {
				var categoryID int64
				if err := db.QueryRowContext(ctx, `SELECT mid FROM gb_metas WHERE mid = ? AND type = 'category'`, mid).Scan(&categoryID); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return fmt.Errorf("category %d does not exist", mid)
					}
					return err
				}
				affected = append(affected, mid)
				if s.db.Dialect() == models.DialectPostgres {
					if _, err := db.ExecContext(ctx, `INSERT INTO gb_relationships (cid, mid) VALUES (?, ?) ON CONFLICT (cid, mid) DO NOTHING`, cid, mid); err != nil {
						return err
					}
				} else if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`, cid, mid); err != nil {
					if _, err = db.ExecContext(ctx, `INSERT IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`, cid, mid); err != nil {
						return err
					}
				}
			}
		}
		for _, tagName := range input.Tags {
			tagName = strings.TrimSpace(tagName)
			if tagName == "" {
				continue
			}
			mid, err := s.ensureTagTxLike(ctx, db, tagName)
			if err != nil {
				return err
			}
			affected = append(affected, mid)
			if s.db.Dialect() == models.DialectPostgres {
				if _, err := db.ExecContext(ctx, `INSERT INTO gb_relationships (cid, mid) VALUES (?, ?) ON CONFLICT (cid, mid) DO NOTHING`, cid, mid); err != nil {
					return err
				}
			} else if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`, cid, mid); err != nil {
				if _, err = db.ExecContext(ctx, `INSERT IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`, cid, mid); err != nil {
					return err
				}
			}
		}
	}
	return s.cleanupMetasTx(ctx, db, affected)
}

func (s *ContentService) cleanupMetasTx(ctx context.Context, db execer, ids []int64) error {
	ids = positiveUniqueIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")
	if _, err := db.ExecContext(ctx, `DELETE FROM gb_metas WHERE type = 'tag' AND mid IN (`+inClause+`) AND NOT EXISTS (SELECT 1 FROM gb_relationships r WHERE r.mid = gb_metas.mid)`, args...); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
		UPDATE gb_metas SET count = (
			SELECT COUNT(*) FROM gb_relationships r JOIN gb_contents c ON c.cid = r.cid
			WHERE r.mid = gb_metas.mid AND c.type = 'post' AND c.status = 'publish' AND COALESCE(c.draftOf, 0) = 0
		) WHERE mid IN (`+inClause+`)
	`, args...)
	return err
}

func metaIDsForContent(ctx context.Context, db execer, cid int64) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT mid FROM gb_relationships WHERE cid = ?`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

var validFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (s *ContentService) ensureTag(ctx context.Context, name string) (int64, error) {
	return s.ensureTagTxLike(ctx, s.db, name)
}

func (s *ContentService) ensureTagTxLike(ctx context.Context, db execer, name string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT mid FROM gb_metas WHERE type = 'tag' AND name = ? LIMIT 1`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	metaSlug, err := s.uniqueMetaSlugTxLike(ctx, db, name)
	if err != nil {
		return 0, err
	}
	if s.db.Dialect() == models.DialectPostgres {
		err = db.QueryRowContext(ctx, `INSERT INTO gb_metas (name, slug, type, description, count, sortOrder, parent) VALUES (?, ?, 'tag', '', 0, 0, 0) RETURNING mid`, name, metaSlug).Scan(&id)
		return id, err
	}
	res, err := db.ExecContext(ctx, `INSERT INTO gb_metas (name, slug, type, description, count, sortOrder, parent) VALUES (?, ?, 'tag', '', 0, 0, 0)`, name, metaSlug)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *ContentService) uniqueSlug(ctx context.Context, raw, title, typ string, exceptID int64) (string, error) {
	base := slug.Make(raw)
	if base == "" {
		base = slug.Make(title)
	}
	if base == "" {
		base = "post"
	}
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		taken, err := s.slugCandidateTaken(ctx, candidate, typ, exceptID)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", errors.New("cannot allocate unique slug")
}

func (s *ContentService) contentSlug(ctx context.Context, input SaveContentInput, exceptID int64) (string, error) {
	if needsSlugID(input.Type) {
		raw := strings.TrimSpace(input.Slug)
		if raw == "" {
			return "", nil
		}
		if input.DraftOf > 0 {
			return slug.Make(raw), nil
		}
		return s.uniqueSlug(ctx, raw, "", input.Type, exceptID)
	}
	if input.DraftOf > 0 {
		base := slug.Make(input.Slug)
		if base == "" {
			base = slug.Make(input.Title)
		}
		if base == "" {
			base = "post"
		}
		return base, nil
	}
	return s.uniqueSlug(ctx, input.Slug, input.Title, input.Type, exceptID)
}

func needsSlugID(typ string) bool {
	return typ == models.ContentTypePost || typ == models.ContentTypePage
}

func (s *ContentService) contentSlugID(ctx context.Context, input SaveContentInput, current models.Content) (int64, error) {
	typ := input.Type
	if typ == "" {
		typ = current.Type
	}
	if !needsSlugID(typ) {
		return 0, nil
	}
	if input.DraftOf > 0 {
		parent, err := s.ByID(ctx, input.DraftOf)
		if err != nil {
			return 0, err
		}
		if parent.SlugID > 0 {
			return parent.SlugID, nil
		}
		slugID, err := s.allocateSlugID(ctx, typ, parent.CID)
		if err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE gb_contents SET slugId = ? WHERE cid = ?`, slugID, parent.CID); err != nil {
			return 0, err
		}
		return slugID, nil
	}
	if input.SlugID > 0 {
		return input.SlugID, nil
	}
	if current.CID > 0 && current.SlugID > 0 {
		return current.SlugID, nil
	}
	return s.allocateSlugID(ctx, typ, current.CID)
}

func (s *ContentService) allocateSlugID(ctx context.Context, typ string, exceptID int64) (int64, error) {
	floor, err := s.slugIDFloor(ctx, typ)
	if err != nil {
		return 0, err
	}
	if floor <= 0 {
		floor = 1
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(slug,''), COALESCE(slugId,0)
		FROM gb_contents
		WHERE type = ? AND COALESCE(draftOf, 0) = 0 AND cid <> ?
	`, typ, exceptID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	used := map[int64]bool{}
	for rows.Next() {
		var customSlug string
		var slugID int64
		if err := rows.Scan(&customSlug, &slugID); err != nil {
			return 0, err
		}
		if slugID > 0 {
			used[slugID] = true
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(customSlug), 10, 64); err == nil && n > 0 {
			used[n] = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for candidate := floor; candidate < floor+1000000; candidate++ {
		if !used[candidate] {
			return candidate, nil
		}
	}
	return 0, errors.New("cannot allocate slug id")
}

func (s *ContentService) slugIDFloor(ctx context.Context, typ string) (int64, error) {
	next := int64(1)
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM gb_options WHERE name = ? AND user = 0`, "content_slug_id_next_"+typ).Scan(&value)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil {
		if parsed, parseErr := strconv.ParseInt(strings.TrimSpace(value), 10, 64); parseErr == nil && parsed > next {
			next = parsed
		}
	}
	var maxPublished sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT MAX(COALESCE(slugId,0))
		FROM gb_contents
		WHERE type = ? AND status = ? AND COALESCE(draftOf, 0) = 0
	`, typ, models.ContentStatusPost).Scan(&maxPublished); err != nil {
		return 0, err
	}
	if maxPublished.Valid && maxPublished.Int64 >= next {
		next = maxPublished.Int64 + 1
	}
	return next, nil
}

func (s *ContentService) commitContentSlugID(ctx context.Context, typ, status string, draftOf, slugID int64) error {
	if !needsSlugID(typ) || status != models.ContentStatusPost || draftOf > 0 || slugID <= 0 {
		return nil
	}
	floor, err := s.slugIDFloor(ctx, typ)
	if err != nil {
		return err
	}
	if slugID < floor {
		return nil
	}
	return s.setSlugIDFloor(ctx, typ, slugID+1)
}

func (s *ContentService) setSlugIDFloor(ctx context.Context, typ string, next int64) error {
	value := strconv.FormatInt(next, 10)
	name := "content_slug_id_next_" + typ
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gb_options (name, user, value) VALUES (?, 0, ?)
		ON CONFLICT(name, user) DO UPDATE SET value = excluded.value
	`, name, value)
	if err == nil {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO gb_options (name, user, value) VALUES (?, 0, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, name, value)
	if err == nil {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO gb_options (name, "user", value) VALUES (?, 0, ?)
		ON CONFLICT(name, "user") DO UPDATE SET value = EXCLUDED.value
	`, name, value)
	return err
}

func (s *ContentService) slugCandidateTaken(ctx context.Context, candidate, typ string, exceptID int64) (bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT cid FROM gb_contents WHERE slug = ? AND slug <> '' AND type = ? AND COALESCE(draftOf, 0) = 0 AND cid <> ? LIMIT 1`, candidate, typ, exceptID).Scan(&id)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if needsSlugID(typ) {
		if slugID, parseErr := strconv.ParseInt(candidate, 10, 64); parseErr == nil && slugID > 0 {
			err = s.db.QueryRowContext(ctx, `SELECT cid FROM gb_contents WHERE slugId = ? AND type = ? AND COALESCE(draftOf, 0) = 0 AND cid <> ? LIMIT 1`, slugID, typ, exceptID).Scan(&id)
			if err == nil {
				return true, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return false, err
			}
		}
	}
	return false, nil
}

func stripNumericSlugSuffix(value string) (string, bool) {
	idx := strings.LastIndex(value, "-")
	if idx <= 0 || idx == len(value)-1 {
		return "", false
	}
	if _, err := strconv.Atoi(value[idx+1:]); err != nil {
		return "", false
	}
	return value[:idx], true
}

func shouldSnapshotOnUpdate(current models.Content, input SaveContentInput) bool {
	if current.Type != models.ContentTypePost && current.Type != models.ContentTypePage {
		return false
	}
	if current.DraftOf > 0 || input.DraftOf > 0 {
		return false
	}
	return current.Status == models.ContentStatusPost
}

func (s *ContentService) uniqueMetaSlug(ctx context.Context, name string) (string, error) {
	return s.uniqueMetaSlugTxLike(ctx, s.db, name)
}

func (s *ContentService) uniqueMetaSlugTxLike(ctx context.Context, db execer, name string) (string, error) {
	base := slug.Make(name)
	if base == "" {
		base = "tag"
	}
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = base + "-" + strconv.Itoa(i+1)
		}
		var id int64
		err := db.QueryRowContext(ctx, `SELECT mid FROM gb_metas WHERE slug = ? AND type = 'tag' LIMIT 1`, candidate).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("cannot allocate unique tag slug")
}

func scanContents(rows *sql.Rows) ([]models.Content, error) {
	var contents []models.Content
	for rows.Next() {
		var c models.Content
		if err := rows.Scan(&c.CID, &c.Title, &c.Slug, &c.SlugID, &c.Created, &c.Modified, &c.Text, &c.SortOrder, &c.AuthorID, &c.Template, &c.Type, &c.Status, &c.Password, &c.CommentsNum, &c.AllowComment, &c.AllowPing, &c.AllowFeed, &c.Parent, &c.DraftOf); err != nil {
			return nil, err
		}
		contents = append(contents, c)
	}
	return contents, rows.Err()
}

func normalizeStatus(status string) string {
	switch status {
	case models.ContentStatusDraft, "hidden", "waiting", "private":
		return status
	default:
		return models.ContentStatusPost
	}
}

func boolChar(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func normalizeFieldType(typ string) string {
	switch typ {
	case "int", "float", "json":
		return typ
	default:
		return "str"
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
