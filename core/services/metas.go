package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"goblog/core/models"
	"goblog/pkg/slug"
)

type MetaService struct {
	db *sql.DB
}

type SaveMetaInput struct {
	Name        string
	Slug        string
	Type        string
	Description string
	Parent      int64
}

func NewMetaService(db *sql.DB) *MetaService {
	return &MetaService{db: db}
}

func (s *MetaService) List(ctx context.Context, typ string) ([]models.Meta, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mid, COALESCE(name,''), COALESCE(slug,''), type, COALESCE(description,''), count, sortOrder, parent
		FROM gb_metas WHERE type = ? ORDER BY sortOrder ASC, mid ASC
	`, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMetas(rows)
}

func (s *MetaService) ByID(ctx context.Context, id int64) (models.Meta, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mid, COALESCE(name,''), COALESCE(slug,''), type, COALESCE(description,''), count, sortOrder, parent
		FROM gb_metas WHERE mid = ? LIMIT 1
	`, id)
	if err != nil {
		return models.Meta{}, err
	}
	defer rows.Close()
	metas, err := scanMetas(rows)
	if err != nil {
		return models.Meta{}, err
	}
	if len(metas) == 0 {
		return models.Meta{}, sql.ErrNoRows
	}
	return metas[0], nil
}

func (s *MetaService) Save(ctx context.Context, input SaveMetaInput, id int64) (int64, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return 0, errors.New("name is required")
	}
	metaSlug, err := s.uniqueSlug(ctx, input.Slug, name, input.Type, id)
	if err != nil {
		return 0, err
	}
	if id > 0 {
		_, err = s.db.ExecContext(ctx, `UPDATE gb_metas SET name = ?, slug = ?, description = ?, parent = ? WHERE mid = ?`, name, metaSlug, input.Description, input.Parent, id)
		return id, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO gb_metas (name, slug, type, description, count, sortOrder, parent)
		VALUES (?, ?, ?, ?, 0, 0, ?)
	`, name, metaSlug, input.Type, input.Description, input.Parent)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *MetaService) Delete(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_relationships WHERE mid = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM gb_metas WHERE mid = ?`, id)
	if err != nil {
		return err
	}
	return s.RefreshCounts(ctx)
}

func (s *MetaService) EnsureDefaultCategory(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_metas WHERE type = 'category'`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := s.Save(ctx, SaveMetaInput{Name: "默认分类", Slug: "default", Type: "category"}, 0)
	return err
}

func (s *MetaService) RefreshCounts(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE gb_metas SET count = (
			SELECT COUNT(*) FROM gb_relationships r
			JOIN gb_contents c ON c.cid = r.cid
			WHERE r.mid = gb_metas.mid AND c.type = 'post'
		)
	`)
	return err
}

func (s *MetaService) TagsForContent(ctx context.Context, cid int64) ([]models.Meta, error) {
	return s.metasForContent(ctx, cid, "tag")
}

func (s *MetaService) CategoriesForContent(ctx context.Context, cid int64) ([]models.Meta, error) {
	return s.metasForContent(ctx, cid, "category")
}

func (s *MetaService) metasForContent(ctx context.Context, cid int64, typ string) ([]models.Meta, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.mid, COALESCE(m.name,''), COALESCE(m.slug,''), m.type, COALESCE(m.description,''), m.count, m.sortOrder, m.parent
		FROM gb_metas m JOIN gb_relationships r ON r.mid = m.mid
		WHERE r.cid = ? AND m.type = ?
		ORDER BY m.sortOrder ASC, m.mid ASC
	`, cid, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMetas(rows)
}

func (s *MetaService) SetDefaultCategory(ctx context.Context, mid int64, options *OptionService) error {
	return options.Set(ctx, "default_category", fmt.Sprint(mid))
}

func (s *MetaService) uniqueSlug(ctx context.Context, raw, name, typ string, exceptID int64) (string, error) {
	base := slug.Make(raw)
	if raw == "" {
		base = slug.Make(name)
	}
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT mid FROM gb_metas WHERE slug = ? AND type = ? LIMIT 1`, candidate, typ).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && id == exceptID) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("cannot allocate unique meta slug")
}

func scanMetas(rows *sql.Rows) ([]models.Meta, error) {
	var metas []models.Meta
	for rows.Next() {
		var m models.Meta
		if err := rows.Scan(&m.MID, &m.Name, &m.Slug, &m.Type, &m.Description, &m.Count, &m.SortOrder, &m.Parent); err != nil {
			return nil, err
		}
		metas = append(metas, m)
	}
	return metas, rows.Err()
}
