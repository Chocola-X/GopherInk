package plugin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Chocola-X/GopherInk/pkg/safeid"
	"github.com/Chocola-X/GopherInk/pkg/sqlutil"
)

const GopherInkVersion = "0.5.0"

type PublicContent struct {
	CID          int64
	Title        string
	Slug         string
	SlugID       int64
	Created      int64
	Modified     int64
	Text         string
	Type         string
	Status       string
	AuthorID     int64
	Password     string
	CommentsNum  int64
	AllowComment string
	AllowPing    string
	AllowFeed    string
	Template     string
	Parent       int64
	SortOrder    int64
	DraftOf      int64
}

type PublicUser struct {
	UID        int64
	Name       string
	Mail       string
	URL        string
	ScreenName string
	Role       string
}

type PublicComment struct {
	COID     int64
	CID      int64
	Created  int64
	Author   string
	AuthorID int64
	// OwnerID is the author UID of the content this comment belongs to.
	OwnerID int64
	Mail    string
	URL     string
	IP      string
	Agent   string
	Text    string
	Type    string
	Status  string
	Parent  int64
	// Title, Slug and ContentType carry the source metadata that trackback and
	// pingback comments store; for a normal comment Title/Slug are empty and
	// ContentType mirrors the parent content type.
	Title       string
	Slug        string
	ContentType string
}

type PublicMeta struct {
	MID         int64
	Name        string
	Slug        string
	Type        string
	Description string
	Count       int64
	SortOrder   int64
	Parent      int64
}

type PublicRevision struct {
	RID          int64
	CID          int64
	Created      int64
	AuthorID     int64
	Title        string
	Slug         string
	Text         string
	Status       string
	Password     string
	SortOrder    int64
	Template     string
	Parent       int64
	AllowComment string
	AllowPing    string
	AllowFeed    string
}

type PublicArchivePeriod struct {
	Year  int
	Month int
	Day   int
	Date  string
	Count int
	URL   string
}

type AttachmentMetaInfo struct {
	Name        string
	Description string
	URL         string
	MIME        string
	Size        int64
	IsImage     bool
	Width       int
	Height      int
}

type WAFStatistics struct {
	BannedIPs    int
	AllowedPaths int
	RecentBlocks int64
}

type ContentFieldInput struct {
	Name       string
	Type       string
	StrValue   string
	IntValue   int64
	FloatValue float64
}

type ContentWriteInput struct {
	ID           int64
	PublishedID  int64
	AuthorID     int64
	Operation    string
	Title        string
	Slug         string
	SlugID       int64
	Text         string
	Type         string
	Status       string
	Password     string
	Created      int64
	SortOrder    int64
	Template     string
	Parent       int64
	AllowComment bool
	AllowPing    bool
	AllowFeed    bool
	CategoryIDs  []int64
	Tags         []string
	Fields       []ContentFieldInput
	DraftOf      int64
}

type CommentWriteInput struct {
	ID        int64
	Operation string
	CID       int64
	Author    string
	AuthorID  int64
	OwnerID   int64
	Mail      string
	URL       string
	Text      string
	Type      string
	Status    string
	Parent    int64
	IP        string
	Agent     string
}

type PublicContentQuery struct {
	CID           int64
	Slug          string
	SlugID        int64
	Type          string
	Status        string
	Keywords      string
	Category      int64
	Tag           int64
	AuthorID      int64
	Year          int
	Month         int
	Day           int
	Limit         int
	Offset        int
	IncludeDrafts bool
	ExcludeFuture bool
}

type PublicCommentQuery struct {
	COID     int64
	Status   string
	Type     string
	Keywords string
	CID      int64
	AuthorID int64
	OwnerID  int64
	Mail     string
	IP       string
	Limit    int
	Offset   int
}

type PublicUserQuery struct {
	UID      int64
	Name     string
	Mail     string
	Role     string
	Keywords string
	Limit    int
	Offset   int
}

type PublicMetaQuery struct {
	MID    int64
	Type   string
	Slug   string
	Parent int64
	Used   bool
	Limit  int
	Offset int
}

