package handlers

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/services"
)

type wafConfig struct {
	Enabled               bool
	HSTSEnabled           bool
	TrustProxy            proxyTrustConfig
	LogMaxEntries         int
	URLIndexEnabled       bool
	URLIndexTTL           time.Duration
	CacheEnabled          bool
	CacheTTL              time.Duration
	DynamicRateEnabled    bool
	DynamicRateWindow     time.Duration
	DynamicRateLimit      int
	StaticRateEnabled     bool
	StaticRateWindow      time.Duration
	StaticRateLimit       int
	UploadRateEnabled     bool
	UploadRateWindow      time.Duration
	UploadRateLimit       int
	AttachmentBanEnabled  bool
	AttachmentBanWindow   time.Duration
	AttachmentBanLimit    int
	AttachmentBan         time.Duration
	InvalidPathEnabled    bool
	InvalidPathWindow     time.Duration
	InvalidPathLimit      int
	InvalidPathBan        time.Duration
	SearchRateEnabled     bool
	SearchRateWindow      time.Duration
	SearchRateLimit       int
	XMLRPCRateEnabled     bool
	XMLRPCRateWindow      time.Duration
	XMLRPCRateLimit       int
	LoginBanEnabled       bool
	LoginWindow           time.Duration
	LoginFailures         int
	LoginBan              time.Duration
	PublicCacheMaxEntries int
	PublicIndexMaxItems   int
	StateMaxEntries       int
}

type wafManager struct {
	app *App

	mu           sync.Mutex
	logMu        sync.Mutex
	config       wafConfig
	configLoaded time.Time

	rates       map[string]*wafCounter
	invalids    map[string]*wafCounter
	attachments map[string]*wafCounter
	loginFails  map[string]*wafCounter
	bans        map[string]time.Time
	loginBans   map[string]time.Time
	publicIndex map[string]struct{}
	indexLoaded time.Time
	cache       map[string]wafCacheEntry
}

type wafCounter struct {
	Start time.Time
	Count int
}

type wafCacheEntry struct {
	Status    int
	Header    http.Header
	Body      []byte
	ExpiresAt time.Time
}

func newWAFManager(app *App) *wafManager {
	return &wafManager{
		app:         app,
		rates:       map[string]*wafCounter{},
		invalids:    map[string]*wafCounter{},
		attachments: map[string]*wafCounter{},
		loginFails:  map[string]*wafCounter{},
		bans:        map[string]time.Time{},
		loginBans:   map[string]time.Time{},
		publicIndex: map[string]struct{}{},
		cache:       map[string]wafCacheEntry{},
	}
}

