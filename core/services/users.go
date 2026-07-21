package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"

	"golang.org/x/crypto/bcrypt"
)

type UserService struct {
	db DB
}

type SaveUserInput struct {
	Name       string
	Password   string
	Mail       string
	URL        string
	ScreenName string
	Role       string
}

func NewUserService(db any) *UserService {
	return &UserService{db: WrapDB(db)}
}

func (s *UserService) EnsureDefaultAdmin(ctx context.Context, name, password, mail string) error {
	ctx = WithWriter(ctx)
	count, err := s.Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO gb_users (name, password, mail, screenName, created, activated, logged, role)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, name, string(hash), mail, name, now, now, 0, "administrator")
	return err
}

func (s *UserService) Count(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_users`).Scan(&count)
	return count, err
}

func (s *UserService) Authenticate(ctx context.Context, name, password string) (models.User, error) {
	ctx = WithWriter(ctx)
	user, err := s.ByName(ctx, name)
	if err != nil {
		return models.User{}, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return models.User{}, errors.New("invalid credentials")
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE gb_users SET logged = ? WHERE uid = ?`, time.Now().Unix(), user.UID)
	return user, nil
}

func (s *UserService) TouchLogged(ctx context.Context, id int64) error {
	ctx = WithWriter(ctx)
	_, err := s.db.ExecContext(ctx, `UPDATE gb_users SET logged = ? WHERE uid = ?`, time.Now().Unix(), id)
	return err
}

func (s *UserService) ByName(ctx context.Context, name string) (models.User, error) {
	var u models.User
	err := s.db.QueryRowContext(ctx, `
		SELECT uid, name, password, COALESCE(mail,''), COALESCE(url,''), COALESCE(screenName,''), created, activated, logged, role, COALESCE(authCode,'')
		FROM gb_users WHERE name = ?
	`, name).Scan(&u.UID, &u.Name, &u.Password, &u.Mail, &u.URL, &u.ScreenName, &u.Created, &u.Activated, &u.Logged, &u.Role, &u.AuthCode)
	return u, err
}

func (s *UserService) ByMail(ctx context.Context, mail string) (models.User, error) {
	var u models.User
	err := s.db.QueryRowContext(ctx, `
		SELECT uid, name, password, COALESCE(mail,''), COALESCE(url,''), COALESCE(screenName,''), created, activated, logged, role, COALESCE(authCode,'')
		FROM gb_users WHERE mail = ?
	`, mail).Scan(&u.UID, &u.Name, &u.Password, &u.Mail, &u.URL, &u.ScreenName, &u.Created, &u.Activated, &u.Logged, &u.Role, &u.AuthCode)
	return u, err
}

func (s *UserService) ExistsName(ctx context.Context, name string, exceptID int64) (bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT uid FROM gb_users WHERE name = ? AND uid <> ?`, strings.TrimSpace(name), exceptID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *UserService) ExistsMail(ctx context.Context, mail string, exceptID int64) (bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT uid FROM gb_users WHERE mail = ? AND uid <> ?`, strings.TrimSpace(mail), exceptID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *UserService) ByID(ctx context.Context, id int64) (models.User, error) {
	var u models.User
	err := s.db.QueryRowContext(ctx, `
		SELECT uid, name, password, COALESCE(mail,''), COALESCE(url,''), COALESCE(screenName,''), created, activated, logged, role, COALESCE(authCode,'')
		FROM gb_users WHERE uid = ?
	`, id).Scan(&u.UID, &u.Name, &u.Password, &u.Mail, &u.URL, &u.ScreenName, &u.Created, &u.Activated, &u.Logged, &u.Role, &u.AuthCode)
	return u, err
}