type Runtime struct {
	OwnerKind                string
	Owner                    string
	ListContents             func(context.Context, PublicContentQuery) ([]PublicContent, int64, error)
	ListComments             func(context.Context, PublicCommentQuery) ([]PublicComment, int64, error)
	ListUsers                func(context.Context, PublicUserQuery) ([]PublicUser, int64, error)
	ListMetas                func(context.Context, PublicMetaQuery) ([]PublicMeta, int64, error)
	ListRevisions            func(context.Context, int64) ([]PublicRevision, error)
	GetRevision              func(context.Context, int64) (PublicRevision, error)
	RestoreRevision          func(context.Context, int64, int64) error
	DeleteRevision           func(context.Context, int64, int64) error
	ArchiveMonths            func(context.Context, int) ([]PublicArchivePeriod, error)
	AdjacentPosts            func(context.Context, int64) (PublicContent, PublicContent, error)
	RelatedPosts             func(context.Context, int64, int) ([]PublicContent, error)
	GetEditingDraft          func(context.Context, int64) (PublicContent, error)
	PublishDraft             func(context.Context, int64) error
	ListThemeFiles           func(context.Context, ...string) ([]string, error)
	ThemeEditableDir         func(context.Context, ...string) (string, bool)
	ContentURL               func(context.Context, int64) (string, error)
	CommentURL               func(context.Context, int64) (string, error)
	AvatarURL                func(context.Context, string, int) string
	Language                 func(context.Context) string
	SiteURL                  func(context.Context) string
	AdminURL                 func(context.Context) string
	ClientIP                 func(*http.Request) string
	CurrentUser              func(*http.Request) (PublicUser, bool)
	CSRFToken                func(*http.Request, string) string
	ValidateCSRF             func(*http.Request, string) bool
	Option                   func(context.Context, string) (string, error)
	SetOption                func(context.Context, string, string) error
	SaveContent              func(context.Context, ContentWriteInput) (PublicContent, error)
	DeleteContent            func(context.Context, int64) error
	SaveComment              func(context.Context, CommentWriteInput) (PublicComment, error)
	DeleteComment            func(context.Context, int64) error
	Config                   func(context.Context, string) (map[string]string, error)
	PersonalConfig           func(context.Context, string, int64) (map[string]string, error)
	DispatchHook             func(context.Context, string, any) (HookDispatch, error)
	ServiceAvailable         func(string) bool
	CallService              func(context.Context, string, ...any) (any, error)
	NotifyAdmin              func(http.ResponseWriter, *http.Request, ...AdminNotice)
	OpenPluginDB             func(context.Context) (*sql.DB, error)
	PluginDBDialect          func(context.Context) string
	IsIPBanned               func(context.Context, string) bool
	IsURLAllowed             func(context.Context, string) bool
	BanIP                    func(context.Context, string, time.Duration, string) error
	UnbanIP                  func(context.Context, string) error
	WAFStats                 func(context.Context) (WAFStatistics, error)
	GetContentAuthor         func(context.Context, int64) (PublicUser, error)
	ListContentMetas         func(context.Context, int64) ([]PublicMeta, error)
	GetContentFields         func(context.Context, int64) (map[string]any, error)
	SetContentField          func(context.Context, int64, ContentFieldInput) error
	IncrementContentFieldInt func(context.Context, int64, string, int64) (int64, error)
	DeleteContentField       func(context.Context, int64, string) error
	ThumbnailURL             func(context.Context, int64, int, int) (string, error)
	AttachmentMeta           func(context.Context, int64) (AttachmentMetaInfo, error)
	ActiveTheme              func(context.Context) string
	ContentRenderMode        func(context.Context) string
	// PluginActive reports whether a named plugin is currently activated,
	// equivalent to Typecho's Plugin::export()['activated'] lookup.
	PluginActive func(string) bool
	// PluginURL returns the public base URL for a plugin's bundled static
	// assets (served under /plugin/<name>/). When owner is empty the calling
	// plugin's own asset base is returned.
	PluginURL func(ctx context.Context, owner string) string
	// RenderContent returns the rendered HTML of any content by CID, applying
	// the same markdown/autop pipeline and render hooks the frontend uses.
	RenderContent func(context.Context, int64) (template.HTML, error)
	// Excerpt returns a plain-text/HTML excerpt of the given text, honoring the
	// content.excerpt hooks. limit is the maximum rune length.
	Excerpt func(ctx context.Context, text string, limit int) string
	// FeedURL, CommentsFeedURL, XMLRPCURL, LoginURL, RegisterURL, LogoutURL,
	// ProfileURL and ThemeURL expose core-defined route shapes so extensions
	// need not hardcode them. ThemeURL(name, file) resolves a theme asset URL.
	FeedURL         func(context.Context) string
	CommentsFeedURL func(context.Context) string
	XMLRPCURL       func(context.Context) string
	LoginURL        func(context.Context) string
	RegisterURL     func(context.Context) string
	LogoutURL       func(context.Context) string
	ProfileURL      func(context.Context) string
	ThemeURL        func(ctx context.Context, name, file string) string
	// PluginDataDir returns and creates a private on-disk directory for the
	// calling extension under the data directory, for swappable side files such
	// as an IP database that should not be bundled into the binary. It mirrors
	// the theme ThemeEditableDir capability for plugins.
	PluginDataDir func(context.Context) (string, error)
}

type runtimeContextKey struct{}

var (
	ErrRuntimeUnavailable = errors.New("plugin runtime unavailable")
	ErrServiceUnavailable = errors.New("plugin service unavailable")
)

func ContextWithRuntime(ctx context.Context, runtime *Runtime) context.Context {
	if runtime == nil {
		return ctx
	}
	return context.WithValue(ctx, runtimeContextKey{}, runtime)
}

func RuntimeFromContext(ctx context.Context) (*Runtime, bool) {
	if ctx == nil {
		return nil, false
	}
	runtime, ok := ctx.Value(runtimeContextKey{}).(*Runtime)
	return runtime, ok && runtime != nil
}

func (r *Runtime) WithOwner(owner string) *Runtime {
	return r.WithComponent("plugin", owner)
}

func (r *Runtime) WithComponent(kind, owner string) *Runtime {
	if r == nil {
		return nil
	}
	next := *r
	next.OwnerKind = kind
	next.Owner = owner
	return &next
}

func (r *Runtime) OpenPluginDatabase(ctx context.Context) (*sql.DB, error) {
	if r == nil || r.OpenPluginDB == nil {
		return nil, ErrRuntimeUnavailable
	}
	if strings.TrimSpace(r.Owner) == "" {
		return nil, errors.New("plugin runtime owner is empty")
	}
	return r.OpenPluginDB(ctx)
}

func runtimeDatabaseOwner(kind, owner string) string {
	kind = strings.TrimSpace(kind)
	owner = strings.TrimSpace(owner)
	if kind == "" {
		return owner
	}
	return kind + "-" + owner
}

func DatabaseTableName(owner, name string) string {
	return "plugin_" + safeid.SQL(owner) + "_" + safeid.SQL(name)
}

func (r *Runtime) DatabaseTableName(table string) string {
	if r == nil {
		return DatabaseTableName("", table)
	}
	owner := r.Owner
	if r.OwnerKind != "" && r.OwnerKind != "plugin" {
		owner = runtimeDatabaseOwner(r.OwnerKind, r.Owner)
	}
	return DatabaseTableName(owner, table)
}

func RebindSQL(dialect, query string) string {
	return sqlutil.Rebind(dialect, query)
}

func (r *Runtime) RebindSQL(ctx context.Context, query string) string {
	dialect := ""
	if r != nil && r.PluginDBDialect != nil {
		dialect = r.PluginDBDialect(ctx)
	}
	return RebindSQL(dialect, query)
}

func (r *Runtime) GetContent(ctx context.Context, cid int64) (PublicContent, error) {
	if r == nil || r.ListContents == nil {
		return PublicContent{}, ErrRuntimeUnavailable
	}
	if cid <= 0 {
		return PublicContent{}, sql.ErrNoRows
	}
	items, _, err := r.ListContents(ctx, PublicContentQuery{CID: cid, Type: "all", Status: "all", IncludeDrafts: true, Limit: 1})
	if err != nil {
		return PublicContent{}, err
	}
	if len(items) == 0 {
		return PublicContent{}, sql.ErrNoRows
	}
	return items[0], nil
}

