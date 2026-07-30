package services

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
)

type CommentService struct {
	db DB
}

type SaveCommentInput struct {
	CID      int64
	Author   string
	AuthorID int64
	OwnerID  int64
	Mail     string
	URL      string
	Text     string
	Status   string
	Parent   int64
	IP       string
	Agent    string
	Type     string
}

type CommentQuery struct {
	COID     int64
	Status   string
	Keywords string
	CID      int64
	Type     string
	AuthorID int64
	OwnerID  int64
	Mail     string
	IP       string
	Limit    int
	Offset   int
}

func NewCommentService(db DB) *CommentService {
	return &CommentService{db: db}
}

func (s *CommentService) List(ctx context.Context, status, keywords string, cid int64) ([]models.Comment, error) {
	return s.ListFiltered(ctx, status, keywords, cid, "")
}

func (s *CommentService) ListFiltered(ctx context.Context, status, keywords string, cid int64, typ string) ([]models.Comment, error) {
	return s.ListPage(ctx, CommentQuery{Status: status, Keywords: keywords, CID: cid, Type: typ, Limit: 200})
}

func (s *CommentService) ListPage(ctx context.Context, query CommentQuery) ([]models.Comment, error) {
	where, args := buildCommentWhere(query)
	sqlQuery := `
		SELECT cm.coid, cm.cid, cm.created, COALESCE(cm.author,''), cm.authorId, cm.ownerId, COALESCE(cm.mail,''), COALESCE(cm.url,''), COALESCE(cm.ip,''), COALESCE(cm.agent,''), COALESCE(cm.text,''), cm.type, cm.status, cm.parent,
			COALESCE(c.title,''), COALESCE(c.slug,''), COALESCE(c.type,'')
		FROM gb_comments cm LEFT JOIN gb_contents c ON c.cid = cm.cid
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY cm.created DESC, cm.coid DESC`
	if query.Limit > 0 {
		sqlQuery += ` LIMIT ? OFFSET ?`
		args = append(args, query.Limit, query.Offset)
	}
	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []models.Comment
	for rows.Next() {
		var c models.Comment
		if err := rows.Scan(&c.COID, &c.CID, &c.Created, &c.Author, &c.AuthorID, &c.OwnerID, &c.Mail, &c.URL, &c.IP, &c.Agent, &c.Text, &c.Type, &c.Status, &c.Parent, &c.Title, &c.Slug, &c.ContentType); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

func (s *CommentService) CountFiltered(ctx context.Context, query CommentQuery) (int64, error) {
	where, args := buildCommentWhere(query)
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_comments cm WHERE `+strings.Join(where, " AND "), args...).Scan(&count)
	return count, err
}

func buildCommentWhere(query CommentQuery) ([]string, []any) {
	status := query.Status
	if status == "" {
		status = "approved"
	}
	var args []any
	var where []string
	if status != "all" {
		where = append(where, "cm.status = ?")
		args = append(args, status)
	}
	if query.COID > 0 {
		where = append(where, "cm.coid = ?")
		args = append(args, query.COID)
	}
	if query.CID > 0 {
		where = append(where, "cm.cid = ?")
		args = append(args, query.CID)
	}
	if query.AuthorID > 0 {
		where = append(where, "cm.authorId = ?")
		args = append(args, query.AuthorID)
	}
	if query.OwnerID > 0 {
		where = append(where, "cm.ownerId = ?")
		args = append(args, query.OwnerID)
	}
	if query.Mail != "" {
		where = append(where, "cm.mail = ?")
		args = append(args, query.Mail)
	}
	if query.IP != "" {
		where = append(where, "cm.ip = ?")
		args = append(args, query.IP)
	}
	if query.Type != "" && query.Type != "all" {
		where = append(where, "cm.type = ?")
		args = append(args, query.Type)
	}
	if query.Keywords != "" {
		where = append(where, "(cm.author LIKE ? OR cm.mail LIKE ? OR cm.text LIKE ?)")
		kw := "%" + query.Keywords + "%"
		args = append(args, kw, kw, kw)
	}
	if len(where) == 0 {
		where = append(where, "1 = 1")
	}
	return where, args
}

func (s *CommentService) ExistsByURLType(ctx context.Context, cid int64, commentURL, typ string) (bool, error) {
	if cid <= 0 || commentURL == "" || typ == "" {
		return false, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_comments WHERE cid = ? AND url = ? AND type = ?`, cid, commentURL, typ).Scan(&count)
	return count > 0, err
}

func (s *CommentService) ListForContent(ctx context.Context, cid int64, order string, limit, offset int) ([]models.Comment, error) {
	return s.ListForContentViewer(ctx, cid, order, limit, offset, 0, nil)
}

func (s *CommentService) ListForContentViewer(ctx context.Context, cid int64, order string, limit, offset int, viewerAuthorID int64, visibleWaitingIDs []int64) ([]models.Comment, error) {
	if strings.ToUpper(order) != "DESC" {
		order = "ASC"
	} else {
		order = "DESC"
	}
	visibility := `status = 'approved'`
	args := []any{cid}
	if viewerAuthorID > 0 {
		visibility += ` OR (status = 'waiting' AND authorId = ?)`
		args = append(args, viewerAuthorID)
	}
	ids := positiveUniqueIDs(visibleWaitingIDs)
	if len(ids) > 0 {
		placeholders := make([]string, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args = append(args, id)
		}
		visibility += ` OR (status = 'waiting' AND coid IN (` + strings.Join(placeholders, ",") + `))`
	}
	query := `
		SELECT coid, cid, created, COALESCE(author,''), authorId, ownerId, COALESCE(mail,''), COALESCE(url,''), COALESCE(ip,''), COALESCE(agent,''), COALESCE(text,''), type, status, parent
		FROM gb_comments WHERE cid = ? AND (` + visibility + `) ORDER BY created ` + order + `, coid ` + order
	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []models.Comment
	for rows.Next() {
		var c models.Comment
		if err := rows.Scan(&c.COID, &c.CID, &c.Created, &c.Author, &c.AuthorID, &c.OwnerID, &c.Mail, &c.URL, &c.IP, &c.Agent, &c.Text, &c.Type, &c.Status, &c.Parent); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

func (s *CommentService) CountForContent(ctx context.Context, cid int64) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_comments WHERE cid = ? AND status = 'approved'`, cid).Scan(&count)
	return count, err
}

func (s *CommentService) CountRecentByIP(ctx context.Context, ip string, since int64) (int64, error) {
	return s.CountRecentByIPForContent(ctx, 0, ip, since)
}

func (s *CommentService) CountRecentByIPForContent(ctx context.Context, cid int64, ip string, since int64) (int64, error) {
	if ip == "" {
		return 0, nil
	}
	var count int64
	query := `SELECT COUNT(*) FROM gb_comments WHERE ip = ? AND created >= ?`
	args := []any{ip, since}
	if cid > 0 {
		query += ` AND cid = ?`
		args = append(args, cid)
	}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

func (s *CommentService) ByID(ctx context.Context, id int64) (models.Comment, error) {
	var c models.Comment
	err := s.db.QueryRowContext(ctx, `
		SELECT coid, cid, created, COALESCE(author,''), authorId, ownerId, COALESCE(mail,''), COALESCE(url,''), COALESCE(ip,''), COALESCE(agent,''), COALESCE(text,''), type, status, parent
		FROM gb_comments WHERE coid = ?
	`, id).Scan(&c.COID, &c.CID, &c.Created, &c.Author, &c.AuthorID, &c.OwnerID, &c.Mail, &c.URL, &c.IP, &c.Agent, &c.Text, &c.Type, &c.Status, &c.Parent)
	return c, err
}

func (s *CommentService) ListCommentsPlugin(ctx context.Context, query plugin.PublicCommentQuery) ([]plugin.PublicComment, int64, error) {
	q := CommentQuery{
		COID:     query.COID,
		Status:   query.Status,
		Type:     query.Type,
		Keywords: query.Keywords,
		CID:      query.CID,
		AuthorID: query.AuthorID,
		OwnerID:  query.OwnerID,
		Mail:     query.Mail,
		IP:       query.IP,
		Limit:    query.Limit,
		Offset:   query.Offset,
	}
	total, err := s.CountFiltered(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	comments, err := s.ListPage(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	out := make([]plugin.PublicComment, 0, len(comments))
	for _, comment := range comments {
		out = append(out, plugin.PublicComment{
			COID: comment.COID, CID: comment.CID, Created: comment.Created, Author: comment.Author,
			AuthorID: comment.AuthorID, OwnerID: comment.OwnerID, Mail: comment.Mail, URL: comment.URL,
			IP: comment.IP, Agent: comment.Agent, Text: comment.Text, Type: comment.Type,
			Status: comment.Status, Parent: comment.Parent,
			Title: comment.Title, Slug: comment.Slug, ContentType: comment.ContentType,
		})
	}
	return out, total, nil
}

func (s *CommentService) HasApprovedAuthor(ctx context.Context, author, mail string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM gb_comments WHERE author = ? AND mail = ? AND status = 'approved'
	`, author, mail).Scan(&count)
	return count > 0, err
}

func (s *CommentService) ParentDepth(ctx context.Context, cid, parent int64) (int, error) {
	chain, err := s.ancestorChain(ctx, cid, parent)
	return len(chain), err
}

func (s *CommentService) NormalizeParent(ctx context.Context, cid, parent int64, maxDepth int) (int64, error) {
	chain, err := s.ancestorChain(ctx, cid, parent)
	if err != nil || len(chain) == 0 || maxDepth <= 0 || len(chain) < maxDepth {
		return parent, err
	}
	allowedParentIndex := len(chain) - (maxDepth - 1)
	if allowedParentIndex < 0 {
		allowedParentIndex = 0
	}
	if allowedParentIndex >= len(chain) {
		allowedParentIndex = len(chain) - 1
	}
	return chain[allowedParentIndex], nil
}

func (s *CommentService) ancestorChain(ctx context.Context, cid, parent int64) ([]int64, error) {
	if parent <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE ancestors(coid, parent, cid) AS (
			SELECT coid, parent, cid FROM gb_comments WHERE coid = ? AND cid = ?
			UNION
			SELECT c.coid, c.parent, c.cid FROM gb_comments c JOIN ancestors a ON c.coid = a.parent WHERE c.cid = ?
		)
		SELECT coid, parent FROM ancestors
	`, parent, cid, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parents := map[int64]int64{}
	for rows.Next() {
		var coid, next int64
		if err := rows.Scan(&coid, &next); err != nil {
			return nil, err
		}
		parents[coid] = next
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, ok := parents[parent]; !ok {
		return nil, sql.ErrNoRows
	}
	chain := make([]int64, 0, len(parents))
	seen := map[int64]bool{}
	for current := parent; current > 0; current = parents[current] {
		if seen[current] {
			break
		}
		seen[current] = true
		chain = append(chain, current)
		if _, ok := parents[current]; !ok {
			return nil, sql.ErrNoRows
		}
	}
	return chain, nil
}

func (s *CommentService) Save(ctx context.Context, input SaveCommentInput, id int64) error {
	_, err := s.SaveReturningID(ctx, input, id)
	return err
}

func (s *CommentService) SaveReturningID(ctx context.Context, input SaveCommentInput, id int64) (int64, error) {
	ctx = WithWriter(ctx)
	status := input.Status
	if status == "" {
		status = "approved"
	}
	if id > 0 {
		var cid int64
		var currentType string
		if err := s.db.QueryRowContext(ctx, `SELECT cid, type FROM gb_comments WHERE coid = ?`, id).Scan(&cid, &currentType); err != nil {
			return 0, err
		}
		typ := strings.TrimSpace(input.Type)
		if typ == "" {
			typ = currentType
		}
		_, err := s.db.ExecContext(ctx, `UPDATE gb_comments SET author = ?, mail = ?, url = ?, ip = ?, text = ?, status = ?, type = ? WHERE coid = ?`, input.Author, input.Mail, input.URL, input.IP, input.Text, status, typ, id)
		if err == nil && cid > 0 {
			err = s.refreshContentCount(ctx, cid)
		}
		return id, err
	}
	typ := strings.TrimSpace(input.Type)
	if typ == "" {
		typ = "comment"
	}
	input.Agent = truncateRunes(input.Agent, 511)
	insertSQL := `
		INSERT INTO gb_comments (cid, created, author, authorId, ownerId, mail, url, ip, agent, text, type, status, parent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	args := []any{input.CID, time.Now().Unix(), input.Author, input.AuthorID, input.OwnerID, input.Mail, input.URL, input.IP, input.Agent, input.Text, typ, status, input.Parent}
	var commentID int64
	var err error
	if s.db.Dialect() == models.DialectPostgres {
		err = s.db.QueryRowContext(ctx, insertSQL+` RETURNING coid`, args...).Scan(&commentID)
	} else {
		var result sql.Result
		result, err = s.db.ExecContext(ctx, insertSQL, args...)
		if err == nil {
			commentID, err = result.LastInsertId()
		}
	}
	if err != nil {
		return 0, err
	}
	return commentID, s.refreshContentCount(ctx, input.CID)
}

func (s *CommentService) Mark(ctx context.Context, id int64, status string) error {
	ctx = WithWriter(ctx)
	comment, err := s.ByID(ctx, id)
	if err != nil || comment.Status == status {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := txExec(ctx, tx, s.db.Dialect(), `UPDATE gb_comments SET status = ? WHERE coid = ?`, status, id); err != nil {
		return err
	}
	delta := int64(0)
	if comment.Status == "approved" && status != "approved" {
		delta = -1
	} else if comment.Status != "approved" && status == "approved" {
		delta = 1
	}
	if delta != 0 {
		if _, err := txExec(ctx, tx, s.db.Dialect(), `UPDATE gb_contents SET commentsNum = CASE WHEN commentsNum + ? < 0 THEN 0 ELSE commentsNum + ? END WHERE cid = ?`, delta, delta, comment.CID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *CommentService) MarkMany(ctx context.Context, ids []int64, status string) error {
	ctx = WithWriter(ctx)
	ids = normalizeCommentIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	inClause, idArgs := commentIDClause(ids)
	rows, err := tx.QueryContext(ctx, models.Rebind(s.db.Dialect(), `SELECT coid, cid FROM gb_comments WHERE coid IN (`+inClause+`)`), idArgs...)
	if err != nil {
		return err
	}
	found := 0
	cids := map[int64]struct{}{}
	for rows.Next() {
		var id, cid int64
		if err := rows.Scan(&id, &cid); err != nil {
			_ = rows.Close()
			return err
		}
		found++
		cids[cid] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found != len(ids) {
		return sql.ErrNoRows
	}
	args := make([]any, 0, len(idArgs)+1)
	args = append(args, status)
	args = append(args, idArgs...)
	if _, err := txExec(ctx, tx, s.db.Dialect(), `UPDATE gb_comments SET status = ? WHERE coid IN (`+inClause+`)`, args...); err != nil {
		return err
	}
	if err := refreshContentCountsTx(ctx, tx, s.db.Dialect(), cids); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *CommentService) Delete(ctx context.Context, id int64) error {
	ctx = WithWriter(ctx)
	comment, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := txExec(ctx, tx, s.db.Dialect(), `UPDATE gb_comments SET parent = ? WHERE parent = ?`, comment.Parent, id); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_comments WHERE coid = ?`, id); err != nil {
		return err
	}
	if comment.Status == "approved" {
		if _, err := txExec(ctx, tx, s.db.Dialect(), `UPDATE gb_contents SET commentsNum = CASE WHEN commentsNum > 0 THEN commentsNum - 1 ELSE 0 END WHERE cid = ?`, comment.CID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *CommentService) DeleteMany(ctx context.Context, ids []int64) error {
	ctx = WithWriter(ctx)
	ids = normalizeCommentIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	inClause, idArgs := commentIDClause(ids)
	rows, err := tx.QueryContext(ctx, models.Rebind(s.db.Dialect(), `SELECT coid, cid, parent FROM gb_comments WHERE coid IN (`+inClause+`)`), idArgs...)
	if err != nil {
		return err
	}
	parents := make(map[int64]int64, len(ids))
	cids := map[int64]struct{}{}
	for rows.Next() {
		var id, cid, parent int64
		if err := rows.Scan(&id, &cid, &parent); err != nil {
			_ = rows.Close()
			return err
		}
		parents[id] = parent
		cids[cid] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(parents) != len(ids) {
		return sql.ErrNoRows
	}
	caseParts := make([]string, 0, len(ids))
	reparentArgs := make([]any, 0, len(ids)*3)
	for _, id := range ids {
		caseParts = append(caseParts, "WHEN ? THEN ?")
		reparentArgs = append(reparentArgs, id, nearestSurvivingCommentParent(id, parents))
	}
	reparentArgs = append(reparentArgs, idArgs...)
	if _, err := txExec(ctx, tx, s.db.Dialect(), `UPDATE gb_comments SET parent = CASE parent `+strings.Join(caseParts, " ")+` ELSE parent END WHERE parent IN (`+inClause+`)`, reparentArgs...); err != nil {
		return err
	}
	if _, err := txExec(ctx, tx, s.db.Dialect(), `DELETE FROM gb_comments WHERE coid IN (`+inClause+`)`, idArgs...); err != nil {
		return err
	}
	if err := refreshContentCountsTx(ctx, tx, s.db.Dialect(), cids); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeCommentIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized
}

func commentIDClause(ids []int64) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for index, id := range ids {
		placeholders[index] = "?"
		args[index] = id
	}
	return strings.Join(placeholders, ","), args
}

func nearestSurvivingCommentParent(id int64, deleting map[int64]int64) int64 {
	parent := deleting[id]
	seen := map[int64]struct{}{id: {}}
	for parent > 0 {
		if _, cycle := seen[parent]; cycle {
			return 0
		}
		seen[parent] = struct{}{}
		next, removed := deleting[parent]
		if !removed {
			return parent
		}
		parent = next
	}
	return 0
}

func refreshContentCountsTx(ctx context.Context, tx *sql.Tx, dialect models.Dialect, cids map[int64]struct{}) error {
	if len(cids) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(cids))
	for cid := range cids {
		ids = append(ids, cid)
	}
	inClause, args := commentIDClause(ids)
	_, err := txExec(ctx, tx, dialect, `
		UPDATE gb_contents SET commentsNum = (
			SELECT COUNT(*) FROM gb_comments
			WHERE gb_comments.cid = gb_contents.cid AND gb_comments.status = 'approved'
		) WHERE gb_contents.cid IN (`+inClause+`)`, args...)
	return err
}

func (s *CommentService) ClearSpam(ctx context.Context) error {
	ctx = WithWriter(ctx)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gb_comments WHERE status = 'spam'`); err != nil {
		return err
	}
	return nil
}

func (s *CommentService) refreshContentCount(ctx context.Context, cid int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE gb_contents SET commentsNum = (
			SELECT COUNT(*) FROM gb_comments WHERE cid = ? AND status = 'approved'
		) WHERE cid = ?
	`, cid, cid)
	return err
}

func (s *CommentService) refreshAllContentCounts(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE gb_contents SET commentsNum = (
			SELECT COUNT(*) FROM gb_comments WHERE gb_comments.cid = gb_contents.cid AND status = 'approved'
		)
	`)
	return err
}

func positiveUniqueIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if max > 0 && len(runes) > max {
		return string(runes[:max])
	}
	return value
}
