package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/Chocola-X/GopherInk/core/models"
)

type OptionService struct {
	db      DB
	allMu   sync.Mutex
	cacheMu sync.RWMutex
	cache   map[optionCacheKey]string
	known   map[optionCacheKey]struct{}
	allRead bool
}

type optionCacheKey struct {
	name   string
	userID int64
}

func NewOptionService(db DB) *OptionService {
	return &OptionService{
		db:    db,
		cache: make(map[optionCacheKey]string),
		known: make(map[optionCacheKey]struct{}),
	}
}

func (s *OptionService) Get(ctx context.Context, name string) (string, error) {
	return s.GetForUser(ctx, name, 0)
}

func (s *OptionService) GetForUser(ctx context.Context, name string, userID int64) (string, error) {
	key := optionCacheKey{name: name, userID: userID}
	if value, ok := s.cachedOption(key); ok {
		return value, nil
	}
	s.allMu.Lock()
	defer s.allMu.Unlock()
	if value, ok := s.cachedOption(key); ok {
		return value, nil
	}

	var value string
	userColumn := "user"
	if s.db.Dialect() == models.DialectPostgres {
		userColumn = `"user"`
	}
	err := s.db.QueryRowContext(ctx, `SELECT value FROM gb_options WHERE name = ? AND `+userColumn+` = ?`, name, userID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		s.storeCached(key, "")
		return "", nil
	}
	if err != nil {
		return "", err
	}
	s.storeCached(key, value)
	return value, nil
}

func (s *OptionService) Set(ctx context.Context, name, value string) error {
	return s.SetForUser(ctx, name, value, 0)
}

func (s *OptionService) SetForUser(ctx context.Context, name, value string, userID int64) error {
	s.allMu.Lock()
	defer s.allMu.Unlock()
	ctx = WithWriter(ctx)
	_, err := s.db.ExecContext(ctx, models.UpsertOptionSQL(s.db.Dialect()), name, userID, value)
	if err == nil {
		s.storeCached(optionCacheKey{name: name, userID: userID}, value)
	}
	return err
}