func (s *UserService) ListUsersPlugin(ctx context.Context, query plugin.PublicUserQuery) ([]plugin.PublicUser, int64, error) {
	users, err := s.ListFiltered(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	out := make([]plugin.PublicUser, 0, len(users))
	for _, user := range users {
		out = append(out, plugin.PublicUser{
			UID: user.UID, Name: user.Name, Mail: user.Mail, URL: user.URL,
			ScreenName: user.ScreenName, Role: user.Role,
		})
	}
	total, err := s.CountFiltered(ctx, query)
	return out, total, err
}

func (s *UserService) List(ctx context.Context, keywords string) ([]models.User, error) {
	return s.ListFiltered(ctx, plugin.PublicUserQuery{Keywords: keywords})
}

func (s *UserService) ListFiltered(ctx context.Context, query plugin.PublicUserQuery) ([]models.User, error) {
	args := []any{}
	var where []string
	if query.UID > 0 {
		where = append(where, "uid = ?")
		args = append(args, query.UID)
	}
	if query.Name != "" {
		where = append(where, "name = ?")
		args = append(args, query.Name)
	}
	if query.Mail != "" {
		where = append(where, "mail = ?")
		args = append(args, query.Mail)
	}
	if query.Role != "" && query.Role != "all" {
		where = append(where, "role = ?")
		args = append(args, query.Role)
	}
	if query.Keywords != "" {
		where = append(where, "(name LIKE ? OR screenName LIKE ? OR mail LIKE ?)")
		kw := "%" + query.Keywords + "%"
		args = append(args, kw, kw, kw)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	sqlQuery := `
		SELECT uid, name, password, COALESCE(mail,''), COALESCE(url,''), COALESCE(screenName,''), created, activated, logged, role, COALESCE(authCode,'')
		FROM gb_users ` + whereSQL + ` ORDER BY uid ASC`
	if query.Limit > 0 {
		sqlQuery += ` LIMIT ? OFFSET ?`
		args = append(args, query.Limit, query.Offset)
	}
	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.UID, &u.Name, &u.Password, &u.Mail, &u.URL, &u.ScreenName, &u.Created, &u.Activated, &u.Logged, &u.Role, &u.AuthCode); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *UserService) CountFiltered(ctx context.Context, query plugin.PublicUserQuery) (int64, error) {
	args := []any{}
	var where []string
	if query.UID > 0 {
		where = append(where, "uid = ?")
		args = append(args, query.UID)
	}
	if query.Name != "" {
		where = append(where, "name = ?")
		args = append(args, query.Name)
	}
	if query.Mail != "" {
		where = append(where, "mail = ?")
		args = append(args, query.Mail)
	}
	if query.Role != "" && query.Role != "all" {
		where = append(where, "role = ?")
		args = append(args, query.Role)
	}
	if query.Keywords != "" {
		where = append(where, "(name LIKE ? OR screenName LIKE ? OR mail LIKE ?)")
		kw := "%" + query.Keywords + "%"
		args = append(args, kw, kw, kw)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_users `+whereSQL, args...).Scan(&total)
	return total, err
}

func (s *UserService) Save(ctx context.Context, input SaveUserInput, id int64) (int64, error) {
	ctx = WithWriter(ctx)
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return 0, errors.New("name is required")
	}
	if input.ScreenName == "" {
		input.ScreenName = input.Name
	}
	if input.Role == "" {
		input.Role = "subscriber"
	}
	if id > 0 {
		if input.Password != "" {
			hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
			if err != nil {
				return 0, err
			}
			authCode, err := newUserAuthCode()
			if err != nil {
				return 0, err
			}
			_, err = s.db.ExecContext(ctx, `UPDATE gb_users SET name = ?, password = ?, mail = ?, url = ?, screenName = ?, role = ?, authCode = ? WHERE uid = ?`, input.Name, string(hash), input.Mail, input.URL, input.ScreenName, input.Role, authCode, id)
			return id, err
		}
		_, err := s.db.ExecContext(ctx, `UPDATE gb_users SET name = ?, mail = ?, url = ?, screenName = ?, role = ? WHERE uid = ?`, input.Name, input.Mail, input.URL, input.ScreenName, input.Role, id)
		return id, err
	}
	if input.Password == "" {
		return 0, errors.New("password is required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	if s.db.Dialect() == models.DialectPostgres {
		var newID int64
		err = s.db.QueryRowContext(ctx, `
			INSERT INTO gb_users (name, password, mail, url, screenName, created, activated, logged, role, authCode)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, '') RETURNING uid
		`, input.Name, string(hash), input.Mail, input.URL, input.ScreenName, now, now, input.Role).Scan(&newID)
		return newID, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO gb_users (name, password, mail, url, screenName, created, activated, logged, role, authCode)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, '')
	`, input.Name, string(hash), input.Mail, input.URL, input.ScreenName, now, now, input.Role)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *UserService) Delete(ctx context.Context, id int64) error {
	ctx = WithWriter(ctx)
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_users`).Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return errors.New("cannot delete the last user")
	}
	var role string
	if err := s.db.QueryRowContext(ctx, `SELECT role FROM gb_users WHERE uid = ?`, id).Scan(&role); err != nil {
		return err
	}
	if role == "administrator" {
		var administrators int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_users WHERE role = 'administrator'`).Scan(&administrators); err != nil {
			return err
		}
		if administrators <= 1 {
			return errors.New("cannot delete the last administrator")
		}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM gb_users WHERE uid = ?`, id)
	return err
}

func (s *UserService) ChangePassword(ctx context.Context, id int64, password string) error {
	ctx = WithWriter(ctx)
	if password == "" {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	authCode, err := newUserAuthCode()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE gb_users SET password = ?, authCode = ? WHERE uid = ?`, string(hash), authCode, id)
	return err
}

func (s *UserService) RevokeSessions(ctx context.Context, id int64) error {
	ctx = WithWriter(ctx)
	authCode, err := newUserAuthCode()
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gb_users SET authCode = ? WHERE uid = ?`, authCode, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func newUserAuthCode() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