func (r *Runtime) GetComment(ctx context.Context, coid int64) (PublicComment, error) {
	if r == nil || r.ListComments == nil {
		return PublicComment{}, ErrRuntimeUnavailable
	}
	if coid <= 0 {
		return PublicComment{}, sql.ErrNoRows
	}
	items, _, err := r.ListComments(ctx, PublicCommentQuery{COID: coid, Status: "all", Type: "all", Limit: 1})
	if err != nil {
		return PublicComment{}, err
	}
	if len(items) == 0 {
		return PublicComment{}, sql.ErrNoRows
	}
	return items[0], nil
}

func (r *Runtime) GetUser(ctx context.Context, uid int64) (PublicUser, error) {
	if r == nil || r.ListUsers == nil {
		return PublicUser{}, ErrRuntimeUnavailable
	}
	if uid <= 0 {
		return PublicUser{}, sql.ErrNoRows
	}
	items, _, err := r.ListUsers(ctx, PublicUserQuery{UID: uid, Limit: 1})
	if err != nil {
		return PublicUser{}, err
	}
	if len(items) == 0 {
		return PublicUser{}, sql.ErrNoRows
	}
	return items[0], nil
}

func (r *Runtime) GetMeta(ctx context.Context, mid int64) (PublicMeta, error) {
	if r == nil || r.ListMetas == nil {
		return PublicMeta{}, ErrRuntimeUnavailable
	}
	if mid <= 0 {
		return PublicMeta{}, sql.ErrNoRows
	}
	items, _, err := r.ListMetas(ctx, PublicMetaQuery{MID: mid, Type: "all", Limit: 1})
	if err != nil {
		return PublicMeta{}, err
	}
	if len(items) == 0 {
		return PublicMeta{}, sql.ErrNoRows
	}
	return items[0], nil
}

func (r *Runtime) GetSiteURL(ctx context.Context) string {
	if r == nil {
		return ""
	}
	if r.SiteURL != nil {
		return r.SiteURL(ctx)
	}
	if r.Option != nil {
		value, _ := r.Option(ctx, "base_url")
		return strings.TrimRight(value, "/")
	}
	return ""
}

func (r *Runtime) GetAdminURL(ctx context.Context) string {
	if r == nil {
		return ""
	}
	if r.AdminURL != nil {
		return r.AdminURL(ctx)
	}
	base := strings.TrimRight(r.GetSiteURL(ctx), "/")
	if base == "" {
		return "/admin"
	}
	return base + "/admin"
}

type RouteHandler func(*Runtime, http.ResponseWriter, *http.Request)

// PublicPathProvider returns exact public paths owned by a plugin whose paths
// are determined from runtime configuration. The core calls providers only
// while rebuilding its public route snapshot, never for each unknown request.
type PublicPathProvider func(context.Context, *Runtime) ([]string, error)

// ServiceFunc exposes one named capability to other active plugins and themes.
// Structured return values remain subject to html/template escaping in themes.
type ServiceFunc func(context.Context, *Runtime, ...any) (any, error)

type Route struct {
	Plugin                string
	Method                string
	Pattern               string
	Handler               RouteHandler
	InvalidatesPublicData bool
}

type HookFunc func(context.Context, any) (any, error)

type RuntimeHookFunc func(context.Context, *Runtime, any) (any, error)

const (
	HookPriorityEarly  = -100
	HookPriorityNormal = 0
	HookPriorityLate   = 100
)

// HookControl lets a hook stop the remaining callbacks while preserving a payload.
type HookControl struct {
	Payload any
	Stop    bool
}

// HookDispatch reports whether a hook point was handled and whether propagation stopped.
type HookDispatch struct {
	Payload   any
	Triggered bool
	Stopped   bool
}

func StopHook(payload any) HookControl {
	return HookControl{Payload: payload, Stop: true}
}

const (
	HookContentBeforeSave       = "content.before_save"
	HookContentAfterSave        = "content.after_save"
	HookContentBeforeDelete     = "content.before_delete"
	HookContentAfterDelete      = "content.after_delete"
	HookContentBeforeStatus     = "content.before_status_change"
	HookContentAfterStatus      = "content.after_status_change"
	HookContentFilter           = "content.filter"
	HookContentBeforeRender     = "content.before_render"
	HookContentAfterRender      = "content.after_render"
	HookContentTitle            = "content.title"
	HookContentParse            = "content.parse"
	HookExcerpt                 = "content.excerpt"
	HookExcerptAfterRender      = "excerpt.after_render"
	HookContentList             = "content.list"
	HookContentFields           = "content.fields"
	HookContentFieldReadOnly    = "content.field_read_only"
	HookContentPermalink        = "content.permalink"
	HookContentAuthor           = "content.author"
	HookCommentBeforeSave       = "comment.before_save"
	HookCommentAfterSave        = "comment.after_save"
	HookCommentBeforeMark       = "comment.before_mark"
	HookCommentAfterMark        = "comment.after_mark"
	HookCommentBeforeDelete     = "comment.before_delete"
	HookCommentAfterDelete      = "comment.after_delete"
	HookCommentFilter           = "comment.filter"
	HookCommentBeforeRender     = "comment.before_render"
	HookCommentAfterRender      = "comment.after_render"
	HookCommentParse            = "comment.parse"
	HookCommentAvatar           = "comment.avatar"
	HookCommentPermalink        = "comment.permalink"
	HookCommentListRender       = "comment.list_render"
	HookCommentReplyLink        = "comment.reply_link"
	HookCommentPageNav          = "comment.page_nav"
	HookCommentGuard            = "comment.guard_validate"
	HookUploadBeforeSave        = "upload.before_save"
	HookUploadHandle            = "upload.handle"
	HookUploadAfterSave         = "upload.after_save"
	HookAttachmentBeforeEdit    = "attachment.before_edit"
	HookAttachmentAfterEdit     = "attachment.after_edit"
	HookAttachmentBeforeReplace = "attachment.before_replace"
	HookAttachmentReplaceHandle = "attachment.replace_handle"
	HookAttachmentAfterReplace  = "attachment.after_replace"
	HookAttachmentBeforeDelete  = "attachment.before_delete"
	HookAttachmentDeleteHandle  = "attachment.delete_handle"
	HookAttachmentAfterDelete   = "attachment.after_delete"
	HookAttachmentURL           = "attachment.url"
	HookAttachmentData          = "attachment.data"
	HookRequestBefore           = "request.before"
	HookRequestFallback         = "request.fallback"
	HookRequestAfter            = "request.after"
	HookUserLoginBefore         = "user.login_before"
	HookUserLoginAuthenticated  = "user.login_authenticated"
	HookUserLoginAfter          = "user.login_after"
	HookUserLoginFail           = "user.login_fail"
	HookUserLogout              = "user.logout"
	HookUserRegisterBefore      = "user.register_before"
	HookUserRegisterAfter       = "user.register_after"
	HookAdminMenu               = "admin.menu"
	HookFrontendHead            = "frontend.head"
	HookFrontendFooter          = "frontend.footer"
	HookArchiveBeforeQuery      = "archive.before_query"
	HookArchiveAfterQuery       = "archive.after_query"
	HookArchiveBeforeRender     = "archive.before_render"
	HookArchiveAfterRender      = "archive.after_render"
	HookArchiveSearch           = "archive.search"
	HookMetaPermalink           = "meta.permalink"
	HookFeedItem                = "feed.item"
	HookFeedCommentItem         = "feed.comment_item"
	HookXMLRPCTextFilter        = "xmlrpc.text_filter"
	HookXMLRPCUpload            = "xmlrpc.upload"
	HookXMLRPCPingback          = "xmlrpc.pingback"
	HookXMLRPCFinishPingback    = "xmlrpc.finish_pingback"
	HookTrackback               = "trackback.handle"
	HookFinishTrackback         = "trackback.finish"
	HookBackupExport            = "backup.export"
	HookBackupImport            = "backup.import"
	HookUserHashValidate        = "user.hash_validate"
	HookRevisionBeforeSave      = "revision.before_save"
	HookRevisionAfterSave       = "revision.after_save"
	HookWAFCheck                = "waf.check"
	HookImageProcess            = "image.process"
	HookAutosaveBeforeSave      = "autosave.before_save"
	HookAutosaveAfterSave       = "autosave.after_save"
)

