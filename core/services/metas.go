package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
	"github.com/Chocola-X/GopherInk/pkg/slug"
)

type MetaService struct {
	db DB
}

type SaveMetaInput struct {
	Name        string
	Slug        string
	Type        string
	Description string
	Parent      int64
}

func NewMetaService(db DB) *MetaService {
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

func (s *MetaService) ListCloud(ctx context.Context, typ string, limit int) ([]models.Meta, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT mid, COALESCE(name,''), COALESCE(slug,''), type, COALESCE(description,''), count, sortOrder, parent
		FROM gb_metas WHERE type = ? AND count > 0 ORDER BY count DESC, name ASC LIMIT ?
	`, typ, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMetas(rows)
}

func (s *MetaService) ListMetasPlugin(ctx context.Context, query plugin.PublicMetaQuery) ([]plugin.PublicMeta, int64, error) {
	metas, err := s.ListFiltered(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	out := make([]plugin.PublicMeta, 0, len(metas))
	for _, meta := range metas {
		out = append(out, plugin.PublicMeta{
			MID: meta.MID, Name: meta.Name, Slug: meta.Slug, Type: meta.Type,
			Description: meta.Description, Count: meta.Count, SortOrder: meta.SortOrder,
			Parent: meta.Parent,
		})
	}
	total, err := s.CountFiltered(ctx, query)
	return out, total, err
}

func (s *MetaService) ListFiltered(ctx context.Context, query plugin.PublicMetaQuery) ([]models.Meta, error) {
	where, args := metaWhere(query)
	sqlQuery := `
		SELECT mid, COALESCE(name,''), COALESCE(slug,''), type, COALESCE(description,''), count, sortOrder, parent
		FROM gb_metas WHERE ` + strings.Join(where, " AND ") + ` ORDER BY sortOrder ASC, mid ASC`
	if query.Limit > 0 {
		sqlQuery += ` LIMIT ? OFFSET ?`
		args = append(args, query.Limit, query.Offset)
	}
	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMetas(rows)
}

func (s *MetaService) CountFiltered(ctx context.Context, query plugin.PublicMetaQuery) (int64, error) {
	where, args := metaWhere(query)
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_metas WHERE `+strings.Join(where, " AND "), args...).Scan(&total)
	return total, err
}

