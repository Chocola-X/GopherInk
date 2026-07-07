package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"goblog/core/models"
	"goblog/core/plugin"
	"goblog/pkg/slug"
)

type ContentService struct {
	db DB
}

type SaveContentInput struct {
	Title        string
	Slug         string
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
}

type ContentQuery struct {
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
	return &ContentService{db: WrapDB(db)}
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
		WHERE type = ? AND status = ? AND created <= ?
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

func (s *ContentService) ListPublishedPlugin(ctx context.Context, limit, offset int) ([]plugin.PublicContent, error) {
	contents, err := s.ListPublished(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]plugin.PublicContent, 0, len(contents))
	for _, c := range contents {
		out = append(out, plugin.PublicContent{
			CID: c.CID, Title: c.Title, Slug: c.Slug, Created: c.Created,
			Modified: c.Modified, Text: c.Text, Type: c.Type, Status: c.Status,
		})
	}
	return out, nil
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

	orderBy := "c.modified DESC"
	if q.Type == models.ContentTypePage {
		orderBy = "c.sortOrder ASC, c.created DESC"
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.cid, c.title, c.slug, c.created, c.modified, c.text, c.sortOrder, c.authorId, COALESCE(c.template,''), c.type, c.status,
			COALESCE(c.password,''), c.commentsNum, c.allowComment, c.allowPing, c.allowFeed, c.parent
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
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_contents WHERE type = ?`, models.ContentTypePost).Scan(&count)
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
		{`SELECT COUNT(*) FROM gb_contents WHERE type = 'post'`, &stats.Posts},
		{`SELECT COUNT(*) FROM gb_contents WHERE type = 'page'`, &stats.Pages},
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
	return s.one(ctx, `WHERE slug = ? AND type = ? AND status = ? AND created <= ?`, postSlug, models.ContentTypePost, models.ContentStatusPost, time.Now().Unix())
}

func (s *ContentService) PageBySlug(ctx context.Context, pageSlug string) (models.Content, error) {
	return s.one(ctx, `WHERE slug = ? AND type = ? AND status = ? AND created <= ?`, pageSlug, models.ContentTypePage, models.ContentStatusPost, time.Now().Unix())
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
	now := time.Now().Unix()
	created := input.Created
	if created <= 0 {
		created = now
	}
	postSlug, err := s.uniqueSlug(ctx, input.Slug, input.Title, input.Type, 0)
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
		INSERT INTO gb_contents (title, slug, created, modified, text, sortOrder, authorId, template, type, status, password, commentsNum, allowComment, allowPing, allowFeed, parent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`
	insertArgs := []any{input.Title, postSlug, created, now, input.Text, input.SortOrder, authorID, input.Template, input.Type, normalizeStatus(input.Status), input.Password, boolChar(input.AllowComment), boolChar(input.AllowPing), boolChar(input.AllowFeed), input.Parent}
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
	return id, tx.Commit()
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
	postSlug, err := s.uniqueSlug(ctx, input.Slug, input.Title, input.Type, id)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if current.Type == models.ContentTypePost || current.Type == models.ContentTypePage {
		if err := s.saveRevisionTx(ctx, tx, current); err != nil {
			return err
		}
	}
	_, err = txExec(ctx, tx, s.db.Dialect(), `
		UPDATE gb_contents
		SET title = ?, slug = ?, modified = ?, text = ?, status = ?, password = ?, sortOrder = ?, template = ?, parent = ?, allowComment = ?, allowPing = ?, allowFeed = ?
		WHERE cid = ?
	`, input.Title, postSlug, time.Now().Unix(), input.Text, normalizeStatus(input.Status), input.Password, input.SortOrder, input.Template, input.Parent, boolChar(input.AllowComment), boolChar(input.AllowPing), boolChar(input.AllowFeed), id)
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
	return tx.Commit()
}

func (s *ContentService) MarkStatus(ctx context.Context, id int64, status string) error {
	ctx = WithWriter(ctx)
	_, err := s.db.ExecContext(ctx, `UPDATE gb_contents SET status = ?, modified = ? WHERE cid = ?`, normalizeStatus(status), time.Now().Unix(), id)
	return err
}

func (s *ContentService) Delete(ctx context.Context, id int64) error {
	ctx = WithWriter(ctx)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_relationships WHERE cid = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_comments WHERE cid = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_fields WHERE cid = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_revisions WHERE cid = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM gb_contents WHERE cid = ?`, id)
	return err
}

func (s *ContentService) SaveFields(ctx context.Context, cid int64, fields []SaveFieldInput) error {
	ctx = WithWriter(ctx)
	return s.saveFieldsTxLike(ctx, s.db, cid, fields)
}

func (s *ContentService) saveFieldsTx(ctx context.Context, tx *sql.Tx, cid int64, fields []SaveFieldInput) error {
	return s.saveFieldsTxLike(ctx, txExecer{tx: tx, dialect: s.db.Dialect()}, cid, fields)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type txExecer struct {
	tx      *sql.Tx
	dialect models.Dialect
}

func (e txExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return e.tx.ExecContext(ctx, models.Rebind(e.dialect, query), args...)
}

func txExec(ctx context.Context, tx *sql.Tx, dialect models.Dialect, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, models.Rebind(dialect, query), args...)
}

func (s *ContentService) saveFieldsTxLike(ctx context.Context, db execer, cid int64, fields []SaveFieldInput) error {
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
		switch f.Type {
		case "int":
			out[f.Name] = f.IntValue
		case "float":
			out[f.Name] = f.FloatValue
		default:
			out[f.Name] = f.StrValue
		}
	}
	return out, nil
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
	if c.CID <= 0 || c.Type == models.ContentTypeRevision || c.Type == models.ContentTypeAttach {
		return nil
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO gb_revisions (cid, created, authorId, title, slug, text, status, password, sortOrder, template, parent, allowComment, allowPing, allowFeed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, c.CID, time.Now().Unix(), c.AuthorID, c.Title, c.Slug, c.Text, c.Status, c.Password, c.SortOrder, c.Template, c.Parent, c.AllowComment, c.AllowPing, c.AllowFeed)
	if err != nil {
		return err
	}
	_, _ = db.ExecContext(ctx, `
		DELETE FROM gb_revisions
		WHERE cid = ? AND rid NOT IN (
			SELECT rid FROM gb_revisions WHERE cid = ? ORDER BY rid DESC LIMIT 20
		)
	`, c.CID, c.CID)
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
	prev, prevErr := s.one(ctx, `WHERE type = ? AND status = ? AND created < ? AND created <= ? ORDER BY created DESC`, c.Type, models.ContentStatusPost, c.Created, now)
	if prevErr != nil && !errors.Is(prevErr, sql.ErrNoRows) {
		return models.Content{}, models.Content{}, prevErr
	}
	next, nextErr := s.one(ctx, `WHERE type = ? AND status = ? AND created > ? AND created <= ? ORDER BY created ASC`, c.Type, models.ContentStatusPost, c.Created, now)
	if nextErr != nil && !errors.Is(nextErr, sql.ErrNoRows) {
		return models.Content{}, models.Content{}, nextErr
	}
	return prev, next, nil
}

func (s *ContentService) one(ctx context.Context, where string, args ...any) (models.Content, error) {
	query := `
		SELECT cid, title, slug, created, modified, text, sortOrder, authorId, COALESCE(template,''), type, status,
			COALESCE(password,''), commentsNum, allowComment, allowPing, allowFeed, parent
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
	args := []any{q.Type}
	where := []string{"c.type = ?"}
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
	return where, args
}

func (s *ContentService) syncRelationships(ctx context.Context, cid int64, input SaveContentInput) error {
	return s.syncRelationshipsTxLike(ctx, s.db, cid, input)
}

func (s *ContentService) syncRelationshipsTx(ctx context.Context, tx *sql.Tx, cid int64, input SaveContentInput) error {
	return s.syncRelationshipsTxLike(ctx, txExecer{tx: tx, dialect: s.db.Dialect()}, cid, input)
}

func (s *ContentService) syncRelationshipsTxLike(ctx context.Context, db execer, cid int64, input SaveContentInput) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM gb_relationships WHERE cid = ?`, cid); err != nil {
		return err
	}
	if input.Type == models.ContentTypePost {
		for _, mid := range input.CategoryIDs {
			if mid > 0 {
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
			mid, err := s.ensureTag(ctx, tagName)
			if err != nil {
				return err
			}
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
	_, _ = db.ExecContext(ctx, `
		UPDATE gb_metas SET count = (
			SELECT COUNT(*) FROM gb_relationships r JOIN gb_contents c ON c.cid = r.cid
			WHERE r.mid = gb_metas.mid AND c.type = 'post'
		)
	`)
	return nil
}

func (s *ContentService) ensureTag(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT mid FROM gb_metas WHERE type = 'tag' AND name = ? LIMIT 1`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	metaSlug, err := s.uniqueMetaSlug(ctx, name)
	if err != nil {
		return 0, err
	}
	if s.db.Dialect() == models.DialectPostgres {
		err = s.db.QueryRowContext(ctx, `INSERT INTO gb_metas (name, slug, type, description, count, sortOrder, parent) VALUES (?, ?, 'tag', '', 0, 0, 0) RETURNING mid`, name, metaSlug).Scan(&id)
		return id, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO gb_metas (name, slug, type, description, count, sortOrder, parent) VALUES (?, ?, 'tag', '', 0, 0, 0)`, name, metaSlug)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *ContentService) uniqueSlug(ctx context.Context, raw, title, typ string, exceptID int64) (string, error) {
	base := slug.Make(raw)
	if raw == "" {
		base = slug.Make(title)
	}
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT cid FROM gb_contents WHERE slug = ? AND type = ? LIMIT 1`, candidate, typ).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && id == exceptID) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("cannot allocate unique slug")
}

func (s *ContentService) uniqueMetaSlug(ctx context.Context, name string) (string, error) {
	base := slug.Make(name)
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = base + "-" + strconv.Itoa(i+1)
		}
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT mid FROM gb_metas WHERE slug = ? AND type = 'tag' LIMIT 1`, candidate).Scan(&id)
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
		if err := rows.Scan(&c.CID, &c.Title, &c.Slug, &c.Created, &c.Modified, &c.Text, &c.SortOrder, &c.AuthorID, &c.Template, &c.Type, &c.Status, &c.Password, &c.CommentsNum, &c.AllowComment, &c.AllowPing, &c.AllowFeed, &c.Parent); err != nil {
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