func (s *OptionService) EnsureDefaults(ctx context.Context) error {
	defaults := map[string]string{
		"site_title":                    "GopherInk",
		"site_description":              "一个极简的 Go 博客",
		"site_keywords":                 "",
		"base_url":                      "http://localhost:8086",
		"site_language":                 "en-US",
		"site_timezone":                 "Local",
		"active_theme":                  "default",
		"default_category":              "1",
		"allow_register":                "0",
		"register_default_role":         "subscriber",
		"cookie_prefix":                 "",
		"cookie_secure":                 "0",
		"cookie_samesite":               "Lax",
		"content_autosave_enabled":      "1",
		"post_date_format":              "2006-01-02",
		"page_size":                     "10",
		"posts_list_size":               "10",
		"content_render_mode":           "markdown",
		"feed_full_text":                "0",
		"front_page_type":               "posts",
		"front_page_cid":                "0",
		"posts_index_path":              "/",
		"revision_enabled":              "1",
		"revision_limit":                "20",
		"comments_require_moderation":   "0",
		"comments_moderation_mode":      "",
		"comments_require_mail":         "1",
		"comments_require_url":          "0",
		"comments_show_url":             "1",
		"comments_order":                "ASC",
		"comment_date_format":           "2006-01-02 15:04",
		"comments_list_size":            "10",
		"comments_page_size":            "20",
		"comments_page_display":         "last",
		"comments_max_nesting_levels":   "3",
		"comments_whitelist":            "0",
		"comments_check_referer":        "1",
		"comments_antispam":             "1",
		"comments_auto_close":           "0",
		"comments_post_interval":        "60",
		"comments_post_interval_enable": "1",
		"comments_html_tag_allowed":     "",
		"comments_stop_words":           "",
		"comments_ip_blacklist":         "",
		"comments_markdown":             "0",
		"comments_url_nofollow":         "1",
		"comments_avatar":               "1",
		"comments_avatar_rating":        "g",
		"avatar_url_template":           "",
		"upload_allowed_exts":           "jpg,jpeg,png,gif,webp,svg,pdf,txt,md,zip",
		"upload_max_size":               "16777216",
		"upload_image_processing":       "original",
		"upload_webp_quality":           "85",
		"image_processing_memory_mb":    "256",
		"thumbnail_format":              "jpg",
		"thumbnail_quality":             "",
		"upload_replace_same_ext_only":  "1",
		"attachment_delete_policy":      "keep",
		"xmlrpc_mode":                   "on",
		"enable_trackback":              "1",
		"http_client_timeout":           "5",
		"http_client_user_agent":        "GopherInk/0.5.0",
		"http_client_proxy":             "",
		"http_client_retries":           "1",
		"db_read_dsn":                   "",
		"db_write_dsn":                  "",
		"permalink_post":                "/post/{slug}.html",
		"permalink_page":                "/page/{slug}.html",
		"permalink_category":            "/category/{slug}",
		"waf_enabled":                   "1",
		"waf_hsts_enabled":              "0",
		"waf_trust_proxy_headers":       "0",
		"waf_trust_proxy_mode":          "allowlist",
		"waf_trust_proxy_ips":           "",
		"waf_url_index_enabled":         "1",
		"waf_url_index_ttl":             "60",
		"waf_cache_enabled":             "1",
		"waf_cache_ttl":                 "30",
		"waf_cache_max_entries":         "512",
		"waf_cache_max_body_kb":         "512",
		"waf_cache_max_memory_mb":       "32",
		"waf_dynamic_rate_enabled":      "1",
		"waf_dynamic_rate_window":       "60",
		"waf_dynamic_rate_limit":        "300",
		"waf_static_rate_enabled":       "1",
		"waf_static_rate_window":        "60",
		"waf_static_rate_limit":         "1200",
		"waf_upload_rate_enabled":       "1",
		"waf_upload_rate_window":        "60",
		"waf_upload_rate_limit":         "600",
		"waf_attachment_ban_enabled":    "1",
		"waf_attachment_ban_window":     "60",
		"waf_attachment_ban_limit":      "120",
		"waf_attachment_ban_seconds":    "600",
		"waf_invalid_path_enabled":      "1",
		"waf_invalid_path_window":       "60",
		"waf_invalid_path_limit":        "20",
		"waf_invalid_path_ban_seconds":  "600",
		"waf_search_rate_enabled":       "1",
		"waf_search_rate_window":        "60",
		"waf_search_rate_limit":         "20",
		"waf_xmlrpc_rate_enabled":       "1",
		"waf_xmlrpc_rate_window":        "60",
		"waf_xmlrpc_rate_limit":         "30",
		"waf_login_ban_enabled":         "1",
		"waf_login_window":              "300",
		"waf_login_failures":            "5",
		"waf_login_ban_seconds":         "900",
		"waf_state_max_entries":         "100000",
		"waf_log_max_entries":           "1000",
		"memory_guard_enabled":          "1",
		"memory_guard_min_available_mb": "50",
		"plugin_db_default_mode":        "sqlite",
		"active_plugins":                `["sitemap","virtual-files"]`,
		"schema_version":                "1",
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
	if options, ok := s.cachedAll(); ok {
		return options, nil
	}
	s.allMu.Lock()
	defer s.allMu.Unlock()
	if options, ok := s.cachedAll(); ok {
		return options, nil
	}

	userColumn := "user"
	if s.db.Dialect() == models.DialectPostgres {
		userColumn = `"user"`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM gb_options WHERE `+userColumn+` = 0`)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	for key := range s.known {
		if key.userID == 0 {
			delete(s.known, key)
			delete(s.cache, key)
		}
	}
	for name, value := range options {
		key := optionCacheKey{name: name}
		s.known[key] = struct{}{}
		s.cache[key] = value
	}
	s.allRead = true
	s.cacheMu.Unlock()
	return cloneStringMap(options), nil
}

func (s *OptionService) cachedAll() (map[string]string, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	if !s.allRead {
		return nil, false
	}
	options := make(map[string]string)
	for key, value := range s.cache {
		if key.userID == 0 {
			options[key.name] = value
		}
	}
	return options, true
}

func (s *OptionService) cachedOption(key optionCacheKey) (string, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	value := s.cache[key]
	_, known := s.known[key]
	if known || (key.userID == 0 && s.allRead) {
		return value, true
	}
	return "", false
}

func (s *OptionService) storeCached(key optionCacheKey, value string) {
	s.cacheMu.Lock()
	s.cache[key] = value
	s.known[key] = struct{}{}
	s.cacheMu.Unlock()
}

func cloneStringMap(values map[string]string) map[string]string {
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
