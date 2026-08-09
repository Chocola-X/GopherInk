package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
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
	CacheMaxBodyBytes     int
	CacheMaxBytes         int64
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
	StateMaxEntries       int
}

type wafManager struct {
	app *App

	configMu        sync.RWMutex
	configRefreshMu sync.Mutex
	rateMu          sync.Mutex
	banMu           sync.Mutex
	indexMu         sync.RWMutex
	indexRefreshMu  sync.Mutex
	cacheMu         sync.RWMutex
	cacheFlightMu   sync.Mutex
	logMu           sync.Mutex
	logOnceMu       sync.Mutex
	config          wafConfig
	configLoaded    time.Time

	rates          map[string]*wafCounter
	invalids       map[string]*wafCounter
	attachments    map[string]*wafCounter
	loginFails     map[string]*wafCounter
	bans           map[string]time.Time
	banIndex       sync.Map
	loginBans      map[string]time.Time
	publicIndex    map[string]struct{}
	pluginRoutes   []plugin.Route
	themeRoutes    []plugin.Route
	indexLoaded    time.Time
	cache          map[string]wafCacheEntry
	cacheBytes     int64
	cacheFlights   map[string]*wafCacheFlight
	logOnce        map[string]time.Time
	logLineCount   int
	logCountLoaded bool
}

type wafCounter struct {
	Start time.Time
	Count int
}

type wafCacheEntry struct {
	Status     int
	Header     http.Header
	Body       []byte
	ETag       string
	ExpiresAt  time.Time
	StaleUntil time.Time
	Persistent bool
}

type wafCacheFlight struct {
	done chan struct{}
}

const themeNotFoundCacheKey = "GET __gopherink_theme_404__"

