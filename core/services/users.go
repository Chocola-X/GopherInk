package services

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"goblog/core/models"

	"golang.org/x/crypto/bcrypt"
)

type UserService struct {
	db *sql.DB
}

type SaveUserInput struct {
	Name       string
	Password   string
	Mail       string
	URL        string
	ScreenName string
	Role       string
}

func NewUserService(db *sql.DB) *UserService {
	return &UserService{db: db}
}

func (s *UserService) EnsureDefaultAdmin(ctx context.Context, name, password, mail string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_users`).Scan(&count); err != nil {
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

func (s *UserService) Authenticate(ctx context.Context, name, password string) (models.User, error) {
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

func (s *UserService) ByName(ctx context.Context, name string) (models.User, error) {
	var u models.User
	err := s.db.QueryRowContext(ctx, `
		SELECT uid, name, password, COALESCE(mail,''), COALESCE(url,''), COALESCE(screenName,''), created, activated, logged, role, COALESCE(authCode,'')
		FROM gb_users WHERE name = ?
	`, name).Scan(&u.UID, &u.Name, &u.Password, &u.Mail, &u.URL, &u.ScreenName, &u.Created, &u.Activated, &u.Logged, &u.Role, &u.AuthCode)
	return u, err
}

func (s *UserService) ByID(ctx context.Context, id int64) (models.User, error) {
	var u models.User
	err := s.db.QueryRowContext(ctx, `
		SELECT uid, name, password, COALESCE(mail,''), COALESCE(url,''), COALESCE(screenName,''), created, activated, logged, role, COALESCE(authCode,'')
		FROM gb_users WHERE uid = ?
	`, id).Scan(&u.UID, &u.Name, &u.Password, &u.Mail, &u.URL, &u.ScreenName, &u.Created, &u.Activated, &u.Logged, &u.Role, &u.AuthCode)
	return u, err
}

func (s *UserService) List(ctx context.Context, keywords string) ([]models.User, error) {
	args := []any{}
	where := ""
	if keywords != "" {
		where = "WHERE name LIKE ? OR screenName LIKE ? OR mail LIKE ?"
		kw := "%" + keywords + "%"
		args = append(args, kw, kw, kw)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT uid, name, password, COALESCE(mail,''), COALESCE(url,''), COALESCE(screenName,''), created, activated, logged, role, COALESCE(authCode,'')
		FROM gb_users `+where+` ORDER BY uid ASC
	`, args...)
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

func (s *UserService) Save(ctx context.Context, input SaveUserInput, id int64) (int64, error) {
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
			_, err = s.db.ExecContext(ctx, `UPDATE gb_users SET name = ?, password = ?, mail = ?, url = ?, screenName = ?, role = ? WHERE uid = ?`, input.Name, string(hash), input.Mail, input.URL, input.ScreenName, input.Role, id)
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
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gb_users`).Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return errors.New("cannot delete the last user")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM gb_users WHERE uid = ?`, id)
	return err
}

func (s *UserService) ChangePassword(ctx context.Context, id int64, password string) error {
	if password == "" {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE gb_users SET password = ? WHERE uid = ?`, string(hash), id)
	return err
}
