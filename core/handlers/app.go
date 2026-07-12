package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"goblog/admin"
	"goblog/core/models"
	"goblog/core/plugin"
	"goblog/core/services"
	"goblog/core/validate"
	"goblog/pkg/auth"
	compathttp "goblog/pkg/httpclient"
	"goblog/pkg/i18n"
	"goblog/pkg/render"
)

type App struct {
	Contents   *services.ContentService
	Metas      *services.MetaService
	Comments   *services.CommentService
	Users      *services.UserService
	Options    *services.OptionService
	Plugins    *plugin.Manager
	UploadDir  string
	HTTPClient *compathttp.Client
	HTTPFetch  func(context.Context, string) (string, error)
	loginMu    sync.Mutex
	loginNext  map[string]time.Time
}

type contextKey string

const currentUserKey contextKey = "currentUser"

func New(contents *services.ContentService, metas *services.MetaService, comments *services.CommentService, users *services.UserService, options *services.OptionService, plugins *plugin.Manager) *App {
	uploadDir := os.Getenv("GOBLOG_UPLOAD_DIR")
	if uploadDir == "" {
		uploadDir = filepath.Join("data", "uploads")
	}
	httpClient, _ := compathttp.New(compathttp.Config{Timeout: 5 * time.Second, UserAgent: "GoBlog/1.0", Retries: 1})
	return &App{Contents: contents, Metas: metas, Comments: comments, Users: users, Options: options, Plugins: plugins, UploadDir: uploadDir, HTTPClient: httpClient, loginNext: map[string]time.Time{}}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	a.syncActivePlugins(context.Background())

	adminAssets, _ := fs.Sub(admin.FS, "assets")
	mux.Handle("/admin/assets/", http.StripPrefix("/admin/assets/", http.FileServer(http.FS(adminAssets))))

	if theme, ok := a.activeTheme(context.Background()); ok && theme.Static != nil {
		mux.Handle("/theme/default/", http.StripPrefix("/theme/default/", http.FileServer(http.FS(theme.Static))))
	}
	if a.UploadDir != "" {
		mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(a.UploadDir))))
	}

	mux.HandleFunc("/admin/login", a.adminLogin)
	mux.HandleFunc("/admin/register", a.adminRegister)
	mux.HandleFunc("/admin/logout", a.adminLogout)
	mux.HandleFunc("/register", a.adminRegister)
	mux.HandleFunc("/install", a.installWizard)

	adminRoutes := map[string]http.HandlerFunc{
		"/admin":                      a.adminDashboard,
		"/admin/":                     a.adminDashboard,
		"/admin/posts":                a.adminPosts,
		"/admin/posts/":               a.adminPostRoutes,
		"/admin/pages":                a.adminPages,
		"/admin/pages/":               a.adminPageRoutes,
		"/admin/categories":           a.adminCategories,
		"/admin/categories/":          a.adminCategoryRoutes,
		"/admin/tags":                 a.adminTags,
		"/admin/tags/":                a.adminTagRoutes,
		"/admin/comments":             a.adminComments,
		"/admin/comments/":            a.adminCommentRoutes,
		"/admin/users":                a.adminUsers,
		"/admin/users/":               a.adminUserRoutes,
		"/admin/profile":              a.adminProfile,
		"/admin/options":              a.adminOptionsGeneral,
		"/admin/options/general":      a.adminOptionsGeneral,
		"/admin/options/reading":      a.adminOptionsReading,
		"/admin/options/discussion":   a.adminOptionsDiscussion,
		"/admin/options/permalink":    a.adminOptionsPermalink,
		"/admin/themes":               a.adminThemes,
		"/admin/themes/":              a.adminThemeRoutes,
		"/admin/plugins":              a.adminPlugins,
		"/admin/plugins/":             a.adminPluginRoutes,
		"/admin/management":           a.adminManagement,
		"/admin/management/upload":    a.adminManagementUpload,
		"/admin/medias":               a.adminMedias,
		"/admin/medias/":              a.adminMediaRoutes,
		"/admin/backup":               a.adminBackup,
		"/admin/upgrade":              a.adminUpgrade,
		"/admin/autosave":             a.adminAutosave,
		"/admin/tags/search":          a.adminTagSearch,
		"/admin/ajax/tags":            a.adminTagSearch,
		"/admin/ajax/preferences":     a.adminAjaxPreferences,
		"/admin/ajax/remote-callback": a.adminAjaxRemoteCallback,
		"/admin/schema/upload":        a.adminSchemaUpload,
		"/admin/theme-editor":         a.adminPlaceholder("主题编辑器", "对应 Typecho 的 theme-editor.php。直接编辑文件需要额外权限和审计，当前先保留入口。"),
	}
	for route, handler := range adminRoutes {
		mux.HandleFunc(route, a.requireAdmin(handler))
	}

	runtime := &plugin.Runtime{ListPublished: a.Contents.ListPublishedPlugin, Option: a.Options.Get, Config: a.pluginConfig}
	for _, route := range a.Plugins.Routes() {
		route := route
		mux.HandleFunc(route.Pattern, func(w http.ResponseWriter, r *http.Request) {
			if route.Plugin != "" && !a.Plugins.IsActive(route.Plugin) {
				http.NotFound(w, r)
				return
			}
			if route.Method != "" && r.Method != route.Method {
				methodNotAllowed(w, route.Method)
				return
			}
			route.Handler(runtime, w, r)
		})
	}

	mux.HandleFunc("/feed.xml", a.frontRSS)
	mux.HandleFunc("/atom.xml", a.frontAtom)
	mux.HandleFunc("/comments/feed.xml", a.frontCommentRSS)
	mux.HandleFunc("/xmlrpc.php", a.xmlRPC)
	mux.HandleFunc("/action/xmlrpc", a.xmlRPC)
	mux.HandleFunc("/action/pingback", a.xmlRPC)
	mux.HandleFunc("/trackback/", a.trackback)
	mux.HandleFunc("/rsd.xml", a.rsdXML)
	mux.HandleFunc("/wlwmanifest.xml", a.wlwManifest)
	mux.HandleFunc("/comment", a.frontComment)
	mux.HandleFunc("/preview/", a.frontPreview)
	mux.HandleFunc("/post/", a.frontPost)
	mux.HandleFunc("/page/", a.frontPage)
	mux.HandleFunc("/category/", a.frontCategory)
	mux.HandleFunc("/tag/", a.frontTag)
	mux.HandleFunc("/author/", a.frontAuthor)
	mux.HandleFunc("/search", a.frontSearch)
	mux.HandleFunc("/search/", a.frontSearch)
	mux.HandleFunc("/archive/", a.frontArchive)
	mux.HandleFunc("/", a.frontDynamic)
	return mux
}