func newWAFManager(app *App) *wafManager {
	return &wafManager{
		app:          app,
		rates:        map[string]*wafCounter{},
		invalids:     map[string]*wafCounter{},
		attachments:  map[string]*wafCounter{},
		loginFails:   map[string]*wafCounter{},
		bans:         map[string]time.Time{},
		loginBans:    map[string]time.Time{},
		publicIndex:  map[string]struct{}{},
		cache:        map[string]wafCacheEntry{},
		cacheFlights: map[string]*wafCacheFlight{},
		logOnce:      map[string]time.Time{},
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
			rejectWAF(sw, http.StatusForbidden)
			return
		}
		kind, rateEnabled, rateWindow, rateLimit := requestRatePolicy(r, cfg)
		if rateEnabled && !m.allowWindow(m.rates, kind+"|"+ip, rateWindow, rateLimit, cfg.StateMaxEntries, now) {
			m.logEventOnce(cfg, "rate|"+kind+"|"+ip, rateWindow, "%s rate limit exceeded for IP %s", kind, ip)
			rejectWAF(sw, http.StatusTooManyRequests)
			return
		}
		if cfg.SearchRateEnabled && isSearchRequest(r) && !m.allowWindow(m.rates, "search|"+ip, cfg.SearchRateWindow, cfg.SearchRateLimit, cfg.StateMaxEntries, now) {
			m.logEventOnce(cfg, "rate|search|"+ip, cfg.SearchRateWindow, "search rate limit exceeded for IP %s", ip)
			rejectWAF(sw, http.StatusTooManyRequests)
			return
		}
		if cfg.XMLRPCRateEnabled && isXMLRPCRequest(r) && !m.allowWindow(m.rates, "xmlrpc|"+ip, cfg.XMLRPCRateWindow, cfg.XMLRPCRateLimit, cfg.StateMaxEntries, now) {
			m.logEventOnce(cfg, "rate|xmlrpc|"+ip, cfg.XMLRPCRateWindow, "XML-RPC rate limit exceeded for IP %s", ip)
			rejectWAF(sw, http.StatusTooManyRequests)
			return
		}
		if m.app.Plugins.HasActiveHook(plugin.HookWAFCheck) {
			wafPayload := plugin.WAFPayload{Request: r, IP: ip, Path: r.URL.Path}
			if out, err := m.app.Plugins.ApplyActive(r.Context(), plugin.HookWAFCheck, wafPayload); err != nil {
				m.logEventOnce(cfg, "plugin-error|"+ip, time.Minute, "plugin WAF check failed for IP %s: %v", ip, err)
				rejectWAF(sw, http.StatusForbidden)
				return
			} else if nextPayload, ok := out.(plugin.WAFPayload); ok {
				if nextPayload.Blocked {
					reason := strings.TrimSpace(nextPayload.Reason)
					if reason == "" {
						reason = "plugin WAF rule"
					}
					m.logEventOnce(cfg, "plugin-block|"+ip+"|"+reason, time.Minute, "%s blocked IP %s", reason, ip)
					rejectWAF(sw, http.StatusForbidden)
					return
				}
				if nextPayload.Handled {
					next.ServeHTTP(sw, r)
					return
				}
			}
		}
		if cfg.URLIndexEnabled && shouldCheckPublicURLIndex(r) {
			exists, err := m.publicURLExists(r.Context(), r.URL.Path, cfg, now)
			if err != nil {
				m.logEventOnce(cfg, "url-index-error", time.Minute, "public URL index refresh failed: %v", err)
			}
			if err == nil {
				if extensionHandled, _ := m.extensionRouteMayHandle(r, true); extensionHandled {
					next.ServeHTTP(sw, r)
					return
				}
			}
			if err == nil && !exists {
				if cfg.InvalidPathEnabled {
					if blocked, newlyBanned := m.recordInvalidPath(ip, cfg, now); blocked {
						if newlyBanned {
							m.logEvent(cfg, "invalid path ban triggered for IP %s on %s", ip, r.URL.Path)
						}
						rejectWAF(sw, http.StatusForbidden)
						return
					}
				}
				m.serveThemeNotFound(sw, r, cfg, now)
				return
			}
		} else if extensionHandled, extensionInvalidates := m.extensionRouteMayHandle(r, false); extensionHandled {
			next.ServeHTTP(sw, r)
			if extensionInvalidates && r.Method != http.MethodGet && r.Method != http.MethodHead {
				m.invalidatePublicData()
			}
			return
		}
		if cacheRequest, key, cacheable := m.publicCacheRequest(r, cfg.CacheEnabled); cacheable {
			r = cacheRequest
			entry, fresh, exists := m.cachedResponse(key, now)
			if fresh {
				serveCachedResponse(sw, r, entry)
				return
			}
			leader, done := m.beginCacheFlight(key)
			if !leader {
				if exists {
					serveCachedResponse(sw, r, entry)
					return
				}
				select {
				case <-done:
					if entry, fresh, _ := m.cachedResponse(key, time.Now()); fresh {
						serveCachedResponse(sw, r, entry)
						return
					}
				case <-r.Context().Done():
				}
				rejectWAF(sw, http.StatusServiceUnavailable)
				return
			}
			defer m.endCacheFlight(key)
			rec := newWAFResponseRecorder(sw, cfg.CacheMaxBodyBytes)
			next.ServeHTTP(rec, r)
			rec.flush()
			if rec.cacheable() && rec.status == http.StatusOK {
				m.storeCachedResponse(key, rec.status, rec.header, rec.body.Bytes(), cfg, now)
			}
			return
		}
		if cfg.AttachmentBanEnabled && isAttachmentDownloadRequest(r) {
			if blocked, newlyBanned := m.recordAttachmentDownload(ip, cfg, now); blocked {
				if newlyBanned {
					m.logEvent(cfg, "attachment download ban triggered for IP %s on %s", ip, r.URL.Path)
				}
				rejectWAF(sw, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(sw, r)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			m.invalidatePublicData()
		}
	})
}

func (m *wafManager) serveThemeNotFound(w http.ResponseWriter, r *http.Request, cfg wafConfig, now time.Time) {
	entry, fresh, exists := m.cachedResponse(themeNotFoundCacheKey, now)
	if fresh {
		serveCachedResponse(w, r, entry)
		return
	}
	leader, done := m.beginCacheFlight(themeNotFoundCacheKey)
	if !leader {
		if exists {
			serveCachedResponse(w, r, entry)
			return
		}
		select {
		case <-done:
			if entry, fresh, _ := m.cachedResponse(themeNotFoundCacheKey, time.Now()); fresh {
				serveCachedResponse(w, r, entry)
				return
			}
		case <-r.Context().Done():
		}
		rejectWAF(w, http.StatusServiceUnavailable)
		return
	}
	defer m.endCacheFlight(themeNotFoundCacheKey)
	anonymousRequest := r.Clone(r.Context())
	anonymousRequest.Header = r.Header.Clone()
	anonymousRequest.Header.Del("Cookie")
	urlCopy := *r.URL
	urlCopy.RawQuery = ""
	anonymousRequest.URL = &urlCopy
	recorder := newWAFResponseRecorder(w, cfg.CacheMaxBodyBytes)
	m.app.renderThemeNotFound(recorder, anonymousRequest)
	recorder.flush()
	if recorder.cacheable() && recorder.status == http.StatusNotFound {
		m.storeCachedResponseWithPolicy(themeNotFoundCacheKey, recorder.status, recorder.header, recorder.body.Bytes(), cfg, now, true)
	}
}