type ContentSavePayload struct {
	ID          int64
	PublishedID int64
	AuthorID    int64
	Operation   string
	Input       any
	Content     any
}

type ContentDeletePayload struct {
	ID      int64
	Content any
}

type ContentStatusPayload struct {
	ID             int64
	PreviousStatus string
	Status         string
	Content        any
}

type ContentRenderPayload struct {
	Content any
	HTML    template.HTML
	Data    map[string]any
}

type ContentFilterPayload struct {
	Content any
}

type ContentTitlePayload struct {
	Content any
	Title   string
}

type ContentParserPayload struct {
	Content any
	Text    string
	Mode    string
	HTML    template.HTML
	Handled bool
}

type ContentListPayload struct {
	Stage   string
	View    string
	Title   string
	Query   any
	Results any
	Total   int64
	Handled bool
}

type ContentFieldsPayload struct {
	ContentID int64
	Type      string
	Fields    []FieldSchema
}

type ContentFieldReadOnlyPayload struct {
	ContentID int64
	Type      string
	Name      string
	ReadOnly  bool
}

type ExcerptPayload struct {
	Text   string
	Limit  int
	Output string
}

type ExcerptAfterPayload struct {
	Content PublicContent
	Text    string
	Limit   int
	Excerpt string
}

type ArchivePayload struct {
	Type    string
	Slug    string
	Query   *PublicContentQuery
	Results []PublicContent
	Total   int64
	Data    map[string]any
	Handled bool
}

type ContentPermalinkPayload struct {
	Content PublicContent
	URL     string
}

type ContentAuthorPayload struct {
	Content PublicContent
	Author  PublicUser
}

type MetaPermalinkPayload struct {
	Meta PublicMeta
	URL  string
}

type FrontendHTMLPayload struct {
	Location string
	HTML     template.HTML
	Data     map[string]any
}

type CommentSavePayload struct {
	ID        int64
	Operation string
	Input     any
	Content   any
	Comment   any
}

type CommentActionPayload struct {
	ID             int64
	Status         string
	PreviousStatus string
	Comment        any
	Content        any
}

type CommentFilterPayload struct {
	Comment any
}

type CommentRenderPayload struct {
	Comment any
	Text    string
	HTML    template.HTML
}

type CommentParserPayload struct {
	Comment any
	Text    string
	Mode    string
	HTML    template.HTML
	Handled bool
}

type CommentAvatarPayload struct {
	Comment any
	Mail    string
	Size    int
	URL     string
}

type CommentPermalinkPayload struct {
	Comment PublicComment
	Content PublicContent
	URL     string
}

type CommentListPayload struct {
	Content  PublicContent
	Comments []PublicComment
	Views    any
	Pager    any
	HTML     template.HTML
	Handled  bool
}

type CommentLinkPayload struct {
	Comment PublicComment
	Content PublicContent
	URL     string
	HTML    template.HTML
}

type CommentGuardPayload struct {
	Request *http.Request
	CID     int64
	Token   string
	Valid   bool
	Reason  string
	Handled bool
}

type UploadPayload struct {
	Name     string
	ParentID int64
	Meta     any
}

type UploadHandlePayload struct {
	Name     string
	ParentID int64
	Bucket   string
	Size     int64
	MIME     string
	Open     func() (io.ReadCloser, error)
	Meta     any
	Handled  bool
}

type AttachmentPayload struct {
	Content any
	Meta    any
}

type AttachmentEditPayload struct {
	Content     any
	Title       string
	Description string
	Meta        any
}

type AttachmentReplacePayload struct {
	Content      any
	PreviousMeta any
	Name         string
	ParentID     int64
	Size         int64
	Open         func() (io.ReadCloser, error)
	Meta         any
	Warning      string
	Handled      bool
}

type AttachmentDeleteHandlePayload struct {
	Content any
	Meta    any
	Handled bool
}

type AttachmentURLPayload struct {
	Content any
	Meta    any
	URL     string
}

type AttachmentDataPayload struct {
	Content any
	Meta    any
	Data    []byte
	Handled bool
}