func (a *App) adminLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.renderAdmin(w, r, "login.html", map[string]any{"Title": "登录", "Next": safeNext(r.URL.Query().Get("next"))})
	case http.MethodPost:
		if !a.validCSRFFor(r, "login") {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		next := safeNext(r.FormValue("next"))
		if !a.loginAllowed(clientIP(r), name) {
			a.renderAdmin(w, r, "login.html", map[string]any{"Title": "登录", "Error": "尝试过于频繁，请稍后再试", "Name": name, "Next": next})
			return
		}
		v := validate.New()
		v.Required("name", name).Required("password", r.FormValue("password"))
		if !v.Errors.Empty() {
			a.renderAdmin(w, r, "login.html", map[string]any{"Title": "登录", "Errors": v.Errors, "Name": name, "Next": next})
			return
		}
		user, err := a.Users.Authenticate(r.Context(), name, r.FormValue("password"))
		if err != nil {
			a.recordLoginFailure(clientIP(r), name)
			a.renderAdmin(w, r, "login.html", map[string]any{"Title": "登录", "Error": "用户名或密码不正确", "Name": name, "Next": next})
			return
		}
		secret, _ := a.Options.Get(r.Context(), "auth_secret")
		auth.SetSessionWithOptions(w, secret, user.UID, a.cookieOptions(r.Context()))
		if next == "" {
			next = "/admin"
		}
		a.flashRedirect(w, r, next, http.StatusSeeOther, flashNotice{Type: "success", Message: "登录成功。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminRegister(w http.ResponseWriter, r *http.Request) {
	if !optionBool(a.option(r.Context(), "allow_register", "0")) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.renderAdmin(w, r, "register.html", map[string]any{"Title": "注册"})
	case http.MethodPost:
		if !a.validCSRFFor(r, "register") {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		role := a.option(r.Context(), "register_default_role", "subscriber")
		if roleRank(role) > roleRank("subscriber") {
			role = "subscriber"
		}
		input := services.SaveUserInput{
			Name:       strings.TrimSpace(r.FormValue("name")),
			Password:   r.FormValue("password"),
			Mail:       strings.TrimSpace(r.FormValue("mail")),
			URL:        strings.TrimSpace(r.FormValue("url")),
			ScreenName: strings.TrimSpace(r.FormValue("screenName")),
			Role:       role,
		}
		errs := validateUserInput(input, true)
		if strings.TrimSpace(input.Mail) == "" {
			errs.Add("mail", "不能为空")
		}
		a.addUserUniqueErrors(r.Context(), &errs, input.Name, input.Mail, 0)
		if !errs.Empty() {
			a.renderAdmin(w, r, "register.html", map[string]any{"Title": "注册", "User": models.User{Name: input.Name, Mail: input.Mail, URL: input.URL, ScreenName: input.ScreenName}, "Errors": errs})
			return
		}
		if _, err := a.Users.Save(r.Context(), input, 0); err != nil {
			a.renderAdmin(w, r, "register.html", map[string]any{"Title": "注册", "User": models.User{Name: input.Name, Mail: input.Mail, URL: input.URL, ScreenName: input.ScreenName}, "Error": err.Error()})
			return
		}
		a.flashRedirect(w, r, "/admin/login", http.StatusSeeOther, flashNotice{Type: "success", Message: "注册成功，请登录。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) installWizard(w http.ResponseWriter, r *http.Request) {
	if !a.needsInstall(r.Context()) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.renderAdmin(w, r, "install.html", map[string]any{"Title": "安装"})
	case http.MethodPost:
		if !a.validCSRFFor(r, "install") {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		siteTitle := strings.TrimSpace(r.FormValue("site_title"))
		if siteTitle == "" {
			siteTitle = "GoBlog"
		}
		input := services.SaveUserInput{
			Name:       strings.TrimSpace(r.FormValue("name")),
			Password:   r.FormValue("password"),
			Mail:       strings.TrimSpace(r.FormValue("mail")),
			ScreenName: strings.TrimSpace(r.FormValue("screenName")),
			Role:       "administrator",
		}
		errs := validateUserInput(input, true)
		if !errs.Empty() {
			a.renderAdmin(w, r, "install.html", map[string]any{"Title": "安装", "User": models.User{Name: input.Name, Mail: input.Mail, ScreenName: input.ScreenName}, "SiteTitle": siteTitle, "Errors": errs})
			return
		}
		if err := a.Options.Set(r.Context(), "site_title", siteTitle); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if baseURL := strings.TrimSpace(r.FormValue("base_url")); baseURL != "" {
			_ = a.Options.Set(r.Context(), "base_url", baseURL)
		}
		if _, err := a.Users.Save(r.Context(), input, 0); err != nil {
			a.renderAdmin(w, r, "install.html", map[string]any{"Title": "安装", "Error": err.Error(), "SiteTitle": siteTitle})
			return
		}
		a.flashRedirect(w, r, "/admin/login", http.StatusSeeOther, flashNotice{Type: "success", Message: "安装完成，请登录。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) needsInstall(ctx context.Context) bool {
	users, err := a.Users.List(ctx, "")
	return err == nil && len(users) == 0
}

func (a *App) adminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if _, ok := a.currentUserID(r); !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	if !a.validCSRFFor(r, "admin") {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	auth.ClearSessionWithOptions(w, a.cookieOptions(r.Context()))
	a.flashRedirect(w, r, "/admin/login", http.StatusSeeOther, flashNotice{Type: "success", Message: "已退出。"})
}

func (a *App) adminDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}
	if err := a.Contents.RepairOrphanEditingDrafts(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stats, err := a.Contents.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, r, "dashboard.html", map[string]any{
		"Title":       "控制台",
		"Stats":       stats,
		"PostCount":   stats.Posts,
		"PluginCount": len(a.Plugins.Plugins()),
		"Plugins":     a.Plugins.Plugins(),
	})
}

func (a *App) adminPosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if err := a.Contents.RepairOrphanEditingDrafts(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	user, _ := a.currentUser(r)
	category, _ := strconv.ParseInt(r.URL.Query().Get("category"), 10, 64)
	query := services.ContentQuery{
		Type:     models.ContentTypePost,
		Status:   r.URL.Query().Get("status"),
		Keywords: r.URL.Query().Get("keywords"),
		Category: category,
		Limit:    200,
	}
	if roleRank(user.Role) < roleRank("editor") {
		query.AuthorID = user.UID
	}
	posts, err := a.Contents.List(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Batch check which published posts have editing drafts
	publishedIDs := make([]int64, 0)
	for _, p := range posts {
		if p.Status == models.ContentStatusPost && p.DraftOf == 0 {
			publishedIDs = append(publishedIDs, p.CID)
		}
	}
	draftMap, _ := a.Contents.DraftMapForContents(r.Context(), publishedIDs)
	if draftMap == nil {
		draftMap = map[int64]bool{}
	}
	categories, _ := a.Metas.List(r.Context(), "category")
	a.renderAdmin(w, r, "posts.html", map[string]any{"Title": "文章", "Posts": posts, "Categories": categories, "Status": r.URL.Query().Get("status"), "Keywords": r.URL.Query().Get("keywords"), "Category": category, "DraftMap": draftMap})
}

func (a *App) adminPostRoutes(w http.ResponseWriter, r *http.Request) {
	a.contentRoutes(w, r, "/admin/posts/", models.ContentTypePost)
}

func (a *App) adminPages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !a.requireRole(w, r, "editor") {
		return
	}
	if err := a.Contents.RepairOrphanEditingDrafts(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pages, err := a.Contents.List(r.Context(), services.ContentQuery{Type: models.ContentTypePage, Status: r.URL.Query().Get("status"), Keywords: r.URL.Query().Get("keywords"), Limit: 200})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	publishedIDs := make([]int64, 0)
	for _, p := range pages {
		if p.Status == models.ContentStatusPost && p.DraftOf == 0 {
			publishedIDs = append(publishedIDs, p.CID)
		}
	}
	draftMap, _ := a.Contents.DraftMapForContents(r.Context(), publishedIDs)
	if draftMap == nil {
		draftMap = map[int64]bool{}
	}
	a.renderAdmin(w, r, "pages.html", map[string]any{"Title": "页面", "Pages": pages, "Keywords": r.URL.Query().Get("keywords"), "Status": r.URL.Query().Get("status"), "DraftMap": draftMap})
}

func (a *App) adminPageRoutes(w http.ResponseWriter, r *http.Request) {
	a.contentRoutes(w, r, "/admin/pages/", models.ContentTypePage)
}

func (a *App) contentRoutes(w http.ResponseWriter, r *http.Request, prefix, typ string) {
	clean := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if clean == "new" {
		if typ == models.ContentTypePage && !a.requireRole(w, r, "editor") {
			return
		}
		if typ == models.ContentTypePost && !a.requireRole(w, r, "contributor") {
			return
		}
		a.contentForm(w, r, typ, 0)
		return
	}
	parts := strings.Split(clean, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "edit":
		if !a.canEditContent(w, r, id, typ) {
			return
		}
		a.contentForm(w, r, typ, id)
	case "revisions":
		if !a.canEditContent(w, r, id, typ) {
			return
		}
		a.contentRevisions(w, r, typ, id)
	case "restore":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !a.canEditContent(w, r, id, typ) {
			return
		}
		rid, _ := strconv.ParseInt(r.FormValue("rid"), 10, 64)
		revision, err := a.Contents.RevisionByID(r.Context(), rid)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if revision.CID != id {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		if _, err := a.Contents.RestoreRevision(r.Context(), id, rid); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, contentActionURL(typ, id), http.StatusSeeOther, flashNotice{Type: "success", Message: "修订版本已恢复。"})
	case "delete":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !a.canEditContent(w, r, id, typ) {
			return
		}
		if err := a.deleteContentWithAttachmentPolicy(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, contentListURL(typ), http.StatusSeeOther, flashNotice{Type: "success", Message: "内容已删除。"})
	case "mark":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !a.canEditContent(w, r, id, typ) {
			return
		}
		newStatus := r.FormValue("status")
		item, itemErr := a.Contents.ByID(r.Context(), id)
		if itemErr != nil {
			http.Error(w, itemErr.Error(), http.StatusInternalServerError)
			return
		}
		// If marking a published article as draft, create an editing draft instead
		if item.Status == models.ContentStatusPost && item.DraftOf == 0 && newStatus == models.ContentStatusDraft {
			input := services.SaveContentInput{
				Title:        item.Title,
				Slug:         item.Slug,
				Text:         item.Text,
				Type:         item.Type,
				Status:       models.ContentStatusDraft,
				Password:     item.Password,
				SortOrder:    item.SortOrder,
				Template:     item.Template,
				Parent:       item.Parent,
				AllowComment: item.AllowComment == "1",
				AllowPing:    item.AllowPing == "1",
				AllowFeed:    item.AllowFeed == "1",
			}
			uid, _ := a.currentUserID(r)
			if _, err := a.Contents.SaveEditingDraft(r.Context(), id, input, uid); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			a.flashRedirect(w, r, contentListURL(typ), http.StatusSeeOther, flashNotice{Type: "success", Message: "已创建编辑草稿。"})
			return
		}
		// If marking a draft (with DraftOf) as published, publish the draft
		if item.DraftOf > 0 && newStatus == models.ContentStatusPost {
			if err := a.Contents.PublishDraft(r.Context(), id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			a.flashRedirect(w, r, contentListURL(typ), http.StatusSeeOther, flashNotice{Type: "success", Message: "内容已发布。"})
			return
		}
		if err := a.Contents.MarkStatus(r.Context(), id, newStatus); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, contentListURL(typ), http.StatusSeeOther, flashNotice{Type: "success", Message: "状态已更新。"})
	case "discard":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !a.canEditContent(w, r, id, typ) {
			return
		}
		// Discard the editing draft for a published article
		if draft, err := a.Contents.DraftForContent(r.Context(), id); err == nil && draft.CID > 0 {
			if err := a.Contents.DeleteDraft(r.Context(), draft.CID); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		a.flashRedirect(w, r, contentListURL(typ), http.StatusSeeOther, flashNotice{Type: "success", Message: "草稿已丢弃。"})
	default:
		http.NotFound(w, r)
	}
}

func (a *App) contentForm(w http.ResponseWriter, r *http.Request, typ string, id int64) {
	var item models.Content
	var err error
	var editingDraft bool
	var draftExists bool
	var publishedID int64
	loadID := id
	sourcePublished := r.URL.Query().Get("source") == "published"
	if id > 0 {
		item, err = a.Contents.ByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if item.DraftOf > 0 {
			publishedID = item.DraftOf
			editingDraft = true
			loadID = item.CID
		} else if item.Status == models.ContentStatusPost {
			publishedID = item.CID
			if !sourcePublished {
				if draft, draftErr := a.Contents.DraftForContent(r.Context(), id); draftErr == nil && draft.CID > 0 {
					item = draft
					editingDraft = true
					draftExists = true
					loadID = draft.CID
				}
			}
			if !editingDraft {
				item.Status = models.ContentStatusDraft
				item.DraftOf = publishedID
				editingDraft = true
				loadID = publishedID
			}
		}
	} else {
		item = models.Content{Type: typ, Status: models.ContentStatusPost, AllowComment: "1", AllowFeed: "1"}
	}

	switch r.Method {
	case http.MethodGet:
		categories, _ := a.Metas.List(r.Context(), "category")
		pages, _ := a.Contents.List(r.Context(), services.ContentQuery{Type: models.ContentTypePage, Limit: 200})
		selectedCategories, _ := a.Metas.CategoriesForContent(r.Context(), loadID)
		selectedTags, _ := a.Metas.TagsForContent(r.Context(), loadID)
		fields, _ := a.Contents.FieldsForContent(r.Context(), loadID)
		fields = mergeThemeFields(a.themeContentFields(r.Context(), typ), fields)
		actionID := id
		formCID := item.CID
		revisionID := id
		if editingDraft && publishedID > 0 {
			actionID = publishedID
			formCID = publishedID
			revisionID = publishedID
		}
		revisions, _ := a.Contents.Revisions(r.Context(), revisionID)
		mediaLibrary, _ := a.editorMediaLibrary(r)
		preview := a.previewURL(r, item)
		if editingDraft && !draftExists {
			preview = ""
		}
		a.renderAdmin(w, r, "content_form.html", map[string]any{
			"Title":              contentFormTitle(typ, id),
			"Content":            item,
			"Type":               typ,
			"Action":             contentActionURL(typ, actionID),
			"FormCID":            formCID,
			"PublishedID":        publishedID,
			"Saved":              r.URL.Query().Get("saved") == "1",
			"Categories":         categories,
			"Pages":              pages,
			"SelectedCategories": selectedCategories,
			"SelectedTags":       selectedTags,
			"Fields":             fields,
			"Revisions":          revisions,
			"PreviewURL":         preview,
			"MediaLibrary":       mediaLibrary,
			"EditingDraft":       editingDraft,
		})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.FormValue("discard") == "1" {
			a.discardContentDraftFromForm(w, r, typ, id)
			return
		}
		input, err := parseContentForm(r, typ)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errs := validateContentInput(input); !errs.Empty() {
			item = applyContentInput(item, input)
			a.renderContentForm(w, r, typ, id, item, metasFromIDs(input.CategoryIDs), metasFromNames(input.Tags), fieldModels(input.Fields), errs)
			return
		}
		if id == 0 {
			draftID, _ := strconv.ParseInt(r.FormValue("cid"), 10, 64)
			if draftID > 0 {
				if !a.canEditContent(w, r, draftID, typ) {
					return
				}
				id = draftID
			}
		}
		uid, _ := a.currentUserID(r)

		if id > 0 {
			existing, existErr := a.Contents.ByID(r.Context(), id)
			if existErr == nil && existing.DraftOf > 0 {
				publishedID = existing.DraftOf
			} else if existErr == nil && existing.Status == models.ContentStatusPost && existing.DraftOf == 0 {
				publishedID = id
			} else if existErr != nil {
				http.Error(w, existErr.Error(), http.StatusInternalServerError)
				return
			}
		}

		if publishedID > 0 {
			draftID, err := a.Contents.SaveEditingDraft(r.Context(), publishedID, input, uid)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if input.Status == models.ContentStatusPost {
				if err := a.Contents.PublishDraft(r.Context(), draftID); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				a.flashRedirect(w, r, contentActionURL(typ, publishedID), http.StatusSeeOther, flashNotice{Type: "success", Message: "内容已发布。"})
			} else {
				a.flashRedirect(w, r, contentActionURL(typ, publishedID), http.StatusSeeOther, flashNotice{Type: "success", Message: "草稿已保存。"})
			}
			return
		}

		savePayload := plugin.ContentSavePayload{ID: id, AuthorID: uid, Input: input}
		if payload, err := a.Plugins.ApplyActive(r.Context(), plugin.HookContentBeforeSave, savePayload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		} else if next, ok := payload.(plugin.ContentSavePayload); ok {
			savePayload = next
			if nextInput, ok := next.Input.(services.SaveContentInput); ok {
				input = nextInput
			}
		}
		if id == 0 {
			id, err = a.Contents.Create(r.Context(), input, uid)
		} else {
			err = a.Contents.Update(r.Context(), id, input)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		savePayload.ID = id
		savePayload.Input = input
		if _, err := a.Plugins.ApplyActive(r.Context(), plugin.HookContentAfterSave, savePayload); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, contentActionURL(typ, id), http.StatusSeeOther, flashNotice{Type: "success", Message: "内容已保存。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) discardContentDraftFromForm(w http.ResponseWriter, r *http.Request, typ string, id int64) {
	targetID := id
	if targetID == 0 {
		targetID, _ = strconv.ParseInt(r.FormValue("cid"), 10, 64)
	}
	if targetID <= 0 {
		a.flashRedirect(w, r, contentListURL(typ), http.StatusSeeOther, flashNotice{Type: "success", Message: "草稿已丢弃。"})
		return
	}
	if !a.canEditContent(w, r, targetID, typ) {
		return
	}
	item, err := a.Contents.ByID(r.Context(), targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectTo := contentListURL(typ)
	switch {
	case item.DraftOf > 0:
		redirectTo = contentActionURL(typ, item.DraftOf)
		err = a.Contents.DeleteDraft(r.Context(), item.CID)
	case item.Status == models.ContentStatusDraft:
		err = a.deleteContentWithAttachmentPolicy(r.Context(), item.CID)
	case item.Status == models.ContentStatusPost:
		redirectTo = contentActionURL(typ, item.CID)
		if draft, draftErr := a.Contents.DraftForContent(r.Context(), item.CID); draftErr == nil && draft.CID > 0 {
			err = a.Contents.DeleteDraft(r.Context(), draft.CID)
		} else if draftErr != nil && !errors.Is(draftErr, sql.ErrNoRows) {
			err = draftErr
		}
	default:
		err = a.deleteContentWithAttachmentPolicy(r.Context(), item.CID)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.flashRedirect(w, r, redirectTo, http.StatusSeeOther, flashNotice{Type: "success", Message: "草稿已丢弃。"})
}

func (a *App) contentRevisions(w http.ResponseWriter, r *http.Request, typ string, id int64) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	item, err := a.Contents.ByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	revisions, err := a.Contents.Revisions(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, r, "revisions.html", map[string]any{"Title": "修订版本", "Content": item, "Type": typ, "Revisions": revisions})
}

func (a *App) renderContentForm(w http.ResponseWriter, r *http.Request, typ string, id int64, item models.Content, selectedCategories, selectedTags []models.Meta, fields []models.Field, errs validate.Errors) {
	categories, _ := a.Metas.List(r.Context(), "category")
	pages, _ := a.Contents.List(r.Context(), services.ContentQuery{Type: models.ContentTypePage, Limit: 200})
	publishedID := item.DraftOf
	formCID := item.CID
	actionID := id
	loadID := id
	if publishedID > 0 {
		formCID = publishedID
		actionID = publishedID
		if item.CID > 0 && item.CID != publishedID {
			loadID = item.CID
		} else {
			loadID = publishedID
		}
	}
	if selectedCategories == nil {
		selectedCategories, _ = a.Metas.CategoriesForContent(r.Context(), loadID)
	}
	if selectedTags == nil {
		selectedTags, _ = a.Metas.TagsForContent(r.Context(), loadID)
	}
	if fields == nil {
		fields, _ = a.Contents.FieldsForContent(r.Context(), loadID)
	}
	fields = mergeThemeFields(a.themeContentFields(r.Context(), typ), fields)
	revisionID := id
	if publishedID > 0 {
		revisionID = publishedID
	}
	revisions, _ := a.Contents.Revisions(r.Context(), revisionID)
	mediaLibrary, _ := a.editorMediaLibrary(r)
	a.renderAdmin(w, r, "content_form.html", map[string]any{
		"Title":              contentFormTitle(typ, id),
		"Content":            item,
		"Type":               typ,
		"Action":             contentActionURL(typ, actionID),
		"FormCID":            formCID,
		"PublishedID":        publishedID,
		"Categories":         categories,
		"Pages":              pages,
		"SelectedCategories": selectedCategories,
		"SelectedTags":       selectedTags,
		"Errors":             errs,
		"Fields":             fields,
		"Revisions":          revisions,
		"PreviewURL":         a.previewURL(r, item),
		"MediaLibrary":       mediaLibrary,
		"EditingDraft":       publishedID > 0,
	})
}

func (a *App) adminCategories(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.metaList(w, r, "category", "分类", "categories.html")
}

func (a *App) adminCategoryRoutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.metaRoutes(w, r, "/admin/categories/", "category")
}

func (a *App) adminTags(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.metaList(w, r, "tag", "标签", "tags.html")
}

func (a *App) adminTagRoutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.metaRoutes(w, r, "/admin/tags/", "tag")
}

func (a *App) metaList(w http.ResponseWriter, r *http.Request, typ, title, tmpl string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	items, err := a.Metas.List(r.Context(), typ)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	options, _ := a.Options.All(r.Context())
	a.renderAdmin(w, r, tmpl, map[string]any{"Title": title, "Items": items, "Options": options})
}

func (a *App) metaRoutes(w http.ResponseWriter, r *http.Request, prefix, typ string) {
	clean := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if clean == "new" {
		a.metaForm(w, r, typ, 0)
		return
	}
	parts := strings.Split(clean, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "edit":
		a.metaForm(w, r, typ, id)
	case "delete":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if err := a.Metas.Delete(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, metaListURL(typ), http.StatusSeeOther)
	case "default":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if err := a.Metas.SetDefaultCategory(r.Context(), id, a.Options); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) metaForm(w http.ResponseWriter, r *http.Request, typ string, id int64) {
	var item models.Meta
	var err error
	if id > 0 {
		item, err = a.Metas.ByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	} else {
		item = models.Meta{Type: typ}
	}
	switch r.Method {
	case http.MethodGet:
		categories, _ := a.Metas.List(r.Context(), "category")
		a.renderAdmin(w, r, "meta_form.html", map[string]any{"Title": metaTitle(typ, id), "Meta": item, "Type": typ, "Action": metaActionURL(typ, id), "Categories": categories})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		parent, _ := strconv.ParseInt(r.FormValue("parent"), 10, 64)
		input := services.SaveMetaInput{Name: strings.TrimSpace(r.FormValue("name")), Slug: strings.TrimSpace(r.FormValue("slug")), Type: typ, Description: r.FormValue("description"), Parent: parent}
		if errs := validateMetaInput(input); !errs.Empty() {
			item = models.Meta{MID: id, Name: input.Name, Slug: input.Slug, Type: typ, Description: input.Description, Parent: input.Parent}
			categories, _ := a.Metas.List(r.Context(), "category")
			a.renderAdmin(w, r, "meta_form.html", map[string]any{"Title": metaTitle(typ, id), "Meta": item, "Type": typ, "Action": metaActionURL(typ, id), "Categories": categories, "Errors": errs})
			return
		}
		_, err := a.Metas.Save(r.Context(), input, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, metaListURL(typ), http.StatusSeeOther)
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminComments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !a.requireRole(w, r, "editor") {
		return
	}
	cid, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
	typ := strings.TrimSpace(r.URL.Query().Get("type"))
	comments, err := a.Comments.ListFiltered(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("keywords"), cid, typ)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, r, "comments.html", map[string]any{"Title": "评论", "Comments": comments, "Status": r.URL.Query().Get("status"), "Keywords": r.URL.Query().Get("keywords"), "CID": cid, "Type": typ})
}

func (a *App) adminCommentRoutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "editor") {
		return
	}
	clean := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/comments/"), "/")
	switch clean {
	case "batch":
		a.adminCommentsBatch(w, r)
		return
	case "clear-spam":
		a.adminCommentsClearSpam(w, r)
		return
	}
	parts := strings.Split(clean, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "edit":
		a.commentForm(w, r, id, false)
	case "reply":
		a.commentForm(w, r, id, true)
	case "mark":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if err := a.Comments.Mark(r.Context(), id, r.FormValue("status")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, "/admin/comments", http.StatusSeeOther, flashNotice{Type: "success", Message: "评论状态已更新。"})
	case "delete":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if err := a.Comments.Delete(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, "/admin/comments", http.StatusSeeOther, flashNotice{Type: "success", Message: "评论已删除。"})
	default:
		http.NotFound(w, r)
	}
}

func (a *App) adminCommentsBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ids := parseInt64Values(r.Form["id"])
	if len(ids) == 0 {
		a.flashRedirect(w, r, "/admin/comments", http.StatusSeeOther, flashNotice{Type: "success", Message: "评论已保存。"})
		return
	}
	switch r.FormValue("action") {
	case "approved", "waiting", "spam":
		if err := a.Comments.MarkMany(r.Context(), ids, r.FormValue("action")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "delete":
		if err := a.Comments.DeleteMany(r.Context(), ids); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "unsupported comment action", http.StatusBadRequest)
		return
	}
	a.flashRedirect(w, r, "/admin/comments", http.StatusSeeOther, flashNotice{Type: "success", Message: "评论已批量处理。"})
}

func (a *App) adminCommentsClearSpam(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := a.Comments.ClearSpam(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.flashRedirect(w, r, "/admin/comments?status=spam", http.StatusSeeOther, flashNotice{Type: "success", Message: "垃圾评论已清空。"})
}

func (a *App) commentForm(w http.ResponseWriter, r *http.Request, id int64, reply bool) {
	comment, err := a.Comments.ByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		title := "编辑评论"
		if reply {
			title = "回复评论"
		}
		a.renderAdmin(w, r, "comment_form.html", map[string]any{"Title": title, "Comment": comment, "Reply": reply, "Action": r.URL.Path})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		input := services.SaveCommentInput{CID: comment.CID, Author: strings.TrimSpace(r.FormValue("author")), Mail: strings.TrimSpace(r.FormValue("mail")), URL: strings.TrimSpace(r.FormValue("url")), Text: strings.TrimSpace(r.FormValue("text")), Status: r.FormValue("status")}
		if reply {
			input.Parent = comment.COID
			input.OwnerID = comment.OwnerID
			if user, ok := a.currentUser(r); ok {
				input.AuthorID = user.UID
			}
			if input.Author == "" {
				input.Author = "admin"
			}
			if input.Status == "" {
				input.Status = "approved"
			}
		}
		if errs := validateCommentInput(input); !errs.Empty() {
			comment.Author = input.Author
			comment.Mail = input.Mail
			comment.URL = input.URL
			comment.Text = input.Text
			comment.Status = input.Status
			a.renderAdmin(w, r, "comment_form.html", map[string]any{"Title": "编辑评论", "Comment": comment, "Reply": reply, "Action": r.URL.Path, "Errors": errs})
			return
		}
		if reply {
			err = a.Comments.Save(r.Context(), input, 0)
		} else {
			err = a.Comments.Save(r.Context(), input, id)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, "/admin/comments", http.StatusSeeOther, flashNotice{Type: "success", Message: "评论已保存。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !a.requireRole(w, r, "administrator") {
		return
	}
	users, err := a.Users.List(r.Context(), r.URL.Query().Get("keywords"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, r, "users.html", map[string]any{"Title": "用户", "Users": users, "Keywords": r.URL.Query().Get("keywords")})
}

func (a *App) adminUserRoutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	clean := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/users/"), "/")
	if clean == "new" {
		a.userForm(w, r, 0)
		return
	}
	parts := strings.Split(clean, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "edit":
		a.userForm(w, r, id)
	case "delete":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if err := a.Users.Delete(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, "/admin/users", http.StatusSeeOther, flashNotice{Type: "success", Message: "用户已删除。"})
	default:
		http.NotFound(w, r)
	}
}

func (a *App) userForm(w http.ResponseWriter, r *http.Request, id int64) {
	var user models.User
	var err error
	if id > 0 {
		user, err = a.Users.ByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		a.renderAdmin(w, r, "user_form.html", map[string]any{"Title": userTitle(id), "User": user, "Action": userActionURL(id)})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		input := services.SaveUserInput{Name: strings.TrimSpace(r.FormValue("name")), Password: r.FormValue("password"), Mail: strings.TrimSpace(r.FormValue("mail")), URL: strings.TrimSpace(r.FormValue("url")), ScreenName: strings.TrimSpace(r.FormValue("screenName")), Role: r.FormValue("role")}
		errs := validateUserInput(input, id == 0)
		a.addUserUniqueErrors(r.Context(), &errs, input.Name, input.Mail, id)
		if !errs.Empty() {
			user = models.User{UID: id, Name: input.Name, Mail: input.Mail, URL: input.URL, ScreenName: input.ScreenName, Role: input.Role}
			a.renderAdmin(w, r, "user_form.html", map[string]any{"Title": userTitle(id), "User": user, "Action": userActionURL(id), "Errors": errs})
			return
		}
		_, err := a.Users.Save(r.Context(), input, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, "/admin/users", http.StatusSeeOther, flashNotice{Type: "success", Message: "用户已保存。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminProfile(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.currentUserID(r)
	user, err := a.Users.ByID(r.Context(), uid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.renderAdmin(w, r, "profile.html", map[string]any{"Title": "个人设置", "User": user, "Saved": r.URL.Query().Get("saved") == "1"})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		input := services.SaveUserInput{Name: user.Name, Mail: strings.TrimSpace(r.FormValue("mail")), URL: strings.TrimSpace(r.FormValue("url")), ScreenName: strings.TrimSpace(r.FormValue("screenName")), Role: user.Role}
		errs := validateUserInput(input, false)
		a.addUserUniqueErrors(r.Context(), &errs, input.Name, input.Mail, uid)
		if password := r.FormValue("password"); password != "" && len([]rune(password)) < 6 {
			errs.Add("password", "长度不能少于 6 个字符")
		}
		if !errs.Empty() {
			user.Mail = input.Mail
			user.URL = input.URL
			user.ScreenName = input.ScreenName
			a.renderAdmin(w, r, "profile.html", map[string]any{"Title": "个人设置", "User": user, "Errors": errs})
			return
		}
		if _, err := a.Users.Save(r.Context(), input, uid); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := a.Users.ChangePassword(r.Context(), uid, r.FormValue("password")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, "/admin/profile", http.StatusSeeOther, flashNotice{Type: "success", Message: "个人资料已保存。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminOptionsGeneral(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.optionsForm(w, r, "General Settings", "options_general.html", []string{"site_title", "site_description", "site_keywords", "base_url", "site_language", "site_timezone", "allow_register", "register_default_role", "cookie_prefix", "cookie_secure", "cookie_samesite", "active_theme", "upload_allowed_exts", "upload_max_size", "upload_replace_same_ext_only", "attachment_delete_policy"})
}

func (a *App) adminUpgrade(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	current := optionInt(a.option(r.Context(), "schema_version", "0"), 0)
	switch r.Method {
	case http.MethodGet:
		a.renderAdmin(w, r, "upgrade.html", map[string]any{"Title": "升级", "CurrentVersion": current, "TargetVersion": models.CurrentSchemaVersion})
	case http.MethodPost:
		if err := models.RunVersionedMigrations(r.Context(), a.Contents.DB()); err != nil {
			a.renderAdmin(w, r, "upgrade.html", map[string]any{"Title": "升级", "CurrentVersion": current, "TargetVersion": models.CurrentSchemaVersion, "Error": err.Error()})
			return
		}
		a.flashRedirect(w, r, "/admin/upgrade", http.StatusSeeOther, flashNotice{Type: "success", Message: "升级检查已完成。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminOptionsReading(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.optionsForm(w, r, "阅读设置", "options_reading.html", []string{"post_date_format", "page_size", "posts_list_size", "content_render_mode", "feed_full_text", "front_page_type", "front_page_cid", "posts_index_path"})
}

func (a *App) adminOptionsDiscussion(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.optionsForm(w, r, "评论设置", "options_discussion.html", []string{
		"comments_require_moderation", "comments_require_mail", "comments_require_url", "comments_show_url", "comments_order",
		"comment_date_format", "comments_list_size", "comments_page_size", "comments_page_display", "comments_max_nesting_levels",
		"comments_whitelist", "comments_check_referer", "comments_antispam", "comments_auto_close", "comments_post_interval", "comments_post_interval_enable",
		"comments_html_tag_allowed", "comments_stop_words", "comments_ip_blacklist",
		"comments_markdown", "comments_url_nofollow", "comments_avatar", "comments_avatar_rating",
	})
}

func (a *App) adminOptionsPermalink(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validatePermalinkOptions(r); err != nil {
			options, _ := a.Options.All(r.Context())
			for _, key := range []string{"permalink_post", "permalink_page", "permalink_category"} {
				options[key] = r.FormValue(key)
			}
			a.renderAdmin(w, r, "options_permalink.html", map[string]any{"Title": "永久链接", "Options": options, "Error": err.Error()})
			return
		}
	}
	a.optionsForm(w, r, "永久链接", "options_permalink.html", []string{"permalink_post", "permalink_page", "permalink_category"})
}

func (a *App) optionsForm(w http.ResponseWriter, r *http.Request, title, tmpl string, keys []string) {
	switch r.Method {
	case http.MethodGet:
		options, err := a.Options.All(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.renderAdmin(w, r, tmpl, map[string]any{"Title": title, "Options": options, "Saved": r.URL.Query().Get("saved") == "1"})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, key := range keys {
			if err := a.Options.Set(r.Context(), key, r.FormValue(key)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		a.flashRedirect(w, r, r.URL.Path, http.StatusSeeOther, flashNotice{Type: "success", Message: "设置已保存。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminThemes(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := r.FormValue("theme")
		if _, ok := a.Plugins.Theme(name); !ok {
			http.Error(w, "theme not found", http.StatusBadRequest)
			return
		}
		if err := a.Options.Set(r.Context(), "active_theme", name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, "/admin/themes", http.StatusSeeOther, flashNotice{Type: "success", Message: "主题已切换。"})
		return
	}
	active, _ := a.Options.Get(r.Context(), "active_theme")
	a.renderAdmin(w, r, "themes.html", map[string]any{"Title": "主题", "Themes": a.Plugins.Themes(), "ActiveTheme": active, "Saved": r.URL.Query().Get("saved") == "1"})
}

func (a *App) adminThemeRoutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/themes/"), "/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	name := parts[0]
	switch parts[1] {
	case "config":
		a.adminThemeConfig(w, r, name)
	case "files":
		a.adminThemeFiles(w, r, name)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) adminPlugins(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.syncActivePlugins(r.Context())
	a.renderAdmin(w, r, "plugins.html", map[string]any{"Title": "插件", "Plugins": a.pluginViews(r.Context()), "Saved": r.URL.Query().Get("saved") == "1"})
}

func (a *App) adminPluginRoutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/plugins/"), "/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	name := parts[0]
	switch parts[1] {
	case "activate", "deactivate":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		a.adminPluginToggle(w, r, name, parts[1] == "activate")
	case "config":
		a.adminPluginConfig(w, r, name, false)
	case "personal":
		a.adminPluginConfig(w, r, name, true)
	default:
		http.NotFound(w, r)
	}
}

type pluginView struct {
	Name          string
	Version       string
	Author        string
	Description   string
	Homepage      string
	RequireGoBlog string
	Active        bool
	Compatible    bool
	HasConfig     bool
	HasPersonal   bool
}

func (a *App) pluginViews(ctx context.Context) []pluginView {
	active := a.activePluginSet(ctx)
	plugins := a.Plugins.Plugins()
	out := make([]pluginView, 0, len(plugins))
	for _, p := range plugins {
		info := a.Plugins.PluginInfo(p)
		view := pluginView{
			Name:          info.Name,
			Version:       info.Version,
			Author:        info.Author,
			Description:   info.Description,
			Homepage:      info.Homepage,
			RequireGoBlog: info.RequireGoBlog,
			Active:        active[info.Name],
			Compatible:    plugin.Compatible(info.RequireGoBlog, plugin.GoBlogVersion),
		}
		if provider, ok := p.(plugin.ConfigProvider); ok && len(provider.ConfigSchema()) > 0 {
			view.HasConfig = true
		}
		if provider, ok := p.(plugin.PersonalConfigProvider); ok && len(provider.PersonalConfigSchema()) > 0 {
			view.HasPersonal = true
		}
		out = append(out, view)
	}
	return out
}

func (a *App) adminPluginToggle(w http.ResponseWriter, r *http.Request, name string, enable bool) {
	p, ok := a.Plugins.Plugin(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	info := a.Plugins.PluginInfo(p)
	if enable && !plugin.Compatible(info.RequireGoBlog, plugin.GoBlogVersion) {
		http.Error(w, "插件要求更高版本的 GoBlog", http.StatusBadRequest)
		return
	}
	active := a.activePluginSet(r.Context())
	runtime := &plugin.Runtime{ListPublished: a.Contents.ListPublishedPlugin, Option: a.Options.Get, Config: a.pluginConfig}
	if enable {
		if activator, ok := p.(plugin.Activator); ok {
			if err := activator.Activate(r.Context(), runtime); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		active[name] = true
	} else {
		if deactivator, ok := p.(plugin.Deactivator); ok {
			if err := deactivator.Deactivate(r.Context(), runtime); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		delete(active, name)
	}
	if err := a.saveActivePluginSet(r.Context(), active); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.syncActivePlugins(r.Context())
	a.flashRedirect(w, r, "/admin/plugins", http.StatusSeeOther, flashNotice{Type: "success", Message: "插件状态已保存。"})
}

func (a *App) adminPluginConfig(w http.ResponseWriter, r *http.Request, name string, personal bool) {
	p, ok := a.Plugins.Plugin(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var schema []plugin.FieldSchema
	title := "插件设置：" + name
	userID := int64(0)
	if personal {
		provider, ok := p.(plugin.PersonalConfigProvider)
		if !ok {
			http.NotFound(w, r)
			return
		}
		schema = provider.PersonalConfigSchema()
		title = "插件个人设置：" + name
		userID, _ = a.currentUserID(r)
	} else {
		provider, ok := p.(plugin.ConfigProvider)
		if !ok {
			http.NotFound(w, r)
			return
		}
		schema = provider.ConfigSchema()
	}
	key := pluginOptionKey(name)
	if personal {
		key = pluginPersonalOptionKey(name)
	}
	a.schemaForm(w, r, schemaFormConfig{
		Title:     title,
		Template:  "schema_form.html",
		BackURL:   "/admin/plugins",
		OptionKey: key,
		UserID:    userID,
		Schema:    schema,
		SavedURL:  r.URL.Path,
		Saved:     r.URL.Query().Get("saved") == "1",
	})
}

func (a *App) adminThemeConfig(w http.ResponseWriter, r *http.Request, name string) {
	theme, ok := a.Plugins.Theme(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if len(theme.ConfigSchema) == 0 {
		http.NotFound(w, r)
		return
	}
	a.schemaForm(w, r, schemaFormConfig{
		Title:     "主题设置：" + name,
		Template:  "schema_form.html",
		BackURL:   "/admin/themes",
		OptionKey: themeOptionKey(name),
		Schema:    theme.ConfigSchema,
		SavedURL:  r.URL.Path,
		Saved:     r.URL.Query().Get("saved") == "1",
	})
}

const (
	adminAppearanceOptionKey  = "admin_appearance"
	adminSettingsUploadBucket = "admin-settings"
)

func (a *App) adminManagement(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.schemaForm(w, r, schemaFormConfig{
		Title:     "管理设置",
		Template:  "schema_form.html",
		BackURL:   "/admin",
		OptionKey: adminAppearanceOptionKey,
		Schema:    adminAppearanceSchema(),
		SavedURL:  r.URL.Path,
		Saved:     r.URL.Query().Get("saved") == "1",
		UploadURL: "/admin/management/upload",
	})
}

func adminAppearanceSchema() []plugin.FieldSchema {
	colorOptions := adminAppearanceColorOptions()
	return []plugin.FieldSchema{
		{
			Name:        "admin_bg_image",
			Label:       "电脑端后台背景图 URL",
			Type:        plugin.FieldImage,
			Description: "用于桌面端后台背景。上传文件会保存到后台设置专用目录。",
		},
		{
			Name:        "admin_mobile_bg_image",
			Label:       "手机端后台背景图 URL",
			Type:        plugin.FieldImage,
			Description: "用于窄屏和手机端后台背景；留空时沿用电脑端背景。",
		},
		{
			Name:        "admin_card_opacity",
			Label:       "后台卡片背景透明度",
			Type:        plugin.FieldNumber,
			Default:     "0.84",
			Description: "取值 0 到 1，仅影响后台卡片背景透明度。",
			Min:         "0",
			Max:         "1",
			Step:        "0.01",
		},
		{
			Name:        "admin_sidebar_opacity",
			Label:       "后台侧边栏背景透明度",
			Type:        plugin.FieldNumber,
			Default:     "0.90",
			Description: "取值 0 到 1，仅影响后台侧边栏背景透明度。",
			Min:         "0",
			Max:         "1",
			Step:        "0.01",
		},
		{
			Name:        "admin_topbar_opacity",
			Label:       "后台顶栏背景透明度",
			Type:        plugin.FieldNumber,
			Default:     "0.92",
			Description: "取值 0 到 1，仅影响后台顶栏主题色背景透明度。",
			Min:         "0",
			Max:         "1",
			Step:        "0.01",
		},
		{
			Name:        "admin_input_opacity",
			Label:       "后台输入框背景透明度",
			Type:        plugin.FieldNumber,
			Default:     "0.62",
			Description: "取值 0 到 1，仅影响后台输入框和选择框背景透明度。",
			Min:         "0",
			Max:         "1",
			Step:        "0.01",
		},
		{
			Name:        "admin_bg_mask_opacity",
			Label:       "后台背景蒙版透明度",
			Type:        plugin.FieldNumber,
			Default:     "0.54",
			Description: "取值 0 到 1，控制背景图片上方的 MDUI 背景色蒙版。",
			Min:         "0",
			Max:         "1",
			Step:        "0.01",
		},
		{
			Name:        "admin_primary_preset",
			Label:       "后台常用主题色",
			Type:        plugin.FieldSelect,
			Default:     "#6750a4",
			Description: "用于 MDUI2 生成后台配色方案。",
			Options:     colorOptions,
		},
		{
			Name:        "admin_custom_primary",
			Label:       "后台自定义主题色",
			Type:        plugin.FieldColor,
			Description: "填写 #RRGGBB 后会覆盖上方预设色。",
			Options:     colorOptions,
		},
	}
}

func adminAppearanceColorOptions() []plugin.FieldOption {
	return []plugin.FieldOption{
		{Label: "MDUI 紫", Value: "#6750a4"},
		{Label: "Cuckoo 粉", Value: "#ff4081"},
		{Label: "蓝色", Value: "#1976d2"},
		{Label: "青色", Value: "#00838f"},
		{Label: "绿色", Value: "#2e7d32"},
		{Label: "琥珀", Value: "#ff8f00"},
		{Label: "橙红", Value: "#e64a19"},
		{Label: "蓝灰", Value: "#546e7a"},
	}
}

func (a *App) adminThemeFiles(w http.ResponseWriter, r *http.Request, name string) {
	theme, ok := a.Plugins.Theme(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if theme.EditableDir == "" || theme.Embedded {
		a.renderAdmin(w, r, "theme_files.html", map[string]any{"Title": "主题文件", "Theme": theme, "ReadOnly": true})
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("file"))
	files, _ := editableThemeFiles(theme.EditableDir)
	if rel == "" && len(files) > 0 {
		rel = files[0]
	}
	full, ok := safeThemeEditPath(theme.EditableDir, rel)
	if !ok || !editableThemeExt(rel) {
		http.Error(w, "invalid theme file", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		body, err := os.ReadFile(full)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.renderAdmin(w, r, "theme_files.html", map[string]any{"Title": "主题文件", "Theme": theme, "Files": files, "File": rel, "Body": string(body), "Saved": r.URL.Query().Get("saved") == "1"})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		backup := full + "." + time.Now().Format("20060102150405") + ".bak"
		if old, err := os.ReadFile(full); err == nil {
			_ = os.WriteFile(backup, old, 0o644)
		}
		if err := os.WriteFile(full, []byte(r.FormValue("body")), 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, r.URL.Path+"?file="+neturl.QueryEscape(rel), http.StatusSeeOther, flashNotice{Type: "success", Message: "主题文件已保存。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

type schemaFormConfig struct {
	Title     string
	Template  string
	BackURL   string
	OptionKey string
	UserID    int64
	Schema    []plugin.FieldSchema
	SavedURL  string
	Saved     bool
	UploadURL string
}

func (a *App) schemaForm(w http.ResponseWriter, r *http.Request, cfg schemaFormConfig) {
	switch r.Method {
	case http.MethodGet:
		values, err := a.optionJSONForUser(r.Context(), cfg.OptionKey, cfg.UserID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.applySchemaDefaults(cfg.Schema, values)
		normalizeSchemaValues(cfg.Schema, values)
		uploadURL := cfg.UploadURL
		if uploadURL == "" {
			uploadURL = "/admin/schema/upload"
		}
		a.renderAdmin(w, r, cfg.Template, map[string]any{"Title": cfg.Title, "BackURL": cfg.BackURL, "Schema": cfg.Schema, "Values": values, "Saved": cfg.Saved, "UploadURL": uploadURL})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		values := valuesFromSchema(r, cfg.Schema)
		if err := a.setOptionJSONForUser(r.Context(), cfg.OptionKey, values, cfg.UserID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, cfg.SavedURL, http.StatusSeeOther, flashNotice{Type: "success", Message: "设置已保存。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminAutosave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !a.requireRole(w, r, "contributor") {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	typ := r.FormValue("type")
	if typ == "" {
		typ = models.ContentTypePost
	}
	if typ != models.ContentTypePost && typ != models.ContentTypePage {
		http.Error(w, "unsupported content type", http.StatusBadRequest)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("cid"), 10, 64)
	if id > 0 && !a.canEditContent(w, r, id, typ) {
		return
	}
	if typ == models.ContentTypePage && !a.requireRole(w, r, "editor") {
		return
	}
	input, err := parseContentForm(r, typ)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(input.Title) == "" {
		input.Title = "自动保存草稿"
	}
	input.Status = models.ContentStatusDraft
	user, _ := a.currentUser(r)

	responseID := id
	previewID := id
	if id > 0 {
		existing, existErr := a.Contents.ByID(r.Context(), id)
		if existErr == nil && existing.DraftOf > 0 {
			var draftID int64
			draftID, err = a.Contents.SaveEditingDraft(r.Context(), existing.DraftOf, input, user.UID)
			responseID = existing.DraftOf
			previewID = draftID
		} else if existErr == nil && existing.Status == models.ContentStatusPost && existing.DraftOf == 0 {
			var draftID int64
			draftID, err = a.Contents.SaveEditingDraft(r.Context(), id, input, user.UID)
			responseID = id
			previewID = draftID
		} else if existErr == nil {
			err = a.Contents.Update(r.Context(), id, input)
			responseID = id
			previewID = id
		} else {
			err = existErr
		}
	} else {
		id, err = a.Contents.Create(r.Context(), input, user.UID)
		responseID = id
		previewID = id
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	item, _ := a.Contents.ByID(r.Context(), previewID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "cid": responseID, "preview": a.previewURL(r, item)})
}

func (a *App) adminTagSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !a.requireRole(w, r, "contributor") {
		return
	}
	keywords := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	tags, err := a.Metas.List(r.Context(), "tag")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type tagResult struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	out := make([]tagResult, 0, 10)
	for _, tag := range tags {
		if keywords == "" || strings.Contains(strings.ToLower(tag.Name), keywords) || strings.Contains(strings.ToLower(tag.Slug), keywords) {
			out = append(out, tagResult{Name: tag.Name, Slug: tag.Slug})
			if len(out) >= 10 {
				break
			}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}

func (a *App) adminAjaxPreferences(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !a.requireRole(w, r, "contributor") {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, ok := a.currentUser(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	allowed := map[string]bool{
		"editor_mode":       true,
		"editor_fullscreen": true,
		"editor_split":      true,
		"content_form_tab":  true,
	}
	for key, values := range r.Form {
		if strings.HasPrefix(key, "_") || !allowed[key] || len(values) == 0 {
			continue
		}
		if err := a.Options.SetForUser(r.Context(), "pref_"+key, values[len(values)-1], user.UID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (a *App) adminAjaxRemoteCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
		return
	}
	if !a.requireRole(w, r, "administrator") {
		return
	}
	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		rawURL = strings.TrimSpace(r.URL.Query().Get("url"))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if rawURL == "" {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "url is required"})
		return
	}
	body, err := a.fetchExternalText(r.Context(), rawURL)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "callback unavailable"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "bytes": len(body)})
}

func (a *App) adminSchemaUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !a.requireRole(w, r, "contributor") {
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "请选择要上传的文件", http.StatusBadRequest)
		return
	}
	defer file.Close()
	meta, err := a.saveUpload(r.Context(), file, header.Filename, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, _ := a.currentUser(r)
	text, _ := json.Marshal(meta)
	if _, err := a.Contents.CreateAttachmentMeta(r.Context(), meta.Name, strings.TrimSuffix(filepath.Base(meta.Name), filepath.Ext(meta.Name)), string(text), user.UID, 0); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": meta.URL})
}

func (a *App) adminManagementUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !a.requireRole(w, r, "administrator") {
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "请选择要上传的文件", http.StatusBadRequest)
		return
	}
	defer file.Close()
	url, err := a.saveAdminSettingUpload(r.Context(), file, header.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": url})
}

func (a *App) adminMedias(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "contributor") {
		return
	}
	user, _ := a.currentUser(r)
	switch r.Method {
	case http.MethodGet:
		query := services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Limit: 200}
		postQuery := services.ContentQuery{Type: models.ContentTypePost, Status: "all", Limit: 200}
		if roleRank(user.Role) < roleRank("editor") {
			query.AuthorID = user.UID
			postQuery.AuthorID = user.UID
		}
		medias, err := a.Contents.List(r.Context(), query)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		posts, _ := a.Contents.List(r.Context(), postQuery)
		pages, _ := a.Contents.List(r.Context(), services.ContentQuery{Type: models.ContentTypePage, Status: "all", Limit: 200})
		users, _ := a.Users.List(r.Context(), "")
		views := a.mediaViews(medias, posts, pages, users)
		views = filterMediaViews(views, r.URL.Query().Get("kind"), r.URL.Query().Get("author"), r.URL.Query().Get("keywords"))
		a.renderAdmin(w, r, "medias.html", map[string]any{"Title": "附件", "Medias": views, "Posts": posts, "Saved": r.URL.Query().Get("saved") == "1", "Kind": r.URL.Query().Get("kind"), "Author": r.URL.Query().Get("author"), "Keywords": r.URL.Query().Get("keywords"), "Users": users})
	case http.MethodPost:
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		parent, ok := a.validateAttachmentParent(w, r, user, r.FormValue("cid"))
		if !ok {
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "请选择要上传的文件", http.StatusBadRequest)
			return
		}
		defer file.Close()
		meta, err := a.saveUpload(r.Context(), file, header.Filename, parent)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		text, _ := json.Marshal(meta)
		id, err := a.Contents.CreateAttachmentMeta(r.Context(), meta.Name, strings.TrimSuffix(filepath.Base(meta.Name), filepath.Ext(meta.Name)), string(text), user.UID, parent)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if wantsJSON(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "cid": id, "url": meta.URL, "markdown": attachmentMarkdown(meta)})
			return
		}
		a.flashRedirect(w, r, "/admin/medias", http.StatusSeeOther, flashNotice{Type: "success", Message: "附件已上传。"})
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminMediaRoutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "contributor") {
		return
	}
	clean := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/medias/"), "/")
	parts := strings.Split(clean, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	item, err := a.Contents.ByID(r.Context(), id)
	if err != nil || item.Type != models.ContentTypeAttach {
		http.NotFound(w, r)
		return
	}
	user, _ := a.currentUser(r)
	if roleRank(user.Role) < roleRank("editor") && item.AuthorID != user.UID {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	switch parts[1] {
	case "delete":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		meta := parseAttachmentMeta(item)
		payload := plugin.AttachmentPayload{Content: item, Meta: meta}
		if _, err := a.Plugins.ApplyActive(r.Context(), plugin.HookAttachmentBeforeDelete, payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := a.Contents.Delete(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.removeAttachmentFile(meta)
		_, _ = a.Plugins.ApplyActive(r.Context(), plugin.HookAttachmentAfterDelete, payload)
		a.flashRedirect(w, r, "/admin/medias", http.StatusSeeOther, flashNotice{Type: "success", Message: "附件已删除。"})
	case "replace":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "请选择要替换的文件", http.StatusBadRequest)
			return
		}
		defer file.Close()
		meta, err := a.replaceUpload(r.Context(), file, header.Filename, item.Parent, parseAttachmentMeta(item))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		text, _ := json.Marshal(meta)
		if err := a.Contents.UpdateAttachmentMeta(r.Context(), item.CID, meta.Name, strings.TrimSuffix(filepath.Base(meta.Name), filepath.Ext(meta.Name)), string(text), item.Parent); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.flashRedirect(w, r, "/admin/medias", http.StatusSeeOther, flashNotice{Type: "success", Message: "附件已替换。"})
	default:
		http.NotFound(w, r)
	}
}

func (a *App) removeAttachmentFile(meta models.AttachmentMeta) {
	rel := meta.Path
	if rel == "" && strings.HasPrefix(meta.URL, "/uploads/") {
		rel = strings.TrimPrefix(meta.URL, "/uploads/")
	}
	if rel == "" {
		return
	}
	_ = os.Remove(filepath.Join(a.UploadDir, filepath.FromSlash(rel)))
}

func (a *App) deleteContentWithAttachmentPolicy(ctx context.Context, cid int64) error {
	item, err := a.Contents.ByID(ctx, cid)
	if err != nil {
		return err
	}
	policy := a.option(ctx, "attachment_delete_policy", "keep")
	var attachments []models.Content
	if item.Type == models.ContentTypePost || item.Type == models.ContentTypePage {
		attachments, _ = a.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Parent: cid, Limit: 10000})
	}
	if policy == "record" || policy == "file" {
		for _, attachment := range attachments {
			meta := parseAttachmentMeta(attachment)
			payload := plugin.AttachmentPayload{Content: attachment, Meta: meta}
			if _, err := a.Plugins.ApplyActive(ctx, plugin.HookAttachmentBeforeDelete, payload); err != nil {
				return err
			}
			if err := a.Contents.Delete(ctx, attachment.CID); err != nil {
				return err
			}
			if policy == "file" {
				a.removeAttachmentFile(meta)
			}
			_, _ = a.Plugins.ApplyActive(ctx, plugin.HookAttachmentAfterDelete, payload)
		}
	}
	if err := a.Contents.Delete(ctx, cid); err != nil {
		return err
	}
	return nil
}

func (a *App) validateAttachmentParent(w http.ResponseWriter, r *http.Request, user models.User, rawCID string) (int64, bool) {
	cid, err := strconv.ParseInt(strings.TrimSpace(rawCID), 10, 64)
	if err != nil || cid <= 0 {
		if roleRank(user.Role) >= roleRank("editor") {
			return 0, true
		}
		http.Error(w, "contributor uploads must be attached to one of their posts", http.StatusForbidden)
		return 0, false
	}
	parent, err := a.Contents.ByID(r.Context(), cid)
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	if parent.Type != models.ContentTypePost && parent.Type != models.ContentTypePage {
		http.Error(w, "attachments must target a post or page", http.StatusBadRequest)
		return 0, false
	}
	if roleRank(user.Role) < roleRank("editor") {
		if parent.Type != models.ContentTypePost || parent.AuthorID != user.UID {
			http.Error(w, "permission denied", http.StatusForbidden)
			return 0, false
		}
	}
	return cid, true
}

func (a *App) adminBackup(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.renderAdmin(w, r, "backup.html", map[string]any{"Title": "备份", "Imported": r.URL.Query().Get("imported") == "1"})
	case http.MethodPost:
		switch r.FormValue("action") {
		case "export":
			payload, err := a.backupPayload(r.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="goblog-backup.json"`)
			_ = json.NewEncoder(w).Encode(payload)
		case "import":
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			file, _, err := r.FormFile("backup")
			if err != nil {
				http.Error(w, "请选择备份文件", http.StatusBadRequest)
				return
			}
			defer file.Close()
			var payload backupData
			if err := json.NewDecoder(io.LimitReader(file, 64<<20)).Decode(&payload); err != nil {
				http.Error(w, "备份 JSON 格式不正确", http.StatusBadRequest)
				return
			}
			if r.FormValue("dry_run") == "1" {
				plan, err := a.backupPlan(r.Context(), payload, importSections(r))
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				a.renderAdmin(w, r, "backup.html", map[string]any{"Title": "备份", "ImportPlan": plan})
				return
			}
			if err := a.importBackupPayload(r.Context(), payload, importSections(r)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			a.flashRedirect(w, r, "/admin/backup", http.StatusSeeOther, flashNotice{Type: "success", Message: "备份已导入。"})
		default:
			http.Error(w, "unsupported backup action", http.StatusBadRequest)
		}
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminPlaceholder(title, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.renderAdmin(w, r, "placeholder.html", map[string]any{"Title": title, "Message": message})
	}
}

func (a *App) frontIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if a.option(r.Context(), "front_page_type", "posts") == "page" {
		cid, _ := strconv.ParseInt(a.option(r.Context(), "front_page_cid", "0"), 10, 64)
		if cid > 0 {
			pageData, err := a.Contents.ByID(r.Context(), cid)
			if err == nil && pageData.Type == models.ContentTypePage && pageData.Status == models.ContentStatusPost {
				a.renderPageContent(w, r, pageData, map[string]any{"CanonicalPath": "/"})
				return
			}
		}
	}
	a.renderPostList(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost}, "")
}

func (a *App) frontDynamic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		a.frontIndex(w, r)
		return
	}
	if a.postsIndexPath(r.Context()) != "/" && trimSlashPath(r.URL.Path) == trimSlashPath(a.postsIndexPath(r.Context())) {
		a.renderPostList(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost}, "")
		return
	}
	if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/feed.xml") && a.tryDynamicTaxonomyFeed(w, r) {
		return
	}
	if a.tryDynamicPermalink(w, r) {
		return
	}
	if a.tryPrettyArchive(w, r) {
		return
	}
	http.NotFound(w, r)
}

func (a *App) frontPost(w http.ResponseWriter, r *http.Request) {
	postSlug := path.Base(strings.TrimSuffix(r.URL.Path, "/"))
	postSlug = strings.TrimSuffix(postSlug, ".html")
	post, err := a.Contents.BySlug(r.Context(), postSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.renderThemeStatus(w, r, "404.html", map[string]any{}, http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if post.Password != "" && r.URL.Query().Get("password") != post.Password {
		a.renderTheme(w, r, "post.html", map[string]any{"Post": post, "PasswordRequired": true})
		return
	}
	if a.redirectCanonical(w, r, a.contentURL(r.Context(), post)) {
		return
	}
	a.renderPostContent(w, r, post)
}

func (a *App) renderPostContent(w http.ResponseWriter, r *http.Request, post models.Content) {
	comments, commentPager, _ := a.commentsForPost(r, post)
	categories, _ := a.Metas.CategoriesForContent(r.Context(), post.CID)
	tags, _ := a.Metas.TagsForContent(r.Context(), post.CID)
	fields, _ := a.Contents.FieldMap(r.Context(), post.CID)
	prev, next, _ := a.Contents.Adjacent(r.Context(), post)
	related, _ := a.relatedPosts(r.Context(), post, categories, tags, 5)
	contentHTML, err := a.renderContentHTML(r.Context(), post, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderTheme(w, r, "post.html", map[string]any{
		"Post":          post,
		"ContentHTML":   contentHTML,
		"Comments":      comments,
		"CommentPager":  commentPager,
		"ReplyTo":       r.URL.Query().Get("reply"),
		"Categories":    categories,
		"Tags":          tags,
		"Fields":        fields,
		"PrevPost":      prev,
		"NextPost":      next,
		"RelatedPosts":  related,
		"CommentError":  r.URL.Query().Get("comment_error"),
		"CommentOK":     r.URL.Query().Get("comment_ok") == "1",
		"CanonicalPath": a.contentURL(r.Context(), post),
	})
}

func (a *App) frontPage(w http.ResponseWriter, r *http.Request) {
	pageSlug := path.Base(strings.TrimSuffix(r.URL.Path, "/"))
	pageSlug = strings.TrimSuffix(pageSlug, ".html")
	pageData, err := a.Contents.PageBySlug(r.Context(), pageSlug)
	if err != nil {
		a.renderThemeStatus(w, r, "404.html", map[string]any{}, http.StatusNotFound)
		return
	}
	if a.redirectCanonical(w, r, a.contentURL(r.Context(), pageData)) {
		return
	}
	a.renderPageContent(w, r, pageData)
}

func (a *App) renderPageContent(w http.ResponseWriter, r *http.Request, pageData models.Content, extra ...map[string]any) {
	comments, commentPager, _ := a.commentsForPost(r, pageData)
	fields, _ := a.Contents.FieldMap(r.Context(), pageData.CID)
	contentHTML, err := a.renderContentHTML(r.Context(), pageData, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Post":          pageData,
		"ContentHTML":   contentHTML,
		"Comments":      comments,
		"CommentPager":  commentPager,
		"ReplyTo":       r.URL.Query().Get("reply"),
		"Fields":        fields,
		"PrevPost":      models.Content{},
		"NextPost":      models.Content{},
		"CommentError":  r.URL.Query().Get("comment_error"),
		"CommentOK":     r.URL.Query().Get("comment_ok") == "1",
		"CanonicalPath": a.contentURL(r.Context(), pageData),
	}
	for _, values := range extra {
		for key, value := range values {
			data[key] = value
		}
	}
	a.renderTheme(w, r, "post.html", data)
}

func (a *App) frontPreview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(path.Base(strings.TrimSuffix(r.URL.Path, "/")), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	item, err := a.Contents.ByID(r.Context(), id)
	if err != nil || (item.Type != models.ContentTypePost && item.Type != models.ContentTypePage) {
		http.NotFound(w, r)
		return
	}
	if !a.validPreviewToken(r, item) {
		http.Error(w, "invalid preview token", http.StatusForbidden)
		return
	}
	fields, _ := a.Contents.FieldMap(r.Context(), item.CID)
	contentHTML, err := a.renderContentHTML(r.Context(), item, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderTheme(w, r, "post.html", map[string]any{
		"Post":         item,
		"ContentHTML":  contentHTML,
		"Fields":       fields,
		"Comments":     []commentView{},
		"CommentPager": commentPagination{},
		"ReplyTo":      "",
		"Categories":   []models.Meta{},
		"Tags":         []models.Meta{},
		"PrevPost":     models.Content{},
		"NextPost":     models.Content{},
		"Preview":      true,
	})
}

func (a *App) frontCategory(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/feed.xml") {
		a.frontTaxonomyRSS(w, r, "category")
		return
	}
	meta, err := a.Metas.BySlug(r.Context(), "category", path.Base(strings.TrimSuffix(r.URL.Path, "/")))
	if err != nil {
		a.renderThemeStatus(w, r, "404.html", map[string]any{}, http.StatusNotFound)
		return
	}
	canonical := a.metaURL(r.Context(), meta)
	if a.redirectCanonical(w, r, canonical) {
		return
	}
	a.renderPostListWithData(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Category: meta.MID}, "分类："+meta.Name, map[string]any{"ArchiveMeta": meta, "CanonicalPath": canonical, "FeedPath": canonical + "/feed.xml"})
}

func (a *App) frontTag(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/feed.xml") {
		a.frontTaxonomyRSS(w, r, "tag")
		return
	}
	meta, err := a.Metas.BySlug(r.Context(), "tag", path.Base(strings.TrimSuffix(r.URL.Path, "/")))
	if err != nil {
		a.renderThemeStatus(w, r, "404.html", map[string]any{}, http.StatusNotFound)
		return
	}
	canonical := "/tag/" + meta.Slug
	if a.redirectCanonical(w, r, canonical) {
		return
	}
	a.renderPostListWithData(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Tag: meta.MID}, "标签："+meta.Name, map[string]any{"ArchiveMeta": meta, "CanonicalPath": canonical, "FeedPath": canonical + "/feed.xml"})
}

func (a *App) frontAuthor(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(path.Base(strings.TrimSuffix(r.URL.Path, "/")), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	user, err := a.Users.ByID(r.Context(), id)
	if err != nil {
		a.renderThemeStatus(w, r, "404.html", map[string]any{}, http.StatusNotFound)
		return
	}
	name := user.ScreenName
	if name == "" {
		name = user.Name
	}
	canonical := "/author/" + strconv.FormatInt(user.UID, 10)
	if a.redirectCanonical(w, r, canonical) {
		return
	}
	a.renderPostListWithData(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, AuthorID: id}, "作者："+name, map[string]any{"CanonicalPath": canonical})
}

func (a *App) frontSearch(w http.ResponseWriter, r *http.Request) {
	keywords := strings.TrimSpace(r.URL.Query().Get("q"))
	if keywords != "" && strings.TrimRight(r.URL.Path, "/") == "/search" {
		http.Redirect(w, r, searchPath(keywords), http.StatusMovedPermanently)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/search/") {
		keywords, _ = neturl.PathUnescape(strings.Trim(strings.TrimPrefix(r.URL.Path, "/search/"), "/"))
	}
	a.renderPostListWithData(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Keywords: keywords}, "搜索："+keywords, map[string]any{"Keywords": keywords, "CanonicalPath": searchPath(keywords)})
}

func (a *App) frontArchive(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/archive/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil || year < 1970 {
		http.NotFound(w, r)
		return
	}
	query := services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Year: year}
	title := fmt.Sprintf("归档：%04d", year)
	if len(parts) > 1 {
		query.Month, _ = strconv.Atoi(parts[1])
		title = fmt.Sprintf("归档：%04d-%02d", year, query.Month)
	}
	if len(parts) > 2 {
		query.Day, _ = strconv.Atoi(parts[2])
		title = fmt.Sprintf("归档：%04d-%02d-%02d", year, query.Month, query.Day)
	}
	a.renderPostListWithData(w, r, query, title, map[string]any{"CanonicalPath": archivePath(query.Year, query.Month, query.Day)})
}

func (a *App) frontComment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cid, _ := strconv.ParseInt(r.FormValue("cid"), 10, 64)
	post, err := a.Contents.ByID(r.Context(), cid)
	if err != nil || (post.Type != models.ContentTypePost && post.Type != models.ContentTypePage) || post.Status != models.ContentStatusPost {
		http.NotFound(w, r)
		return
	}
	redirectTo := a.contentURL(r.Context(), post)
	if optionBool(a.option(r.Context(), "comments_check_referer", "1")) && !a.validCommentReferer(r, redirectTo) {
		http.Redirect(w, r, redirectTo+"?comment_error=referer", http.StatusSeeOther)
		return
	}
	if post.AllowComment != "1" {
		http.Redirect(w, r, redirectTo+"?comment_error=closed", http.StatusSeeOther)
		return
	}
	if closeDays := optionInt(a.option(r.Context(), "comments_auto_close", "0"), 0); closeDays > 0 && time.Since(time.Unix(post.Created, 0)) > time.Duration(closeDays)*24*time.Hour {
		http.Redirect(w, r, redirectTo+"?comment_error=closed", http.StatusSeeOther)
		return
	}
	ip := clientIP(r)
	if optionBool(a.option(r.Context(), "comments_antispam", "1")) {
		if matchList(ip, a.option(r.Context(), "comments_ip_blacklist", "")) {
			http.Redirect(w, r, redirectTo+"?comment_error=blocked", http.StatusSeeOther)
			return
		}
		if optionBool(a.option(r.Context(), "comments_post_interval_enable", "1")) {
			interval := optionInt(a.option(r.Context(), "comments_post_interval", "30"), 30)
			recent, _ := a.Comments.CountRecentByIP(r.Context(), ip, time.Now().Add(-time.Duration(interval)*time.Second).Unix())
			if recent > 0 {
				http.Redirect(w, r, redirectTo+"?comment_error=frequent", http.StatusSeeOther)
				return
			}
		}
	}
	if r.FormValue("website") != "" {
		http.Redirect(w, r, redirectTo+"?comment_error=spam", http.StatusSeeOther)
		return
	}
	author := strings.TrimSpace(r.FormValue("author"))
	mail := strings.TrimSpace(r.FormValue("mail"))
	text := strings.TrimSpace(r.FormValue("text"))
	urlValue := normalizeCommentURL(r.FormValue("url"))
	user, loggedIn := a.currentUser(r)
	if loggedIn {
		author = user.ScreenName
		if author == "" {
			author = user.Name
		}
		mail = user.Mail
		urlValue = normalizeCommentURL(user.URL)
	}
	if author == "" || text == "" || (optionBool(a.option(r.Context(), "comments_require_mail", "1")) && mail == "") || (optionBool(a.option(r.Context(), "comments_require_url", "0")) && urlValue == "") {
		http.Redirect(w, r, redirectTo+"?comment_error=required", http.StatusSeeOther)
		return
	}
	if !loggedIn && a.nameReserved(r.Context(), author) {
		http.Redirect(w, r, redirectTo+"?comment_error=reserved", http.StatusSeeOther)
		return
	}
	status := "approved"
	if optionBool(a.option(r.Context(), "comments_require_moderation", "0")) && !loggedIn {
		status = "waiting"
	}
	if optionBool(a.option(r.Context(), "comments_whitelist", "0")) && !loggedIn {
		approved, _ := a.Comments.HasApprovedAuthor(r.Context(), author, mail)
		if approved {
			status = "approved"
		} else {
			status = "waiting"
		}
	}
	if loggedIn && roleRank(user.Role) >= roleRank("contributor") {
		status = "approved"
	}
	if optionBool(a.option(r.Context(), "comments_antispam", "1")) && containsListItem(text, a.option(r.Context(), "comments_stop_words", "")) {
		status = "spam"
	}
	parent, _ := strconv.ParseInt(r.FormValue("parent"), 10, 64)
	if parent > 0 {
		depth, err := a.Comments.ParentDepth(r.Context(), cid, parent)
		if err != nil {
			http.Redirect(w, r, redirectTo+"?comment_error=parent", http.StatusSeeOther)
			return
		}
		maxDepth := optionInt(a.option(r.Context(), "comments_max_nesting_levels", "3"), 3)
		if maxDepth > 0 && depth >= maxDepth {
			http.Redirect(w, r, redirectTo+"?comment_error=depth", http.StatusSeeOther)
			return
		}
	}
	authorID := int64(0)
	if loggedIn {
		authorID = user.UID
	}
	input := services.SaveCommentInput{CID: cid, Author: author, AuthorID: authorID, OwnerID: post.AuthorID, Mail: mail, URL: urlValue, Text: text, Status: status, Parent: parent, IP: ip, Agent: r.UserAgent()}
	if errs := validatePublicCommentInput(input, optionBool(a.option(r.Context(), "comments_require_mail", "1")), optionBool(a.option(r.Context(), "comments_require_url", "0"))); !errs.Empty() {
		http.Redirect(w, r, redirectTo+"?comment_error=invalid", http.StatusSeeOther)
		return
	}
	commentPayload := plugin.CommentSavePayload{Input: input, Content: post}
	if payload, err := a.Plugins.ApplyActive(r.Context(), plugin.HookCommentBeforeSave, commentPayload); err != nil {
		http.Redirect(w, r, redirectTo+"?comment_error=blocked", http.StatusSeeOther)
		return
	} else if next, ok := payload.(plugin.CommentSavePayload); ok {
		commentPayload = next
		if nextInput, ok := next.Input.(services.SaveCommentInput); ok {
			input = nextInput
		}
	}
	if err := a.Comments.Save(r.Context(), input, 0); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = a.Plugins.ApplyActive(r.Context(), plugin.HookCommentAfterSave, commentPayload)
	cookies := a.cookieOptions(r.Context())
	http.SetCookie(w, &http.Cookie{Name: cookies.Name("comment_author"), Value: author, Path: "/", MaxAge: 86400 * 365, HttpOnly: true, SameSite: cookies.SameSite, Secure: cookies.Secure})
	http.SetCookie(w, &http.Cookie{Name: cookies.Name("comment_mail"), Value: mail, Path: "/", MaxAge: 86400 * 365, HttpOnly: true, SameSite: cookies.SameSite, Secure: cookies.Secure})
	http.Redirect(w, r, redirectTo+"?comment_ok=1#comments", http.StatusSeeOther)
}

func (a *App) frontRSS(w http.ResponseWriter, r *http.Request) {
	posts, err := a.Contents.ListPublished(r.Context(), optionInt(a.option(r.Context(), "posts_list_size", "10"), 10), 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.writeRSS(w, r, posts, nil, a.option(r.Context(), "site_title", "GoBlog"), a.option(r.Context(), "site_description", ""), "/feed.xml")
}

func (a *App) frontTaxonomyRSS(w http.ResponseWriter, r *http.Request, typ string) {
	clean := strings.TrimSuffix(strings.Trim(r.URL.Path, "/"), "/feed.xml")
	slug := path.Base(clean)
	meta, err := a.Metas.BySlug(r.Context(), typ, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	query := services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, ExcludeFuture: true, Limit: optionInt(a.option(r.Context(), "posts_list_size", "10"), 10)}
	if typ == "category" {
		query.Category = meta.MID
	} else {
		query.Tag = meta.MID
	}
	posts, err := a.Contents.List(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.writeRSS(w, r, posts, nil, meta.Name, meta.Description, a.metaURL(r.Context(), meta)+"/feed.xml")
}

func (a *App) frontCommentRSS(w http.ResponseWriter, r *http.Request) {
	comments, err := a.Comments.List(r.Context(), "approved", "", 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.writeRSS(w, r, nil, comments, a.option(r.Context(), "site_title", "GoBlog")+" 评论", a.option(r.Context(), "site_description", ""), "/comments/feed.xml")
}

func (a *App) writeRSS(w http.ResponseWriter, r *http.Request, posts []models.Content, comments []models.Comment, title, description, feedPath string) {
	site, _ := a.Options.All(r.Context())
	baseURL := strings.TrimRight(site["base_url"], "/")
	items := make([]rssItem, 0, len(posts))
	for _, post := range posts {
		text := feedText(post.Text, site["feed_full_text"] == "1")
		link := baseURL + a.contentURL(r.Context(), post)
		items = append(items, rssItem{Title: post.Title, Link: link, GUID: link, PubDate: time.Unix(post.Created, 0).Format(time.RFC1123Z), Description: text})
	}
	for _, comment := range comments {
		link := baseURL + "#comment-" + strconv.FormatInt(comment.COID, 10)
		if content, err := a.Contents.ByID(r.Context(), comment.CID); err == nil && (content.Type == models.ContentTypePost || content.Type == models.ContentTypePage) {
			link = baseURL + a.contentURL(r.Context(), content) + "#comment-" + strconv.FormatInt(comment.COID, 10)
		}
		items = append(items, rssItem{Title: comment.Author + " 的评论", Link: link, GUID: link, PubDate: time.Unix(comment.Created, 0).Format(time.RFC1123Z), Description: render.Excerpt(comment.Text, 240)})
	}
	feed := rssFeed{Version: "2.0", Channel: rssChannel{Title: title, Link: baseURL + strings.TrimSuffix(feedPath, "/feed.xml"), Description: description, Items: items}}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(feed)
}

func (a *App) frontAtom(w http.ResponseWriter, r *http.Request) {
	posts, err := a.Contents.ListPublished(r.Context(), optionInt(a.option(r.Context(), "posts_list_size", "10"), 10), 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	site, _ := a.Options.All(r.Context())
	baseURL := strings.TrimRight(site["base_url"], "/")
	feed := atomFeed{Xmlns: "http://www.w3.org/2005/Atom", ID: baseURL + "/", Title: site["site_title"], Updated: time.Now().Format(time.RFC3339), Links: []atomLink{{Href: baseURL + "/atom.xml", Rel: "self"}, {Href: baseURL + "/", Rel: "alternate"}}}
	for _, post := range posts {
		link := baseURL + a.contentURL(r.Context(), post)
		feed.Entries = append(feed.Entries, atomEntry{ID: link, Title: post.Title, Link: atomLink{Href: link, Rel: "alternate"}, Updated: time.Unix(post.Modified, 0).Format(time.RFC3339), Published: time.Unix(post.Created, 0).Format(time.RFC3339), Content: atomContent{Type: "html", Body: feedText(post.Text, site["feed_full_text"] == "1")}})
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(feed)
}

func feedText(text string, full bool) string {
	if full {
		return strings.Replace(text, "<!--more-->", "", 1)
	}
	if i := strings.Index(text, "<!--more-->"); i >= 0 {
		return text[:i]
	}
	return render.Excerpt(text, 240)
}

func (a *App) renderPostList(w http.ResponseWriter, r *http.Request, query services.ContentQuery, title string) {
	a.renderPostListWithData(w, r, query, title, nil)
}

func (a *App) renderPostListWithData(w http.ResponseWriter, r *http.Request, query services.ContentQuery, title string, extra map[string]any) {
	page := optionInt(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	size := optionInt(a.option(r.Context(), "page_size", "10"), 10)
	if size < 1 {
		size = 10
	}
	query.Limit = size
	query.Offset = (page - 1) * size
	query.ExcludeFuture = true
	total, err := a.Contents.CountList(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	posts, err := a.Contents.List(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	totalPages := int((total + int64(size) - 1) / int64(size))
	data := map[string]any{
		"Title":        title,
		"ArchiveTitle": title,
		"Posts":        posts,
		"Keywords":     query.Keywords,
		"Pagination": pagination{
			Page:       page,
			PageSize:   size,
			Total:      total,
			TotalPages: totalPages,
			PrevURL:    pageURL(r, page-1),
			NextURL:    pageURL(r, page+1),
			HasPrev:    page > 1,
			HasNext:    totalPages > page,
		},
	}
	for key, value := range extra {
		data[key] = value
	}
	a.renderTheme(w, r, "index.html", data)
}

func (a *App) commentsForPost(r *http.Request, post models.Content) ([]commentView, commentPagination, error) {
	pageSize := optionInt(a.option(r.Context(), "comments_page_size", "20"), 20)
	if pageSize <= 0 {
		pageSize = 20
	}
	order := a.option(r.Context(), "comments_order", "ASC")
	comments, err := a.Comments.ListForContent(r.Context(), post.CID, order, 0, 0)
	if err != nil {
		return nil, commentPagination{}, err
	}
	roots := topLevelComments(comments)
	total := int64(len(roots))
	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages < 1 {
		totalPages = 1
	}
	page := optionInt(r.URL.Query().Get("comments_page"), 0)
	if page <= 0 {
		if a.option(r.Context(), "comments_page_display", "last") == "first" {
			page = 1
		} else {
			page = totalPages
		}
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(roots) {
		start = len(roots)
	}
	if end > len(roots) {
		end = len(roots)
	}
	maxLevel := optionInt(a.option(r.Context(), "comments_max_nesting_levels", "3"), 3)
	views := a.commentViews(r, comments, roots[start:end], maxLevel)
	pager := commentPagination{
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages,
		PrevURL:    commentPageURL(r, page-1),
		NextURL:    commentPageURL(r, page+1),
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
	}
	return views, pager, nil
}

func (a *App) commentViews(r *http.Request, comments []models.Comment, roots []models.Comment, maxLevel int) []commentView {
	children := make(map[int64][]models.Comment)
	byID := make(map[int64]models.Comment)
	for _, comment := range comments {
		byID[comment.COID] = comment
		if comment.Parent > 0 {
			children[comment.Parent] = append(children[comment.Parent], comment)
		}
	}
	var out []commentView
	var walk func(parent int64, level int)
	walk = func(parent int64, level int) {
		for _, comment := range children[parent] {
			displayLevel := level
			if maxLevel > 0 && displayLevel >= maxLevel {
				displayLevel = maxLevel - 1
			}
			out = append(out, a.commentView(r, comment, displayLevel))
			if _, ok := byID[comment.COID]; ok {
				walk(comment.COID, level+1)
			}
		}
	}
	for _, root := range roots {
		out = append(out, a.commentView(r, root, 0))
		walk(root.COID, 1)
	}
	return out
}

func (a *App) commentView(r *http.Request, comment models.Comment, level int) commentView {
	return commentView{
		Comment:    comment,
		Level:      level,
		BodyHTML:   a.renderCommentText(r, comment.Text),
		AuthorHTML: a.commentAuthorHTML(r, comment),
		AvatarURL:  a.gravatarURL(r, comment.Mail),
		ReplyURL:   commentReplyURL(r, comment.COID),
		Anchor:     fmt.Sprintf("comment-%d", comment.COID),
	}
}

func (a *App) relatedPosts(ctx context.Context, post models.Content, categories, tags []models.Meta, limit int) ([]models.Content, error) {
	if limit <= 0 {
		limit = 5
	}
	seen := map[int64]bool{post.CID: true}
	out := make([]models.Content, 0, limit)
	for _, tag := range tags {
		items, err := a.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Tag: tag.MID, ExcludeFuture: true, Limit: limit})
		if err != nil {
			return out, err
		}
		for _, item := range items {
			if !seen[item.CID] {
				seen[item.CID] = true
				out = append(out, item)
				if len(out) >= limit {
					return out, nil
				}
			}
		}
	}
	for _, category := range categories {
		items, err := a.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Category: category.MID, ExcludeFuture: true, Limit: limit})
		if err != nil {
			return out, err
		}
		for _, item := range items {
			if !seen[item.CID] {
				seen[item.CID] = true
				out = append(out, item)
				if len(out) >= limit {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

func (a *App) mediaViews(items, posts, pages []models.Content, users []models.User) []mediaView {
	parents := map[int64]string{}
	for _, post := range posts {
		parents[post.CID] = post.Title
	}
	for _, page := range pages {
		parents[page.CID] = page.Title
	}
	authors := map[int64]string{}
	for _, user := range users {
		name := user.ScreenName
		if name == "" {
			name = user.Name
		}
		authors[user.UID] = name
	}
	out := make([]mediaView, 0, len(items))
	for _, item := range items {
		meta := parseAttachmentMeta(item)
		view := mediaView{
			Content:     item,
			Meta:        meta,
			Name:        meta.Name,
			URL:         meta.URL,
			Kind:        mediaKind(meta),
			MIME:        meta.MIME,
			SizeLabel:   formatBytes(meta.Size),
			AuthorName:  authors[item.AuthorID],
			ParentTitle: parents[item.Parent],
			Markdown:    attachmentMarkdown(meta),
		}
		if view.Name == "" {
			view.Name = item.Title
		}
		if view.URL == "" {
			view.URL = item.Text
		}
		out = append(out, view)
	}
	return out
}

func (a *App) editorMediaLibrary(r *http.Request) ([]mediaView, error) {
	user, _ := a.currentUser(r)
	query := services.ContentQuery{Type: models.ContentTypeAttach, Status: "all", Limit: 50}
	if roleRank(user.Role) < roleRank("editor") {
		query.AuthorID = user.UID
	}
	items, err := a.Contents.List(r.Context(), query)
	if err != nil {
		return nil, err
	}
	posts, _ := a.Contents.List(r.Context(), services.ContentQuery{Type: models.ContentTypePost, Status: "all", Limit: 200})
	pages, _ := a.Contents.List(r.Context(), services.ContentQuery{Type: models.ContentTypePage, Status: "all", Limit: 200})
	users, _ := a.Users.List(r.Context(), "")
	return a.mediaViews(items, posts, pages, users), nil
}

func filterMediaViews(items []mediaView, kind, author, keywords string) []mediaView {
	kind = strings.TrimSpace(kind)
	authorID, _ := strconv.ParseInt(author, 10, 64)
	keywords = strings.ToLower(strings.TrimSpace(keywords))
	if kind == "" && authorID <= 0 && keywords == "" {
		return items
	}
	out := make([]mediaView, 0, len(items))
	for _, item := range items {
		if kind != "" && item.Kind != kind {
			continue
		}
		if authorID > 0 && item.AuthorID != authorID {
			continue
		}
		if keywords != "" {
			haystack := strings.ToLower(item.Name + " " + item.URL + " " + item.ParentTitle)
			if !strings.Contains(haystack, keywords) {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

func parseAttachmentMeta(item models.Content) models.AttachmentMeta {
	var meta models.AttachmentMeta
	text := strings.TrimSpace(item.Text)
	if strings.HasPrefix(text, "{") {
		if err := json.Unmarshal([]byte(text), &meta); err == nil {
			if meta.Name == "" {
				meta.Name = item.Title
			}
			return meta
		}
	}
	if text != "" {
		meta.URL = text
		meta.Path = strings.TrimPrefix(text, "/uploads/")
		meta.Name = item.Title
		meta.Type = strings.TrimPrefix(filepath.Ext(text), ".")
		meta.IsImage = imageExt(meta.Type)
	}
	return meta
}

func mediaKind(meta models.AttachmentMeta) string {
	if meta.IsImage || strings.HasPrefix(meta.MIME, "image/") {
		return "image"
	}
	if meta.Type == "pdf" {
		return "document"
	}
	if meta.Type == "zip" {
		return "archive"
	}
	return "file"
}

func attachmentMarkdown(meta models.AttachmentMeta) string {
	alt := meta.Name
	if alt == "" {
		alt = "attachment"
	}
	if meta.IsImage {
		return "![" + alt + "](" + meta.URL + ")"
	}
	return "[" + alt + "](" + meta.URL + ")"
}

func formatBytes(size int64) string {
	if size <= 0 {
		return "-"
	}
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(size)
	unit := units[0]
	for i := 1; i < len(units) && value >= 1024; i++ {
		value /= 1024
		unit = units[i]
	}
	if unit == "B" {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

func imageExt(ext string) bool {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "jpg", "jpeg", "png", "gif", "webp", "svg":
		return true
	default:
		return false
	}
}

func topLevelComments(comments []models.Comment) []models.Comment {
	byID := make(map[int64]bool, len(comments))
	for _, comment := range comments {
		byID[comment.COID] = true
	}
	roots := make([]models.Comment, 0, len(comments))
	for _, comment := range comments {
		if comment.Parent == 0 || !byID[comment.Parent] {
			roots = append(roots, comment)
		}
	}
	return roots
}

func (a *App) saveUpload(ctx context.Context, src io.Reader, original string, parent int64) (models.AttachmentMeta, error) {
	var meta models.AttachmentMeta
	maxSize := int64(optionInt(a.option(ctx, "upload_max_size", "10485760"), 10485760))
	if maxSize <= 0 {
		maxSize = 10 << 20
	}
	data, err := io.ReadAll(io.LimitReader(src, maxSize+1))
	if err != nil {
		return meta, err
	}
	if int64(len(data)) > maxSize {
		return meta, fmt.Errorf("文件超过大小限制")
	}
	name := sanitizeFilename(original)
	if name == "" {
		name = "file"
	}
	uploadPayload := plugin.UploadPayload{Name: name, ParentID: parent}
	if payload, err := a.Plugins.ApplyActive(ctx, plugin.HookUploadBeforeSave, uploadPayload); err != nil {
		return meta, err
	} else if next, ok := payload.(plugin.UploadPayload); ok {
		if strings.TrimSpace(next.Name) != "" {
			name = sanitizeFilename(next.Name)
		}
		parent = next.ParentID
	}
	if dangerousUpload(name) || !allowedUploadExt(name, a.option(ctx, "upload_allowed_exts", "")) {
		return meta, fmt.Errorf("不允许上传该文件类型")
	}
	mimeType := http.DetectContentType(data)
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if !mimeAllowedForExt(ext, mimeType) {
		return meta, fmt.Errorf("文件内容与扩展名不匹配")
	}
	bucket := "unattached"
	if parent > 0 {
		bucket = strconv.FormatInt(parent, 10)
	}
	dir := filepath.Join(a.UploadDir, bucket)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return meta, err
	}
	targetName := uniqueUploadName(dir, name)
	fullPath := filepath.Join(dir, targetName)
	dst, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return meta, err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, bytes.NewReader(data)); err != nil {
		return meta, err
	}
	relPath := path.Join(bucket, targetName)
	meta = models.AttachmentMeta{
		Name: name,
		Path: relPath,
		URL:  "/uploads/" + relPath,
		Size: int64(len(data)),
		Type: ext,
		MIME: mimeType,
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		meta.IsImage = true
		meta.Width = cfg.Width
		meta.Height = cfg.Height
	} else if strings.HasPrefix(mimeType, "image/") {
		meta.IsImage = true
	}
	uploadPayload.Name = name
	uploadPayload.ParentID = parent
	uploadPayload.Meta = meta
	if _, err := a.Plugins.ApplyActive(ctx, plugin.HookUploadAfterSave, uploadPayload); err != nil {
		return meta, err
	}
	return meta, nil
}

func (a *App) saveAdminSettingUpload(ctx context.Context, src io.Reader, original string) (string, error) {
	maxSize := int64(optionInt(a.option(ctx, "upload_max_size", "10485760"), 10485760))
	if maxSize <= 0 {
		maxSize = 10 << 20
	}
	data, err := io.ReadAll(io.LimitReader(src, maxSize+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxSize {
		return "", fmt.Errorf("文件超过大小限制")
	}
	name := sanitizeFilename(original)
	if name == "" {
		name = "admin-setting-image"
	}
	if dangerousUpload(name) || !allowedUploadExt(name, a.option(ctx, "upload_allowed_exts", "")) {
		return "", fmt.Errorf("不允许上传该文件类型")
	}
	mimeType := http.DetectContentType(data)
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if !adminSettingImageExt(ext) {
		return "", fmt.Errorf("后台设置仅支持图片文件")
	}
	if !mimeAllowedForExt(ext, mimeType) {
		return "", fmt.Errorf("文件内容与扩展名不匹配")
	}
	dir := filepath.Join(a.UploadDir, adminSettingsUploadBucket)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	targetName := uniqueUploadName(dir, name)
	fullPath := filepath.Join(dir, targetName)
	dst, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, bytes.NewReader(data)); err != nil {
		return "", err
	}
	return "/uploads/" + path.Join(adminSettingsUploadBucket, targetName), nil
}

func adminSettingImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case "jpg", "jpeg", "png", "gif", "webp", "svg":
		return true
	default:
		return false
	}
}

func (a *App) replaceUpload(ctx context.Context, src io.Reader, original string, parent int64, old models.AttachmentMeta) (models.AttachmentMeta, error) {
	if optionBool(a.option(ctx, "upload_replace_same_ext_only", "1")) && old.Type != "" {
		newExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(original), "."))
		if newExt != "" && !strings.EqualFold(newExt, old.Type) {
			return models.AttachmentMeta{}, fmt.Errorf("替换文件必须保持相同扩展名")
		}
	}
	meta, err := a.saveUpload(ctx, src, original, parent)
	if err != nil {
		return meta, err
	}
	if old.Path != "" {
		_ = os.Remove(filepath.Join(a.UploadDir, filepath.FromSlash(old.Path)))
	}
	return meta, nil
}

func (a *App) backupPayload(ctx context.Context) (backupData, error) {
	out := backupData{Version: 1, Generator: "goblog", Dialect: "portable-json"}
	out.GeneratedAt = time.Now().Format(time.RFC3339)
	options, err := a.Options.All(ctx)
	if err != nil {
		return out, err
	}
	out.Options = options
	users, err := a.Users.List(ctx, "")
	if err != nil {
		return out, err
	}
	out.Users = users
	for _, typ := range []string{models.ContentTypePost, models.ContentTypePage, models.ContentTypeAttach} {
		items, err := a.Contents.List(ctx, services.ContentQuery{Type: typ, Status: "all", IncludeDrafts: true, Limit: 10000})
		if err != nil {
			return out, err
		}
		out.Contents = append(out.Contents, items...)
	}
	for _, typ := range []string{"category", "tag"} {
		items, err := a.Metas.List(ctx, typ)
		if err != nil {
			return out, err
		}
		out.Metas = append(out.Metas, items...)
	}
	relationships, err := a.Contents.AllRelationships(ctx)
	if err != nil {
		return out, err
	}
	out.Relationships = relationships
	comments, err := a.Comments.List(ctx, "all", "", 0)
	if err != nil {
		return out, err
	}
	out.Comments = comments
	fields, err := a.Contents.AllFields(ctx)
	if err != nil {
		return out, err
	}
	out.Fields = fields
	return out, nil
}

type importSectionSet struct {
	Options  bool
	Users    bool
	Contents bool
	Metas    bool
	Comments bool
	Fields   bool
	Media    bool
}

func importSections(r *http.Request) importSectionSet {
	all := len(r.Form["section"]) == 0
	has := func(name string) bool {
		if all {
			return true
		}
		for _, item := range r.Form["section"] {
			if item == name {
				return true
			}
		}
		return false
	}
	return importSectionSet{
		Options:  has("options"),
		Users:    has("users"),
		Contents: has("contents"),
		Metas:    has("metas"),
		Comments: has("comments"),
		Fields:   has("fields"),
		Media:    has("media"),
	}
}

func (a *App) backupPlan(ctx context.Context, payload backupData, sections importSectionSet) (backupImportPlan, error) {
	var plan backupImportPlan
	db := a.Contents.DB()
	dialect := a.Contents.Dialect()
	if sections.Options {
		optionExistsQuery := `SELECT 1 FROM gb_options WHERE name = ? AND user = 0`
		if dialect == models.DialectPostgres {
			optionExistsQuery = `SELECT 1 FROM gb_options WHERE name = ? AND "user" = 0`
		}
		for key := range payload.Options {
			if key == "" {
				plan.Options.Skip++
				continue
			}
			exists, err := dbExists(ctx, db, dialect, optionExistsQuery, key)
			if err != nil {
				return plan, err
			}
			if exists {
				plan.Options.Update++
			} else {
				plan.Options.Add++
			}
		}
	}
	if sections.Users {
		for _, user := range payload.Users {
			if err := addSkipPlan(ctx, db, dialect, &plan.Users, user.UID, `SELECT 1 FROM gb_users WHERE uid = ?`); err != nil {
				return plan, err
			}
		}
	}
	if sections.Metas {
		for _, meta := range payload.Metas {
			if err := addSkipPlan(ctx, db, dialect, &plan.Metas, meta.MID, `SELECT 1 FROM gb_metas WHERE mid = ?`); err != nil {
				return plan, err
			}
		}
	}
	for _, content := range payload.Contents {
		if content.Type == models.ContentTypeAttach {
			if sections.Media {
				if err := addSkipPlan(ctx, db, dialect, &plan.Media, content.CID, `SELECT 1 FROM gb_contents WHERE cid = ?`); err != nil {
					return plan, err
				}
			}
			continue
		}
		if sections.Contents {
			if err := addSkipPlan(ctx, db, dialect, &plan.Contents, content.CID, `SELECT 1 FROM gb_contents WHERE cid = ?`); err != nil {
				return plan, err
			}
		}
	}
	if sections.Metas && (sections.Contents || sections.Media) {
		for _, rel := range payload.Relationships {
			if rel.CID <= 0 || rel.MID <= 0 {
				plan.Relationships.Skip++
				continue
			}
			exists, err := dbExists(ctx, db, dialect, `SELECT 1 FROM gb_relationships WHERE cid = ? AND mid = ?`, rel.CID, rel.MID)
			if err != nil {
				return plan, err
			}
			if exists {
				plan.Relationships.Skip++
			} else {
				plan.Relationships.Add++
			}
		}
	}
	if sections.Comments {
		for _, comment := range payload.Comments {
			if err := addSkipPlan(ctx, db, dialect, &plan.Comments, comment.COID, `SELECT 1 FROM gb_comments WHERE coid = ?`); err != nil {
				return plan, err
			}
		}
	}
	if sections.Fields {
		for _, field := range payload.Fields {
			if err := addSkipPlan(ctx, db, dialect, &plan.Fields, field.FID, `SELECT 1 FROM gb_fields WHERE fid = ?`); err != nil {
				return plan, err
			}
		}
	}
	return plan, nil
}

func (a *App) importBackupPayload(ctx context.Context, payload backupData, sections importSectionSet) error {
	if payload.Version > 1 {
		return fmt.Errorf("不支持的备份版本")
	}
	tx, err := a.Contents.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	dialect := a.Contents.Dialect()
	if sections.Options {
		for key, value := range payload.Options {
			if err := txUpsertOption(ctx, tx, dialect, key, value); err != nil {
				return err
			}
		}
	}
	if sections.Users {
		for _, user := range payload.Users {
			if user.UID <= 0 || strings.TrimSpace(user.Name) == "" {
				continue
			}
			if user.Password == "" {
				continue
			}
			if err := txInsertIgnore(ctx, tx, dialect,
				`INSERT OR IGNORE INTO gb_users (uid, name, password, mail, url, screenName, created, activated, logged, role, authCode) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				`INSERT IGNORE INTO gb_users (uid, name, password, mail, url, screenName, created, activated, logged, role, authCode) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				`INSERT INTO gb_users (uid, name, password, mail, url, screenName, created, activated, logged, role, authCode) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (uid) DO NOTHING`,
				user.UID, user.Name, user.Password, user.Mail, user.URL, user.ScreenName, user.Created, user.Activated, user.Logged, user.Role, user.AuthCode); err != nil {
				return err
			}
		}
	}
	if sections.Metas {
		for _, meta := range payload.Metas {
			if meta.MID <= 0 || meta.Type == "" {
				continue
			}
			if err := txInsertIgnore(ctx, tx, dialect,
				`INSERT OR IGNORE INTO gb_metas (mid, name, slug, type, description, count, sortOrder, parent) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				`INSERT IGNORE INTO gb_metas (mid, name, slug, type, description, count, sortOrder, parent) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				`INSERT INTO gb_metas (mid, name, slug, type, description, count, sortOrder, parent) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (mid) DO NOTHING`,
				meta.MID, meta.Name, meta.Slug, meta.Type, meta.Description, meta.Count, meta.SortOrder, meta.Parent); err != nil {
				return err
			}
		}
	}
	if sections.Contents || sections.Media {
		for _, content := range payload.Contents {
			if content.CID <= 0 {
				continue
			}
			if strings.TrimSpace(content.Type) == "" {
				return fmt.Errorf("备份内容 %d 缺少 type", content.CID)
			}
			if content.Type == models.ContentTypeAttach && !sections.Media {
				continue
			}
			if content.Type != models.ContentTypeAttach && !sections.Contents {
				continue
			}
			slugID := content.SlugID
			if slugID <= 0 && (content.Type == models.ContentTypePost || content.Type == models.ContentTypePage) {
				slugID = content.CID
			}
			if err := txInsertIgnore(ctx, tx, dialect,
				`INSERT OR IGNORE INTO gb_contents (cid, title, slug, slugId, created, modified, text, sortOrder, authorId, template, type, status, password, commentsNum, allowComment, allowPing, allowFeed, parent, draftOf) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				`INSERT IGNORE INTO gb_contents (cid, title, slug, slugId, created, modified, text, sortOrder, authorId, template, type, status, password, commentsNum, allowComment, allowPing, allowFeed, parent, draftOf) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				`INSERT INTO gb_contents (cid, title, slug, slugId, created, modified, text, sortOrder, authorId, template, type, status, password, commentsNum, allowComment, allowPing, allowFeed, parent, draftOf) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (cid) DO NOTHING`,
				content.CID, content.Title, content.Slug, slugID, content.Created, content.Modified, content.Text, content.SortOrder, content.AuthorID, content.Template, content.Type, content.Status, content.Password, content.CommentsNum, content.AllowComment, content.AllowPing, content.AllowFeed, content.Parent, content.DraftOf); err != nil {
				return err
			}
		}
		if sections.Metas {
			for _, rel := range payload.Relationships {
				if err := txInsertIgnore(ctx, tx, dialect,
					`INSERT OR IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`,
					`INSERT IGNORE INTO gb_relationships (cid, mid) VALUES (?, ?)`,
					`INSERT INTO gb_relationships (cid, mid) VALUES (?, ?) ON CONFLICT (cid, mid) DO NOTHING`,
					rel.CID, rel.MID); err != nil {
					return err
				}
			}
		}
	}
	if sections.Comments {
		for _, comment := range payload.Comments {
			if err := txInsertIgnore(ctx, tx, dialect,
				`INSERT OR IGNORE INTO gb_comments (coid, cid, created, author, authorId, ownerId, mail, url, ip, agent, text, type, status, parent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				`INSERT IGNORE INTO gb_comments (coid, cid, created, author, authorId, ownerId, mail, url, ip, agent, text, type, status, parent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				`INSERT INTO gb_comments (coid, cid, created, author, authorId, ownerId, mail, url, ip, agent, text, type, status, parent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (coid) DO NOTHING`,
				comment.COID, comment.CID, comment.Created, comment.Author, comment.AuthorID, comment.OwnerID, comment.Mail, comment.URL, comment.IP, comment.Agent, comment.Text, comment.Type, comment.Status, comment.Parent); err != nil {
				return err
			}
		}
	}
	if sections.Fields {
		for _, field := range payload.Fields {
			if err := txInsertIgnore(ctx, tx, dialect,
				`INSERT OR IGNORE INTO gb_fields (fid, cid, name, type, strValue, intValue, floatValue) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				`INSERT IGNORE INTO gb_fields (fid, cid, name, type, strValue, intValue, floatValue) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				`INSERT INTO gb_fields (fid, cid, name, type, strValue, intValue, floatValue) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (fid) DO NOTHING`,
				field.FID, field.CID, field.Name, field.Type, field.StrValue, field.IntValue, field.FloatValue); err != nil {
				return err
			}
		}
	}
	_, _ = tx.ExecContext(ctx, `
		UPDATE gb_metas SET count = (
			SELECT COUNT(*) FROM gb_relationships r JOIN gb_contents c ON c.cid = r.cid
			WHERE r.mid = gb_metas.mid AND c.type = 'post'
		)
	`)
	return tx.Commit()
}

func txUpsertOption(ctx context.Context, tx *sql.Tx, dialect models.Dialect, name, value string) error {
	if dialect == models.DialectPostgres {
		_, err := tx.ExecContext(ctx, `INSERT INTO gb_options (name, "user", value) VALUES ($1, 0, $2) ON CONFLICT(name, "user") DO UPDATE SET value = EXCLUDED.value`, name, value)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO gb_options (name, user, value) VALUES (?, 0, ?)`, name, value)
	if err == nil {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gb_options (name, user, value) VALUES (?, 0, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)`, name, value)
	return err
}

func txInsertIgnore(ctx context.Context, tx *sql.Tx, dialect models.Dialect, sqliteStmt, mysqlStmt, postgresStmt string, args ...any) error {
	if dialect == models.DialectPostgres {
		_, err := tx.ExecContext(ctx, models.Rebind(dialect, postgresStmt), args...)
		return err
	}
	_, err := tx.ExecContext(ctx, sqliteStmt, args...)
	if err == nil {
		return nil
	}
	_, err = tx.ExecContext(ctx, mysqlStmt, args...)
	return err
}

func addSkipPlan(ctx context.Context, db *sql.DB, dialect models.Dialect, count *backupPlanCount, id int64, query string) error {
	if id <= 0 {
		count.Skip++
		return nil
	}
	exists, err := dbExists(ctx, db, dialect, query, id)
	if err != nil {
		return err
	}
	if exists {
		count.Skip++
	} else {
		count.Add++
	}
	return nil
}

func dbExists(ctx context.Context, db *sql.DB, dialect models.Dialect, query string, args ...any) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, models.Rebind(dialect, query), args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *App) option(ctx context.Context, key, fallback string) string {
	value, err := a.Options.Get(ctx, key)
	if err != nil || value == "" {
		return fallback
	}
	return value
}

type pagination struct {
	Page       int
	PageSize   int
	Total      int64
	TotalPages int
	PrevURL    string
	NextURL    string
	HasPrev    bool
	HasNext    bool
}

type archiveLink struct {
	Year  int
	Month int
	Title string
	URL   string
	Count int
}

type commentView struct {
	models.Comment
	Level      int
	BodyHTML   template.HTML
	AuthorHTML template.HTML
	AvatarURL  string
	ReplyURL   string
	Anchor     string
}

type commentPagination struct {
	Page       int
	PageSize   int
	Total      int64
	TotalPages int
	PrevURL    string
	NextURL    string
	HasPrev    bool
	HasNext    bool
}

type mediaView struct {
	models.Content
	Meta        models.AttachmentMeta
	Name        string
	URL         string
	Kind        string
	MIME        string
	SizeLabel   string
	AuthorName  string
	ParentTitle string
	Markdown    string
}

type backupData struct {
	Version       int                   `json:"version"`
	Generator     string                `json:"generator"`
	Dialect       string                `json:"dialect"`
	GeneratedAt   string                `json:"generated_at"`
	Options       map[string]string     `json:"options"`
	Users         []models.User         `json:"users"`
	Contents      []models.Content      `json:"contents"`
	Metas         []models.Meta         `json:"metas"`
	Relationships []models.Relationship `json:"relationships"`
	Comments      []models.Comment      `json:"comments"`
	Fields        []models.Field        `json:"fields"`
}

type backupPlanCount struct {
	Add    int `json:"add"`
	Update int `json:"update"`
	Skip   int `json:"skip"`
}

type backupImportPlan struct {
	Options       backupPlanCount `json:"options"`
	Users         backupPlanCount `json:"users"`
	Contents      backupPlanCount `json:"contents"`
	Media         backupPlanCount `json:"media"`
	Metas         backupPlanCount `json:"metas"`
	Relationships backupPlanCount `json:"relationships"`
	Comments      backupPlanCount `json:"comments"`
	Fields        backupPlanCount `json:"fields"`
}

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Xmlns   string      `xml:"xmlns,attr"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

type atomEntry struct {
	ID        string      `xml:"id"`
	Title     string      `xml:"title"`
	Link      atomLink    `xml:"link"`
	Updated   string      `xml:"updated"`
	Published string      `xml:"published"`
	Content   atomContent `xml:"content"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := a.currentUserID(r)
		if !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !a.validCSRF(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		user, err := a.Users.ByID(r.Context(), uid)
		if err != nil {
			auth.ClearSessionWithOptions(w, a.cookieOptions(r.Context()))
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if roleRank(user.Role) < roleRank("contributor") && r.URL.Path != "/admin/profile" && r.URL.Path != "/admin/logout" {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), currentUserKey, user)))
	}
}

func (a *App) currentUserID(r *http.Request) (int64, bool) {
	secret, err := a.Options.Get(r.Context(), "auth_secret")
	if err != nil || secret == "" {
		return 0, false
	}
	return auth.ParseSessionWithOptions(r, secret, a.cookieOptions(r.Context()))
}

func (a *App) currentUser(r *http.Request) (models.User, bool) {
	if user, ok := r.Context().Value(currentUserKey).(models.User); ok {
		return user, true
	}
	uid, ok := a.currentUserID(r)
	if !ok {
		return models.User{}, false
	}
	user, err := a.Users.ByID(r.Context(), uid)
	return user, err == nil
}

func (a *App) requireRole(w http.ResponseWriter, r *http.Request, minimum string) bool {
	user, ok := a.currentUser(r)
	if !ok || roleRank(user.Role) < roleRank(minimum) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return false
	}
	return true
}

func (a *App) canEditContent(w http.ResponseWriter, r *http.Request, cid int64, expectedType string) bool {
	user, ok := a.currentUser(r)
	if !ok {
		http.Error(w, "permission denied", http.StatusForbidden)
		return false
	}
	content, err := a.Contents.ByID(r.Context(), cid)
	if err != nil {
		http.NotFound(w, r)
		return false
	}
	if content.Type != expectedType {
		http.NotFound(w, r)
		return false
	}
	if expectedType == models.ContentTypePage && roleRank(user.Role) < roleRank("editor") {
		http.Error(w, "permission denied", http.StatusForbidden)
		return false
	}
	if roleRank(user.Role) >= roleRank("editor") || content.AuthorID == user.UID {
		return true
	}
	http.Error(w, "permission denied", http.StatusForbidden)
	return false
}

func (a *App) csrfToken(r *http.Request) string {
	return a.csrfTokenFor(r, a.csrfPurpose(r))
}

func (a *App) csrfTokenFor(r *http.Request, purpose string) string {
	secret, _ := a.Options.Get(r.Context(), "auth_secret")
	if secret == "" {
		secret = "goblog"
	}
	subject := "anon"
	if uid, ok := a.currentUserID(r); ok {
		subject = strconv.FormatInt(uid, 10)
	}
	return signCSRF(secret, subject, purpose, time.Now().UTC())
}

func (a *App) validCSRF(r *http.Request) bool {
	return a.validCSRFFor(r, a.csrfPurpose(r))
}

func (a *App) validCSRFFor(r *http.Request, purpose string) bool {
	token := r.FormValue("_csrf")
	if token == "" {
		token = r.Header.Get("X-CSRF-Token")
	}
	secret, _ := a.Options.Get(r.Context(), "auth_secret")
	if secret == "" || token == "" {
		return false
	}
	subject := "anon"
	if uid, ok := a.currentUserID(r); ok {
		subject = strconv.FormatInt(uid, 10)
	}
	now := time.Now().UTC()
	for _, t := range []time.Time{now, now.AddDate(0, 0, -1)} {
		if hmac.Equal([]byte(token), []byte(signCSRF(secret, subject, purpose, t))) {
			return true
		}
	}
	return false
}

func (a *App) loginAllowed(ip, name string) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	key := ip + "|" + strings.ToLower(name)
	next, ok := a.loginNext[key]
	if !ok || time.Now().After(next) {
		delete(a.loginNext, key)
		return true
	}
	return false
}

func (a *App) recordLoginFailure(ip, name string) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	key := ip + "|" + strings.ToLower(name)
	a.loginNext[key] = time.Now().Add(3 * time.Second)
}

func (a *App) csrfPurpose(r *http.Request) string {
	switch {
	case r.URL.Path == "/admin/login":
		return "login"
	case r.URL.Path == "/admin/register" || r.URL.Path == "/register":
		return "register"
	case r.URL.Path == "/install":
		return "install"
	case r.URL.Path == "/comment":
		return "comment"
	case strings.HasPrefix(r.URL.Path, "/admin"):
		return "admin"
	default:
		return "public"
	}
}

func signCSRF(secret, subject, purpose string, t time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(subject))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(t.Format("2006-01-02")))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *App) previewURL(r *http.Request, c models.Content) string {
	if c.CID <= 0 {
		return ""
	}
	return fmt.Sprintf("/preview/%d?token=%s", c.CID, a.previewToken(r, c))
}

func (a *App) previewToken(r *http.Request, c models.Content) string {
	secret, _ := a.Options.Get(r.Context(), "auth_secret")
	if secret == "" {
		secret = "goblog"
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "preview:%d:%d:%s", c.CID, c.Modified, c.Status)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *App) renderCommentText(r *http.Request, text string) template.HTML {
	if a.option(r.Context(), "comments_markdown", "0") == "1" {
		return render.MarkdownHTML(text)
	}
	allowedTags := a.option(r.Context(), "comments_html_tag_allowed", "")
	if strings.TrimSpace(allowedTags) != "" {
		return sanitizeCommentHTML(text, allowedTags, optionBool(a.option(r.Context(), "comments_url_nofollow", "1")))
	}
	return render.PlainTextHTML(text)
}

func (a *App) commentAuthorHTML(r *http.Request, comment models.Comment) template.HTML {
	name := template.HTMLEscapeString(comment.Author)
	if a.option(r.Context(), "comments_show_url", "1") != "1" || strings.TrimSpace(comment.URL) == "" {
		return template.HTML(name)
	}
	rel := ""
	if a.option(r.Context(), "comments_url_nofollow", "1") == "1" {
		rel = ` rel="nofollow"`
	}
	return template.HTML(`<a href="` + template.HTMLEscapeString(comment.URL) + `"` + rel + `>` + name + `</a>`)
}

func (a *App) gravatarURL(r *http.Request, mail string) string {
	if a.option(r.Context(), "comments_avatar", "1") != "1" {
		return ""
	}
	sum := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(mail))))
	rating := a.option(r.Context(), "comments_avatar_rating", "g")
	return "https://www.gravatar.com/avatar/" + hex.EncodeToString(sum[:]) + "?s=48&d=mp&r=" + rating
}

func adminAvatarURL(mail string, size int) string {
	if size <= 0 {
		size = 96
	}
	sum := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(mail))))
	return "https://www.gravatar.com/avatar/" + hex.EncodeToString(sum[:]) + "?s=" + strconv.Itoa(size) + "&d=mp&r=g"
}

func (a *App) nameReserved(ctx context.Context, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	users, err := a.Users.List(ctx, name)
	if err != nil {
		return false
	}
	for _, user := range users {
		if strings.ToLower(user.Name) == name || strings.ToLower(user.ScreenName) == name {
			return true
		}
	}
	return false
}

func (a *App) validCommentReferer(r *http.Request, targetPath string) bool {
	ref := strings.TrimSpace(r.Referer())
	if ref == "" {
		return false
	}
	u, err := neturl.Parse(ref)
	if err != nil || u.Host == "" {
		return false
	}
	if !a.commentRefererHostAllowed(r, u.Host) {
		return false
	}
	refPath := strings.TrimRight(u.Path, "/")
	if refPath == "" {
		refPath = "/"
	}
	targetPath = strings.TrimRight(targetPath, "/")
	if targetPath == "" {
		targetPath = "/"
	}
	return refPath == targetPath || strings.HasPrefix(refPath, targetPath+"/")
}

func (a *App) commentRefererHostAllowed(r *http.Request, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if sameHost(host, r.Host) {
		return true
	}
	baseURL := a.option(r.Context(), "base_url", "")
	if baseURL == "" {
		return false
	}
	u, err := neturl.Parse(baseURL)
	return err == nil && sameHost(host, u.Host)
}

func (a *App) validPreviewToken(r *http.Request, c models.Content) bool {
	token := r.URL.Query().Get("token")
	if token == "" {
		return false
	}
	expected := a.previewToken(r, c)
	return hmac.Equal([]byte(token), []byte(expected))
}

func (a *App) renderAdmin(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	lang := a.option(r.Context(), "site_language", "zh-CN")
	funcs := template.FuncMap{
		"date":                   func(ts int64) string { return a.formatDate(r.Context(), ts, "post_date_format") },
		"T":                      func(key string) string { return i18n.T(lang, key) },
		"statusLabel":            statusLabel,
		"contentStatus":          contentStatusLabel,
		"roleLabel":              roleLabel,
		"excerpt":                render.Excerpt,
		"containsMeta":           containsMeta,
		"joinMetaNames":          joinMetaNames,
		"checked":                checked,
		"contentPublicURL":       contentPublicURL,
		"fieldError":             fieldError,
		"fieldValue":             fieldValue,
		"schemaValue":            schemaValue,
		"schemaChecked":          schemaChecked,
		"schemaOptionsAreColors": schemaOptionsAreColors,
		"adminAvatarURL":         adminAvatarURL,
	}
	tmpl, err := template.New("base.html").Funcs(funcs).ParseFS(admin.FS, "templates/base.html", "templates/"+page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.enrichData(r.Context(), data)
	a.enrichAdminAppearance(r.Context(), data)
	if notices := a.consumeFlash(w, r); len(notices) > 0 {
		data["Notices"] = notices
	}
	if out, err := a.Plugins.ApplyActive(r.Context(), plugin.HookAdminMenu, []plugin.AdminMenuItem{}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else {
		if items, ok := out.([]plugin.AdminMenuItem); ok {
			data["AdminMenu"] = items
		}
	}
	data["CSRF"] = a.csrfToken(r)
	if user, ok := a.currentUser(r); ok {
		data["CurrentUser"] = user
	}
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) enrichAdminAppearance(ctx context.Context, data map[string]any) {
	values := a.adminAppearanceValues(ctx)
	data["AdminPrimary"] = adminAppearancePrimary(values)
	data["AdminCardOpacity"] = adminAppearanceOpacity(values["admin_card_opacity"], 0.84)
	data["AdminSidebarOpacity"] = adminAppearanceOpacity(values["admin_sidebar_opacity"], 0.90)
	data["AdminTopbarOpacity"] = adminAppearanceOpacity(values["admin_topbar_opacity"], 0.92)
	data["AdminInputOpacity"] = adminAppearanceOpacity(values["admin_input_opacity"], 0.62)
	data["AdminBackgroundMaskOpacity"] = adminAppearanceOpacity(values["admin_bg_mask_opacity"], 0.54)
	data["AdminBackgroundImage"] = adminAppearanceURL(values["admin_bg_image"])
	data["AdminMobileBackgroundImage"] = adminAppearanceURL(values["admin_mobile_bg_image"])
}

func (a *App) adminAppearanceValues(ctx context.Context) map[string]string {
	values, err := a.optionJSONForUser(ctx, adminAppearanceOptionKey, 0)
	if err != nil {
		values = map[string]string{}
	}
	a.applySchemaDefaults(adminAppearanceSchema(), values)
	return values
}

func adminAppearancePrimary(values map[string]string) string {
	if color := adminAppearanceHexColor(values["admin_custom_primary"]); color != "" {
		return color
	}
	if color := adminAppearanceHexColor(values["admin_primary_preset"]); color != "" {
		return color
	}
	return "#6750a4"
}

var adminAppearanceHexColorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func adminAppearanceHexColor(value string) string {
	value = strings.TrimSpace(value)
	if !adminAppearanceHexColorRE.MatchString(value) {
		return ""
	}
	return strings.ToLower(value)
}

func adminAppearanceOpacity(value string, fallback float64) string {
	opacity, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		opacity = fallback
	}
	if opacity < 0 {
		opacity = 0
	}
	if opacity > 1 {
		opacity = 1
	}
	return strconv.FormatFloat(opacity, 'f', 2, 64)
}

func adminAppearanceURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return value
	}
	parsed, err := neturl.Parse(value)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return value
	}
	return ""
}

func (a *App) renderTheme(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	a.renderThemeStatus(w, r, page, data, http.StatusOK)
}

func (a *App) renderContentHTML(ctx context.Context, content models.Content, data map[string]any) (template.HTML, error) {
	payload := plugin.ContentRenderPayload{Content: content, Data: data}
	if out, err := a.Plugins.ApplyActive(ctx, plugin.HookContentBeforeRender, payload); err != nil {
		return "", err
	} else {
		if next, ok := out.(plugin.ContentRenderPayload); ok {
			payload = next
			if changed, ok := next.Content.(models.Content); ok {
				content = changed
			}
		}
	}
	payload.Content = content
	payload.HTML = render.ContentHTML(content.Text, a.option(ctx, "content_render_mode", "markdown"))
	if out, err := a.Plugins.ApplyActive(ctx, plugin.HookContentAfterRender, payload); err != nil {
		return "", err
	} else {
		if next, ok := out.(plugin.ContentRenderPayload); ok {
			return next.HTML, nil
		}
	}
	return payload.HTML, nil
}

func (a *App) excerpt(ctx context.Context, text string, limit int) string {
	output := render.Excerpt(text, limit)
	payload := plugin.ExcerptPayload{Text: text, Limit: limit, Output: output}
	if out, err := a.Plugins.ApplyActive(ctx, plugin.HookExcerpt, payload); err == nil {
		if next, ok := out.(plugin.ExcerptPayload); ok {
			return next.Output
		}
	}
	return output
}

func (a *App) renderThemeStatus(w http.ResponseWriter, r *http.Request, page string, data map[string]any, status int) {
	theme, ok := a.activeTheme(r.Context())
	if !ok {
		http.Error(w, "active theme not found", http.StatusInternalServerError)
		return
	}
	lang := a.option(r.Context(), "site_language", "zh-CN")
	funcs := template.FuncMap{
		"date": func(ts int64) string { return a.formatDate(r.Context(), ts, "post_date_format") },
		"T":    func(key string) string { return i18n.T(lang, key) },
		"excerpt": func(text string, limit int) string {
			return a.excerpt(r.Context(), text, limit)
		},
		"contentURL": func(c models.Content) string {
			return a.contentURL(r.Context(), c)
		},
		"metaURL": func(m models.Meta) string {
			return a.metaURL(r.Context(), m)
		},
		"commentURL": func(c models.Comment) string {
			if c.CID <= 0 {
				return "#comment-" + strconv.FormatInt(c.COID, 10)
			}
			content, err := a.Contents.ByID(r.Context(), c.CID)
			if err != nil || (content.Type != models.ContentTypePost && content.Type != models.ContentTypePage) {
				return "#comment-" + strconv.FormatInt(c.COID, 10)
			}
			return a.contentURL(r.Context(), content) + "#comment-" + strconv.FormatInt(c.COID, 10)
		},
		"commentDate": func(ts int64) string {
			layout := a.option(r.Context(), "comment_date_format", "2006-01-02 15:04")
			if strings.TrimSpace(layout) == "" {
				layout = "2006-01-02 15:04"
			}
			return time.Unix(ts, 0).In(a.siteLocation(r.Context())).Format(layout)
		},
	}
	for name, fn := range theme.Funcs {
		funcs[name] = fn
	}
	tmpl, err := template.New("base.html").Funcs(funcs).ParseFS(theme.Templates, "templates/base.html", "templates/"+page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.enrichData(r.Context(), data)
	a.enrichThemeData(r.Context(), data)
	if themeConfig, err := a.themeConfig(r.Context(), theme.Name); err == nil {
		data["ThemeConfig"] = themeConfig
	}
	if theme.AdjustData != nil {
		if err := theme.AdjustData(r.Context(), data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	data["CSRF"] = a.csrfToken(r)
	data["CommentCSRF"] = a.csrfTokenFor(r, "comment")
	if site, ok := data["Site"].(map[string]string); ok {
		canonicalPath := r.URL.Path
		if pathValue, ok := data["CanonicalPath"].(string); ok && pathValue != "" {
			canonicalPath = pathValue
		}
		baseURL := strings.TrimRight(site["base_url"], "/")
		data["CurrentURL"] = baseURL + canonicalPath
		if _, ok := data["SeoImage"]; !ok {
			if post, ok := data["Post"].(models.Content); ok {
				if imageURL := firstContentImage(post.Text); imageURL != "" {
					data["SeoImage"] = absolutePublicURL(baseURL, imageURL)
				}
			}
		}
		if _, ok := data["FeedPath"]; !ok {
			data["FeedPath"] = "/feed.xml"
		}
		data["XMLRPCEnabled"] = optionBool(a.option(r.Context(), "enable_xmlrpc", "1"))
		data["PingbackEnabled"] = optionBool(a.option(r.Context(), "enable_pingback", "1"))
		data["XMLRPCURL"] = baseURL + "/xmlrpc.php"
		data["RSDURL"] = baseURL + "/rsd.xml"
		data["WLWManifestURL"] = baseURL + "/wlwmanifest.xml"
	}
	if out, err := a.Plugins.ApplyActive(r.Context(), plugin.HookFrontendHead, ""); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if head, ok := out.(string); ok {
		data["FrontendHead"] = template.HTML(head)
	}
	if out, err := a.Plugins.ApplyActive(r.Context(), plugin.HookFrontendFooter, ""); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if footer, ok := out.(string); ok {
		data["FrontendFooter"] = template.HTML(footer)
	}
	w.WriteHeader(status)
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) activeTheme(ctx context.Context) (plugin.Theme, bool) {
	name, _ := a.Options.Get(ctx, "active_theme")
	if name == "" {
		name = "default"
	}
	theme, ok := a.Plugins.Theme(name)
	if ok {
		return theme, true
	}
	theme, ok = a.Plugins.Theme("default")
	if ok {
		_ = a.Options.Set(ctx, "active_theme", "default")
		return theme, true
	}
	return plugin.Theme{}, false
}

func (a *App) enrichData(ctx context.Context, data map[string]any) {
	options, err := a.Options.All(ctx)
	if err == nil {
		data["Site"] = options
	}
}

func (a *App) enrichThemeData(ctx context.Context, data map[string]any) {
	if _, ok := data["ProfileEmail"]; !ok {
		data["ProfileEmail"] = a.defaultThemeProfileEmail(ctx, data)
	}
	if _, ok := data["RecentPosts"]; !ok {
		if posts, err := a.Contents.ListPublished(ctx, 5, 0); err == nil {
			data["RecentPosts"] = posts
		}
	}
	if _, ok := data["Pages"]; !ok {
		if pages, err := a.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePage, Status: models.ContentStatusPost, ExcludeFuture: true, Limit: 20}); err == nil {
			data["Pages"] = pages
		}
	}
	if _, ok := data["AllCategories"]; !ok {
		if categories, err := a.Metas.List(ctx, "category"); err == nil {
			data["AllCategories"] = categories
		}
	}
	if _, ok := data["Archives"]; !ok {
		data["Archives"] = a.archiveLinks(ctx, 18)
	}
	if _, ok := data["Tags"]; !ok {
		if tags, err := a.Metas.ListCloud(ctx, "tag", 30); err == nil {
			data["Tags"] = tags
		}
	}
	if _, ok := data["RecentComments"]; !ok {
		if comments, err := a.Comments.List(ctx, "approved", "", 0); err == nil {
			size := 10
			if site, ok := data["Site"].(map[string]string); ok {
				size = optionInt(site["comments_list_size"], 10)
			}
			if size < 1 {
				size = 10
			}
			if len(comments) > size {
				comments = comments[:size]
			}
			data["RecentComments"] = comments
		}
	}
}

func (a *App) archiveLinks(ctx context.Context, limit int) []archiveLink {
	periods, err := a.Contents.ArchiveMonths(ctx, a.siteLocation(ctx), limit)
	if err != nil {
		return nil
	}
	out := make([]archiveLink, 0, len(periods))
	for _, period := range periods {
		out = append(out, archiveLink{
			Year:  period.Year,
			Month: period.Month,
			Title: period.Date,
			URL:   archivePath(period.Year, period.Month, 0),
			Count: period.Count,
		})
	}
	return out
}

func (a *App) defaultThemeProfileEmail(ctx context.Context, data map[string]any) string {
	if post, ok := data["Post"].(models.Content); ok && post.AuthorID > 0 {
		if user, err := a.Users.ByID(ctx, post.AuthorID); err == nil && strings.TrimSpace(user.Mail) != "" {
			return strings.TrimSpace(user.Mail)
		}
	}
	users, err := a.Users.List(ctx, "")
	if err != nil {
		return ""
	}
	for _, user := range users {
		if user.Role == "administrator" && strings.TrimSpace(user.Mail) != "" {
			return strings.TrimSpace(user.Mail)
		}
	}
	for _, user := range users {
		if strings.TrimSpace(user.Mail) != "" {
			return strings.TrimSpace(user.Mail)
		}
	}
	return ""
}

func parseContentForm(r *http.Request, typ string) (services.SaveContentInput, error) {
	if err := r.ParseForm(); err != nil {
		return services.SaveContentInput{}, err
	}
	sortOrder, _ := strconv.ParseInt(r.FormValue("sortOrder"), 10, 64)
	parent, _ := strconv.ParseInt(r.FormValue("parent"), 10, 64)
	created := parseDate(r.FormValue("created"))
	categoryIDs := parseInt64Values(r.Form["category"])
	tags := splitTags(r.FormValue("tags"))
	fields := parseFieldInputs(r)
	status := r.FormValue("status")
	if status == "" {
		status = models.ContentStatusDraft
	}
	return services.SaveContentInput{
		Title:        strings.TrimSpace(r.FormValue("title")),
		Slug:         strings.TrimSpace(r.FormValue("slug")),
		Text:         strings.TrimSpace(r.FormValue("text")),
		Type:         typ,
		Status:       status,
		Password:     r.FormValue("password"),
		Created:      created,
		SortOrder:    sortOrder,
		Template:     r.FormValue("template"),
		Parent:       parent,
		AllowComment: r.FormValue("allowComment") == "1",
		AllowPing:    r.FormValue("allowPing") == "1",
		AllowFeed:    r.FormValue("allowFeed") == "1",
		CategoryIDs:  categoryIDs,
		Tags:         tags,
		Fields:       fields,
	}, nil
}

func parseFieldInputs(r *http.Request) []services.SaveFieldInput {
	names := r.Form["field_name"]
	types := r.Form["field_type"]
	values := r.Form["field_value"]
	out := make([]services.SaveFieldInput, 0, len(names))
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		typ := "str"
		if i < len(types) {
			typ = types[i]
		}
		value := ""
		if i < len(values) {
			value = values[i]
		}
		field := services.SaveFieldInput{Name: name, Type: typ, StrValue: value}
		switch typ {
		case "int":
			field.IntValue, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		case "float":
			field.FloatValue, _ = strconv.ParseFloat(strings.TrimSpace(value), 64)
		}
		out = append(out, field)
	}
	return out
}

func validateContentInput(input services.SaveContentInput) validate.Errors {
	v := validate.New()
	v.Required("title", input.Title).
		MaxLength("title", input.Title, 150).
		Slug("slug", input.Slug).
		In("status", input.Status, "publish", "draft", "hidden", "waiting", "private").
		SafeText("text", input.Text)
	if input.Type == models.ContentTypePage {
		v.MaxLength("template", input.Template, 32)
	}
	for _, field := range input.Fields {
		v.Required("field_name", field.Name).
			In("field_type", field.Type, "str", "int", "float", "json").
			SafeText("field_value", field.StrValue)
		if field.Type == "json" && strings.TrimSpace(field.StrValue) != "" && !json.Valid([]byte(field.StrValue)) {
			v.Errors.Add("field_value", "JSON 格式不正确")
		}
	}
	return v.Errors
}

func validateMetaInput(input services.SaveMetaInput) validate.Errors {
	v := validate.New()
	v.Required("name", input.Name).
		MaxLength("name", input.Name, 150).
		Slug("slug", input.Slug).
		MaxLength("description", input.Description, 150).
		SafeText("description", input.Description)
	return v.Errors
}

func validateUserInput(input services.SaveUserInput, requirePassword bool) validate.Errors {
	v := validate.New()
	v.Required("name", input.Name).
		MaxLength("name", input.Name, 32).
		MaxLength("screenName", input.ScreenName, 32).
		Email("mail", input.Mail).
		URL("url", input.URL).
		In("role", input.Role, "administrator", "editor", "contributor", "subscriber", "visitor")
	if requirePassword {
		v.Required("password", input.Password)
	}
	if input.Password != "" {
		v.MinLength("password", input.Password, 6)
	}
	return v.Errors
}

func validateCommentInput(input services.SaveCommentInput) validate.Errors {
	v := validate.New()
	v.Required("author", input.Author).
		MaxLength("author", input.Author, 150).
		Email("mail", input.Mail).
		URL("url", input.URL).
		Required("text", input.Text).
		SafeText("text", input.Text).
		In("status", input.Status, "approved", "waiting", "spam")
	return v.Errors
}

func validatePublicCommentInput(input services.SaveCommentInput, requireMail, requireURL bool) validate.Errors {
	v := validate.New()
	v.Required("author", input.Author).
		MaxLength("author", input.Author, 150).
		URL("url", input.URL).
		Required("text", input.Text).
		SafeText("text", input.Text)
	if requireMail {
		v.Required("mail", input.Mail)
	}
	if requireURL {
		v.Required("url", input.URL)
	}
	v.Email("mail", input.Mail)
	return v.Errors
}

func applyContentInput(item models.Content, input services.SaveContentInput) models.Content {
	item.Title = input.Title
	item.Slug = input.Slug
	item.Text = input.Text
	item.Type = input.Type
	item.Status = input.Status
	item.Password = input.Password
	item.Created = input.Created
	item.SortOrder = input.SortOrder
	item.Template = input.Template
	item.Parent = input.Parent
	item.AllowComment = boolString(input.AllowComment)
	item.AllowPing = boolString(input.AllowPing)
	item.AllowFeed = boolString(input.AllowFeed)
	return item
}

func metasFromIDs(ids []int64) []models.Meta {
	out := make([]models.Meta, 0, len(ids))
	for _, id := range ids {
		out = append(out, models.Meta{MID: id})
	}
	return out
}

func metasFromNames(names []string) []models.Meta {
	out := make([]models.Meta, 0, len(names))
	for _, name := range names {
		out = append(out, models.Meta{Name: name})
	}
	return out
}

func fieldModels(inputs []services.SaveFieldInput) []models.Field {
	out := make([]models.Field, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, models.Field{Name: input.Name, Type: input.Type, StrValue: input.StrValue, IntValue: input.IntValue, FloatValue: input.FloatValue})
	}
	return out
}

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func parseInt64Values(values []string) []int64 {
	var out []int64
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func splitTags(input string) []string {
	parts := strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == '，' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if tag := strings.TrimSpace(part); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

func parseDate(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func formatDate(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

func statusLabel(status string) string {
	switch status {
	case models.ContentStatusDraft:
		return "草稿"
	case "hidden":
		return "隐藏"
	case "waiting":
		return "待审核"
	case "private":
		return "私密"
	default:
		return "已发布"
	}
}

func contentStatusLabel(c models.Content) string {
	if c.Status == models.ContentStatusPost && c.Created > time.Now().Unix() {
		return "定时发布"
	}
	return statusLabel(c.Status)
}

func roleLabel(role string) string {
	switch role {
	case "administrator":
		return "管理员"
	case "editor":
		return "编辑"
	case "contributor":
		return "贡献者"
	case "subscriber":
		return "关注者"
	default:
		return "访问者"
	}
}

func roleRank(role string) int {
	switch role {
	case "administrator":
		return 40
	case "editor":
		return 30
	case "contributor":
		return 20
	case "subscriber":
		return 10
	default:
		return 0
	}
}

func checked(value string) bool {
	return value == "1"
}

func containsMeta(items []models.Meta, id int64) bool {
	for _, item := range items {
		if item.MID == id {
			return true
		}
	}
	return false
}

func joinMetaNames(items []models.Meta) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return strings.Join(names, ", ")
}

func (a *App) contentURL(ctx context.Context, c models.Content) string {
	switch c.Type {
	case models.ContentTypePage:
		pattern := a.option(ctx, "permalink_page", "/page/{slug}.html")
		return cleanPublicPath(applyContentPattern(pattern, c, a.pageDirectory(ctx, c)))
	default:
		pattern := a.option(ctx, "permalink_post", "/post/{slug}.html")
		category, directory := a.primaryCategoryPath(ctx, c.CID)
		return cleanPublicPath(applyContentPattern(pattern, c, directory, category))
	}
}

func (a *App) metaURL(ctx context.Context, m models.Meta) string {
	if m.Type == "category" {
		pattern := a.option(ctx, "permalink_category", "/category/{slug}")
		return cleanPublicPath(applyMetaPattern(pattern, m, a.metaDirectory(ctx, m)))
	}
	return cleanPublicPath("/tag/" + m.Slug)
}

func (a *App) pageDirectory(ctx context.Context, c models.Content) string {
	parts := []string{contentRouteSlug(c)}
	parent := c.Parent
	seen := map[int64]bool{c.CID: true}
	for parent > 0 && !seen[parent] {
		seen[parent] = true
		p, err := a.Contents.ByID(ctx, parent)
		if err != nil || p.Type != models.ContentTypePage {
			break
		}
		parts = append([]string{contentRouteSlug(p)}, parts...)
		parent = p.Parent
	}
	return strings.Join(parts, "/")
}

func (a *App) metaDirectory(ctx context.Context, m models.Meta) string {
	parts := []string{m.Slug}
	parent := m.Parent
	seen := map[int64]bool{m.MID: true}
	for parent > 0 && !seen[parent] {
		seen[parent] = true
		p, err := a.Metas.ByID(ctx, parent)
		if err != nil || p.Type != m.Type {
			break
		}
		parts = append([]string{p.Slug}, parts...)
		parent = p.Parent
	}
	return strings.Join(parts, "/")
}

func (a *App) primaryCategoryPath(ctx context.Context, cid int64) (slug, directory string) {
	categories, err := a.Metas.CategoriesForContent(ctx, cid)
	if err != nil || len(categories) == 0 {
		return "", ""
	}
	return categories[0].Slug, a.metaDirectory(ctx, categories[0])
}

func applyContentPattern(pattern string, c models.Content, directory string, category ...string) string {
	cat := ""
	if len(category) > 0 {
		cat = category[0]
	}
	t := time.Unix(c.Created, 0)
	routeSlug := contentRouteSlug(c)
	replacer := strings.NewReplacer(
		"{cid}", strconv.FormatInt(c.CID, 10),
		"{slug}", routeSlug,
		"{directory}", directory,
		"{category}", cat,
		"{year}", t.Format("2006"),
		"{month}", t.Format("01"),
		"{day}", t.Format("02"),
	)
	return replacer.Replace(pattern)
}

func applyMetaPattern(pattern string, m models.Meta, directory string) string {
	return strings.NewReplacer(
		"{mid}", strconv.FormatInt(m.MID, 10),
		"{slug}", m.Slug,
		"{directory}", directory,
	).Replace(pattern)
}

func cleanPublicPath(value string) string {
	value = "/" + strings.Trim(value, "/")
	value = strings.ReplaceAll(value, "//", "/")
	if value == "" {
		return "/"
	}
	return value
}

func trimSlashPath(value string) string {
	return strings.Trim(value, "/")
}

func (a *App) postsIndexPath(ctx context.Context) string {
	value := cleanPublicPath(a.option(ctx, "posts_index_path", "/"))
	if value == "" {
		return "/"
	}
	return value
}

func searchPath(keywords string) string {
	keywords = strings.TrimSpace(keywords)
	if keywords == "" {
		return "/search"
	}
	return "/search/" + neturl.PathEscape(keywords)
}

var (
	markdownImageRE = regexp.MustCompile(`!\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+["'][^)]*["'])?\s*\)`)
	htmlImageRE     = regexp.MustCompile(`(?is)<img\b[^>]*\bsrc\s*=\s*(?:"([^"]+)"|'([^']+)'|([^\s>]+))`)
)

func firstContentImage(text string) string {
	mdMatch := markdownImageRE.FindStringSubmatchIndex(text)
	htmlMatch := htmlImageRE.FindStringSubmatchIndex(text)
	if mdMatch == nil && htmlMatch == nil {
		return ""
	}
	if mdMatch != nil && (htmlMatch == nil || mdMatch[0] <= htmlMatch[0]) {
		return strings.TrimSpace(text[mdMatch[2]:mdMatch[3]])
	}
	if htmlMatch != nil {
		for i := 2; i+1 < len(htmlMatch); i += 2 {
			if htmlMatch[i] >= 0 && htmlMatch[i+1] >= 0 {
				return strings.TrimSpace(text[htmlMatch[i]:htmlMatch[i+1]])
			}
		}
	}
	return ""
}

func absolutePublicURL(baseURL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		scheme := "https"
		if u, err := neturl.Parse(baseURL); err == nil && u.Scheme != "" {
			scheme = u.Scheme
		}
		return scheme + ":" + raw
	}
	u, err := neturl.Parse(raw)
	if err == nil && u.IsAbs() {
		return raw
	}
	base, err := neturl.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		return raw
	}
	ref, err := neturl.Parse(raw)
	if err != nil {
		return raw
	}
	return base.ResolveReference(ref).String()
}

func archivePath(year, month, day int) string {
	if day > 0 {
		return fmt.Sprintf("/archive/%04d/%02d/%02d", year, month, day)
	}
	if month > 0 {
		return fmt.Sprintf("/archive/%04d/%02d", year, month)
	}
	return fmt.Sprintf("/archive/%04d", year)
}

func (a *App) redirectCanonical(w http.ResponseWriter, r *http.Request, canonical string) bool {
	if canonical == "" || r.Method != http.MethodGet {
		return false
	}
	if trimSlashPath(r.URL.Path) == trimSlashPath(canonical) {
		return false
	}
	target := canonical
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
	return true
}

func (a *App) tryDynamicPermalink(w http.ResponseWriter, r *http.Request) bool {
	ctx := r.Context()
	if vars, ok := matchPermalink(a.option(ctx, "permalink_post", "/post/{slug}.html"), r.URL.Path); ok {
		post, err := a.contentFromPermalinkVars(ctx, vars, models.ContentTypePost)
		if err == nil && post.Status == models.ContentStatusPost && post.Created <= time.Now().Unix() {
			if post.Password != "" && r.URL.Query().Get("password") != post.Password {
				a.renderTheme(w, r, "post.html", map[string]any{"Post": post, "PasswordRequired": true})
				return true
			}
			if a.redirectCanonical(w, r, a.contentURL(ctx, post)) {
				return true
			}
			a.renderPostContent(w, r, post)
			return true
		}
	}
	if vars, ok := matchPermalink(a.option(ctx, "permalink_page", "/page/{slug}.html"), r.URL.Path); ok {
		pageData, err := a.contentFromPermalinkVars(ctx, vars, models.ContentTypePage)
		if err == nil && pageData.Status == models.ContentStatusPost && pageData.Created <= time.Now().Unix() {
			if a.redirectCanonical(w, r, a.contentURL(ctx, pageData)) {
				return true
			}
			a.renderPageContent(w, r, pageData)
			return true
		}
	}
	if vars, ok := matchPermalink(a.option(ctx, "permalink_category", "/category/{slug}"), r.URL.Path); ok {
		meta, err := a.metaFromPermalinkVars(ctx, vars, "category")
		if err == nil {
			if a.redirectCanonical(w, r, a.metaURL(ctx, meta)) {
				return true
			}
			a.renderPostListWithData(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Category: meta.MID}, "分类："+meta.Name, map[string]any{"ArchiveMeta": meta, "CanonicalPath": a.metaURL(ctx, meta), "FeedPath": a.metaURL(ctx, meta) + "/feed.xml"})
			return true
		}
	}
	return false
}

func (a *App) tryDynamicTaxonomyFeed(w http.ResponseWriter, r *http.Request) bool {
	ctx := r.Context()
	cleanPath := strings.TrimSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/feed.xml")
	vars, ok := matchPermalink(a.option(ctx, "permalink_category", "/category/{slug}"), cleanPath)
	if !ok {
		return false
	}
	meta, err := a.metaFromPermalinkVars(ctx, vars, "category")
	if err != nil {
		return false
	}
	query := services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Category: meta.MID, ExcludeFuture: true, Limit: optionInt(a.option(ctx, "posts_list_size", "10"), 10)}
	posts, err := a.Contents.List(ctx, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	a.writeRSS(w, r, posts, nil, meta.Name, meta.Description, a.metaURL(ctx, meta)+"/feed.xml")
	return true
}

func (a *App) tryPrettyArchive(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 0 || len(parts[0]) != 4 {
		return false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil || year < 1970 {
		return false
	}
	query := services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Year: year}
	title := fmt.Sprintf("归档：%04d", year)
	if len(parts) > 1 {
		query.Month, _ = strconv.Atoi(parts[1])
		title = fmt.Sprintf("归档：%04d-%02d", year, query.Month)
	}
	if len(parts) > 2 {
		query.Day, _ = strconv.Atoi(parts[2])
		title = fmt.Sprintf("归档：%04d-%02d-%02d", year, query.Month, query.Day)
	}
	a.renderPostListWithData(w, r, query, title, map[string]any{"CanonicalPath": archivePath(query.Year, query.Month, query.Day)})
	return true
}

func (a *App) contentFromPermalinkVars(ctx context.Context, vars map[string]string, typ string) (models.Content, error) {
	if raw := vars["cid"]; raw != "" {
		id, _ := strconv.ParseInt(raw, 10, 64)
		c, err := a.Contents.ByID(ctx, id)
		if err != nil || c.Type != typ {
			return models.Content{}, sql.ErrNoRows
		}
		return c, nil
	}
	slug := vars["slug"]
	if slug == "" {
		slug = path.Base(vars["directory"])
	}
	if typ == models.ContentTypePage {
		return a.Contents.PageBySlug(ctx, slug)
	}
	return a.Contents.BySlug(ctx, slug)
}

func (a *App) metaFromPermalinkVars(ctx context.Context, vars map[string]string, typ string) (models.Meta, error) {
	if raw := vars["mid"]; raw != "" {
		id, _ := strconv.ParseInt(raw, 10, 64)
		m, err := a.Metas.ByID(ctx, id)
		if err != nil || m.Type != typ {
			return models.Meta{}, sql.ErrNoRows
		}
		return m, nil
	}
	slug := vars["slug"]
	if slug == "" {
		slug = path.Base(vars["directory"])
	}
	return a.Metas.BySlug(ctx, typ, slug)
}

func matchPermalink(pattern, value string) (map[string]string, bool) {
	pattern = cleanPublicPath(pattern)
	value = cleanPublicPath(value)
	var names []string
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(pattern); {
		if pattern[i] == '{' {
			end := strings.IndexByte(pattern[i:], '}')
			if end > 0 {
				name := pattern[i+1 : i+end]
				names = append(names, name)
				if name == "directory" {
					re.WriteString("(.+)")
				} else {
					re.WriteString("([^/]+)")
				}
				i += end + 1
				continue
			}
		}
		re.WriteString(regexp.QuoteMeta(string(pattern[i])))
		i++
	}
	re.WriteString("/?$")
	match := regexp.MustCompile(re.String()).FindStringSubmatch(value)
	if match == nil {
		return nil, false
	}
	out := map[string]string{}
	for i, name := range names {
		if i+1 < len(match) {
			out[name] = match[i+1]
		}
	}
	return out, true
}

func contentRouteSlug(c models.Content) string {
	if slug := strings.TrimSpace(c.Slug); slug != "" {
		return slug
	}
	if c.SlugID > 0 {
		return strconv.FormatInt(c.SlugID, 10)
	}
	if c.CID > 0 {
		return strconv.FormatInt(c.CID, 10)
	}
	return ""
}

func contentPublicURL(c models.Content) string {
	if c.Type == models.ContentTypePage {
		return "/page/" + contentRouteSlug(c) + ".html"
	}
	return "/post/" + contentRouteSlug(c) + ".html"
}

func contentListURL(typ string) string {
	if typ == models.ContentTypePage {
		return "/admin/pages"
	}
	return "/admin/posts"
}

func contentActionURL(typ string, id int64) string {
	base := "/admin/posts"
	if typ == models.ContentTypePage {
		base = "/admin/pages"
	}
	if id == 0 {
		return base + "/new"
	}
	return fmt.Sprintf("%s/%d/edit", base, id)
}

func contentFormTitle(typ string, id int64) string {
	if typ == models.ContentTypePage {
		if id == 0 {
			return "写页面"
		}
		return "编辑页面"
	}
	if id == 0 {
		return "写文章"
	}
	return "编辑文章"
}

func metaListURL(typ string) string {
	if typ == "tag" {
		return "/admin/tags"
	}
	return "/admin/categories"
}

func metaActionURL(typ string, id int64) string {
	base := metaListURL(typ)
	if id == 0 {
		return base + "/new"
	}
	return fmt.Sprintf("%s/%d/edit", base, id)
}

func metaTitle(typ string, id int64) string {
	name := "分类"
	if typ == "tag" {
		name = "标签"
	}
	if id == 0 {
		return "新增" + name
	}
	return "编辑" + name
}

func userActionURL(id int64) string {
	if id == 0 {
		return "/admin/users/new"
	}
	return fmt.Sprintf("/admin/users/%d/edit", id)
}

func userTitle(id int64) string {
	if id == 0 {
		return "新增用户"
	}
	return "编辑用户"
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func validatePermalinkOptions(r *http.Request) error {
	patterns := map[string]string{
		"文章路径": r.FormValue("permalink_post"),
		"页面路径": r.FormValue("permalink_page"),
		"分类路径": r.FormValue("permalink_category"),
	}
	seen := map[string]string{}
	for label, pattern := range patterns {
		pattern = cleanPublicPath(pattern)
		if pattern == "/" || !strings.Contains(pattern, "{") {
			return fmt.Errorf("%s 必须包含至少一个变量", label)
		}
		for _, reserved := range []string{"/admin", "/uploads", "/theme", "/feed.xml", "/atom.xml", "/comments", "/comment", "/search", "/archive", "/author", "/preview"} {
			if pattern == reserved || strings.HasPrefix(pattern, reserved+"/") {
				return fmt.Errorf("%s 与内置路径 %s 冲突", label, reserved)
			}
		}
		shape := permalinkShape(pattern)
		if prev := seen[shape]; prev != "" {
			return fmt.Errorf("%s 与 %s 规则冲突", label, prev)
		}
		seen[shape] = label
	}
	return nil
}

func permalinkShape(pattern string) string {
	re := regexp.MustCompile(`\{[^}]+\}`)
	return re.ReplaceAllString(cleanPublicPath(pattern), "{}")
}

func pluginOptionKey(name string) string {
	return "plugin:" + name
}

func pluginPersonalOptionKey(name string) string {
	return "plugin:" + name + ":personal"
}

func themeOptionKey(name string) string {
	return "theme:" + name
}

func (a *App) pluginConfig(ctx context.Context, name string) (map[string]string, error) {
	return a.optionJSONForUser(ctx, pluginOptionKey(name), 0)
}

func (a *App) themeConfig(ctx context.Context, name string) (map[string]string, error) {
	return a.optionJSONForUser(ctx, themeOptionKey(name), 0)
}

func (a *App) optionJSONForUser(ctx context.Context, key string, userID int64) (map[string]string, error) {
	raw, err := a.Options.GetForUser(ctx, key, userID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) setOptionJSONForUser(ctx context.Context, key string, values map[string]string, userID int64) error {
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return a.Options.SetForUser(ctx, key, string(data), userID)
}

func (a *App) applySchemaDefaults(schema []plugin.FieldSchema, values map[string]string) {
	for _, field := range schema {
		if _, ok := values[field.Name]; !ok && field.Default != "" {
			values[field.Name] = field.Default
		}
	}
}

type flashNotice struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (a *App) flashRedirect(w http.ResponseWriter, r *http.Request, target string, code int, notices ...flashNotice) {
	a.setFlash(w, r, notices...)
	http.Redirect(w, r, target, code)
}

func (a *App) setFlash(w http.ResponseWriter, r *http.Request, notices ...flashNotice) {
	if len(notices) == 0 {
		return
	}
	data, err := json.Marshal(notices)
	if err != nil {
		return
	}
	secret := a.option(r.Context(), "auth_secret", "goblog")
	value := base64.RawURLEncoding.EncodeToString(data)
	sig := flashSign(secret, value)
	options := a.cookieOptions(r.Context())
	http.SetCookie(w, &http.Cookie{
		Name:     options.Name("flash"),
		Value:    value + "." + sig,
		Path:     "/",
		MaxAge:   120,
		HttpOnly: true,
		SameSite: options.SameSite,
		Secure:   options.Secure,
	})
}

func (a *App) consumeFlash(w http.ResponseWriter, r *http.Request) []flashNotice {
	options := a.cookieOptions(r.Context())
	cookie, err := r.Cookie(options.Name("flash"))
	if err != nil || cookie.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: options.Name("flash"), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: options.SameSite, Secure: options.Secure})
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return nil
	}
	secret := a.option(r.Context(), "auth_secret", "goblog")
	if !hmac.Equal([]byte(flashSign(secret, parts[0])), []byte(parts[1])) {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil
	}
	var notices []flashNotice
	_ = json.Unmarshal(raw, &notices)
	return notices
}

func flashSign(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *App) cookieOptions(ctx context.Context) auth.CookieOptions {
	return auth.CookieOptions{
		Prefix:   a.option(ctx, "cookie_prefix", ""),
		Secure:   optionBool(a.option(ctx, "cookie_secure", "0")),
		HTTPOnly: true,
		SameSite: sameSiteMode(a.option(ctx, "cookie_samesite", "Lax")),
	}
}

func sameSiteMode(value string) http.SameSite {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

func (a *App) siteLocation(ctx context.Context) *time.Location {
	name := a.option(ctx, "site_timezone", "Local")
	if name == "" || name == "Local" {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}

func (a *App) formatDate(ctx context.Context, ts int64, optionName string) string {
	if ts <= 0 {
		return ""
	}
	layout := a.option(ctx, optionName, "2006-01-02 15:04")
	if strings.TrimSpace(layout) == "" {
		layout = "2006-01-02 15:04"
	}
	return time.Unix(ts, 0).In(a.siteLocation(ctx)).Format(layout)
}

func (a *App) addUserUniqueErrors(ctx context.Context, errs *validate.Errors, name, mail string, exceptID int64) {
	if exists, err := a.Users.ExistsName(ctx, name, exceptID); err == nil && exists {
		errs.Add("name", "用户名已存在")
	}
	if mail != "" {
		if exists, err := a.Users.ExistsMail(ctx, mail, exceptID); err == nil && exists {
			errs.Add("mail", "邮箱已存在")
		}
	}
}

func valuesFromSchema(r *http.Request, schema []plugin.FieldSchema) map[string]string {
	out := make(map[string]string, len(schema))
	for _, field := range schema {
		if field.Type == plugin.FieldCheckbox {
			if r.FormValue(field.Name) == "1" {
				out[field.Name] = "1"
			} else {
				out[field.Name] = "0"
			}
			continue
		}
		value := strings.TrimSpace(r.FormValue(field.Name))
		if field.Type == plugin.FieldNumber {
			value = normalizeSchemaNumber(value, field)
		}
		out[field.Name] = value
	}
	return out
}

func normalizeSchemaValues(schema []plugin.FieldSchema, values map[string]string) {
	for _, field := range schema {
		if field.Type != plugin.FieldNumber {
			continue
		}
		if value, ok := values[field.Name]; ok {
			values[field.Name] = normalizeSchemaNumber(value, field)
		}
	}
}

func normalizeSchemaNumber(value string, field plugin.FieldSchema) string {
	value = strings.TrimSpace(value)
	if value == "" || field.Step == "" {
		return value
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return value
	}
	if minValue, err := strconv.ParseFloat(strings.TrimSpace(field.Min), 64); err == nil && number < minValue {
		number = minValue
	}
	if maxValue, err := strconv.ParseFloat(strings.TrimSpace(field.Max), 64); err == nil && number > maxValue {
		number = maxValue
	}
	if digits := stepFractionDigits(field.Step); digits >= 0 {
		return strconv.FormatFloat(number, 'f', digits, 64)
	}
	return strconv.FormatFloat(number, 'f', -1, 64)
}

func stepFractionDigits(step string) int {
	step = strings.TrimSpace(step)
	if step == "" || strings.EqualFold(step, "any") {
		return -1
	}
	if idx := strings.IndexAny(step, "eE"); idx >= 0 {
		parsed, err := strconv.ParseFloat(step, 64)
		if err != nil {
			return -1
		}
		step = strconv.FormatFloat(parsed, 'f', -1, 64)
	}
	if dot := strings.IndexByte(step, '.'); dot >= 0 {
		return len(strings.TrimRight(step[dot+1:], "0"))
	}
	return 0
}

func schemaValue(values map[string]string, name string) string {
	if values == nil {
		return ""
	}
	return values[name]
}

func schemaChecked(values map[string]string, name string) bool {
	return checked(schemaValue(values, name))
}

func schemaOptionsAreColors(options []plugin.FieldOption) bool {
	if len(options) == 0 {
		return false
	}
	for _, option := range options {
		if adminAppearanceHexColor(option.Value) == "" {
			return false
		}
	}
	return true
}

func (a *App) activePluginSet(ctx context.Context) map[string]bool {
	raw, _ := a.Options.Get(ctx, "active_plugins")
	var names []string
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &names)
	}
	active := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			active[name] = true
		}
	}
	return active
}

func (a *App) saveActivePluginSet(ctx context.Context, active map[string]bool) error {
	names := make([]string, 0, len(active))
	for name, enabled := range active {
		if enabled {
			names = append(names, name)
		}
	}
	data, err := json.Marshal(names)
	if err != nil {
		return err
	}
	return a.Options.Set(ctx, "active_plugins", string(data))
}

func (a *App) syncActivePlugins(ctx context.Context) {
	active := a.activePluginSet(ctx)
	names := make([]string, 0, len(active))
	for name, enabled := range active {
		if enabled {
			names = append(names, name)
		}
	}
	a.Plugins.SetActivePlugins(names)
}

func (a *App) themeContentFields(ctx context.Context, typ string) []plugin.FieldSchema {
	theme, ok := a.activeTheme(ctx)
	if !ok {
		return nil
	}
	fields := make([]plugin.FieldSchema, 0, len(theme.ContentFields))
	for _, field := range theme.ContentFields {
		if len(field.ForTypes) == 0 || containsString(field.ForTypes, typ) {
			fields = append(fields, field)
		}
	}
	return fields
}

func mergeThemeFields(schema []plugin.FieldSchema, fields []models.Field) []models.Field {
	if len(schema) == 0 {
		return fields
	}
	seen := map[string]bool{}
	for _, field := range fields {
		seen[field.Name] = true
	}
	out := append([]models.Field(nil), fields...)
	for _, item := range schema {
		if item.Name == "" || seen[item.Name] {
			continue
		}
		out = append(out, models.Field{Name: item.Name, Type: "str", StrValue: item.Default})
	}
	return out
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func editableThemeExt(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".html", ".css", ".js", ".txt", ".md", ".json":
		return true
	default:
		return false
	}
}

func safeThemeEditPath(root, rel string) (string, bool) {
	if root == "" || rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	full, err := filepath.Abs(filepath.Join(cleanRoot, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	if full != cleanRoot && !strings.HasPrefix(full, cleanRoot+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return "", false
	}
	return full, true
}

func editableThemeFiles(root string) ([]string, error) {
	var files []string
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(cleanRoot, func(pathValue string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(cleanRoot, pathValue)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if editableThemeExt(rel) {
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json") || r.URL.Query().Get("format") == "json"
}

func fieldError(errors any, field string) string {
	if errors == nil {
		return ""
	}
	switch e := errors.(type) {
	case validate.Errors:
		return e.First(field)
	case map[string][]string:
		if len(e[field]) > 0 {
			return e[field][0]
		}
	}
	return ""
}

func fieldValue(f models.Field) string {
	switch f.Type {
	case "int":
		return strconv.FormatInt(f.IntValue, 10)
	case "float":
		return strconv.FormatFloat(f.FloatValue, 'f', -1, 64)
	default:
		return f.StrValue
	}
}

func safeNext(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "//") || strings.Contains(value, "://") || !strings.HasPrefix(value, "/") {
		return ""
	}
	return value
}

func optionInt(value string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return n
}

func optionBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func matchList(value, list string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, item := range splitList(list) {
		item = strings.ToLower(item)
		switch {
		case item == value:
			return true
		case strings.HasSuffix(item, "*") && strings.HasPrefix(value, strings.TrimSuffix(item, "*")):
			return true
		}
	}
	return false
}

func containsListItem(text, list string) bool {
	text = strings.ToLower(text)
	for _, item := range splitList(list) {
		if item != "" && strings.Contains(text, strings.ToLower(item)) {
			return true
		}
	}
	return false
}

func normalizeCommentURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") {
		return value
	}
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	if !strings.Contains(value, "://") {
		return "https://" + value
	}
	return value
}

var (
	commentTagPattern  = regexp.MustCompile(`(?is)<\s*(/)?\s*([a-zA-Z0-9]+)([^>]*)>`)
	commentHrefPattern = regexp.MustCompile(`(?is)\bhref\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
)

func sanitizeCommentHTML(text, allowedList string, nofollow bool) template.HTML {
	allowed := map[string]bool{}
	for _, tag := range splitList(allowedList) {
		tag = strings.ToLower(strings.Trim(tag, "<> /"))
		if tag != "" {
			allowed[tag] = true
		}
	}
	if len(allowed) == 0 {
		return render.PlainTextHTML(text)
	}
	var out strings.Builder
	last := 0
	matches := commentTagPattern.FindAllStringSubmatchIndex(text, -1)
	for _, match := range matches {
		out.WriteString(escapeCommentTextSegment(text[last:match[0]]))
		full := text[match[0]:match[1]]
		closing := match[2] >= 0 && strings.TrimSpace(text[match[2]:match[3]]) == "/"
		tag := strings.ToLower(text[match[4]:match[5]])
		attrs := ""
		if match[6] >= 0 && match[7] >= 0 {
			attrs = full[strings.Index(full, tag)+len(tag):]
		}
		if allowed[tag] {
			out.WriteString(safeCommentTag(tag, attrs, closing, nofollow))
		} else {
			out.WriteString(template.HTMLEscapeString(full))
		}
		last = match[1]
	}
	out.WriteString(escapeCommentTextSegment(text[last:]))
	return template.HTML(out.String())
}

func escapeCommentTextSegment(text string) string {
	return strings.ReplaceAll(template.HTMLEscapeString(text), "\n", "<br>")
}

func safeCommentTag(tag, attrs string, closing, nofollow bool) string {
	switch tag {
	case "a", "b", "strong", "i", "em", "code", "pre", "blockquote", "p", "ul", "ol", "li", "br":
	default:
		return ""
	}
	if closing {
		if tag == "br" {
			return ""
		}
		return "</" + tag + ">"
	}
	if tag == "br" {
		return "<br>"
	}
	if tag == "a" {
		href := commentHref(attrs)
		if href == "" {
			return "<a>"
		}
		rel := ""
		if nofollow {
			rel = ` rel="nofollow"`
		}
		return `<a href="` + template.HTMLEscapeString(href) + `"` + rel + `>`
	}
	return "<" + tag + ">"
}

func commentHref(attrs string) string {
	match := commentHrefPattern.FindStringSubmatch(attrs)
	if len(match) == 0 {
		return ""
	}
	for _, group := range match[2:] {
		if group != "" {
			href := normalizeCommentURL(group)
			u, err := neturl.Parse(href)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return ""
			}
			return href
		}
	}
	return ""
}

func sameHost(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	return a == b || stripPort(a) == stripPort(b)
}

func stripPort(host string) string {
	if strings.Count(host, ":") == 1 {
		if i := strings.LastIndex(host, ":"); i > -1 {
			return host[:i]
		}
	}
	return host
}

func pageURL(r *http.Request, page int) string {
	if page < 1 {
		page = 1
	}
	q := r.URL.Query()
	q.Set("page", strconv.Itoa(page))
	if len(q) == 0 {
		return r.URL.Path
	}
	return r.URL.Path + "?" + q.Encode()
}

func commentPageURL(r *http.Request, page int) string {
	if page < 1 {
		page = 1
	}
	q := r.URL.Query()
	q.Set("comments_page", strconv.Itoa(page))
	return r.URL.Path + "?" + q.Encode() + "#comments"
}

func commentReplyURL(r *http.Request, id int64) string {
	q := r.URL.Query()
	q.Set("reply", strconv.FormatInt(id, 10))
	return r.URL.Path + "?" + q.Encode() + "#comment-form"
}

func clientIP(r *http.Request) string {
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		if value := r.Header.Get(header); value != "" {
			return strings.TrimSpace(strings.Split(value, ",")[0])
		}
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > -1 {
		host = host[:i]
	}
	return host
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	name = strings.Trim(name, ".-")
	if len(name) > 120 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if len(base) > 100 {
			base = base[:100]
		}
		name = base + ext
	}
	return name
}

func dangerousUpload(name string) bool {
	lower := strings.ToLower(name)
	parts := strings.Split(lower, ".")
	for _, part := range parts[1:] {
		switch "." + part {
		case ".php", ".phtml", ".php3", ".php4", ".php5", ".phar", ".cgi", ".pl", ".py", ".rb", ".sh", ".bash", ".zsh", ".fish", ".exe", ".dll", ".so", ".jsp", ".asp", ".aspx", ".js", ".html", ".htm":
			return true
		}
	}
	return false
}

func allowedUploadExt(name, allowed string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if ext == "" {
		return false
	}
	items := splitList(allowed)
	if len(items) == 0 {
		return true
	}
	for _, item := range items {
		if strings.EqualFold(strings.TrimPrefix(item, "."), ext) {
			return true
		}
	}
	return false
}

func mimeAllowedForExt(ext, mimeType string) bool {
	ext = strings.ToLower(ext)
	mimeType = strings.ToLower(mimeType)
	if ext == "svg" {
		return strings.Contains(mimeType, "text/plain") || strings.Contains(mimeType, "image/svg") || strings.Contains(mimeType, "xml")
	}
	switch ext {
	case "jpg", "jpeg":
		return strings.HasPrefix(mimeType, "image/jpeg")
	case "png":
		return strings.HasPrefix(mimeType, "image/png")
	case "gif":
		return strings.HasPrefix(mimeType, "image/gif")
	case "webp":
		return strings.HasPrefix(mimeType, "image/webp") || mimeType == "application/octet-stream"
	case "pdf":
		return mimeType == "application/pdf" || strings.HasPrefix(mimeType, "application/octet-stream")
	case "txt", "md":
		return strings.HasPrefix(mimeType, "text/plain") || strings.HasPrefix(mimeType, "text/markdown")
	case "zip":
		return mimeType == "application/zip" || mimeType == "application/x-zip-compressed" || mimeType == "application/octet-stream"
	default:
		return !strings.HasPrefix(mimeType, "text/html") && !strings.Contains(mimeType, "javascript")
	}
}

func uniqueUploadName(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		candidate := fmt.Sprintf("%d-%s", time.Now().UnixNano(), name)
		if i > 0 {
			candidate = fmt.Sprintf("%d-%s-%d%s", time.Now().UnixNano(), base, i, ext)
		}
		if _, err := os.Stat(filepath.Join(dir, candidate)); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), name)
}