func (m *wafManager) authenticatedAdminBackendRequest(r *http.Request) bool {
	if !isBackendPath(r.URL.Path) {
		return false
	}
	user, ok := m.app.currentUser(r)
	return ok && roleRank(user.Role) >= roleRank("administrator")
}

func (m *wafManager) extensionRouteMayHandle(r *http.Request, cachedTheme bool) (bool, bool) {
	if cachedTheme {
		m.indexMu.RLock()
		defer m.indexMu.RUnlock()
		for _, route := range m.pluginRoutes {
			if (route.Method == "" || route.Method == r.Method) && extensionRouteMatches(route.Pattern, r.URL.Path) {
				return true, true
			}
		}
		for _, route := range m.themeRoutes {
			if (route.Method == "" || route.Method == r.Method) && extensionRouteMatches(route.Pattern, r.URL.Path) {
				return true, route.InvalidatesPublicData
			}
		}
		return false, false
	}
	for _, route := range m.app.Plugins.ActiveRoutes() {
		if (route.Method == "" || route.Method == r.Method) && extensionRouteMatches(route.Pattern, r.URL.Path) {
			return true, true
		}
	}
	if theme, ok := m.app.activeTheme(r.Context()); ok {
		for _, route := range theme.Routes {
			if (route.Method == "" || route.Method == r.Method) && extensionRouteMatches(route.Pattern, r.URL.Path) {
				return true, route.InvalidatesPublicData
			}
		}
	}
	return false, false
}

func extensionRouteMatches(pattern, requestPath string) bool {
	cleanPattern := cleanIndexPath(pattern)
	cleanRequest := cleanIndexPath(requestPath)
	if strings.HasSuffix(pattern, "/") {
		return cleanRequest == cleanPattern || strings.HasPrefix(cleanRequest, strings.TrimRight(cleanPattern, "/")+"/")
	}
	return cleanRequest == cleanPattern
}

func isBackendPath(value string) bool {
	return value == "/admin" || strings.HasPrefix(value, "/admin/")
}

func (m *wafManager) currentConfig(ctx context.Context) wafConfig {
	now := time.Now()
	m.configMu.RLock()
	cfg := m.config
	loadedAt := m.configLoaded
	m.configMu.RUnlock()
	if !loadedAt.IsZero() && now.Sub(loadedAt) < 5*time.Second {
		return cfg
	}
	if !loadedAt.IsZero() && !m.configRefreshMu.TryLock() {
		return cfg
	}
	if loadedAt.IsZero() {
		m.configRefreshMu.Lock()
	}
	defer m.configRefreshMu.Unlock()

	now = time.Now()
	m.configMu.RLock()
	cfg = m.config
	loadedAt = m.configLoaded
	m.configMu.RUnlock()
	if !loadedAt.IsZero() && now.Sub(loadedAt) < 5*time.Second {
		return cfg
	}

	options, err := m.app.Options.All(ctx)
	if err != nil && !loadedAt.IsZero() {
		return cfg
	}
	if err != nil {
		options = map[string]string{}
	}
	cfg = wafConfig{
		Enabled:               optionBool(defaultString(options["waf_enabled"], "1")),
		HSTSEnabled:           optionBool(defaultString(options["waf_hsts_enabled"], "0")),
		TrustProxy:            loadProxyTrustConfig(options),
		LogMaxEntries:         boundedInt(options["waf_log_max_entries"], 1000, 1, 100000),
		URLIndexEnabled:       optionBool(defaultString(options["waf_url_index_enabled"], "1")),
		URLIndexTTL:           durationSeconds(options["waf_url_index_ttl"], 60),
		CacheEnabled:          optionBool(defaultString(options["waf_cache_enabled"], "1")),
		CacheTTL:              durationSeconds(options["waf_cache_ttl"], 30),
		CacheMaxBodyBytes:     boundedInt(options["waf_cache_max_body_kb"], 512, 16, 16384) * 1024,
		CacheMaxBytes:         int64(boundedInt(options["waf_cache_max_memory_mb"], 32, 1, 1024)) * 1024 * 1024,
		DynamicRateEnabled:    optionBool(defaultString(options["waf_dynamic_rate_enabled"], "1")),
		DynamicRateWindow:     durationSeconds(options["waf_dynamic_rate_window"], 60),
		DynamicRateLimit:      boundedInt(options["waf_dynamic_rate_limit"], 300, 1, 100000),
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
		StateMaxEntries:       boundedInt(options["waf_state_max_entries"], 100000, 1000, 1000000),
	}

	m.configMu.Lock()
	m.config = cfg
	m.configLoaded = now
	m.configMu.Unlock()
	return cfg
}