func (m *wafManager) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := m.currentConfig(r.Context())
		sw := newSecurityResponseWriter(w, r, cfg.HSTSEnabled)
		if !cfg.Enabled {
			next.ServeHTTP(sw, r)
			return
		}
		if m.authenticatedAdminBackendRequest(r) {
			next.ServeHTTP(sw, r)
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				m.invalidatePublicData()
			}
			return
		}
		ip := clientIP(r, cfg.TrustProxy)
		now := time.Now()
		if m.isBanned(ip, now) {
			m.logEvent(cfg, "blocked banned IP %s on %s", ip, r.URL.Path)
			http.Error(sw, "forbidden", http.StatusForbidden)
			return
		}
		if kind, enabled, window, limit := requestRatePolicy(r, cfg); enabled {
			if !m.allowWindow(m.rates, kind+"|"+ip, window, limit, now) {
				m.logEvent(cfg, "%s rate limit exceeded for IP %s on %s", kind, ip, r.URL.Path)
				http.Error(sw, "too many requests", http.StatusTooManyRequests)
				return
			}
		}
		if cfg.SearchRateEnabled && isSearchRequest(r) && !m.allowWindow(m.rates, "search|"+ip, cfg.SearchRateWindow, cfg.SearchRateLimit, now) {
			m.logEvent(cfg, "search rate limit exceeded for IP %s on %s", ip, r.URL.Path)
			http.Error(sw, "too many search requests", http.StatusTooManyRequests)
			return
		}
		if cfg.XMLRPCRateEnabled && isXMLRPCRequest(r) && !m.allowWindow(m.rates, "xmlrpc|"+ip, cfg.XMLRPCRateWindow, cfg.XMLRPCRateLimit, now) {
			m.logEvent(cfg, "XML-RPC rate limit exceeded for IP %s on %s", ip, r.URL.Path)
			http.Error(sw, "too many XML-RPC requests", http.StatusTooManyRequests)
			return
		}
		if cfg.URLIndexEnabled && m.pluginRouteMayHandle(r) {
			next.ServeHTTP(sw, r)
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				m.invalidatePublicData()
			}
			return
		}
		if cfg.URLIndexEnabled && shouldCheckPublicURLIndex(r) {
			exists, err := m.publicURLExists(r.Context(), r.URL.Path, cfg, now)
			if err == nil && !exists {
				if cfg.InvalidPathEnabled && m.recordInvalidPath(ip, cfg, now) {
					m.logEvent(cfg, "invalid path ban triggered for IP %s on %s", ip, r.URL.Path)
					http.Error(sw, "forbidden", http.StatusForbidden)
					return
				}
				http.NotFound(sw, r)
				return
			}
		}
		if cfg.CacheEnabled && m.cacheablePublicRequest(r) {
			if entry, ok := m.cachedResponse(cacheKey(r), now); ok {
				copyHeaders(sw.Header(), entry.Header)
				sw.WriteHeader(entry.Status)
				if r.Method != http.MethodHead {
					_, _ = sw.Write(entry.Body)
				}
				return
			}
			rec := newWAFResponseRecorder(sw)
			next.ServeHTTP(rec, r)
			rec.flush()
			if rec.status == http.StatusOK && len(rec.body.Bytes()) > 0 {
				m.storeCachedResponse(cacheKey(r), rec.status, rec.header, rec.body.Bytes(), cfg, now)
			}
			return
		}
		if cfg.AttachmentBanEnabled && isAttachmentDownloadRequest(r) && m.recordAttachmentDownload(ip, cfg, now) {
			m.mu.Lock()
			m.bans[ip] = now.Add(cfg.AttachmentBan)
			m.trimTimeMapLocked(m.bans, now, m.config.StateMaxEntries)
			m.mu.Unlock()
			m.logEvent(cfg, "attachment download ban triggered for IP %s on %s", ip, r.URL.Path)
			http.Error(sw, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(sw, r)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			m.invalidatePublicData()
		}
	})
}

func (m *wafManager) authenticatedAdminBackendRequest(r *http.Request) bool {
	if !isBackendPath(r.URL.Path) {
		return false
	}
	user, ok := m.app.currentUser(r)
	return ok && roleRank(user.Role) >= roleRank("administrator")
}

func (m *wafManager) pluginRouteMayHandle(r *http.Request) bool {
	for _, route := range m.app.Plugins.Routes() {
		if route.Plugin != "" && !m.app.Plugins.IsActive(route.Plugin) {
			continue
		}
		if route.Method != "" && route.Method != r.Method {
			continue
		}
		pattern := cleanIndexPath(route.Pattern)
		requestPath := cleanIndexPath(r.URL.Path)
		if strings.HasSuffix(route.Pattern, "/") {
			if requestPath == pattern || strings.HasPrefix(requestPath, strings.TrimRight(pattern, "/")+"/") {
				return true
			}
			continue
		}
		if requestPath == pattern {
			return true
		}
	}
	return false
}

func isBackendPath(value string) bool {
	return value == "/admin" || strings.HasPrefix(value, "/admin/")
}

