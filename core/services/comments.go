package services

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"goblog/core/models"
)

type CommentService struct {
	db *sql.DB
}

type SaveCommentInput struct {
	CID    int64
	Author string
	Mail   string
	URL    string
	Text   string
	Status string
	Parent int64
}

func NewCommentService(db *sql.DB) *CommentService {
	return &CommentService{db: db}
}

func (s *CommentService) List(ctx context.Context, status, keywords string, cid int64) ([]models.Comment, error) {
	if status == "" {
		status = "approved"
	}
	args := []any{status}
	where := []string{"cm.status = ?"}
	if cid > 0 {
		where = append(where, "cm.cid = ?")
		args = append(args, cid)
	}
	if keywords != "" {
		where = append(where, "(cm.author LIKE ? OR cm.mail LIKE ? OR cm.text LIKE ?)")
		kw := "%" + keywords + "%"
		args = append(args, kw, kw, kw)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT cm.coid, cm.cid, cm.created, COALESCE(cm.author,''), cm.authorId, cm.ownerId, COALESCE(cm.mail,''), COALESCE(cm.url,''), COALESCE(cm.ip,''), COALESCE(cm.agent,''), COALESCE(cm.text,''), cm.type, cm.status, cm.parent,
			COALESCE(c.title,''), COALESCE(c.slug,'')
		FROM gb_comments cm LEFT JOIN gb_contents c ON c.cid = cm.cid
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY cm.created DESC LIMIT 200
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []models.Comment
	for rows.Next() {
		var c models.Comment
		if err := rows.Scan(&c.COID, &c.CID, &c.Created, &c.Author, &c.AuthorID, &c.OwnerID, &c.Mail, &c.URL, &c.IP, &c.Agent, &c.Text, &c.Type, &c.Status, &c.Parent, &c.Title, &c.Slug); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

func (s *CommentService) ByID(ctx context.Context, id int64) (models.Comment, error) {
	var c models.Comment
	err := s.db.QueryRowContext(ctx, `
		SELECT coid, cid, created, COALESCE(author,''), authorId, ownerId, COALESCE(mail,''), COALESCE(url,''), COALESCE(ip,''), COALESCE(agent,''), COALESCE(text,''), type, status, parent
		FROM gb_comments WHERE coid = ?
	`, id).Scan(&c.COID, &c.CID, &c.Created, &c.Author, &c.AuthorID, &c.OwnerID, &c.Mail, &c.URL, &c.IP, &c.Agent, &c.Text, &c.Type, &c.Status, &c.Parent)
	return c, err
}

func (s *CommentService) Save(ctx context.Context, input SaveCommentInput, id int64) error {
	status := input.Status
	if status == "" {
		status = "approved"
	}
	if id > 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE gb_comments SET author = ?, mail = ?, url = ?, text = ?, status = ? WHERE coid = ?`, input.Author, input.Mail, input.URL, input.Text, status, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gb_comments (cid, created, author, authorId, ownerId, mail, url, ip, agent, text, type, status, parent)
		VALUES (?, ?, ?, 0, 0, ?, ?, '', '', ?, 'comment', ?, ?)
	`, input.CID, time.Now().Unix(), input.Author, input.Mail, input.URL, input.Text, status, input.Parent)
	if err != nil {
		return err
	}
	return s.refreshContentCount(ctx, input.CID)
}

func (s *CommentService) Mark(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE gb_comments SET status = ? WHERE coid = ?`, status, id)
	return err
}

func (s *CommentService) Delete(ctx context.Context, id int64) error {
	var cid int64
	_ = s.db.QueryRowContext(ctx, `SELECT cid FROM gb_comments WHERE coid = ?`, id).Scan(&cid)
	_, err := s.db.ExecContext(ctx, `DELETE FROM gb_comments WHERE coid = ?`, id)
	if err == nil && cid > 0 {
		err = s.refreshContentCount(ctx, cid)
	}
	return err
}

func (s *CommentService) refreshContentCount(ctx context.Context, cid int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE gb_contents SET commentsNum = (
			SELECT COUNT(*) FROM gb_comments WHERE cid = ? AND status = 'approved'
		) WHERE cid = ?
	`, cid, cid)
	return err
}