func (m *wafManager) publicURLExists(ctx context.Context, requestPath string, cfg wafConfig, now time.Time) (bool, error) {
	m.indexMu.RLock()
	loadedAt := m.indexLoaded
	loaded := !loadedAt.IsZero() && now.Sub(loadedAt) < cfg.URLIndexTTL
	m.indexMu.RUnlock()
	if !loaded && !loadedAt.IsZero() {
		if m.indexRefreshMu.TryLock() {
			now = time.Now()
			m.indexMu.RLock()
			loaded = !m.indexLoaded.IsZero() && now.Sub(m.indexLoaded) < cfg.URLIndexTTL
			m.indexMu.RUnlock()
			if !loaded {
				if err := m.refreshPublicIndex(ctx, cfg, now); err != nil {
					m.logEventOnce(cfg, "url-index-refresh-error", time.Minute, "public URL index refresh failed: %v", err)
				}
			}
			m.indexRefreshMu.Unlock()
		}
		loaded = true
	}
	if !loaded {
		m.indexRefreshMu.Lock()
		defer m.indexRefreshMu.Unlock()
		now = time.Now()
		m.indexMu.RLock()
		loaded = !m.indexLoaded.IsZero() && now.Sub(m.indexLoaded) < cfg.URLIndexTTL
		m.indexMu.RUnlock()
	}
	if !loaded {
		if err := m.refreshPublicIndex(ctx, cfg, now); err != nil {
			return true, err
		}
	}
	clean := cleanIndexPath(requestPath)
	m.indexMu.RLock()
	_, ok := m.publicIndex[clean]
	m.indexMu.RUnlock()
	return ok, nil
}