func (m *wafManager) currentConfig(ctx context.Context) wafConfig {
	now := time.Now()
	m.mu.Lock()
	if !m.configLoaded.IsZero() && now.Sub(m.configLoaded) < 5*time.Second {
		cfg := m.config
		m.mu.Unlock()
		return cfg
	}
	m.mu.Unlock()

	options, err := m.app.Options.All(ctx)
	if err != nil {
		options = map[string]string{}
	}
	cfg := wafConfig{
		Enabled:               optionBool(defaultString(options["waf_enabled"], "1")),
		HSTSEnabled:           optionBool(defaultString(options["waf_hsts_enabled"], "0")),
		TrustProxy:            loadProxyTrustConfig(options),
		LogMaxEntries:         boundedInt(options["waf_log_max_entries"], 1000, 1, 100000),
		URLIndexEnabled:       optionBool(defaultString(options["waf_url_index_enabled"], "1")),
		URLIndexTTL:           durationSeconds(options["waf_url_index_ttl"], 60),
		CacheEnabled:          optionBool(defaultString(options["waf_cache_enabled"], "1")),
		CacheTTL:              durationSeconds(options["waf_cache_ttl"], 30),
		DynamicRateEnabled:    optionBool(defaultString(options["waf_dynamic_rate_enabled"], legacyOption(options, "waf_ip_rate_enabled", "1"))),
		DynamicRateWindow:     durationSeconds(defaultString(options["waf_dynamic_rate_window"], legacyOption(options, "waf_ip_rate_window", "60")), 60),
		DynamicRateLimit:      boundedInt(defaultString(options["waf_dynamic_rate_limit"], legacyOption(options, "waf_ip_rate_limit", "300")), 300, 1, 100000),
		StaticRateEnabled:     optionBool(defaultString(options["waf_static_rate_enabled"], "1")),
		StaticRateWindow:      durationSeconds(options["waf_static_rate_window"], 60),
		StaticRateLimit:       boundedInt(options["waf_static_rate_limit"], 1200, 1, 100000),
		UploadRateEnabled:     optionBool(defaultString(options["waf_upload_rate_enabled"], "1")),
		UploadRateWindow:      durationSeconds(options["waf_upload_rate_window"], 60),
		UploadRateLimit:       boundedInt(options["waf_upload_rate_limit"], 600, 1, 100000),
		AttachmentBanEnabled:  optionBool(defaultString(options["waf_attachment_ban_enabled"], "1")),
		AttachmentBanWindow:   durationSeconds(options["waf_attachment_ban_window"], 60),
		AttachmentBanLimit:    boundedInt(options["waf_attachment_ban_limit"], 120, 1, 100000),
		AttachmentBan:         durationSeconds(options["waf_attachment_ban_seconds"], 600),
		InvalidPathEnabled:    optionBool(defaultString(options["waf_invalid_path_enabled"], "1")),
		InvalidPathWindow:     durationSeconds(options["waf_invalid_path_window"], 60),
		InvalidPathLimit:      boundedInt(options["waf_invalid_path_limit"], 20, 1, 100000),
		InvalidPathBan:        durationSeconds(options["waf_invalid_path_ban_seconds"], 600),
		SearchRateEnabled:     optionBool(defaultString(options["waf_search_rate_enabled"], "1")),
		SearchRateWindow:      durationSeconds(options["waf_search_rate_window"], 60),
		SearchRateLimit:       boundedInt(options["waf_search_rate_limit"], 20, 1, 100000),
		XMLRPCRateEnabled:     optionBool(defaultString(options["waf_xmlrpc_rate_enabled"], "1")),
		XMLRPCRateWindow:      durationSeconds(options["waf_xmlrpc_rate_window"], 60),
		XMLRPCRateLimit:       boundedInt(options["waf_xmlrpc_rate_limit"], 30, 1, 100000),
		LoginBanEnabled:       optionBool(defaultString(options["waf_login_ban_enabled"], "1")),
		LoginWindow:           durationSeconds(options["waf_login_window"], 300),
		LoginFailures:         boundedInt(options["waf_login_failures"], 5, 1, 100000),
		LoginBan:              durationSeconds(options["waf_login_ban_seconds"], 900),
		PublicCacheMaxEntries: boundedInt(options["waf_cache_max_entries"], 512, 1, 100000),
		PublicIndexMaxItems:   boundedInt(options["waf_index_max_items"], 10000, 100, 1000000),
		StateMaxEntries:       boundedInt(options["waf_state_max_entries"], 100000, 1000, 1000000),
	}

	m.mu.Lock()
	m.config = cfg
	m.configLoaded = now
	m.mu.Unlock()
	return cfg
}

