package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"goblog/admin"
	"goblog/core/models"
	"goblog/core/plugin"
	"goblog/core/services"
	"goblog/pkg/auth"
	"goblog/pkg/render"
)

type App struct {
	Contents *services.ContentService
	Metas    *services.MetaService
	Comments *services.CommentService
	Users    *services.UserService
	Options  *services.OptionService
	Plugins  *plugin.Manager
}

func New(contents *services.ContentService, metas *services.MetaService, comments *services.CommentService, users *services.UserService, options *services.OptionService, plugins *plugin.Manager) *App {
	return &App{Contents: contents, Metas: metas, Comments: comments, Users: users, Options: options, Plugins: plugins}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	adminAssets, _ := fs.Sub(admin.FS, "assets")
	mux.Handle("/admin/assets/", http.StripPrefix("/admin/assets/", http.FileServer(http.FS(adminAssets))))

	if theme, ok := a.activeTheme(context.Background()); ok && theme.Static != nil {
		mux.Handle("/theme/default/", http.StripPrefix("/theme/default/", http.FileServer(http.FS(theme.Static))))
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
		"/admin/medias":             a.adminPlaceholder("附件管理", "对应 Typecho 的 manage-medias.php / media.php，文件上传与附件归属将在下一阶段实现。"),
		"/admin/backup":             a.adminPlaceholder("数据备份", "对应 Typecho 的 backup.php，导入导出需要数据库方言适配，当前先保留入口。"),
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

	mux.HandleFunc("/post/", a.frontPost)
	mux.HandleFunc("/page/", a.frontPage)
	mux.HandleFunc("/", a.frontIndex)
	return mux
}

func (a *App) adminLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.renderAdmin(w, r, "login.html", map[string]any{"Title": "登录"})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		user, err := a.Users.Authenticate(r.Context(), r.FormValue("name"), r.FormValue("password"))
		if err != nil {
			a.renderAdmin(w, r, "login.html", map[string]any{"Title": "登录", "Error": "用户名或密码不正确"})
			return
		}
		secret, _ := a.Options.Get(r.Context(), "auth_secret")
		auth.SetSession(w, secret, user.UID)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (a *App) adminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
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
	category, _ := strconv.ParseInt(r.URL.Query().Get("category"), 10, 64)
	posts, err := a.Contents.List(r.Context(), services.ContentQuery{
		Type:     models.ContentTypePost,
		Status:   r.URL.Query().Get("status"),
		Keywords: r.URL.Query().Get("keywords"),
		Category: category,
		Limit:    200,
	})
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
		a.contentForm(w, r, typ, id)
	case "delete":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
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
		})
	case http.MethodPost:
		input, err := parseContentForm(r, typ)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
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

func (a *App) adminCategories(w http.ResponseWriter, r *http.Request) {
	a.metaList(w, r, "category", "分类", "categories.html")
}

func (a *App) adminCategoryRoutes(w http.ResponseWriter, r *http.Request) {
	a.metaRoutes(w, r, "/admin/categories/", "category")
}

func (a *App) adminTags(w http.ResponseWriter, r *http.Request) {
	a.metaList(w, r, "tag", "标签", "tags.html")
}

func (a *App) adminTagRoutes(w http.ResponseWriter, r *http.Request) {
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
		_, err := a.Metas.Save(r.Context(), services.SaveMetaInput{Name: r.FormValue("name"), Slug: r.FormValue("slug"), Type: typ, Description: r.FormValue("description"), Parent: parent}, id)
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
	cid, _ := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
	comments, err := a.Comments.List(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("keywords"), cid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, r, "comments.html", map[string]any{"Title": "评论", "Comments": comments, "Status": r.URL.Query().Get("status"), "Keywords": r.URL.Query().Get("keywords"), "CID": cid})
}

