package plugin

import (
	"context"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"sync"
)

const GopherInkVersion = "0.5.0"

type PublicContent struct {
	CID      int64
	Title    string
	Slug     string
	SlugID   int64
	Created  int64
	Modified int64
	Text     string
	Type     string
	Status   string
	AuthorID int64
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
	OwnerID  int64
	Mail     string
	URL      string
	IP       string
	Agent    string
	Text     string
	Type     string
	Status   string
	Parent   int64
}

type Runtime struct {
	ListPublished     func(context.Context, int, int) ([]PublicContent, error)
	ContentByID       func(context.Context, int64) (PublicContent, error)
	PageBySlug        func(context.Context, string) (PublicContent, error)
	UserByID          func(context.Context, int64) (PublicUser, error)
	CommentByID       func(context.Context, int64) (PublicComment, error)
	ContentURL        func(context.Context, int64) (string, error)
	CommentURL        func(context.Context, int64) (string, error)
	AvatarURL         func(context.Context, string, int) string
	IncrementIntField func(context.Context, int64, string, int64) (int64, error)
	Option            func(context.Context, string) (string, error)
	Config            func(context.Context, string) (map[string]string, error)
	PersonalConfig    func(context.Context, string, int64) (map[string]string, error)
	DispatchHook      func(context.Context, string, any) (HookDispatch, error)
	ServiceAvailable  func(string) bool
	CallService       func(context.Context, string, ...any) (any, error)
	NotifyAdmin       func(http.ResponseWriter, *http.Request, ...AdminNotice)
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

type RouteHandler func(*Runtime, http.ResponseWriter, *http.Request)

// ServiceFunc exposes one named capability to other active plugins and themes.
// Structured return values remain subject to html/template escaping in themes.
type ServiceFunc func(context.Context, *Runtime, ...any) (any, error)

type Route struct {
	Plugin  string
	Method  string
	Pattern string
	Handler RouteHandler
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
	HookContentAfterDraftSave   = "content.after_draft_save"
	HookContentAfterPublish     = "content.after_publish"
	HookContentBeforeDelete     = "content.before_delete"
	HookContentAfterDelete      = "content.after_delete"
	HookContentBeforeStatus     = "content.before_status_change"
	HookContentAfterStatus      = "content.after_status_change"
	HookContentFilter           = "content.filter"
	HookContentBeforeRender     = "content.before_render"
	HookContentAfterRender      = "content.after_render"
	HookContentTitle            = "content.title"
	HookContentMarkdown         = "content.markdown"
	HookContentAutoParagraph    = "content.auto_paragraph"
	HookExcerpt                 = "content.excerpt"
	HookContentSearch           = "content.search"
	HookContentFields           = "content.fields"
	HookContentFieldReadOnly    = "content.field_read_only"
	HookCommentBeforeSave       = "comment.before_save"
	HookCommentAfterSave        = "comment.after_save"
	HookCommentBeforeReply      = "comment.before_reply"
	HookCommentAfterReply       = "comment.after_reply"
	HookCommentBeforeEdit       = "comment.before_edit"
	HookCommentAfterEdit        = "comment.after_edit"
	HookCommentBeforeMark       = "comment.before_mark"
	HookCommentAfterMark        = "comment.after_mark"
	HookCommentBeforeDelete     = "comment.before_delete"
	HookCommentAfterDelete      = "comment.after_delete"
	HookCommentFilter           = "comment.filter"
	HookCommentBeforeRender     = "comment.before_render"
	HookCommentAfterRender      = "comment.after_render"
	HookCommentMarkdown         = "comment.markdown"
	HookCommentAutoParagraph    = "comment.auto_paragraph"
	HookCommentAvatar           = "comment.avatar"
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
	HookAdminMenu               = "admin.menu"
	HookFrontendHead            = "frontend.head"
	HookFrontendFooter          = "frontend.footer"
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
	HTML    template.HTML
	Handled bool
}

type ContentSearchPayload struct {
	Stage    string
	Keywords string
	Query    any
	Results  any
	Total    int64
	Handled  bool
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
	HTML    template.HTML
	Handled bool
}

type CommentAvatarPayload struct {
	Comment any
	Mail    string
	Size    int
	URL     string
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

type AdminMenuItem struct {
	Label string
	URL   string
	Icon  string
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
	Type    string `json:"type"`
	Mode    string `json:"mode,omitempty"`
	Message string `json:"message"`
}

type AdminMenuProvider interface {
	AdminMenuItems(context.Context) []AdminMenuItem
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

type ContentFieldsProvider interface {
	ContentFieldSchema() []FieldSchema
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
	ConfigSchema          []FieldSchema
	ContentFields         []FieldSchema
	AdminNotices          func(context.Context, *Runtime, map[string]string) []AdminNotice
	AdminPages            []AdminPage
	RenderAdminPage       func(context.Context, *Runtime, string, AdminPageRenderContext) (template.HTML, error)
	HandleAdminPageAction func(context.Context, *Runtime, string, map[string][]string) (AdminPageActionResult, error)
	Capabilities          ThemeCapabilities
	AdjustData            func(context.Context, map[string]any) error
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
	hooks := append([]ownedHook(nil), m.hooks[name]...)
	active := copyBoolMap(m.activePlugins)
	m.mu.RUnlock()
	return dispatchHooks(ctx, hooks, active, payload)
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
	return service.Fn(ContextWithRuntime(ctx, runtime), runtime, args...)
}

func (m *Manager) RegisterRoute(method, pattern string, handler RouteHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes = append(m.routes, ownedRoute{Plugin: m.registering, Route: Route{Method: method, Pattern: pattern, Handler: handler}})
}

func (m *Manager) RegisterAdminMenu(item AdminMenuItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
			out = append(out, item.Item)
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
		out = append(out, provider.AdminMenuItems(ctx)...)
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
		var (
			next any
			err  error
		)
		if hook.RuntimeFn != nil {
			if runtime == nil {
				return HookDispatch{}, ErrRuntimeUnavailable
			}
			next, err = hook.RuntimeFn(ctx, runtime, result.Payload)
		} else if hook.Fn != nil {
			next, err = hook.Fn(ctx, result.Payload)
		} else {
			continue
		}
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