func (m *wafManager) publicURLExists(ctx context.Context, requestPath string, cfg wafConfig, now time.Time) (bool, error) {
	m.mu.Lock()
	loaded := !m.indexLoaded.IsZero() && now.Sub(m.indexLoaded) < cfg.URLIndexTTL
	m.mu.Unlock()
	if !loaded {
		if err := m.refreshPublicIndex(ctx, cfg, now); err != nil {
			return true, err
		}
	}
	clean := cleanIndexPath(requestPath)
	m.mu.Lock()
	_, ok := m.publicIndex[clean]
	m.mu.Unlock()
	return ok, nil
}

func (m *wafManager) refreshPublicIndex(ctx context.Context, cfg wafConfig, now time.Time) error {
	index := map[string]struct{}{}
	add := func(value string) {
		value = cleanIndexPath(value)
		if value != "" {
			index[value] = struct{}{}
		}
	}
	add("/")
	add("/feed.xml")
	add("/atom.xml")
	add("/comments/feed.xml")
	add("/rsd.xml")
	add("/wlwmanifest.xml")
	add("/xmlrpc.php")
	add("/action/xmlrpc")
	add("/action/pingback")
	for _, route := range m.app.Plugins.Routes() {
		if route.Plugin != "" && !m.app.Plugins.IsActive(route.Plugin) {
			continue
		}
		if route.Method == "" || route.Method == http.MethodGet {
			add(route.Pattern)
		}
	}
	add(m.app.postsIndexPath(ctx))
	if m.app.postsIndexPath(ctx) != "/" {
		add(m.app.postsIndexPath(ctx) + "/feed.xml")
	}

	posts, err := m.app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, ExcludeFuture: true, Limit: cfg.PublicIndexMaxItems})
	if err != nil {
		return err
	}
	for _, item := range posts {
		add(m.app.contentURL(ctx, item))
		add("/post/" + contentRouteSlug(item))
		add("/post/" + contentRouteSlug(item) + ".html")
	}
	pages, err := m.app.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePage, Status: models.ContentStatusPost, ExcludeFuture: true, Limit: cfg.PublicIndexMaxItems})
	if err != nil {
		return err
	}
	for _, item := range pages {
		add(m.app.contentURL(ctx, item))
		add("/page/" + contentRouteSlug(item))
		add("/page/" + contentRouteSlug(item) + ".html")
	}
	for _, typ := range []string{"category", "tag"} {
		metas, err := m.app.Metas.List(ctx, typ)
		if err != nil {
			return err
		}
		for _, meta := range metas {
			if meta.Count <= 0 && typ == "tag" {
				continue
			}
			u := m.app.metaURL(ctx, meta)
			add(u)
			add(u + "/feed.xml")
		}
	}
	users, err := m.app.Users.List(ctx, "")
	if err == nil {
		for _, user := range users {
			add("/author/" + strconv.FormatInt(user.UID, 10))
		}
	}
	archives := m.app.archiveLinks(ctx, cfg.PublicIndexMaxItems)
	for _, archive := range archives {
		add(archive.URL)
	}

	m.mu.Lock()
	m.publicIndex = index
	m.indexLoaded = now
	m.mu.Unlock()
	return nil
}

func (m *wafManager) invalidatePublicData() {
	m.mu.Lock()
	m.publicIndex = map[string]struct{}{}
	m.indexLoaded = time.Time{}
	m.cache = map[string]wafCacheEntry{}
	m.configLoaded = time.Time{}
	m.mu.Unlock()
}

