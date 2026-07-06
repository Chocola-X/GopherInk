package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
)

type OptionService struct {
	db *sql.DB
}

func NewOptionService(db *sql.DB) *OptionService {
	return &OptionService{db: db}
}

func (s *OptionService) Get(ctx context.Context, name string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM gb_options WHERE name = ? AND user = 0`, name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s *OptionService) Set(ctx context.Context, name, value string) error {
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
	return err
}

func (s *OptionService) EnsureDefaults(ctx context.Context) error {
	defaults := map[string]string{
		"site_title":                  "GoBlog",
		"site_description":            "一个极简的 Go 博客",
		"site_keywords":               "",
		"base_url":                    "http://localhost:8080",
		"active_theme":                "default",
		"default_category":            "1",
		"allow_register":              "0",
		"post_date_format":            "2006-01-02",
		"page_size":                   "10",
		"posts_list_size":             "10",
		"content_render_mode":         "markdown",
		"feed_full_text":              "0",
		"comments_require_moderation": "0",
		"comments_require_mail":       "1",
		"comments_show_url":           "1",
		"comments_order":              "ASC",
		"permalink_post":              "/post/{slug}",
		"permalink_page":              "/page/{slug}",
		"permalink_category":          "/category/{slug}",
	}
	for key, value := range defaults {
		current, err := s.Get(ctx, key)
		if err != nil {
			return err
		}
		if current == "" {
			if err := s.Set(ctx, key, value); err != nil {
				return err
			}
		}
	}

	secret, err := s.Get(ctx, "auth_secret")
	if err != nil {
		return err
	}
	if secret == "" {
		secret, err = randomHex(32)
		if err != nil {
			return err
		}
		return s.Set(ctx, "auth_secret", secret)
	}
	return nil
}

func (s *OptionService) All(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM gb_options WHERE user = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	options := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		options[key] = value
	}
	return options, rows.Err()
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