type RequestPayload struct {
	Method          string
	Path            string
	RawQuery        string
	RemoteAddr      string
	IP              string
	UserAgent       string
	Referer         string
	Status          int
	Bytes           int64
	Duration        int64
	Admin           bool
	Static          bool
	ContentType     string
	Headers         map[string]string
	ResponseHeaders map[string]string
	Body            string
	Handled         bool
}

type UserLoginPayload struct {
	Name      string
	User      PublicUser
	IP        string
	UserAgent string
	Next      string
	Success   bool
	Error     string
	Blocked   bool
	Message   string
}

type UserHashValidatePayload struct {
	Name     string
	Password string
	Hash     string
	User     PublicUser
	Valid    bool
	Handled  bool
}

type UserLogoutPayload struct {
	User      PublicUser
	IP        string
	UserAgent string
}

type UserRegisterPayload struct {
	User      PublicUser
	Input     any
	IP        string
	UserAgent string
	Blocked   bool
	Message   string
}

type AdminMenuItem struct {
	Label      string
	URL        string
	Icon       string
	OpenNewTab bool
	// Owner is assigned by Manager so the admin renderer can use the
	// registering plugin's optional Translator. Plugins normally leave it blank.
	Owner string
}

const (
	NoticeInfo    = "info"
	NoticeSuccess = "success"
	NoticeWarning = "warning"
	NoticeError   = "error"

	NoticeAuto     = "auto"
	NoticeSnackbar = "snackbar"
	NoticeCard     = "card"
)

// AdminNotice is a plain-text message displayed by the native admin UI.
type AdminNotice struct {
	Type         string `json:"type"`
	Mode         string `json:"mode,omitempty"`
	Message      string `json:"message"`
	SkipCoreI18n bool   `json:"skipCoreI18n,omitempty"`
}

type AdminMenuProvider interface {
	AdminMenuItems(context.Context) []AdminMenuItem
}

type FeedItemPayload struct {
	Kind    string
	Content PublicContent
	Comment PublicComment
	Item    any
	Handled bool
}

type XMLRPCTextPayload struct {
	Method  string
	Content PublicContent
	Text    string
}

type XMLRPCUploadPayload struct {
	Name    string
	Data    []byte
	Result  map[string]any
	Handled bool
}

type XMLRPCPingbackPayload struct {
	SourceURI string
	TargetURI string
	Content   PublicContent
	Comment   PublicComment
	Message   string
	Handled   bool
}

type TrackbackPayload struct {
	Content PublicContent
	Input   any
	Comment PublicComment
	Handled bool
}

type BackupPayload struct {
	Data    any
	Handled bool
}

type RevisionPayload struct {
	ContentID int64
	Revision  any
	Input     any
	Handled   bool
}

type WAFPayload struct {
	Request *http.Request
	IP      string
	Path    string
	Blocked bool
	Reason  string
	Handled bool
}

type ImageProcessPayload struct {
	Name    string
	Data    []byte
	MIME    string
	Result  []byte
	Warning string
	Handled bool
}

type AutosavePayload struct {
	ContentID int64
	Input     any
	Result    any
	Handled   bool
}

// AdminNoticeProvider supplies messages for a plugin's native configuration page.
type AdminNoticeProvider interface {
	AdminNotices(context.Context, *Runtime, map[string]string) []AdminNotice
}

// AdminAction describes a POST action rendered next to a plugin's save button.
type AdminAction struct {
	Name        string
	Label       string
	Icon        string
	Variant     string
	Description string
}

// AdminActionProvider handles authenticated, CSRF-protected plugin settings actions.
type AdminActionProvider interface {
	AdminActions() []AdminAction
	HandleAdminAction(context.Context, *Runtime, string) (AdminNotice, error)
}

// AdminPage describes a native tab on a plugin or theme configuration page.
type AdminPage struct {
	Name        string
	Label       string
	Icon        string
	Title       string
	Description string
}

type AdminPageRenderContext struct {
	CSRF   string
	Config map[string]string
	// Query carries the GET query parameters of the page request (same shape as
	// url.Values), so native pages can support pagination, filtering and other
	// stateful views through their own ?key=value links without a custom route.
	Query map[string][]string
}

type AdminPageActionResult struct {
	ConfigPatch map[string]string
	Notice      AdminNotice
}

// AdminPageProvider renders trusted plugin UI inside the authenticated admin shell.
type AdminPageProvider interface {
	AdminPages() []AdminPage
	RenderAdminPage(context.Context, *Runtime, string, AdminPageRenderContext) (template.HTML, error)
}

// AdminPageActionProvider handles POST actions from a native plugin page.
type AdminPageActionProvider interface {
	HandleAdminPageAction(context.Context, *Runtime, string, map[string][]string) (AdminPageActionResult, error)
}

type FieldType string

const (
	FieldText     FieldType = "text"
	FieldPassword FieldType = "password"
	FieldTextarea FieldType = "textarea"
	FieldRadio    FieldType = "radio"
	FieldCheckbox FieldType = "checkbox"
	FieldSelect   FieldType = "select"
	FieldNumber   FieldType = "number"
	FieldColor    FieldType = "color"
	FieldImage    FieldType = "image"
	// FieldSwitch renders an MDUI switch; the stored value is "1"/"0" like a checkbox.
	FieldSwitch FieldType = "switch"
	// FieldSlider renders an MDUI slider bound to Min/Max/Step; the stored value is numeric.
	FieldSlider FieldType = "slider"
	// FieldDate, FieldTime and FieldDatetime render native date/time pickers; the
	// stored value is the raw control value (YYYY-MM-DD, HH:MM, or YYYY-MM-DDTHH:MM).
	FieldDate     FieldType = "date"
	FieldTime     FieldType = "time"
	FieldDatetime FieldType = "datetime"
	// FieldMultiSelect and FieldMultiCheckbox store multiple selected values joined
	// by newlines, mirroring Typecho's Checkbox->multiMode()/multi-select forms.
	FieldMultiSelect   FieldType = "multiselect"
	FieldMultiCheckbox FieldType = "multicheckbox"
)

type FieldOption struct {
	Label string
	Value string
}