func metaWhere(query plugin.PublicMetaQuery) ([]string, []any) {
	var args []any
	var where []string
	if query.MID > 0 {
		where = append(where, "mid = ?")
		args = append(args, query.MID)
	}
	if query.Type != "" && query.Type != "all" {
		where = append(where, "type = ?")
		args = append(args, query.Type)
	}
	if query.Slug != "" {
		where = append(where, "slug = ?")
		args = append(args, query.Slug)
	}
	if query.Parent > 0 {
		where = append(where, "parent = ?")
		args = append(args, query.Parent)
	}
	if query.Used {
		where = append(where, "count > 0")
	}
	if len(where) == 0 {
		where = append(where, "1 = 1")
	}
	return where, args
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

func (s *MetaService) BySlug(ctx context.Context, typ, slug string) (models.Meta, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mid, COALESCE(name,''), COALESCE(slug,''), type, COALESCE(description,''), count, sortOrder, parent
		FROM gb_metas WHERE type = ? AND slug = ? LIMIT 1
	`, typ, slug)
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
	ctx = WithWriter(ctx)
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return 0, errors.New("name is required")
	}
	metaSlug, err := s.uniqueSlug(ctx, input.Slug, name, input.Type, id)
	if err != nil {
		return 0, err
	}
	if input.Type != "category" {
		input.Parent = 0
	}
	if err := s.validateParent(ctx, input.Type, id, input.Parent); err != nil {
		return 0, err
	}
	if id > 0 {
		current, currentErr := s.ByID(ctx, id)
		if currentErr != nil {
			return 0, currentErr
		}
		sortOrder := current.SortOrder
		if current.Parent != input.Parent {
			sortOrder, err = s.nextSortOrder(ctx, input.Type, input.Parent)
			if err != nil {
				return 0, err
			}
		}
		_, err = s.db.ExecContext(ctx, `UPDATE gb_metas SET name = ?, slug = ?, description = ?, parent = ?, sortOrder = ? WHERE mid = ?`, name, metaSlug, input.Description, input.Parent, sortOrder, id)
		return id, err
	}
	sortOrder, err := s.nextSortOrder(ctx, input.Type, input.Parent)
	if err != nil {
		return 0, err
	}
	if s.db.Dialect() == models.DialectPostgres {
		var newID int64
		err = s.db.QueryRowContext(ctx, `
			INSERT INTO gb_metas (name, slug, type, description, count, sortOrder, parent)
			VALUES (?, ?, ?, ?, 0, ?, ?) RETURNING mid
		`, name, metaSlug, input.Type, input.Description, sortOrder, input.Parent).Scan(&newID)
		return newID, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO gb_metas (name, slug, type, description, count, sortOrder, parent)
		VALUES (?, ?, ?, ?, 0, ?, ?)
	`, name, metaSlug, input.Type, input.Description, sortOrder, input.Parent)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *MetaService) Delete(ctx context.Context, id int64) error {
	ctx = WithWriter(ctx)
	item, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if item.Type == "category" {
		if _, err := txExec(ctx, tx, s.db.Dialect(), `UPDATE gb_metas SET parent = ? WHERE type = 'category' AND parent = ?`, item.Parent, id); err != nil {
			return err
		}
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_relationships WHERE mid = ?`, id); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_metas WHERE mid = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *MetaService) EnsureDefaultCategory(ctx context.Context) error {
	ctx = WithWriter(ctx)
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
	ctx = WithWriter(ctx)
	_, err := s.db.ExecContext(ctx, `
		UPDATE gb_metas SET count = (
			SELECT COUNT(*) FROM gb_relationships r
			JOIN gb_contents c ON c.cid = r.cid
			WHERE r.mid = gb_metas.mid AND c.type = 'post' AND c.status = 'publish' AND COALESCE(c.draftOf, 0) = 0
		)
	`)
	return err
}

func (s *MetaService) RefreshCount(ctx context.Context, id int64) error {
	if id <= 0 {
		return nil
	}
	ctx = WithWriter(ctx)
	_, err := s.db.ExecContext(ctx, `
		UPDATE gb_metas SET count = (
			SELECT COUNT(*) FROM gb_relationships r
			JOIN gb_contents c ON c.cid = r.cid
			WHERE r.mid = gb_metas.mid AND c.type = 'post' AND c.status = 'publish' AND COALESCE(c.draftOf, 0) = 0
		) WHERE mid = ?
	`, id)
	return err
}

func (s *MetaService) Merge(ctx context.Context, targetID int64, sourceIDs []int64, typ string) error {
	ctx = WithWriter(ctx)
	target, err := s.ByID(ctx, targetID)
	if err != nil || target.Type != typ {
		return fmt.Errorf("invalid merge target")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[int64]bool{targetID: true}
	for _, sourceID := range sourceIDs {
		if sourceID <= 0 || seen[sourceID] {
			continue
		}
		seen[sourceID] = true
		var sourceType string
		if err := tx.QueryRowContext(ctx, models.Rebind(s.db.Dialect(), `SELECT type FROM gb_metas WHERE mid = ?`), sourceID).Scan(&sourceType); err != nil {
			return err
		}
		if sourceType != typ {
			return fmt.Errorf("merge source type mismatch")
		}
		if typ == "category" {
			contains, err := categoryAncestorContainsTx(ctx, tx, s.db.Dialect(), targetID, sourceID)
			if err != nil {
				return err
			}
			if contains {
				return fmt.Errorf("cannot merge a category into its descendant")
			}
		}
		rows, err := tx.QueryContext(ctx, models.Rebind(s.db.Dialect(), `SELECT cid FROM gb_relationships WHERE mid = ?`), sourceID)
		if err != nil {
			return err
		}
		var contentIDs []int64
		for rows.Next() {
			var cid int64
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return err
			}
			contentIDs = append(contentIDs, cid)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, cid := range contentIDs {
			if err := insertRelationship(ctx, tx, s.db.Dialect(), cid, targetID); err != nil {
				return err
			}
		}
		if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_relationships WHERE mid = ?`, sourceID); err != nil {
			return err
		}
		if typ == "category" {
			if _, err := txExec(ctx, tx, s.db.Dialect(), `UPDATE gb_metas SET parent = ? WHERE type = 'category' AND parent = ?`, targetID, sourceID); err != nil {
				return err
			}
		}
		if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_metas WHERE mid = ? AND type = ?`, sourceID, typ); err != nil {
			return err
		}
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `
		UPDATE gb_metas SET count = (
			SELECT COUNT(*) FROM gb_relationships r JOIN gb_contents c ON c.cid = r.cid
			WHERE r.mid = gb_metas.mid AND c.type = 'post' AND c.status = 'publish' AND COALESCE(c.draftOf, 0) = 0
		) WHERE mid = ?
	`, targetID); err != nil {
		return err
	}
	return tx.Commit()
}

func categoryAncestorContainsTx(ctx context.Context, tx *sql.Tx, dialect models.Dialect, categoryID, ancestorID int64) (bool, error) {
	seen := map[int64]bool{}
	for categoryID > 0 && !seen[categoryID] {
		if categoryID == ancestorID {
			return true, nil
		}
		seen[categoryID] = true
		var parent int64
		err := tx.QueryRowContext(ctx, models.Rebind(dialect, `SELECT parent FROM gb_metas WHERE mid = ? AND type = 'category'`), categoryID).Scan(&parent)
		if err != nil {
			return false, err
		}
		categoryID = parent
	}
	return false, nil
}

func (s *MetaService) Move(ctx context.Context, id int64, direction string) error {
	ctx = WithWriter(ctx)
	item, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	items, err := s.List(ctx, item.Type)
	if err != nil {
		return err
	}
	var siblings []models.Meta
	for _, candidate := range items {
		if candidate.Parent == item.Parent {
			siblings = append(siblings, candidate)
		}
	}
	index := -1
	for i := range siblings {
		if siblings[i].MID == id {
			index = i
			break
		}
	}
	other := index - 1
	if direction == "down" {
		other = index + 1
	}
	if index < 0 || other < 0 || other >= len(siblings) {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	siblings[index], siblings[other] = siblings[other], siblings[index]
	for position, sibling := range siblings {
		if _, err := txExec(ctx, tx, s.db.Dialect(), `UPDATE gb_metas SET sortOrder = ? WHERE mid = ?`, position+1, sibling.MID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *MetaService) CleanOrphanTags(ctx context.Context) (int64, error) {
	ctx = WithWriter(ctx)
	result, err := s.db.ExecContext(ctx, `DELETE FROM gb_metas WHERE type = 'tag' AND NOT EXISTS (SELECT 1 FROM gb_relationships r WHERE r.mid = gb_metas.mid)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
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

func (s *MetaService) nextSortOrder(ctx context.Context, typ string, parent int64) (int64, error) {
	var order int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sortOrder), 0) + 1 FROM gb_metas WHERE type = ? AND parent = ?`, typ, parent).Scan(&order)
	return order, err
}

func (s *MetaService) validateParent(ctx context.Context, typ string, id, parent int64) error {
	if typ != "category" || parent <= 0 {
		return nil
	}
	if parent == id {
		return errors.New("category cannot be its own parent")
	}
	seen := map[int64]bool{id: true}
	for current := parent; current > 0; {
		if seen[current] {
			return errors.New("category parent would create a cycle")
		}
		seen[current] = true
		var next int64
		var parentType string
		if err := s.db.QueryRowContext(ctx, `SELECT parent, type FROM gb_metas WHERE mid = ?`, current).Scan(&next, &parentType); err != nil {
			return err
		}
		if parentType != "category" {
			return errors.New("invalid category parent")
		}
		current = next
	}
	return nil
}

func insertRelationship(ctx context.Context, db execer, dialect models.Dialect, cid, mid int64) error {
	var query string
	switch dialect {
	case models.DialectPostgres:
		query = `INSERT INTO gb_relationships (cid, mid) VALUES (?, ?) ON CONFLICT (cid, mid) DO NOTHING`
	case models.DialectMySQL:
		query = `INSERT IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`
	default:
		query = `INSERT OR IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`
	}
	_, err := db.ExecContext(ctx, models.Rebind(dialect, query), cid, mid)
	return err
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