func (m *wafManager) warmPublicIndex(ctx context.Context) {
	cfg := m.currentConfig(ctx)
	if !cfg.Enabled || !cfg.URLIndexEnabled {
		return
	}
	if err := m.refreshPublicIndex(ctx, cfg, time.Now()); err != nil {
		m.logEventOnce(cfg, "url-index-warmup-error", time.Minute, "public URL index warmup failed: %v", err)
	}
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
	dynamicPaths, err := m.app.Plugins.ActivePublicPaths(ctx, m.app.pluginRuntime())
	if err != nil {
		return err
	}
	for _, publicPath := range dynamicPaths {
		add(publicPath)
	}
	pluginRoutes := m.app.Plugins.ActiveRoutes()
	for _, route := range pluginRoutes {
		if route.Method == "" || route.Method == http.MethodGet {
			add(route.Pattern)
		}
	}
	themeRoutes := []plugin.Route(nil)
	for _, theme := range m.app.Plugins.Themes() {
		for _, route := range theme.Routes {
			if route.Method == "" || route.Method == http.MethodGet {
				add(route.Pattern)
			}
			themeRoutes = append(themeRoutes, route)
		}
	}
	postsIndexPath := m.app.postsIndexPath(ctx)
	add(postsIndexPath)
	if postsIndexPath != "/" {
		add(postsIndexPath + "/feed.xml")
	}

	for _, query := range []services.ContentQuery{
		{Type: models.ContentTypePost, Status: models.ContentStatusPost, ExcludeFuture: true},
		{Type: models.ContentTypePost, Status: "private"},
		{Type: models.ContentTypePage, Status: models.ContentStatusPost, ExcludeFuture: true},
		{Type: models.ContentTypePage, Status: "private"},
	} {
		if err := m.addContentRoutes(ctx, add, query); err != nil {
			return err
		}
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
	archives := m.app.archiveLinks(ctx, 0)
	for _, archive := range archives {
		add(archive.URL)
		add(archivePath(archive.Year, 0, 0))
	}

	m.indexMu.Lock()
	m.publicIndex = index
	m.pluginRoutes = pluginRoutes
	m.themeRoutes = themeRoutes
	m.indexLoaded = now
	m.indexMu.Unlock()
	return nil
}

func (m *wafManager) addContentRoutes(ctx context.Context, add func(string), query services.ContentQuery) error {
	const batchSize = 500
	for offset := 0; ; offset += batchSize {
		query.Limit = batchSize
		query.Offset = offset
		contents, err := m.app.Contents.List(ctx, query)
		if err != nil {
			return err
		}
		for _, item := range contents {
			add(m.app.contentURL(ctx, item))
			prefix := "/post/"
			if item.Type == models.ContentTypePage {
				prefix = "/page/"
			}
			add(prefix + contentRouteSlug(item))
			add(prefix + contentRouteSlug(item) + ".html")
		}
		if len(contents) < batchSize {
			return nil
		}
	}
}

func (m *wafManager) invalidatePublicData() {
	m.app.invalidateThemeData()
	m.indexMu.Lock()
	m.publicIndex = map[string]struct{}{}
	m.pluginRoutes = nil
	m.themeRoutes = nil
	m.indexLoaded = time.Time{}
	m.indexMu.Unlock()
	m.cacheMu.Lock()
	m.cache = map[string]wafCacheEntry{}
	m.cacheBytes = 0
	m.cacheMu.Unlock()
	m.configMu.Lock()
	m.configLoaded = time.Time{}
	m.configMu.Unlock()
}

func (m *wafManager) resetRuntimeState() {
	m.app.invalidateThemeData()
	m.rateMu.Lock()
	m.rates = map[string]*wafCounter{}
	m.invalids = map[string]*wafCounter{}
	m.attachments = map[string]*wafCounter{}
	m.rateMu.Unlock()
	m.banMu.Lock()
	m.loginFails = map[string]*wafCounter{}
	m.bans = map[string]time.Time{}
	m.loginBans = map[string]time.Time{}
	m.banMu.Unlock()
	m.banIndex.Range(func(key, _ any) bool {
		m.banIndex.Delete(key)
		return true
	})
	m.indexMu.Lock()
	m.publicIndex = map[string]struct{}{}
	m.pluginRoutes = nil
	m.themeRoutes = nil
	m.indexLoaded = time.Time{}
	m.indexMu.Unlock()
	m.cacheMu.Lock()
	m.cache = map[string]wafCacheEntry{}
	m.cacheBytes = 0
	m.cacheMu.Unlock()
	m.logOnceMu.Lock()
	m.logOnce = map[string]time.Time{}
	m.logOnceMu.Unlock()
	m.configMu.Lock()
	m.configLoaded = time.Time{}
	m.configMu.Unlock()
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

func (m *wafManager) logEventOnce(cfg wafConfig, key string, window time.Duration, format string, args ...any) {
	if window <= 0 {
		window = time.Minute
	}
	now := time.Now()
	m.logOnceMu.Lock()
	if until, ok := m.logOnce[key]; ok && now.Before(until) {
		m.logOnceMu.Unlock()
		return
	}
	m.logOnce[key] = now.Add(window)
	if len(m.logOnce) > cfg.StateMaxEntries {
		for item, until := range m.logOnce {
			if now.After(until) {
				delete(m.logOnce, item)
			}
		}
		for item := range m.logOnce {
			if len(m.logOnce) <= cfg.StateMaxEntries {
				break
			}
			delete(m.logOnce, item)
		}
	}
	m.logOnceMu.Unlock()
	m.logEvent(cfg, format, args...)
}

func (m *wafManager) appendLogLine(line string, maxEntries int) error {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	logPath := m.logPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	if !m.logCountLoaded {
		body, err := os.ReadFile(logPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		m.logLineCount = len(splitLogLines(string(body)))
		m.logCountLoaded = true
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
	m.logLineCount++
	trimSlack := maxEntries / 8
	if trimSlack < 64 && maxEntries >= 64 {
		trimSlack = 64
	}
	if trimSlack > 1024 {
		trimSlack = 1024
	}
	if m.logLineCount > maxEntries+trimSlack {
		return m.trimLogLocked(maxEntries)
	}
	return nil
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
	m.logLineCount = len(lines)
	m.logCountLoaded = true
	if len(lines) <= maxEntries {
		return nil
	}
	lines = lines[len(lines)-maxEntries:]
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	m.logLineCount = len(lines)
	return nil
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
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		return err
	}
	m.logLineCount = 0
	m.logCountLoaded = true
	return nil
}

func splitLogLines(value string) []string {
	value = strings.TrimRight(value, "\r\n")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

func (m *wafManager) cachedResponse(key string, now time.Time) (wafCacheEntry, bool, bool) {
	m.cacheMu.RLock()
	entry, ok := m.cache[key]
	m.cacheMu.RUnlock()
	if !ok {
		return wafCacheEntry{}, false, false
	}
	if entry.Persistent {
		return entry, true, true
	}
	if !now.After(entry.ExpiresAt) {
		return entry, true, true
	}
	if !now.After(entry.StaleUntil) {
		return entry, false, true
	}
	m.cacheMu.Lock()
	if current, exists := m.cache[key]; exists && now.After(current.StaleUntil) {
		m.cacheBytes -= int64(len(current.Body))
		delete(m.cache, key)
	}
	m.cacheMu.Unlock()
	return wafCacheEntry{}, false, false
}

func serveCachedResponse(w http.ResponseWriter, r *http.Request, entry wafCacheEntry) {
	copyHeaders(w.Header(), entry.Header)
	if entry.ETag != "" {
		w.Header().Set("ETag", entry.ETag)
	}
	if entry.ETag != "" && r.Header.Get("If-None-Match") == entry.ETag {
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(entry.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(entry.Body)
	}
}

func (m *wafManager) storeCachedResponse(key string, status int, header http.Header, body []byte, cfg wafConfig, now time.Time) {
	m.storeCachedResponseWithPolicy(key, status, header, body, cfg, now, false)
}

func (m *wafManager) storeCachedResponseWithPolicy(key string, status int, header http.Header, body []byte, cfg wafConfig, now time.Time, persistent bool) {
	if len(body) == 0 || len(body) > cfg.CacheMaxBodyBytes || int64(len(body)) > cfg.CacheMaxBytes {
		return
	}
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if current, ok := m.cache[key]; ok {
		m.cacheBytes -= int64(len(current.Body))
		delete(m.cache, key)
	}
	for len(m.cache) >= cfg.PublicCacheMaxEntries || m.cacheBytes+int64(len(body)) > cfg.CacheMaxBytes {
		var oldestKey string
		var oldest time.Time
		for key, entry := range m.cache {
			if entry.Persistent {
				continue
			}
			if oldest.IsZero() || entry.ExpiresAt.Before(oldest) {
				oldest = entry.ExpiresAt
				oldestKey = key
			}
		}
		if oldestKey != "" {
			m.cacheBytes -= int64(len(m.cache[oldestKey].Body))
			delete(m.cache, oldestKey)
		} else {
			return
		}
	}
	cachedBody := append([]byte(nil), body...)
	expiresAt := now.Add(cfg.CacheTTL)
	etag := fmt.Sprintf("\"%x\"", sha256.Sum256(cachedBody))
	m.cache[key] = wafCacheEntry{Status: status, Header: cloneCacheHeaders(header), Body: cachedBody, ETag: etag, ExpiresAt: expiresAt, StaleUntil: expiresAt.Add(cfg.CacheTTL), Persistent: persistent}
	m.cacheBytes += int64(len(cachedBody))
}

func (m *wafManager) beginCacheFlight(key string) (bool, <-chan struct{}) {
	m.cacheFlightMu.Lock()
	defer m.cacheFlightMu.Unlock()
	if flight, ok := m.cacheFlights[key]; ok {
		return false, flight.done
	}
	flight := &wafCacheFlight{done: make(chan struct{})}
	m.cacheFlights[key] = flight
	return true, flight.done
}

func (m *wafManager) endCacheFlight(key string) {
	m.cacheFlightMu.Lock()
	flight, ok := m.cacheFlights[key]
	if ok {
		delete(m.cacheFlights, key)
		close(flight.done)
	}
	m.cacheFlightMu.Unlock()
}

func (m *wafManager) isBanned(ip string, now time.Time) bool {
	value, ok := m.banIndex.Load(ip)
	if !ok {
		return false
	}
	until, ok := value.(time.Time)
	if !ok {
		m.banIndex.Delete(ip)
		return false
	}
	if now.After(until) {
		if m.banIndex.CompareAndDelete(ip, value) {
			m.banMu.Lock()
			if current, exists := m.bans[ip]; exists && now.After(current) {
				delete(m.bans, ip)
			}
			m.banMu.Unlock()
		} else if current, exists := m.banIndex.Load(ip); exists {
			if currentUntil, valid := current.(time.Time); valid && now.Before(currentUntil) {
				return true
			}
		}
		return false
	}
	return true
}

func (m *wafManager) banIP(ctx context.Context, ip string, duration time.Duration, reason string) error {
	ip = strings.TrimSpace(ip)
	if _, err := netip.ParseAddr(ip); err != nil {
		return fmt.Errorf("invalid IP address: %w", err)
	}
	if duration <= 0 {
		return fmt.Errorf("ban duration must be positive")
	}
	cfg := m.currentConfig(ctx)
	now := time.Now()
	if !m.activateBan(ip, duration, cfg.StateMaxEntries, now) {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "runtime WAF ban"
	}
	m.logEvent(cfg, "%s banned IP %s for %s", reason, ip, duration)
	return nil
}

func (m *wafManager) unbanIP(ctx context.Context, ip string) error {
	ip = strings.TrimSpace(ip)
	if _, err := netip.ParseAddr(ip); err != nil {
		return fmt.Errorf("invalid IP address: %w", err)
	}
	cfg := m.currentConfig(ctx)
	m.banMu.Lock()
	delete(m.bans, ip)
	m.banIndex.Delete(ip)
	m.banMu.Unlock()
	m.logEvent(cfg, "runtime WAF unbanned IP %s", ip)
	return nil
}

func (m *wafManager) stats(ctx context.Context) (plugin.WAFStatistics, error) {
	cfg := m.currentConfig(ctx)
	now := time.Now()
	if cfg.URLIndexEnabled {
		if _, err := m.publicURLExists(ctx, "/", cfg, now); err != nil {
			return plugin.WAFStatistics{}, err
		}
	}
	m.banMu.Lock()
	m.trimBanMapLocked(now, cfg.StateMaxEntries)
	bannedIPs := len(m.bans)
	m.banMu.Unlock()
	m.indexMu.RLock()
	allowedPaths := len(m.publicIndex)
	m.indexMu.RUnlock()
	stats := plugin.WAFStatistics{
		BannedIPs:    bannedIPs,
		AllowedPaths: allowedPaths,
	}
	for _, line := range splitLogLines(m.logText(cfg.LogMaxEntries)) {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "blocked") || strings.Contains(lower, "ban triggered") || strings.Contains(lower, "rate limit exceeded") {
			stats.RecentBlocks++
		}
	}
	return stats, nil
}

func (a *App) pluginIsIPBanned(ctx context.Context, ip string) bool {
	if a.WAF == nil {
		return false
	}
	return a.WAF.isBanned(strings.TrimSpace(ip), time.Now())
}

func (a *App) pluginIsURLAllowed(ctx context.Context, pathValue string) bool {
	if a.WAF == nil {
		return false
	}
	cfg := a.WAF.currentConfig(ctx)
	if !cfg.URLIndexEnabled {
		return true
	}
	exists, err := a.WAF.publicURLExists(ctx, pathValue, cfg, time.Now())
	return err == nil && exists
}

func (a *App) pluginBanIP(ctx context.Context, ip string, duration time.Duration, reason string) error {
	if a.WAF == nil {
		return fmt.Errorf("WAF is unavailable")
	}
	return a.WAF.banIP(ctx, ip, duration, reason)
}

func (a *App) pluginUnbanIP(ctx context.Context, ip string) error {
	if a.WAF == nil {
		return fmt.Errorf("WAF is unavailable")
	}
	return a.WAF.unbanIP(ctx, ip)
}

func (a *App) pluginWAFStats(ctx context.Context) (plugin.WAFStatistics, error) {
	if a.WAF == nil {
		return plugin.WAFStatistics{}, fmt.Errorf("WAF is unavailable")
	}
	return a.WAF.stats(ctx)
}

func (m *wafManager) recordInvalidPath(ip string, cfg wafConfig, now time.Time) (blocked, newlyBanned bool) {
	if !m.allowWindow(m.invalids, ip, cfg.InvalidPathWindow, cfg.InvalidPathLimit, cfg.StateMaxEntries, now) {
		return true, m.activateBan(ip, cfg.InvalidPathBan, cfg.StateMaxEntries, now)
	}
	return false, false
}

func (m *wafManager) recordAttachmentDownload(ip string, cfg wafConfig, now time.Time) (blocked, newlyBanned bool) {
	if m.allowWindow(m.attachments, ip, cfg.AttachmentBanWindow, cfg.AttachmentBanLimit, cfg.StateMaxEntries, now) {
		return false, false
	}
	return true, m.activateBan(ip, cfg.AttachmentBan, cfg.StateMaxEntries, now)
}

func (m *wafManager) activateBan(ip string, duration time.Duration, maxEntries int, now time.Time) bool {
	m.banMu.Lock()
	defer m.banMu.Unlock()
	if until, ok := m.bans[ip]; ok && now.Before(until) {
		return false
	}
	m.bans[ip] = now.Add(duration)
	m.banIndex.Store(ip, m.bans[ip])
	m.trimBanMapLocked(now, maxEntries)
	return true
}

func (m *wafManager) loginAllowed(ctx context.Context, ip string) bool {
	cfg := m.currentConfig(ctx)
	if !cfg.Enabled || !cfg.LoginBanEnabled {
		return true
	}
	now := time.Now()
	m.banMu.Lock()
	defer m.banMu.Unlock()
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
	m.banMu.Lock()
	counter := m.loginFails[ip]
	if counter == nil || now.Sub(counter.Start) >= cfg.LoginWindow {
		counter = &wafCounter{Start: now}
		m.loginFails[ip] = counter
	}
	counter.Count++
	newlyBanned := false
	if counter.Count >= cfg.LoginFailures {
		until, alreadyBanned := m.loginBans[ip]
		newlyBanned = !alreadyBanned || now.After(until)
		m.loginBans[ip] = now.Add(cfg.LoginBan)
		m.trimTimeMapLocked(m.loginBans, now, cfg.StateMaxEntries)
	}
	count := counter.Count
	m.banMu.Unlock()
	if newlyBanned {
		m.logEvent(cfg, "login ban triggered for IP %s after %d failures", ip, count)
	}
}

func (m *wafManager) recordLoginSuccess(ip string) {
	m.banMu.Lock()
	delete(m.loginFails, ip)
	delete(m.loginBans, ip)
	m.banMu.Unlock()
}

func (m *wafManager) allowWindow(store map[string]*wafCounter, key string, window time.Duration, limit, maxEntries int, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	if window <= 0 {
		window = time.Second
	}
	m.rateMu.Lock()
	defer m.rateMu.Unlock()
	m.trimCounterMapLocked(store, window, now, maxEntries)
	counter := store[key]
	if counter == nil || now.Sub(counter.Start) >= window {
		store[key] = &wafCounter{Start: now, Count: 1}
		return true
	}
	if counter.Count <= limit {
		counter.Count++
	}
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

func (m *wafManager) trimBanMapLocked(now time.Time, max int) {
	if max <= 0 {
		max = 100000
	}
	if len(m.bans) <= max {
		return
	}
	for ip, until := range m.bans {
		if now.After(until) {
			delete(m.bans, ip)
			m.banIndex.Delete(ip)
		}
	}
	for ip := range m.bans {
		if len(m.bans) <= max {
			return
		}
		delete(m.bans, ip)
		m.banIndex.Delete(ip)
	}
}

func (m *wafManager) publicCacheRequest(r *http.Request, enabled bool) (*http.Request, string, bool) {
	if !enabled || (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
		!isPublicHTMLPath(r.URL.Path) || strings.HasPrefix(r.URL.Path, "/preview/") ||
		m.app.hasStatefulPublicSession(r) {
		return r, "", false
	}
	query, ok := normalizedPublicCacheQuery(r)
	if !ok {
		return r, "", false
	}
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	clone.Header.Del("Cookie")
	urlCopy := *r.URL
	urlCopy.RawQuery = query
	clone.URL = &urlCopy
	return clone, cacheKey(clone), true
}

type wafResponseRecorder struct {
	dst         http.ResponseWriter
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
	maxBody     int
	overflow    bool
}

func newWAFResponseRecorder(dst http.ResponseWriter, maxBody int) *wafResponseRecorder {
	return &wafResponseRecorder{dst: dst, header: http.Header{}, status: http.StatusOK, maxBody: maxBody}
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
	copyHeaders(r.dst.Header(), r.header)
	r.dst.WriteHeader(status)
}

func (r *wafResponseRecorder) Write(data []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if !r.overflow {
		if r.maxBody > 0 && r.body.Len()+len(data) <= r.maxBody {
			_, _ = r.body.Write(data)
		} else {
			r.overflow = true
			r.body = bytes.Buffer{}
		}
	}
	return r.dst.Write(data)
}

func (r *wafResponseRecorder) flush() {
	if !r.wroteHeader {
		r.WriteHeader(r.status)
	}
}

func (r *wafResponseRecorder) Flush() {
	r.flush()
	if flusher, ok := r.dst.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *wafResponseRecorder) cacheable() bool {
	return !r.overflow && r.body.Len() > 0 && len(r.header.Values("Set-Cookie")) == 0
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

func rejectWAF(w http.ResponseWriter, status int) {
	if securityWriter, ok := w.(*securityResponseWriter); ok {
		w = securityWriter.ResponseWriter
	}
	message := strconv.Itoa(status) + " " + http.StatusText(status) + "\n"
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(message)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
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
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	if r.URL.RawQuery == "" {
		return method + " " + cleanIndexPath(r.URL.Path)
	}
	return method + " " + cleanIndexPath(r.URL.Path) + "?" + r.URL.RawQuery
}

func normalizedPublicCacheQuery(r *http.Request) (string, bool) {
	query := r.URL.Query()
	for _, name := range []string{"password", "reply", "comment_error", "comment_ok", "comment_status", "format", "token"} {
		if query.Has(name) {
			return "", false
		}
	}
	allowed := map[string]struct{}{"page": {}, "comments_page": {}}
	if isSearchRequest(r) {
		allowed["q"] = struct{}{}
	}
	normalized := neturl.Values{}
	for name := range allowed {
		for _, value := range query[name] {
			normalized.Add(name, value)
		}
	}
	return normalized.Encode(), true
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
	if !isPublicHTMLPath(r.URL.Path) {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/preview/") || strings.HasPrefix(r.URL.Path, "/trackback/") {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/search") {
		return false
	}
	return true
}

func requestRatePolicy(r *http.Request, cfg wafConfig) (string, bool, time.Duration, int) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/uploads/"):
		return "upload", cfg.UploadRateEnabled, cfg.UploadRateWindow, cfg.UploadRateLimit
	case requestPathIsStatic(r.URL.Path):
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
	case requestPathIsStatic(value):
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
