package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
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
	"goblog/pkg/render"
)

type App struct {
	Contents  *services.ContentService
	Metas     *services.MetaService
	Comments  *services.CommentService
	Users     *services.UserService
	Options   *services.OptionService
	Plugins   *plugin.Manager
	UploadDir string
	loginMu   sync.Mutex
	loginNext map[string]time.Time
}

type contextKey string

const currentUserKey contextKey = "currentUser"

func New(contents *services.ContentService, metas *services.MetaService, comments *services.CommentService, users *services.UserService, options *services.OptionService, plugins *plugin.Manager) *App {
	uploadDir := os.Getenv("GOBLOG_UPLOAD_DIR")
	if uploadDir == "" {
		uploadDir = filepath.Join("data", "uploads")
	}
	return &App{Contents: contents, Metas: metas, Comments: comments, Users: users, Options: options, Plugins: plugins, UploadDir: uploadDir, loginNext: map[string]time.Time{}}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	adminAssets, _ := fs.Sub(admin.FS, "assets")
	mux.Handle("/admin/assets/", http.StripPrefix("/admin/assets/", http.FileServer(http.FS(adminAssets))))

	if theme, ok := a.activeTheme(context.Background()); ok && theme.Static != nil {
		mux.Handle("/theme/default/", http.StripPrefix("/theme/default/", http.FileServer(http.FS(theme.Static))))
	}
	if a.UploadDir != "" {
		mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(a.UploadDir))))
	}

	mux.HandleFunc("/admin/login", a.adminLogin)
	mux.HandleFunc("/admin/logout", a.adminLogout)

	adminRoutes := map[string]http.HandlerFunc{
		"/admin":                    a.adminDashboard,
		"/admin/":                   a.adminDashboard,
		"/admin/posts":              a.adminPosts,
		"/admin/posts/":             a.adminPostRoutes,
		"/admin/pages":              a.adminPages,
		"/admin/pages/":             a.adminPageRoutes,
		"/admin/categories":         a.adminCategories,
		"/admin/categories/":        a.adminCategoryRoutes,
		"/admin/tags":               a.adminTags,
		"/admin/tags/":              a.adminTagRoutes,
		"/admin/comments":           a.adminComments,
		"/admin/comments/":          a.adminCommentRoutes,
		"/admin/users":              a.adminUsers,
		"/admin/users/":             a.adminUserRoutes,
		"/admin/profile":            a.adminProfile,
		"/admin/options":            a.adminOptionsGeneral,
		"/admin/options/general":    a.adminOptionsGeneral,
		"/admin/options/reading":    a.adminOptionsReading,
		"/admin/options/discussion": a.adminOptionsDiscussion,
		"/admin/options/permalink":  a.adminOptionsPermalink,
		"/admin/themes":             a.adminThemes,
		"/admin/plugins":            a.adminPlugins,
		"/admin/medias":             a.adminMedias,
		"/admin/medias/":            a.adminMediaRoutes,
		"/admin/backup":             a.adminBackup,
		"/admin/autosave":           a.adminAutosave,
		"/admin/tags/search":        a.adminTagSearch,
		"/admin/theme-editor":       a.adminPlaceholder("主题编辑器", "对应 Typecho 的 theme-editor.php。直接编辑文件需要额外权限和审计，当前先保留入口。"),
	}
	for route, handler := range adminRoutes {
		mux.HandleFunc(route, a.requireAdmin(handler))
	}

	runtime := &plugin.Runtime{ListPublished: a.Contents.ListPublishedPlugin, Option: a.Options.Get}
	for _, route := range a.Plugins.Routes() {
		route := route
		mux.HandleFunc(route.Pattern, func(w http.ResponseWriter, r *http.Request) {
			if route.Method != "" && r.Method != route.Method {
				methodNotAllowed(w, route.Method)
				return
			}
			route.Handler(runtime, w, r)
		})
	}

	mux.HandleFunc("/feed.xml", a.frontRSS)
	mux.HandleFunc("/atom.xml", a.frontRSS)
	mux.HandleFunc("/comment", a.frontComment)
	mux.HandleFunc("/preview/", a.frontPreview)
	mux.HandleFunc("/post/", a.frontPost)
	mux.HandleFunc("/page/", a.frontPage)
	mux.HandleFunc("/category/", a.frontCategory)
	mux.HandleFunc("/tag/", a.frontTag)
	mux.HandleFunc("/author/", a.frontAuthor)
	mux.HandleFunc("/search", a.frontSearch)
	mux.HandleFunc("/archive/", a.frontArchive)
	mux.HandleFunc("/", a.frontIndex)
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
		auth.SetSession(w, secret, user.UID)
		if next == "" {
			next = "/admin"
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
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
	auth.ClearSession(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (a *App) adminDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.NotFound(w, r)
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
	categories, _ := a.Metas.List(r.Context(), "category")
	a.renderAdmin(w, r, "posts.html", map[string]any{"Title": "文章", "Posts": posts, "Categories": categories, "Status": r.URL.Query().Get("status"), "Keywords": r.URL.Query().Get("keywords"), "Category": category})
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
	pages, err := a.Contents.List(r.Context(), services.ContentQuery{Type: models.ContentTypePage, Keywords: r.URL.Query().Get("keywords"), Limit: 200})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, r, "pages.html", map[string]any{"Title": "页面", "Pages": pages, "Keywords": r.URL.Query().Get("keywords")})
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
		http.Redirect(w, r, contentActionURL(typ, id)+"?saved=1", http.StatusSeeOther)
	case "delete":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !a.canEditContent(w, r, id, typ) {
			return
		}
		if err := a.Contents.Delete(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, contentListURL(typ), http.StatusSeeOther)
	case "mark":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !a.canEditContent(w, r, id, typ) {
			return
		}
		if err := a.Contents.MarkStatus(r.Context(), id, r.FormValue("status")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, contentListURL(typ), http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) contentForm(w http.ResponseWriter, r *http.Request, typ string, id int64) {
	var item models.Content
	var err error
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
	} else {
		item = models.Content{Type: typ, Status: models.ContentStatusPost, AllowComment: "1", AllowFeed: "1"}
	}

	switch r.Method {
	case http.MethodGet:
		categories, _ := a.Metas.List(r.Context(), "category")
		pages, _ := a.Contents.List(r.Context(), services.ContentQuery{Type: models.ContentTypePage, Limit: 200})
		selectedCategories, _ := a.Metas.CategoriesForContent(r.Context(), id)
		selectedTags, _ := a.Metas.TagsForContent(r.Context(), id)
		fields, _ := a.Contents.FieldsForContent(r.Context(), id)
		revisions, _ := a.Contents.Revisions(r.Context(), id)
		a.renderAdmin(w, r, "content_form.html", map[string]any{
			"Title":              contentFormTitle(typ, id),
			"Content":            item,
			"Type":               typ,
			"Action":             contentActionURL(typ, id),
			"Saved":              r.URL.Query().Get("saved") == "1",
			"Categories":         categories,
			"Pages":              pages,
			"SelectedCategories": selectedCategories,
			"SelectedTags":       selectedTags,
			"Fields":             fields,
			"Revisions":          revisions,
			"PreviewURL":         a.previewURL(r, item),
		})
	case http.MethodPost:
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
		if id == 0 {
			id, err = a.Contents.Create(r.Context(), input, uid)
		} else {
			err = a.Contents.Update(r.Context(), id, input)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, contentActionURL(typ, id)+"?saved=1", http.StatusSeeOther)
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
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
	if selectedCategories == nil {
		selectedCategories, _ = a.Metas.CategoriesForContent(r.Context(), id)
	}
	if selectedTags == nil {
		selectedTags, _ = a.Metas.TagsForContent(r.Context(), id)
	}
	if fields == nil {
		fields, _ = a.Contents.FieldsForContent(r.Context(), id)
	}
	revisions, _ := a.Contents.Revisions(r.Context(), id)
	a.renderAdmin(w, r, "content_form.html", map[string]any{
		"Title":              contentFormTitle(typ, id),
		"Content":            item,
		"Type":               typ,
		"Action":             contentActionURL(typ, id),
		"Categories":         categories,
		"Pages":              pages,
		"SelectedCategories": selectedCategories,
		"SelectedTags":       selectedTags,
		"Errors":             errs,
		"Fields":             fields,
		"Revisions":          revisions,
		"PreviewURL":         a.previewURL(r, item),
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
	comments, err := a.Comments.List(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("keywords"), cid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, r, "comments.html", map[string]any{"Title": "评论", "Comments": comments, "Status": r.URL.Query().Get("status"), "Keywords": r.URL.Query().Get("keywords"), "CID": cid})
}

func (a *App) adminCommentRoutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "editor") {
		return
	}
	clean := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/comments/"), "/")
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
		http.Redirect(w, r, "/admin/comments", http.StatusSeeOther)
	case "delete":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if err := a.Comments.Delete(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/comments", http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
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
		http.Redirect(w, r, "/admin/comments", http.StatusSeeOther)
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
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
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
		if errs := validateUserInput(input, id == 0); !errs.Empty() {
			user = models.User{UID: id, Name: input.Name, Mail: input.Mail, URL: input.URL, ScreenName: input.ScreenName, Role: input.Role}
			a.renderAdmin(w, r, "user_form.html", map[string]any{"Title": userTitle(id), "User": user, "Action": userActionURL(id), "Errors": errs})
			return
		}
		_, err := a.Users.Save(r.Context(), input, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
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
		http.Redirect(w, r, "/admin/profile?saved=1", http.StatusSeeOther)
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminOptionsGeneral(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.optionsForm(w, r, "基本设置", "options_general.html", []string{"site_title", "site_description", "site_keywords", "base_url", "allow_register", "active_theme"})
}

func (a *App) adminOptionsReading(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.optionsForm(w, r, "阅读设置", "options_reading.html", []string{"post_date_format", "page_size", "posts_list_size", "content_render_mode", "feed_full_text"})
}

func (a *App) adminOptionsDiscussion(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.optionsForm(w, r, "评论设置", "options_discussion.html", []string{"comments_require_moderation", "comments_require_mail", "comments_show_url", "comments_order"})
}

func (a *App) adminOptionsPermalink(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
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
		http.Redirect(w, r, r.URL.Path+"?saved=1", http.StatusSeeOther)
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
		if err := a.Options.Set(r.Context(), "active_theme", r.FormValue("theme")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/themes?saved=1", http.StatusSeeOther)
		return
	}
	a.renderAdmin(w, r, "themes.html", map[string]any{"Title": "主题", "Themes": []string{"default"}, "Saved": r.URL.Query().Get("saved") == "1"})
}

func (a *App) adminPlugins(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "administrator") {
		return
	}
	a.renderAdmin(w, r, "plugins.html", map[string]any{"Title": "插件", "Plugins": a.Plugins.Plugins()})
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
	if id == 0 {
		id, err = a.Contents.Create(r.Context(), input, user.UID)
	} else {
		err = a.Contents.Update(r.Context(), id, input)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	item, _ := a.Contents.ByID(r.Context(), id)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "cid": id, "preview": a.previewURL(r, item)})
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
		a.renderAdmin(w, r, "medias.html", map[string]any{"Title": "附件", "Medias": medias, "Posts": posts, "Saved": r.URL.Query().Get("saved") == "1"})
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
		relPath, err := a.saveUpload(file, header.Filename)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := a.Contents.CreateAttachment(r.Context(), header.Filename, strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath)), "/uploads/"+relPath, user.UID, parent); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/medias?saved=1", http.StatusSeeOther)
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
	if len(parts) < 2 || parts[1] != "delete" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
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
	if err := a.Contents.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.HasPrefix(item.Text, "/uploads/") {
		_ = os.Remove(filepath.Join(a.UploadDir, strings.TrimPrefix(item.Text, "/uploads/")))
	}
	http.Redirect(w, r, "/admin/medias", http.StatusSeeOther)
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
		a.renderAdmin(w, r, "backup.html", map[string]any{"Title": "备份"})
	case http.MethodPost:
		if r.FormValue("action") != "export" {
			http.Error(w, "unsupported backup action", http.StatusBadRequest)
			return
		}
		payload, err := a.backupPayload(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="goblog-backup.json"`)
		_ = json.NewEncoder(w).Encode(payload)
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
	a.renderPostList(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost}, "")
}

func (a *App) frontPost(w http.ResponseWriter, r *http.Request) {
	postSlug := path.Base(strings.TrimSuffix(r.URL.Path, "/"))
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
	comments, _ := a.Comments.ListForContent(r.Context(), post.CID, a.option(r.Context(), "comments_order", "ASC"))
	categories, _ := a.Metas.CategoriesForContent(r.Context(), post.CID)
	tags, _ := a.Metas.TagsForContent(r.Context(), post.CID)
	fields, _ := a.Contents.FieldMap(r.Context(), post.CID)
	prev, next, _ := a.Contents.Adjacent(r.Context(), post)
	a.renderTheme(w, r, "post.html", map[string]any{
		"Post":         post,
		"ContentHTML":  render.ContentHTML(post.Text, a.option(r.Context(), "content_render_mode", "markdown")),
		"Comments":     comments,
		"Categories":   categories,
		"Tags":         tags,
		"Fields":       fields,
		"PrevPost":     prev,
		"NextPost":     next,
		"CommentError": r.URL.Query().Get("comment_error"),
		"CommentOK":    r.URL.Query().Get("comment_ok") == "1",
	})
}

func (a *App) frontPage(w http.ResponseWriter, r *http.Request) {
	pageSlug := path.Base(strings.TrimSuffix(r.URL.Path, "/"))
	pageData, err := a.Contents.PageBySlug(r.Context(), pageSlug)
	if err != nil {
		a.renderThemeStatus(w, r, "404.html", map[string]any{}, http.StatusNotFound)
		return
	}
	fields, _ := a.Contents.FieldMap(r.Context(), pageData.CID)
	a.renderTheme(w, r, "post.html", map[string]any{"Post": pageData, "ContentHTML": render.ContentHTML(pageData.Text, a.option(r.Context(), "content_render_mode", "markdown")), "Fields": fields})
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
	a.renderTheme(w, r, "post.html", map[string]any{
		"Post":        item,
		"ContentHTML": render.ContentHTML(item.Text, a.option(r.Context(), "content_render_mode", "markdown")),
		"Fields":      fields,
		"Comments":    []models.Comment{},
		"Categories":  []models.Meta{},
		"Tags":        []models.Meta{},
		"PrevPost":    models.Content{},
		"NextPost":    models.Content{},
		"Preview":     true,
	})
}

func (a *App) frontCategory(w http.ResponseWriter, r *http.Request) {
	meta, err := a.Metas.BySlug(r.Context(), "category", path.Base(strings.TrimSuffix(r.URL.Path, "/")))
	if err != nil {
		a.renderThemeStatus(w, r, "404.html", map[string]any{}, http.StatusNotFound)
		return
	}
	a.renderPostList(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Category: meta.MID}, "分类："+meta.Name)
}

func (a *App) frontTag(w http.ResponseWriter, r *http.Request) {
	meta, err := a.Metas.BySlug(r.Context(), "tag", path.Base(strings.TrimSuffix(r.URL.Path, "/")))
	if err != nil {
		a.renderThemeStatus(w, r, "404.html", map[string]any{}, http.StatusNotFound)
		return
	}
	a.renderPostList(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Tag: meta.MID}, "标签："+meta.Name)
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
	a.renderPostList(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, AuthorID: id}, "作者："+name)
}

func (a *App) frontSearch(w http.ResponseWriter, r *http.Request) {
	keywords := strings.TrimSpace(r.URL.Query().Get("q"))
	a.renderPostList(w, r, services.ContentQuery{Type: models.ContentTypePost, Status: models.ContentStatusPost, Keywords: keywords}, "搜索："+keywords)
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
	a.renderPostList(w, r, query, title)
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
	if err != nil || post.Type != models.ContentTypePost || post.Status != models.ContentStatusPost {
		http.NotFound(w, r)
		return
	}
	redirectTo := "/post/" + post.Slug
	if ref := r.Referer(); ref != "" && !strings.Contains(ref, redirectTo) {
		http.Redirect(w, r, redirectTo+"?comment_error=referer", http.StatusSeeOther)
		return
	}
	if post.AllowComment != "1" {
		http.Redirect(w, r, redirectTo+"?comment_error=closed", http.StatusSeeOther)
		return
	}
	ip := clientIP(r)
	recent, _ := a.Comments.CountRecentByIP(r.Context(), ip, time.Now().Add(-30*time.Second).Unix())
	if recent > 0 {
		http.Redirect(w, r, redirectTo+"?comment_error=frequent", http.StatusSeeOther)
		return
	}
	if r.FormValue("website") != "" {
		http.Redirect(w, r, redirectTo+"?comment_error=spam", http.StatusSeeOther)
		return
	}
	author := strings.TrimSpace(r.FormValue("author"))
	mail := strings.TrimSpace(r.FormValue("mail"))
	text := strings.TrimSpace(r.FormValue("text"))
	if author == "" || text == "" || (a.option(r.Context(), "comments_require_mail", "1") == "1" && mail == "") {
		http.Redirect(w, r, redirectTo+"?comment_error=required", http.StatusSeeOther)
		return
	}
	if registered, err := a.Users.ByName(r.Context(), author); err == nil && registered.UID > 0 {
		http.Redirect(w, r, redirectTo+"?comment_error=reserved", http.StatusSeeOther)
		return
	}
	status := "approved"
	if a.option(r.Context(), "comments_require_moderation", "0") == "1" {
		status = "waiting"
	}
	parent, _ := strconv.ParseInt(r.FormValue("parent"), 10, 64)
	input := services.SaveCommentInput{CID: cid, Author: author, Mail: mail, URL: strings.TrimSpace(r.FormValue("url")), Text: text, Status: status, Parent: parent, IP: ip, Agent: r.UserAgent()}
	if errs := validatePublicCommentInput(input, a.option(r.Context(), "comments_require_mail", "1") == "1"); !errs.Empty() {
		http.Redirect(w, r, redirectTo+"?comment_error=invalid", http.StatusSeeOther)
		return
	}
	if err := a.Comments.Save(r.Context(), input, 0); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "comment_author", Value: author, Path: "/", MaxAge: 86400 * 365})
	http.SetCookie(w, &http.Cookie{Name: "comment_mail", Value: mail, Path: "/", MaxAge: 86400 * 365})
	http.Redirect(w, r, redirectTo+"?comment_ok=1#comments", http.StatusSeeOther)
}

func (a *App) frontRSS(w http.ResponseWriter, r *http.Request) {
	posts, err := a.Contents.ListPublished(r.Context(), optionInt(a.option(r.Context(), "posts_list_size", "10"), 10), 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	site, _ := a.Options.All(r.Context())
	baseURL := strings.TrimRight(site["base_url"], "/")
	items := make([]rssItem, 0, len(posts))
	for _, post := range posts {
		text := render.Excerpt(post.Text, 240)
		if site["feed_full_text"] == "1" {
			text = post.Text
		}
		items = append(items, rssItem{Title: post.Title, Link: baseURL + "/post/" + post.Slug, GUID: baseURL + "/post/" + post.Slug, PubDate: time.Unix(post.Created, 0).Format(time.RFC1123Z), Description: text})
	}
	feed := rssFeed{Version: "2.0", Channel: rssChannel{Title: site["site_title"], Link: baseURL, Description: site["site_description"], Items: items}}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(feed)
}

func (a *App) renderPostList(w http.ResponseWriter, r *http.Request, query services.ContentQuery, title string) {
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
	a.renderTheme(w, r, "index.html", map[string]any{
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
	})
}

func (a *App) saveUpload(src io.Reader, original string) (string, error) {
	now := time.Now()
	dir := filepath.Join(a.UploadDir, now.Format("2006"), now.Format("01"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	name := sanitizeFilename(original)
	if name == "" {
		name = "file"
	}
	if dangerousUpload(name) {
		return "", fmt.Errorf("不允许上传该文件类型")
	}
	targetName := fmt.Sprintf("%d-%s", now.UnixNano(), name)
	dst, err := os.Create(filepath.Join(dir, targetName))
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	return path.Join(now.Format("2006"), now.Format("01"), targetName), nil
}

func (a *App) backupPayload(ctx context.Context) (backupData, error) {
	var out backupData
	out.GeneratedAt = time.Now().Format(time.RFC3339)
	options, err := a.Options.All(ctx)
	if err != nil {
		return out, err
	}
	out.Options = options
	for _, typ := range []string{models.ContentTypePost, models.ContentTypePage, models.ContentTypeAttach} {
		items, err := a.Contents.List(ctx, services.ContentQuery{Type: typ, Status: "all", Limit: 10000})
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
	comments, err := a.Comments.List(ctx, "all", "", 0)
	if err != nil {
		return out, err
	}
	out.Comments = comments
	return out, nil
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

type backupData struct {
	GeneratedAt string            `json:"generated_at"`
	Options     map[string]string `json:"options"`
	Contents    []models.Content  `json:"contents"`
	Metas       []models.Meta     `json:"metas"`
	Comments    []models.Comment  `json:"comments"`
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
			auth.ClearSession(w)
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
	return auth.ParseSession(r, secret)
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

func (a *App) validPreviewToken(r *http.Request, c models.Content) bool {
	token := r.URL.Query().Get("token")
	if token == "" {
		return false
	}
	expected := a.previewToken(r, c)
	return hmac.Equal([]byte(token), []byte(expected))
}

func (a *App) renderAdmin(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	funcs := template.FuncMap{
		"date":             formatDate,
		"statusLabel":      statusLabel,
		"contentStatus":    contentStatusLabel,
		"roleLabel":        roleLabel,
		"excerpt":          render.Excerpt,
		"containsMeta":     containsMeta,
		"joinMetaNames":    joinMetaNames,
		"checked":          checked,
		"contentPublicURL": contentPublicURL,
		"fieldError":       fieldError,
		"fieldValue":       fieldValue,
	}
	tmpl, err := template.New("base.html").Funcs(funcs).ParseFS(admin.FS, "templates/base.html", "templates/"+page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.enrichData(r.Context(), data)
	data["CSRF"] = a.csrfToken(r)
	if user, ok := a.currentUser(r); ok {
		data["CurrentUser"] = user
	}
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) renderTheme(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	a.renderThemeStatus(w, r, page, data, http.StatusOK)
}

func (a *App) renderThemeStatus(w http.ResponseWriter, r *http.Request, page string, data map[string]any, status int) {
	theme, ok := a.activeTheme(r.Context())
	if !ok {
		http.Error(w, "active theme not found", http.StatusInternalServerError)
		return
	}
	funcs := template.FuncMap{"date": formatDate, "excerpt": render.Excerpt, "commentHTML": render.PlainTextHTML}
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
	data["CSRF"] = a.csrfToken(r)
	data["CommentCSRF"] = a.csrfTokenFor(r, "comment")
	if site, ok := data["Site"].(map[string]string); ok {
		data["CurrentURL"] = strings.TrimRight(site["base_url"], "/") + r.URL.RequestURI()
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
	return a.Plugins.Theme(name)
}

func (a *App) enrichData(ctx context.Context, data map[string]any) {
	options, err := a.Options.All(ctx)
	if err == nil {
		data["Site"] = options
	}
}

func (a *App) enrichThemeData(ctx context.Context, data map[string]any) {
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
	if _, ok := data["Tags"]; !ok {
		if tags, err := a.Metas.ListCloud(ctx, "tag", 30); err == nil {
			data["Tags"] = tags
		}
	}
	if _, ok := data["RecentComments"]; !ok {
		if comments, err := a.Comments.List(ctx, "approved", "", 0); err == nil && len(comments) > 5 {
			data["RecentComments"] = comments[:5]
		} else if err == nil {
			data["RecentComments"] = comments
		}
	}
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
	return services.SaveContentInput{
		Title:        strings.TrimSpace(r.FormValue("title")),
		Slug:         strings.TrimSpace(r.FormValue("slug")),
		Text:         strings.TrimSpace(r.FormValue("text")),
		Type:         typ,
		Status:       r.FormValue("status"),
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

func validatePublicCommentInput(input services.SaveCommentInput, requireMail bool) validate.Errors {
	v := validate.New()
	v.Required("author", input.Author).
		MaxLength("author", input.Author, 150).
		URL("url", input.URL).
		Required("text", input.Text).
		SafeText("text", input.Text)
	if requireMail {
		v.Required("mail", input.Mail)
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

func contentPublicURL(c models.Content) string {
	if c.Type == models.ContentTypePage {
		return "/page/" + c.Slug
	}
	return "/post/" + c.Slug
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
	switch strings.ToLower(filepath.Ext(name)) {
	case ".php", ".phtml", ".php3", ".php4", ".php5", ".phar", ".cgi", ".pl", ".py", ".rb", ".sh", ".bash", ".zsh", ".fish", ".exe", ".dll", ".so", ".jsp", ".asp", ".aspx":
		return true
	default:
		return false
	}
}