type FieldSchema struct {
	Name          string
	Label         string
	Group         string
	Type          FieldType
	Default       string
	Description   string
	Required      bool
	ShowWhenField string
	ShowWhenValue string
	Min           string
	Max           string
	Step          string
	Options       []FieldOption
	ForTypes      []string
	ReadOnly      bool
	Wide          bool
	// Translate is attached while core collects theme and plugin content
	// fields, keeping extension-owned labels out of the core translation table.
	Translate func(string) string
}

// IsMultiValueField reports whether a field type stores several selected values.
func (t FieldType) IsMultiValue() bool {
	return t == FieldMultiSelect || t == FieldMultiCheckbox
}

// IsBooleanField reports whether a field type stores a "1"/"0" boolean value.
func (t FieldType) IsBoolean() bool {
	return t == FieldCheckbox || t == FieldSwitch
}

// SplitMultiValue decodes a stored multi-value field back into its values.
// Multi-value fields persist as newline-joined tokens.
func SplitMultiValue(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// JoinMultiValue encodes selected values for a multi-value field.
func JoinMultiValue(values []string) string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, "\n")
}

type PluginInfo struct {
	Name             string
	Version          string
	Author           string
	Description      string
	Homepage         string
	RequireGopherInk string
}

type Plugin interface {
	Name() string
	Version() string
	Description() string
	Init(*Manager)
}

type InfoProvider interface {
	Info() PluginInfo
}

type Activator interface {
	Activate(context.Context, *Runtime) error
}

type Deactivator interface {
	Deactivate(context.Context, *Runtime) error
}

type ConfigProvider interface {
	ConfigSchema() []FieldSchema
}

type PersonalConfigProvider interface {
	PersonalConfigSchema() []FieldSchema
}

// StaticProvider lets a plugin ship bundled front-end assets (JS/CSS/images).
// Core serves the returned filesystem at /plugin/<name>/ and exposes the base
// URL through Runtime.PluginURL and the pluginURL template function, mirroring
// the theme Static mechanism and Typecho's pluginUrl option.
type StaticProvider interface {
	PluginStatic() fs.FS
}

// Translator lets an extension localize its own labels and messages.
// Core only provides the current language through Runtime.Language and calls
// this method when rendering native extension UI.
type Translator interface {
	Translate(lang, key string) string
}

type ColumnType string

const (
	ColInt64    ColumnType = "int64"
	ColVarchar  ColumnType = "varchar"
	ColText     ColumnType = "text"
	ColFloat    ColumnType = "float"
	ColDatetime ColumnType = "datetime"
	ColBool     ColumnType = "bool"
)

type ColumnDefinition struct {
	Name     string
	Type     ColumnType
	Length   int
	Nullable bool
	Default  string
	Primary  bool
	AutoInc  bool
}

type IndexDefinition struct {
	Name    string
	Columns []string
	Unique  bool
}

type TableDefinition struct {
	Name    string
	Columns []ColumnDefinition
	Indexes []IndexDefinition
}

type DatabaseProvider interface {
	DatabaseTables() []TableDefinition
	DatabaseVersion() int
}

type DatabaseMigrator interface {
	Migrate(ctx context.Context, db *sql.DB, dialect string, fromVersion, toVersion int) error
}

type ContentFieldsProvider interface {
	ContentFieldSchema() []FieldSchema
}

type ConfigValidator interface {
	ValidateConfig(values map[string]string) map[string]string
}

type ConfigHandler interface {
	HandleConfig(ctx context.Context, rt *Runtime, values map[string]string, isInit bool) error
}

type CommentBadge struct {
	Label string
	Icon  string
	Tone  string
}

type CommentEnrichment struct {
	Badges     []CommentBadge
	CSSClasses []string
	Extra      map[string]any
}

type Theme struct {
	Name                  string
	DisplayName           string
	Version               string
	Author                string
	Description           string
	Homepage              string
	Screenshot            string
	TemplateList          []string
	Templates             fs.FS
	Static                fs.FS
	Funcs                 template.FuncMap
	Translate             func(lang, key string) string
	ConfigSchema          []FieldSchema
	ContentFields         []FieldSchema
	ConfigValidator       func(map[string]string) map[string]string
	ConfigHandler         func(context.Context, *Runtime, map[string]string, bool) error
	AdminNotices          func(context.Context, *Runtime, map[string]string) []AdminNotice
	AdminPages            []AdminPage
	RenderAdminPage       func(context.Context, *Runtime, string, AdminPageRenderContext) (template.HTML, error)
	HandleAdminPageAction func(context.Context, *Runtime, string, map[string][]string) (AdminPageActionResult, error)
	EnrichComments        func(context.Context, *Runtime, map[string]string, []PublicComment) map[int64]CommentEnrichment
	Capabilities          ThemeCapabilities
	InitRuntime           func(context.Context, *Runtime) error
	AdjustData            func(context.Context, map[string]any) error
	Routes                []Route
	EditableDir           string
	Embedded              bool
}

// ThemeCapabilities declares optional core protocols implemented by a theme.
// The core remains responsible for enforcing security-sensitive capabilities.
type ThemeCapabilities struct {
	CommentGuard bool
}

type Manager struct {
	mu            sync.RWMutex
	plugins       []Plugin
	pluginNames   map[string]Plugin
	hooks         map[string][]ownedHook
	services      map[string]ownedService
	routes        []ownedRoute
	publicPaths   []ownedPublicPathProvider
	adminMenus    []ownedAdminMenu
	themes        map[string]Theme
	activePlugins map[string]bool
	registering   string
	hookSequence  uint64
}

var Default = NewManager()

func NewManager() *Manager {
	return &Manager{
		hooks:         make(map[string][]ownedHook),
		services:      make(map[string]ownedService),
		themes:        make(map[string]Theme),
		pluginNames:   make(map[string]Plugin),
		activePlugins: make(map[string]bool),
	}
}

func Register(p Plugin) {
	Default.Register(p)
}

func RegisterTheme(theme Theme) {
	Default.RegisterTheme(theme)
}

func (m *Manager) Register(p Plugin) {
	name := p.Name()
	m.mu.Lock()
	m.plugins = append(m.plugins, p)
	m.pluginNames[name] = p
	m.registering = name
	m.mu.Unlock()
	p.Init(m)
	m.mu.Lock()
	if m.registering == name {
		m.registering = ""
	}
	m.mu.Unlock()
}