func (a *App) adminCommentRoutes(w http.ResponseWriter, r *http.Request) {
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
		input := services.SaveCommentInput{CID: comment.CID, Author: r.FormValue("author"), Mail: r.FormValue("mail"), URL: r.FormValue("url"), Text: r.FormValue("text"), Status: r.FormValue("status")}
		if reply {
			input.Parent = comment.COID
			if input.Author == "" {
				input.Author = "admin"
			}
			if input.Status == "" {
				input.Status = "approved"
			}
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
	users, err := a.Users.List(r.Context(), r.URL.Query().Get("keywords"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, r, "users.html", map[string]any{"Title": "用户", "Users": users, "Keywords": r.URL.Query().Get("keywords")})
}

func (a *App) adminUserRoutes(w http.ResponseWriter, r *http.Request) {
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
		_, err := a.Users.Save(r.Context(), services.SaveUserInput{Name: r.FormValue("name"), Password: r.FormValue("password"), Mail: r.FormValue("mail"), URL: r.FormValue("url"), ScreenName: r.FormValue("screenName"), Role: r.FormValue("role")}, id)
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
		if _, err := a.Users.Save(r.Context(), services.SaveUserInput{Name: user.Name, Mail: r.FormValue("mail"), URL: r.FormValue("url"), ScreenName: r.FormValue("screenName"), Role: user.Role}, uid); err != nil {
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
	a.optionsForm(w, r, "基本设置", "options_general.html", []string{"site_title", "site_description", "site_keywords", "base_url", "allow_register", "active_theme"})
}

func (a *App) adminOptionsReading(w http.ResponseWriter, r *http.Request) {
	a.optionsForm(w, r, "阅读设置", "options_reading.html", []string{"post_date_format", "page_size", "posts_list_size", "feed_full_text"})
}

func (a *App) adminOptionsDiscussion(w http.ResponseWriter, r *http.Request) {
	a.optionsForm(w, r, "评论设置", "options_discussion.html", []string{"comments_require_moderation", "comments_require_mail", "comments_show_url", "comments_order"})
}

func (a *App) adminOptionsPermalink(w http.ResponseWriter, r *http.Request) {
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
	a.renderAdmin(w, r, "plugins.html", map[string]any{"Title": "插件", "Plugins": a.Plugins.Plugins()})
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
	posts, err := a.Contents.ListPublished(r.Context(), 20, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderTheme(w, r, "index.html", map[string]any{"Posts": posts})
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
	a.renderTheme(w, r, "post.html", map[string]any{"Post": post, "ContentHTML": render.PlainTextHTML(post.Text)})
}

func (a *App) frontPage(w http.ResponseWriter, r *http.Request) {
	pageSlug := path.Base(strings.TrimSuffix(r.URL.Path, "/"))
	pageData, err := a.Contents.PageBySlug(r.Context(), pageSlug)
	if err != nil {
		a.renderThemeStatus(w, r, "404.html", map[string]any{}, http.StatusNotFound)
		return
	}
	a.renderTheme(w, r, "post.html", map[string]any{"Post": pageData, "ContentHTML": render.PlainTextHTML(pageData.Text)})
}

func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.currentUserID(r); !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (a *App) currentUserID(r *http.Request) (int64, bool) {
	secret, err := a.Options.Get(r.Context(), "auth_secret")
	if err != nil || secret == "" {
		return 0, false
	}
	return auth.ParseSession(r, secret)
}

func (a *App) renderAdmin(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	funcs := template.FuncMap{
		"date":             formatDate,
		"statusLabel":      statusLabel,
		"roleLabel":        roleLabel,
		"excerpt":          render.Excerpt,
		"containsMeta":     containsMeta,
		"joinMetaNames":    joinMetaNames,
		"checked":          checked,
		"contentPublicURL": contentPublicURL,
	}
	tmpl, err := template.New("base.html").Funcs(funcs).ParseFS(admin.FS, "templates/base.html", "templates/"+page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.enrichData(r.Context(), data)
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
	funcs := template.FuncMap{"date": formatDate, "excerpt": render.Excerpt}
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
		if pages, err := a.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePage, Status: models.ContentStatusPost, Limit: 20}); err == nil {
			data["Pages"] = pages
		}
	}
	if _, ok := data["Tags"]; !ok {
		if tags, err := a.Metas.List(ctx, "tag"); err == nil {
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
	}, nil
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
