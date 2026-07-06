package plugin

import (
	"context"
	"html/template"
	"io/fs"
	"net/http"
	"sync"
)

const GoBlogVersion = "0.1.0"

type PublicContent struct {
	CID      int64
	Title    string
	Slug     string
	Created  int64
	Modified int64
	Text     string
	Type     string
	Status   string
}

type Runtime struct {
	ListPublished func(context.Context, int, int) ([]PublicContent, error)
	Option        func(context.Context, string) (string, error)
	Config        func(context.Context, string) (map[string]string, error)
}

type RouteHandler func(*Runtime, http.ResponseWriter, *http.Request)

type Route struct {
	Plugin  string
	Method  string
	Pattern string
	Handler RouteHandler
}

type HookFunc func(context.Context, any) (any, error)

const (
	HookContentBeforeSave      = "content.before_save"
	HookContentAfterSave       = "content.after_save"
	HookContentBeforeRender    = "content.before_render"
	HookContentAfterRender     = "content.after_render"
	HookExcerpt                = "content.excerpt"
	HookCommentBeforeSave      = "comment.before_save"
	HookCommentAfterSave       = "comment.after_save"
	HookUploadBeforeSave       = "upload.before_save"
	HookUploadAfterSave        = "upload.after_save"
	HookAttachmentBeforeDelete = "attachment.before_delete"
	HookAttachmentAfterDelete  = "attachment.after_delete"
	HookAdminMenu              = "admin.menu"
	HookFrontendHead           = "frontend.head"
	HookFrontendFooter         = "frontend.footer"
)

type ContentSavePayload struct {
	ID       int64
	AuthorID int64
	Input    any
}

type ContentRenderPayload struct {
	Content any
	HTML    template.HTML
	Data    map[string]any
}

type ExcerptPayload struct {
	Text   string
	Limit  int
	Output string
}

type CommentSavePayload struct {
	ID      int64
	Input   any
	Content any
}

type UploadPayload struct {
	Name     string
	ParentID int64
	Meta     any
}

type AttachmentPayload struct {
	Content any
	Meta    any
}

type AdminMenuItem struct {
	Label string
	URL   string
	Icon  string
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
)

type FieldOption struct {
	Label string
	Value string
}

type FieldSchema struct {
	Name        string
	Label       string
	Type        FieldType
	Default     string
	Description string
	Options     []FieldOption
	ForTypes    []string
}

type PluginInfo struct {
	Name          string
	Version       string
	Author        string
	Description   string
	Homepage      string
	RequireGoBlog string
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

type Theme struct {
	Name          string
	Version       string
	Author        string
	Description   string
	Homepage      string
	Screenshot    string
	TemplateList  []string
	Templates     fs.FS
	Static        fs.FS
	Funcs         template.FuncMap
	ConfigSchema  []FieldSchema
	ContentFields []FieldSchema
	AdjustData    func(context.Context, map[string]any) error
	EditableDir   string
	Embedded      bool
}

type Manager struct {
	mu            sync.RWMutex
	plugins       []Plugin
	pluginNames   map[string]Plugin
	hooks         map[string][]ownedHook
	routes        []ownedRoute
	themes        map[string]Theme
	activePlugins map[string]bool
	registering   string
}

var Default = NewManager()

func NewManager() *Manager {
	return &Manager{
		hooks:         make(map[string][]ownedHook),
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hooks[name] = append(m.hooks[name], ownedHook{Plugin: m.registering, Fn: fn})
}

func (m *Manager) Apply(ctx context.Context, name string, payload any) (any, error) {
	m.mu.RLock()
	hooks := append([]ownedHook(nil), m.hooks[name]...)
	m.mu.RUnlock()

	var err error
	for _, hook := range hooks {
		payload, err = hook.Fn(ctx, payload)
		if err != nil {
			return nil, err
		}
	}

	return payload, nil
}

func (m *Manager) ApplyActive(ctx context.Context, name string, payload any) (any, error) {
	m.mu.RLock()
	hooks := append([]ownedHook(nil), m.hooks[name]...)
	active := copyBoolMap(m.activePlugins)
	m.mu.RUnlock()

	var err error
	for _, hook := range hooks {
		if hook.Plugin != "" && !active[hook.Plugin] {
			continue
		}
		payload, err = hook.Fn(ctx, payload)
		if err != nil {
			return nil, err
		}
	}
	return payload, nil
}

func (m *Manager) RegisterRoute(method, pattern string, handler RouteHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes = append(m.routes, ownedRoute{Plugin: m.registering, Route: Route{Method: method, Pattern: pattern, Handler: handler}})
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
	Plugin string
	Fn     HookFunc
}

type ownedRoute struct {
	Plugin string
	Route  Route
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