func (m *wafManager) resetRuntimeState() {
	m.mu.Lock()
	m.rates = map[string]*wafCounter{}
	m.invalids = map[string]*wafCounter{}
	m.attachments = map[string]*wafCounter{}
	m.loginFails = map[string]*wafCounter{}
	m.bans = map[string]time.Time{}
	m.loginBans = map[string]time.Time{}
	m.publicIndex = map[string]struct{}{}
	m.indexLoaded = time.Time{}
	m.cache = map[string]wafCacheEntry{}
	m.configLoaded = time.Time{}
	m.mu.Unlock()
}

func (m *wafManager) logPath() string {
	dataDir := m.app.DataDir
	if dataDir == "" {
		dataDir = "data"
	}
	return filepath.Join(dataDir, "waf.log")
}

func (m *wafManager) logEvent(cfg wafConfig, format string, args ...any) {
	line := time.Now().Format(time.RFC3339) + " WAF: " + fmt.Sprintf(format, args...) + "\n"
	if err := m.appendLogLine(line, cfg.LogMaxEntries); err != nil {
		log.Printf("WAF: failed to write waf log: %v", err)
	}
}

func (m *wafManager) appendLogLine(line string, maxEntries int) error {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	logPath := m.logPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(line); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return m.trimLogLocked(maxEntries)
}

