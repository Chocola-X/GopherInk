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
	db *sql.DB
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
}

type ContentQuery struct {
	Type     string
	Status   string
	Keywords string
	Category int64
	Limit    int
	Offset   int
}

func NewContentService(db *sql.DB) *ContentService {
	return &ContentService{db: db}
}

func (s *ContentService) ListPublished(ctx context.Context, limit, offset int) ([]models.Content, error) {
	return s.List(ctx, ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Limit: limit, Offset: offset})
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
	return s.one(ctx, `WHERE slug = ? AND type = ? AND status = ?`, postSlug, models.ContentTypePost, models.ContentStatusPost)
}

func (s *ContentService) PageBySlug(ctx context.Context, pageSlug string) (models.Content, error) {
	return s.one(ctx, `WHERE slug = ? AND type = ? AND status = ?`, pageSlug, models.ContentTypePage, models.ContentStatusPost)
}

func (s *ContentService) ByID(ctx context.Context, id int64) (models.Content, error) {
	return s.one(ctx, `WHERE cid = ?`, id)
}

func (s *ContentService) Create(ctx context.Context, input SaveContentInput, authorID int64) (int64, error) {
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
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO gb_contents (title, slug, created, modified, text, sortOrder, authorId, template, type, status, password, commentsNum, allowComment, allowPing, allowFeed, parent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)
	`, input.Title, postSlug, created, now, input.Text, input.SortOrder, authorID, input.Template, input.Type, normalizeStatus(input.Status), input.Password, boolChar(input.AllowComment), boolChar(input.AllowPing), boolChar(input.AllowFeed), input.Parent)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, s.syncRelationships(ctx, id, input)
}

func (s *ContentService) Update(ctx context.Context, id int64, input SaveContentInput) error {
	if input.Type == "" {
		current, err := s.ByID(ctx, id)
		if err != nil {
			return err
		}
		input.Type = current.Type
	}
	postSlug, err := s.uniqueSlug(ctx, input.Slug, input.Title, input.Type, id)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE gb_contents
		SET title = ?, slug = ?, modified = ?, text = ?, status = ?, password = ?, sortOrder = ?, template = ?, parent = ?, allowComment = ?, allowPing = ?, allowFeed = ?
		WHERE cid = ?
	`, input.Title, postSlug, time.Now().Unix(), input.Text, normalizeStatus(input.Status), input.Password, input.SortOrder, input.Template, input.Parent, boolChar(input.AllowComment), boolChar(input.AllowPing), boolChar(input.AllowFeed), id)
	if err != nil {
		return err
	}
	return s.syncRelationships(ctx, id, input)
}

func (s *ContentService) MarkStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE gb_contents SET status = ?, modified = ? WHERE cid = ?`, normalizeStatus(status), time.Now().Unix(), id)
	return err
}

func (s *ContentService) Delete(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_relationships WHERE cid = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_comments WHERE cid = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM gb_contents WHERE cid = ?`, id)
	return err
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

func (s *ContentService) syncRelationships(ctx context.Context, cid int64, input SaveContentInput) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_relationships WHERE cid = ?`, cid); err != nil {
		return err
	}
	if input.Type == models.ContentTypePost {
		for _, mid := range input.CategoryIDs {
			if mid > 0 {
				if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`, cid, mid); err != nil {
					if _, err = s.db.ExecContext(ctx, `INSERT IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`, cid, mid); err != nil {
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
			if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`, cid, mid); err != nil {
				if _, err = s.db.ExecContext(ctx, `INSERT IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`, cid, mid); err != nil {
					return err
				}
			}
		}
	}
	_, _ = s.db.ExecContext(ctx, `
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