func (m *Manager) RegisterHook(name string, fn HookFunc) {
	m.RegisterHookWithPriority(name, HookPriorityNormal, fn)
}

func (m *Manager) RegisterHookWithPriority(name string, priority int, fn HookFunc) {
	m.registerHook(name, priority, fn, nil)
}

func (m *Manager) RegisterRuntimeHook(name string, fn RuntimeHookFunc) {
	m.RegisterRuntimeHookWithPriority(name, HookPriorityNormal, fn)
}

func (m *Manager) RegisterRuntimeHookWithPriority(name string, priority int, fn RuntimeHookFunc) {
	m.registerHook(name, priority, nil, fn)
}

func (m *Manager) registerHook(name string, priority int, fn HookFunc, runtimeFn RuntimeHookFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hookSequence++
	m.hooks[name] = append(m.hooks[name], ownedHook{Plugin: m.registering, Priority: priority, Sequence: m.hookSequence, Fn: fn, RuntimeFn: runtimeFn})
	sort.SliceStable(m.hooks[name], func(i, j int) bool {
		if m.hooks[name][i].Priority == m.hooks[name][j].Priority {
			return m.hooks[name][i].Sequence < m.hooks[name][j].Sequence
		}
		return m.hooks[name][i].Priority < m.hooks[name][j].Priority
	})
}

func (m *Manager) Apply(ctx context.Context, name string, payload any) (any, error) {
	dispatch, err := m.Dispatch(ctx, name, payload)
	return dispatch.Payload, err
}

func (m *Manager) Dispatch(ctx context.Context, name string, payload any) (HookDispatch, error) {
	m.mu.RLock()
	hooks := append([]ownedHook(nil), m.hooks[name]...)
	m.mu.RUnlock()
	return dispatchHooks(ctx, hooks, nil, payload)
}

func (m *Manager) ApplyActive(ctx context.Context, name string, payload any) (any, error) {
	dispatch, err := m.DispatchActive(ctx, name, payload)
	return dispatch.Payload, err
}

func (m *Manager) DispatchActive(ctx context.Context, name string, payload any) (HookDispatch, error) {
	m.mu.RLock()
	registered := m.hooks[name]
	hooks := make([]ownedHook, 0, len(registered))
	for _, hook := range registered {
		if hook.Plugin == "" || m.activePlugins[hook.Plugin] {
			hooks = append(hooks, hook)
		}
	}
	m.mu.RUnlock()
	return dispatchHooks(ctx, hooks, nil, payload)
}

func (m *Manager) HasActiveHook(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, hook := range m.hooks[name] {
		if hook.Plugin == "" || m.activePlugins[hook.Plugin] {
			return true
		}
	}
	return false
}

// RegisterService publishes a single-owner named service from Plugin.Init.
// Duplicate names fail during startup so callers never observe an ambiguous provider.
func (m *Manager) RegisterService(name string, fn ServiceFunc) {
	if name == "" {
		panic("plugin: service name must not be empty")
	}
	if fn == nil {
		panic("plugin: service handler must not be nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.registering == "" {
		panic("plugin: RegisterService must be called from Plugin.Init")
	}
	if _, exists := m.services[name]; exists {
		panic("plugin: duplicate service " + name)
	}
	m.services[name] = ownedService{Plugin: m.registering, Fn: fn}
}

func (m *Manager) HasActiveService(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	service, ok := m.services[name]
	return ok && (service.Plugin == "" || m.activePlugins[service.Plugin])
}

func (m *Manager) CallActiveService(ctx context.Context, runtime *Runtime, name string, args ...any) (any, error) {
	m.mu.RLock()
	service, ok := m.services[name]
	active := ok && (service.Plugin == "" || m.activePlugins[service.Plugin])
	m.mu.RUnlock()
	if !active {
		return nil, ErrServiceUnavailable
	}
	if runtime == nil {
		return nil, ErrRuntimeUnavailable
	}
	serviceRuntime := runtime
	if service.Plugin != "" {
		serviceRuntime = runtime.WithOwner(service.Plugin)
	}
	return service.Fn(ContextWithRuntime(ctx, serviceRuntime), serviceRuntime, args...)
}

func (m *Manager) RegisterRoute(method, pattern string, handler RouteHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes = append(m.routes, ownedRoute{Plugin: m.registering, Route: Route{Method: method, Pattern: pattern, Handler: handler}})
}

// RegisterPublicPathProvider registers runtime-configured exact public paths.
// The handler for those paths can be implemented by a registered route or by
// HookRequestFallback after the WAF route snapshot has accepted the path.
func (m *Manager) RegisterPublicPathProvider(provider PublicPathProvider) {
	if provider == nil {
		panic("plugin: public path provider must not be nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.registering == "" {
		panic("plugin: RegisterPublicPathProvider must be called from Plugin.Init")
	}
	m.publicPaths = append(m.publicPaths, ownedPublicPathProvider{Plugin: m.registering, Provider: provider})
}

// ActivePublicPaths collects configured paths from active plugins. Providers
// execute outside the manager lock so they can safely use Runtime services.
func (m *Manager) ActivePublicPaths(ctx context.Context, runtime *Runtime) ([]string, error) {
	m.mu.RLock()
	providers := make([]ownedPublicPathProvider, 0, len(m.publicPaths))
	for _, provider := range m.publicPaths {
		if m.activePlugins[provider.Plugin] {
			providers = append(providers, provider)
		}
	}
	m.mu.RUnlock()

	var paths []string
	for _, provider := range providers {
		if runtime == nil {
			return nil, ErrRuntimeUnavailable
		}
		ownerRuntime := runtime.WithOwner(provider.Plugin)
		registered, err := provider.Provider(ContextWithRuntime(ctx, ownerRuntime), ownerRuntime)
		if err != nil {
			return nil, fmt.Errorf("plugin %s public paths: %w", provider.Plugin, err)
		}
		for _, publicPath := range registered {
			publicPath = strings.TrimSpace(publicPath)
			if publicPath == "" {
				continue
			}
			clean := path.Clean(publicPath)
			if !strings.HasPrefix(publicPath, "/") || strings.ContainsAny(publicPath, "?#\\") ||
				(clean != publicPath && clean+"/" != publicPath) {
				return nil, fmt.Errorf("plugin %s public path %q is not a canonical absolute path", provider.Plugin, publicPath)
			}
			paths = append(paths, publicPath)
		}
	}
	return paths, nil
}

func (m *Manager) RegisterAdminMenu(item AdminMenuItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item.Owner = m.registering
	m.adminMenus = append(m.adminMenus, ownedAdminMenu{Plugin: m.registering, Item: item})
}

func (m *Manager) Routes() []Route {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Route, 0, len(m.routes))
	for _, route := range m.routes {
		out = append(out, route.withOwner())
	}
	return out
}

func (m *Manager) ActiveRoutes() []Route {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Route, 0, len(m.routes))
	for _, route := range m.routes {
		if route.Plugin == "" || m.activePlugins[route.Plugin] {
			out = append(out, route.withOwner())
		}
	}
	return out
}