func (m *wafManager) trimLogLocked(maxEntries int) error {
	if maxEntries <= 0 {
		return nil
	}
	logPath := m.logPath()
	body, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := splitLogLines(string(body))
	if len(lines) <= maxEntries {
		return nil
	}
	lines = lines[len(lines)-maxEntries:]
	return os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func (m *wafManager) logText(maxEntries int) string {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	body, err := os.ReadFile(m.logPath())
	if err != nil {
		return ""
	}
	lines := splitLogLines(string(body))
	if maxEntries > 0 && len(lines) > maxEntries {
		lines = lines[len(lines)-maxEntries:]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func (m *wafManager) clearLog() error {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	logPath := m.logPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(logPath, nil, 0o644)
}

func splitLogLines(value string) []string {
	value = strings.TrimRight(value, "\r\n")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

func (m *wafManager) cachedResponse(key string, now time.Time) (wafCacheEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.cache[key]
	if !ok {
		return wafCacheEntry{}, false
	}
	if now.After(entry.ExpiresAt) {
		delete(m.cache, key)
		return wafCacheEntry{}, false
	}
	return entry, true
}

func (m *wafManager) storeCachedResponse(key string, status int, header http.Header, body []byte, cfg wafConfig, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cache) >= cfg.PublicCacheMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range m.cache {
			if oldest.IsZero() || entry.ExpiresAt.Before(oldest) {
				oldest = entry.ExpiresAt
				oldestKey = key
			}
		}
		if oldestKey != "" {
			delete(m.cache, oldestKey)
		}
	}
	m.cache[key] = wafCacheEntry{Status: status, Header: cloneCacheHeaders(header), Body: append([]byte(nil), body...), ExpiresAt: now.Add(cfg.CacheTTL)}
}

func (m *wafManager) isBanned(ip string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.bans[ip]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(m.bans, ip)
		return false
	}
	return true
}

func (m *wafManager) recordInvalidPath(ip string, cfg wafConfig, now time.Time) bool {
	if !m.allowWindow(m.invalids, ip, cfg.InvalidPathWindow, cfg.InvalidPathLimit, now) {
		m.mu.Lock()
		m.bans[ip] = now.Add(cfg.InvalidPathBan)
		m.trimTimeMapLocked(m.bans, now, m.config.StateMaxEntries)
		m.mu.Unlock()
		return true
	}
	return false
}

func (m *wafManager) recordAttachmentDownload(ip string, cfg wafConfig, now time.Time) bool {
	return !m.allowWindow(m.attachments, ip, cfg.AttachmentBanWindow, cfg.AttachmentBanLimit, now)
}

func (m *wafManager) loginAllowed(ctx context.Context, ip string) bool {
	cfg := m.currentConfig(ctx)
	if !cfg.Enabled || !cfg.LoginBanEnabled {
		return true
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.loginBans[ip]
	if !ok {
		return true
	}
	if now.After(until) {
		delete(m.loginBans, ip)
		delete(m.loginFails, ip)
		return true
	}
	return false
}

func (m *wafManager) recordLoginFailure(ctx context.Context, ip string) {
	cfg := m.currentConfig(ctx)
	if !cfg.Enabled || !cfg.LoginBanEnabled {
		return
	}
	now := time.Now()
	if cfg.LoginWindow <= 0 {
		cfg.LoginWindow = time.Second
	}
	m.mu.Lock()
	counter := m.loginFails[ip]
	if counter == nil || now.Sub(counter.Start) >= cfg.LoginWindow {
		counter = &wafCounter{Start: now}
		m.loginFails[ip] = counter
	}
	counter.Count++
	if counter.Count >= cfg.LoginFailures {
		m.loginBans[ip] = now.Add(cfg.LoginBan)
		m.trimTimeMapLocked(m.loginBans, now, m.config.StateMaxEntries)
		m.logEvent(cfg, "login ban triggered for IP %s after %d failures", ip, counter.Count)
	}
	m.mu.Unlock()
}

func (m *wafManager) recordLoginSuccess(ip string) {
	m.mu.Lock()
	delete(m.loginFails, ip)
	delete(m.loginBans, ip)
	m.mu.Unlock()
}

func (m *wafManager) allowWindow(store map[string]*wafCounter, key string, window time.Duration, limit int, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	if window <= 0 {
		window = time.Second
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trimCounterMapLocked(store, window, now, m.config.StateMaxEntries)
	counter := store[key]
	if counter == nil || now.Sub(counter.Start) >= window {
		store[key] = &wafCounter{Start: now, Count: 1}
		return true
	}
	counter.Count++
	return counter.Count <= limit
}

func (m *wafManager) trimCounterMapLocked(store map[string]*wafCounter, window time.Duration, now time.Time, max int) {
	if max <= 0 {
		max = 100000
	}
	if len(store) <= max {
		return
	}
	for key, counter := range store {
		if counter == nil || now.Sub(counter.Start) >= window {
			delete(store, key)
		}
	}
	for key := range store {
		if len(store) <= max {
			return
		}
		delete(store, key)
	}
}

func (m *wafManager) trimTimeMapLocked(store map[string]time.Time, now time.Time, max int) {
	if max <= 0 {
		max = 100000
	}
	if len(store) <= max {
		return
	}
	for key, until := range store {
		if now.After(until) {
			delete(store, key)
		}
	}
	for key := range store {
		if len(store) <= max {
			return
		}
		delete(store, key)
	}
}

func (m *wafManager) cacheablePublicRequest(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		r.URL.RawQuery == "" &&
		r.Header.Get("Cookie") == "" &&
		isPublicHTMLPath(r.URL.Path) &&
		!strings.HasPrefix(r.URL.Path, "/preview/")
}

type wafResponseRecorder struct {
	dst         http.ResponseWriter
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func newWAFResponseRecorder(dst http.ResponseWriter) *wafResponseRecorder {
	return &wafResponseRecorder{dst: dst, header: http.Header{}, status: http.StatusOK}
}

func (r *wafResponseRecorder) Header() http.Header {
	return r.header
}

func (r *wafResponseRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
}

func (r *wafResponseRecorder) Write(data []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(data)
}

func (r *wafResponseRecorder) flush() {
	copyHeaders(r.dst.Header(), r.header)
	if r.wroteHeader {
		r.dst.WriteHeader(r.status)
	}
	_, _ = r.dst.Write(r.body.Bytes())
}

type securityResponseWriter struct {
	http.ResponseWriter
	request     *http.Request
	hstsEnabled bool
	wroteHeader bool
}

func newSecurityResponseWriter(dst http.ResponseWriter, r *http.Request, hstsEnabled bool) *securityResponseWriter {
	return &securityResponseWriter{ResponseWriter: dst, request: r, hstsEnabled: hstsEnabled}
}

func (w *securityResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	writeSecurityHeaders(w.Header(), w.request, w.hstsEnabled)
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *securityResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *securityResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeSecurityHeaders(header http.Header, r *http.Request, hstsEnabled bool) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	header.Set("X-XSS-Protection", "0")
	if hstsEnabled && r != nil && r.TLS != nil {
		header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

func copyHeaders(dst, src http.Header) {
	for key := range dst {
		delete(dst, key)
	}
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func cloneCacheHeaders(src http.Header) http.Header {
	out := http.Header{}
	for key, values := range src {
		if strings.EqualFold(key, "Set-Cookie") {
			continue
		}
		for _, value := range values {
			out.Add(key, value)
		}
	}
	out.Set("X-GopherInk-Cache", "HIT")
	return out
}

func cacheKey(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return r.Method + " " + cleanIndexPath(r.URL.Path)
	}
	return r.Method + " " + cleanIndexPath(r.URL.Path) + "?" + r.URL.RawQuery
}

func cleanIndexPath(value string) string {
	value = path.Clean("/" + strings.TrimSpace(value))
	if value == "." || value == "" {
		return "/"
	}
	if value == "/" {
		return "/"
	}
	return strings.TrimRight(value, "/")
}

func shouldCheckPublicURLIndex(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.Header.Get("Cookie") != "" {
		return false
	}
	if !isPublicHTMLPath(r.URL.Path) {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/preview/") || strings.HasPrefix(r.URL.Path, "/trackback/") {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/search") {
		return false
	}
	if looksArchivePath(r.URL.Path) {
		return false
	}
	return true
}

func requestRatePolicy(r *http.Request, cfg wafConfig) (string, bool, time.Duration, int) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/uploads/"):
		return "upload", cfg.UploadRateEnabled, cfg.UploadRateWindow, cfg.UploadRateLimit
	case strings.HasPrefix(r.URL.Path, "/admin/assets/"), strings.HasPrefix(r.URL.Path, "/theme/"):
		return "static", cfg.StaticRateEnabled, cfg.StaticRateWindow, cfg.StaticRateLimit
	default:
		return "dynamic", cfg.DynamicRateEnabled, cfg.DynamicRateWindow, cfg.DynamicRateLimit
	}
}

func isAttachmentDownloadRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return strings.HasPrefix(r.URL.Path, "/uploads/") && path.Ext(r.URL.Path) != ""
}

func isPublicHTMLPath(value string) bool {
	switch {
	case strings.HasPrefix(value, "/admin"):
		return false
	case strings.HasPrefix(value, "/theme/"), strings.HasPrefix(value, "/uploads/"):
		return false
	case strings.HasPrefix(value, "/admin/assets/"):
		return false
	case value == "/comment" || value == "/comment/guard" || value == "/register" || value == "/install":
		return false
	default:
		return true
	}
}

func isSearchRequest(r *http.Request) bool {
	return r.Method == http.MethodGet && (r.URL.Path == "/search" || strings.HasPrefix(r.URL.Path, "/search/"))
}

func isXMLRPCRequest(r *http.Request) bool {
	switch r.URL.Path {
	case "/xmlrpc.php", "/action/xmlrpc", "/action/pingback":
		return true
	default:
		return false
	}
}

func looksArchivePath(value string) bool {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) == 0 || len(parts[0]) != 4 {
		return false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil || year < 1970 {
		return false
	}
	return true
}

func durationSeconds(value string, fallback int) time.Duration {
	seconds := optionInt(value, fallback)
	if seconds < 0 {
		seconds = fallback
	}
	return time.Duration(seconds) * time.Second
}

func boundedInt(value string, fallback, min, max int) int {
	n := optionInt(value, fallback)
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func legacyOption(options map[string]string, key, fallback string) string {
	value := strings.TrimSpace(options[key])
	if value == "" {
		return fallback
	}
	return value
}