func (m *Manager) ActiveAdminMenuItems(ctx context.Context) []AdminMenuItem {
	m.mu.RLock()
	registered := append([]ownedAdminMenu(nil), m.adminMenus...)
	plugins := append([]Plugin(nil), m.plugins...)
	active := copyBoolMap(m.activePlugins)
	m.mu.RUnlock()
	out := make([]AdminMenuItem, 0, len(registered))
	for _, item := range registered {
		if item.Plugin == "" || active[item.Plugin] {
			entry := item.Item
			entry.Owner = item.Plugin
			out = append(out, entry)
		}
	}
	for _, p := range plugins {
		name := p.Name()
		if name == "" || !active[name] {
			continue
		}
		provider, ok := p.(AdminMenuProvider)
		if !ok {
			continue
		}
		for _, item := range provider.AdminMenuItems(ctx) {
			item.Owner = name
			out = append(out, item)
		}
	}
	return out
}

func (m *Manager) RegisterTheme(theme Theme) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.themes[theme.Name] = theme
}

func (m *Manager) Theme(name string) (Theme, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	theme, ok := m.themes[name]
	return theme, ok
}

func (m *Manager) Themes() []Theme {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Theme, 0, len(m.themes))
	for _, theme := range m.themes {
		out = append(out, theme)
	}
	return out
}

func (m *Manager) Plugins() []Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Plugin(nil), m.plugins...)
}

func (m *Manager) Plugin(name string) (Plugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.pluginNames[name]
	return p, ok
}

func (m *Manager) PluginInfo(p Plugin) PluginInfo {
	if provider, ok := p.(InfoProvider); ok {
		info := provider.Info()
		if info.Name == "" {
			info.Name = p.Name()
		}
		if info.Version == "" {
			info.Version = p.Version()
		}
		if info.Description == "" {
			info.Description = p.Description()
		}
		return info
	}
	return PluginInfo{Name: p.Name(), Version: p.Version(), Description: p.Description()}
}

func (m *Manager) SetActivePlugins(names []string) {
	active := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			active[name] = true
		}
	}
	m.mu.Lock()
	m.activePlugins = active
	m.mu.Unlock()
}

func (m *Manager) IsActive(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activePlugins[name]
}

func Compatible(required, current string) bool {
	if required == "" {
		return true
	}
	return compareVersion(current, required) >= 0
}

type ownedHook struct {
	Plugin    string
	Priority  int
	Sequence  uint64
	Fn        HookFunc
	RuntimeFn RuntimeHookFunc
}

type ownedService struct {
	Plugin string
	Fn     ServiceFunc
}

type ownedRoute struct {
	Plugin string
	Route  Route
}

type ownedPublicPathProvider struct {
	Plugin   string
	Provider PublicPathProvider
}

type ownedAdminMenu struct {
	Plugin string
	Item   AdminMenuItem
}

func (r ownedRoute) withOwner() Route {
	route := r.Route
	route.Plugin = r.Plugin
	return route
}

func copyBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func dispatchHooks(ctx context.Context, hooks []ownedHook, active map[string]bool, payload any) (HookDispatch, error) {
	result := HookDispatch{Payload: payload}
	runtime, _ := RuntimeFromContext(ctx)
	for _, hook := range hooks {
		if active != nil && hook.Plugin != "" && !active[hook.Plugin] {
			continue
		}
		result.Triggered = true
		if hook.RuntimeFn == nil && hook.Fn == nil {
			continue
		}
		next, err := invokeHook(ctx, runtime, hook, result.Payload)
		if err != nil {
			return HookDispatch{}, err
		}
		if control, ok := next.(HookControl); ok {
			result.Payload = control.Payload
			if control.Stop {
				result.Stopped = true
				break
			}
			continue
		}
		result.Payload = next
	}
	return result, nil
}

func invokeHook(ctx context.Context, runtime *Runtime, hook ownedHook, payload any) (next any, err error) {
	owner := hook.Plugin
	if owner == "" {
		owner = "core"
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("plugin hook panic (owner=%q): %v\n%s", owner, recovered, debug.Stack())
			next = nil
			err = fmt.Errorf("plugin hook panicked (owner=%q)", owner)
		}
	}()
	if hook.RuntimeFn != nil {
		if runtime == nil {
			return nil, ErrRuntimeUnavailable
		}
		hookRuntime := runtime
		if hook.Plugin != "" {
			hookRuntime = runtime.WithOwner(hook.Plugin)
		}
		hookCtx := ContextWithRuntime(ctx, hookRuntime)
		return hook.RuntimeFn(hookCtx, hookRuntime, payload)
	}
	return hook.Fn(ctx, payload)
}

func compareVersion(a, b string) int {
	ap := versionParts(a)
	bp := versionParts(b)
	for i := 0; i < len(ap) || i < len(bp); i++ {
		av, bv := 0, 0
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return 0
}

func versionParts(value string) []int {
	var parts []int
	current := 0
	inNumber := false
	for _, r := range value {
		if r >= '0' && r <= '9' {
			inNumber = true
			current = current*10 + int(r-'0')
			continue
		}
		if inNumber {
			parts = append(parts, current)
			current = 0
			inNumber = false
		}
	}
	if inNumber {
		parts = append(parts, current)
	}
	return parts
}
