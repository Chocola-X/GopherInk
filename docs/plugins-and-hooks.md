# 插件与钩子开发

## 插件模型

GopherInk 插件是参与主程序构建的 Go 包。插件包调用 `plugin.Register`，统一构建器扫描 `plugins/` 并生成临时空白导入，使插件的 `init` 执行。插件代码不支持运行时安装、热加载或卸载；每次新增或修改后必须重新编译并重启。

后台“启用/停用”只改变当前二进制中插件的活动状态：

- 停用后，其关联钩子不会由 `ApplyActive` 调用。
- 停用后，其注册的命名服务不可调用。
- 停用后，其路由不会挂载到活动路由集合。
- 停用后，其后台菜单不会显示。
- Go 代码仍然存在于进程中，不是动态卸载。

## 最小插件

目录：

```text
plugins/example/example.go
```

代码：

```go
package example

import (
    "context"

    "github.com/Chocola-X/GopherInk/core/plugin"
)

type Plugin struct{}

func init() {
    plugin.Register(Plugin{})
}

func (Plugin) Name() string        { return "example" }
func (Plugin) Version() string     { return "0.1.0" }
func (Plugin) Description() string { return "示例插件。" }

func (Plugin) Init(m *plugin.Manager) {
    m.RegisterHook(plugin.HookContentTitle, filterTitle)
}

func filterTitle(ctx context.Context, value any) (any, error) {
    payload, ok := value.(plugin.ContentTitlePayload)
    if !ok {
        return value, nil
    }
    payload.Title = "[Example] " + payload.Title
    return payload, nil
}
```

插件目录根部存在非测试 Go 文件即可被发现。普通目录直接属于 GopherInk module；需要作为独立仓库发布时，也可以在插件目录放置自己的 `go.mod`。检查并构建：

```bash
make list-plugins
make build
```

不使用 `make` 时执行 `go run ./cmd/gopherink-builder -list` 和 `go run ./cmd/gopherink-builder -o gopherink`。构建器只加载每个直接子目录的根包，不递归导入内部辅助包。直接使用原生 `go build ./cmd/gopherink` 不触发自动发现。

插件的 `Name()` 是稳定技术标识，配置、启用状态和钩子归属都使用它；发布后不要随意更名。

## 元信息和版本要求

插件可以实现 `InfoProvider`：

```go
func (Plugin) Info() plugin.PluginInfo {
    return plugin.PluginInfo{
        Name:             "example",
        Version:          "0.1.0",
        Author:           "Example Author",
        Description:      "示例插件。",
        Homepage:         "https://example.com/gopherink-example",
        RequireGopherInk: "0.5.0",
    }
}
```

`RequireGopherInk` 在启用时与 `plugin.GopherInkVersion` 比较。版本比较提取各段数字，不是完整 SemVer 预发布语义；建议使用简单的 `major.minor.patch`。

## 生命周期

插件可选择实现：

```go
func (Plugin) Activate(ctx context.Context, rt *plugin.Runtime) error {
    // 后台从停用切换为启用时执行。
    return nil
}

func (Plugin) Deactivate(ctx context.Context, rt *plugin.Runtime) error {
    // 后台从启用切换为停用时执行。
    return nil
}
```

对应接口为 `Activator` 和 `Deactivator`。这里适合验证配置或初始化插件自己的外部资源，不适合依赖每次进程启动都执行；启动时已有启用状态的恢复与后台点击启用是不同事件。

GopherInk 没有为插件提供自动建表迁移框架。需要持久化复杂数据的插件应明确维护自己的 Schema 策略，避免修改核心表结构；简单配置优先使用配置 Schema。

## Runtime

插件路由、生命周期和通过 `RegisterRuntimeHook` 注册的钩子会收到 `*plugin.Runtime`，当前提供：

| 方法 | 用途 |
|---|---|
| `ListPublished(ctx, limit, offset)` | 读取已发布、非未来文章的精简列表 |
| `ContentByID(ctx, id)` | 按内部 ID 读取单项精简内容，不自动检查公开状态 |
| `PageBySlug(ctx, slug)` | 按自定义 slug 或数字 slug ID 读取已发布独立页面 |
| `UserByID(ctx, id)` | 按用户 ID 读取不含密码和会话密钥的精简用户信息 |
| `CommentByID(ctx, id)` | 按评论 ID 读取精简评论，适合查询父评论 |
| `ContentURL(ctx, id)` | 生成内容的绝对固定链接，遵循站点固定链接规则 |
| `CommentURL(ctx, id)` | 生成带评论锚点的绝对链接 |
| `AvatarURL(ctx, email, size)` | 根据邮箱生成头像 URL，自动应用站点自定义头像地址、哈希和尺寸设置 |
| `IncrementIntField(ctx, cid, name, delta)` | 原子增加内容整数自定义字段 |
| `Option(ctx, name)` | 读取站点选项 |
| `Config(ctx, pluginName)` | 读取插件站点配置 |
| `PersonalConfig(ctx, pluginName, userID)` | 读取用户个人配置，并回落站点配置 |
| `DispatchHook(ctx, name, payload)` | 调用已启用插件的自定义钩子 |
| `ServiceAvailable(name)` | 判断已启用插件是否提供指定命名服务 |
| `CallService(ctx, name, args...)` | 调用已启用插件提供的唯一命名服务并取得结果 |
| `NotifyAdmin(w, r, notices...)` | 写入一次性后台提示，供重定向后的页面展示 |

`PublicContent`、`PublicUser` 和 `PublicComment` 是供插件使用的精简结构，不等同于核心模型。`ContentByID` 底层按内部 ID 读取，可能返回草稿、隐藏内容或附件；公开插件路由必须检查 `Type`、`Status` 并建立自己的授权边界，不能把“返回 PublicContent”理解成“已经确认公开”。需要核心服务模型的钩子 payload 会明确携带当前具体类型。

## 插件命名服务

命名服务用于插件向其他插件或主题公开一个有明确返回值的能力，例如友情链接列表、访问统计摘要或外部数据查询。它与钩子的语义不同：钩子允许多个处理器依次过滤同一 payload，命名服务只有一个提供方和一个直接返回值。

服务必须在插件的 `Init` 中注册，建议使用 `<插件名>.<能力>` 形式命名。空名称、空处理器、在 `Init` 之外注册或重复注册同名服务都会在启动阶段失败，避免运行时出现不确定提供方：

```go
type Link struct {
    Name        string
    URL         string
    Image       string
    Description string
    Category    string
}

func (Plugin) Init(m *plugin.Manager) {
    m.RegisterService("links.list", listLinks)
}

func listLinks(ctx context.Context, rt *plugin.Runtime, args ...any) (any, error) {
    limit := 0
    category := ""
    if len(args) > 0 {
        if value, ok := args[0].(int); ok && value >= 0 {
            limit = value
        }
    }
    if len(args) > 1 {
        category, _ = args[1].(string)
    }
    return queryLinks(ctx, limit, category)
}
```

另一个插件应通过收到的 `Runtime` 调用，不要保存全局数据库指针或直接依赖提供方的内部实现：

```go
if rt.ServiceAvailable != nil && rt.ServiceAvailable("links.list") {
    value, err := rt.CallService(ctx, "links.list", 12, "friends")
    if err != nil {
        return err
    }
    links, ok := value.([]Link)
    if !ok {
        return fmt.Errorf("links.list returned %T", value)
    }
    _ = links
}
```

插件之间需要稳定的静态类型时，应把请求和返回结构放到提供方的独立 `api` 子包，调用方只导入该 API 包。主题模板不需要 Go 类型断言，可以直接遍历导出的结构字段。服务处理器可能被并发请求调用，插件自己的存储和缓存必须保证并发安全。

`Runtime.CallService` 只调用当前已启用插件的服务。服务不存在或所属插件已停用时返回 `plugin.ErrServiceUnavailable`；调用方应把第三方集成视为可选能力并先检查 `ServiceAvailable`。服务调用不会创建 URL，因此无需额外注册 WAF 路径；服务内部若再发起外部请求或公开 HTTP 路由，仍需分别遵守超时、鉴权和 WAF 边界。

## 插件配置

实现 `ConfigProvider`：

```go
func (Plugin) ConfigSchema() []plugin.FieldSchema {
    return []plugin.FieldSchema{
        {
            Name:        "prefix",
            Label:       "标题前缀",
            Group:       "内容",
            Type:        plugin.FieldText,
            Default:     "[Example]",
            Description: "留空时不添加。",
        },
        {
            Name:    "enabled_for_pages",
            Label:   "处理独立页面",
            Group:   "内容",
            Type:    plugin.FieldCheckbox,
            Default: "0",
        },
    }
}
```

站点配置保存到选项 `plugin:example` 的 JSON 中。`Runtime.Config` 返回已保存配置，并为尚未保存的字段补齐 `ConfigSchema` 声明的非空默认值。插件路由中读取：

```go
cfg, err := rt.Config(r.Context(), "example")
if err != nil {
    http.Error(w, "configuration unavailable", http.StatusInternalServerError)
    return
}
prefix := cfg["prefix"]
```

支持字段类型为 `text`、`password`、`textarea`、`radio`、`checkbox`、`select`、`number`、`color` 和 `image`。`FieldSchema` 还支持分组、默认值、说明、必填、条件显示、数值边界、步长、选项、适用内容类型、只读和整行宽度。条件字段使用 `ShowWhenField` 和 `ShowWhenValue` 声明依赖，`Required` 只会校验当前可见的字段。

核心会自动提供 `/admin/plugins/<插件名>/config` 配置页。该页面使用后台原生 MDUI Schema 表单，并经过管理员鉴权、CSRF 校验、字段校验、持久化和统一 Snackbar 提示。需要设置页的插件应优先使用这个入口，不必注册自定义写入路由。

### 设置页操作按钮

设置之外的即时操作，例如测试 SMTP、验证 API 凭据或重建插件索引，应实现 `AdminActionProvider`。核心会把操作按钮放在插件配置页的“保存”按钮旁边：

```go
func (Plugin) AdminActions() []plugin.AdminAction {
    return []plugin.AdminAction{{
        Name:        "test-connection",
        Label:       "测试连接",
        Icon:        "send",
        Variant:     "outlined",
        Description: "使用已经保存的配置发起连接测试",
    }}
}

func (Plugin) HandleAdminAction(ctx context.Context, rt *plugin.Runtime, action string) (plugin.AdminNotice, error) {
    if action != "test-connection" {
        return plugin.AdminNotice{}, fmt.Errorf("unknown action %q", action)
    }
    cfg, err := rt.Config(ctx, "example")
    if err != nil {
        return plugin.AdminNotice{}, err
    }
    if err := testConnection(ctx, cfg); err != nil {
        return plugin.AdminNotice{}, fmt.Errorf("连接测试失败：%w", err)
    }
    return plugin.AdminNotice{
        Type:    plugin.NoticeSuccess,
        Mode:    plugin.NoticeSnackbar,
        Message: "连接测试成功。",
    }, nil
}
```

`Name` 只能使用小写字母开头，并由小写字母、数字、`-`、`_` 组成，最长 64 个字符。`Variant` 支持 `filled`、`elevated`、`tonal`、`text` 和 `outlined`；空值或未知值会回落为 `outlined`。

操作固定以 POST 请求到 `/admin/plugins/<插件名>/action/<操作名>`。核心会校验插件声明、管理员权限和 CSRF，并通过 PJAX 返回插件设置页。处理器只接收 `context.Context` 和 `Runtime`，有意不接收表单内容；动作应读取已经保存的配置，不能顺带持久化当前表单。返回的 `AdminNotice` 会按指定模式展示；返回错误时核心会自动生成错误 Snackbar。不要再为这类动作注册公开测试路由。

### 原生扩展页签

需要比 Schema 表单更复杂的设置界面时，实现 `AdminPageProvider`。扩展页仍位于插件原生配置地址，通过 `tab` 参数切换，核心负责管理员鉴权、CSRF、PJAX、页签导航和后台外壳：

```go
func (Plugin) AdminPages() []plugin.AdminPage {
    return []plugin.AdminPage{{
        Name:        "templates",
        Label:       "模板",
        Icon:        "code",
        Title:       "消息模板",
        Description: "维护插件发送的消息模板。",
    }}
}

func (Plugin) RenderAdminPage(ctx context.Context, rt *plugin.Runtime, page string, rc plugin.AdminPageRenderContext) (template.HTML, error) {
    if page != "templates" {
        return "", fmt.Errorf("unknown page %q", page)
    }
    // rc.Config 是当前插件配置的副本，rc.CSRF 用于页面内 POST 表单。
    return renderTemplates(rc.Config, rc.CSRF)
}
```

页面 `Name` 遵循与设置动作相同的命名限制。插件设置页会自动出现“设置”和扩展页签，示例页面地址为 `/admin/plugins/example/config?tab=templates`。

需要保存页面内容时，再实现 `AdminPageActionProvider`：

```go
func (Plugin) HandleAdminPageAction(ctx context.Context, rt *plugin.Runtime, page string, form map[string][]string) (plugin.AdminPageActionResult, error) {
    if page != "templates" || first(form["action"]) != "save" {
        return plugin.AdminPageActionResult{}, fmt.Errorf("unsupported action")
    }
    return plugin.AdminPageActionResult{
        ConfigPatch: map[string]string{"message_template": first(form["body"])},
        Notice: plugin.AdminNotice{
            Type: plugin.NoticeSuccess, Mode: plugin.NoticeSnackbar, Message: "模板已保存。",
        },
    }, nil
}
```

`ConfigPatch` 只会合并到当前插件自己的配置，不会覆盖未列出的字段。页面返回的 HTML 来自已编译插件，属于可信代码；核心不会清洗它。插件必须转义数据库内容、请求参数和用户可编辑模板，不能直接把这些内容转换为 `template.HTML`。需要预览用户提供的 HTML 或 JavaScript 时，应使用不带 `allow-same-origin` 的沙箱 iframe，不能直接插入后台 DOM。

## 后台提示信息

后台扩展提示使用 `plugin.AdminNotice`。`Type` 支持 `NoticeInfo`、`NoticeSuccess`、`NoticeWarning` 和 `NoticeError`；`Mode` 支持：

| 模式 | 展示方式 | 适用场景 |
|---|---|---|
| `NoticeSnackbar` | 页面底部 MDUI Snackbar，自动关闭 | 保存成功、复制成功、短错误 |
| `NoticeCard` | 后台内容顶部提示卡片，持续显示 | 配置缺失、兼容性和较长说明 |
| `NoticeAuto` 或空值 | 兼容旧行为，由内容长度自动判断 | 核心旧提示，不建议新插件依赖 |

提示内容按纯文本输出，不解析插件提供的 HTML。需要交互控件时应使用插件自己的、具备完整鉴权和 CSRF 防护的管理页面，不能把 HTML 塞入提示消息。

### 一次性提示

插件路由完成操作后，在重定向前调用 `Runtime.NotifyAdmin`：

```go
func rebuildIndex(rt *plugin.Runtime, w http.ResponseWriter, r *http.Request) {
    // 插件仍须自行完成管理员鉴权、权限判断和 CSRF 校验。
    if rt.NotifyAdmin != nil {
        rt.NotifyAdmin(w, r, plugin.AdminNotice{
            Type:    plugin.NoticeSuccess,
            Mode:    plugin.NoticeSnackbar,
            Message: "索引已重建。",
        })
    }
    http.Redirect(w, r, "/admin/plugins/example/config", http.StatusSeeOther)
}
```

提示通过签名、HttpOnly、SameSite Cookie 在下一次后台页面渲染时消费，不是跨用户消息队列，也不会为插件路由附加后台鉴权。插件必须先完成自身动作的安全检查。

### 设置页持续提示

实现 `AdminNoticeProvider` 后，核心会在插件原生配置页调用它。`values` 是当前配置的副本，插件修改它不会改写已保存配置：

```go
func (Plugin) AdminNotices(ctx context.Context, rt *plugin.Runtime, values map[string]string) []plugin.AdminNotice {
    if values["api_key"] != "" {
        return nil
    }
    return []plugin.AdminNotice{{
        Type:    plugin.NoticeWarning,
        Mode:    plugin.NoticeCard,
        Message: "API 密钥尚未配置，插件当前不会处理请求。",
    }}
}
```

持续提示会在每次打开设置页时重新计算。不要用 `NoticeSnackbar` 展示永久性配置警告，否则每次访问都会重复弹出。

## 个人配置

实现 `PersonalConfigProvider` 后，用户可以在个人设置中维护自己的值：

```go
func (Plugin) PersonalConfigSchema() []plugin.FieldSchema {
    return []plugin.FieldSchema{
        {
            Name:    "compact_mode",
            Label:   "紧凑显示",
            Type:    plugin.FieldCheckbox,
            Default: "0",
        },
    }
}
```

个人配置使用 `plugin:<name>:personal` 和用户维度保存。`Runtime.PersonalConfig` 会把未被个人值覆盖的字段回落到插件站点配置。访客和订阅者也可以维护自己的插件个人配置，但只能操作当前登录用户。

## 路由

插件在 `Init` 中注册路由：

```go
func (Plugin) Init(m *plugin.Manager) {
    m.RegisterRoute(http.MethodGet, "/example/status", statusHandler)
}

func statusHandler(rt *plugin.Runtime, w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    _ = json.NewEncoder(w).Encode(map[string]any{
        "plugin": "example",
        "ok":     true,
    })
}
```

路由的 method 为空表示不限制方法；公开接口应显式填写方法。以 `/` 结尾的 pattern 可匹配其子路径，普通 pattern 只匹配清理后的同一路径。

### 路由安全边界

插件路由不会因为路径以 `/admin/` 开头就自动获得核心后台鉴权、角色判断或 CSRF 防护。当前 `Runtime` 也没有暴露“当前管理员”认证 API。

因此：

- 不要把敏感写接口仅通过 `RegisterRoute` 挂到 `/admin/...` 并假定它是安全的。
- 公开写接口必须自行设计认证、CSRF 和权限边界。
- 只读公开路由仍应验证参数、限制响应大小并设置正确 Content-Type。
- 保存插件配置时应实现 `ConfigProvider`，使用核心提供的原生配置页。只有原生 Schema 无法表达的复杂管理功能才需要自定义路由，并且插件必须自行完成鉴权和 CSRF 防护。

WAF 会识别已启用插件路由，使其不被公开 URL 索引当成非法路径；它仍会经过常规 IP和分类限流。

## 后台侧边栏入口

插件主动注册菜单时才显示插件侧边栏；没有管理页面的简单插件无需注册。

直接注册：

```go
func (Plugin) Init(m *plugin.Manager) {
    m.RegisterAdminMenu(plugin.AdminMenuItem{
        Label: "示例设置",
        URL:   "/admin/plugins/example/config",
        Icon:  "settings",
    })
}
```

也可实现动态提供接口：

```go
func (Plugin) AdminMenuItems(ctx context.Context) []plugin.AdminMenuItem {
    return []plugin.AdminMenuItem{
        {Label: "示例设置", URL: "/admin/plugins/example/config", Icon: "settings"},
    }
}
```

`Icon` 使用后台 Material Icon 名称，留空时界面使用默认 `extension`。菜单本身只负责导航外观；上例目标是核心原生配置页，因此具备后台权限和 CSRF 防护。需要自定义界面时优先实现 `AdminPageProvider`；只有公开接口或确实不能放入后台外壳的功能才使用自定义路由，并自行处理对应安全边界。

## 钩子调度

### 注册与优先级

```go
m.RegisterHook(plugin.HookContentFilter, normalFilter)
m.RegisterHookWithPriority(plugin.HookContentFilter, plugin.HookPriorityEarly, earlyFilter)
m.RegisterHookWithPriority(plugin.HookContentFilter, 20, laterFilter)
```

优先级数值越小越先执行。内置常量：

| 常量 | 值 |
|---|---:|
| `HookPriorityEarly` | -100 |
| `HookPriorityNormal` | 0 |
| `HookPriorityLate` | 100 |

相同优先级严格保持注册顺序。钩子是链式过滤器：上一个返回值成为下一个输入。

需要读取插件配置或查询关联数据时，使用运行时钩子：

```go
func (Plugin) Init(m *plugin.Manager) {
    m.RegisterRuntimeHook(plugin.HookCommentAfterSave, notifyComment)
}

func notifyComment(ctx context.Context, rt *plugin.Runtime, value any) (any, error) {
    payload, ok := value.(plugin.CommentSavePayload)
    if !ok {
        return value, nil
    }
    cfg, err := rt.Config(ctx, "example")
    if err != nil || cfg["enabled"] != "1" {
        return value, nil
    }
    comment, err := rt.CommentByID(ctx, payload.ID)
    if err != nil {
        return value, nil
    }
    link, err := rt.CommentURL(ctx, comment.COID)
    if err != nil {
        return value, nil
    }
    _ = link // 交给插件自身的有界邮件队列。
    return value, nil
}
```

`RegisterHook` 继续适合不依赖运行时的纯过滤器，两套接口可以并存。运行时由核心随请求上下文注入；插件自行调用 `Manager.Apply` 测试运行时钩子时，应先用 `plugin.ContextWithRuntime` 包装 context。

### Apply 与 Dispatch

- `Apply` / `Dispatch` 不检查插件启用状态，主要供管理器内部或明确场景使用。
- `ApplyActive` / `DispatchActive` 只调用已启用插件，核心生命周期使用这一组。
- `Dispatch` 比 `Apply` 多返回 `Triggered` 和 `Stopped`。

```go
result, err := rt.DispatchHook(ctx, "example.cache.lookup", key)
if err != nil {
    return err
}
if !result.Triggered {
    // 没有已启用处理器，执行默认查询。
}
```

插件可使用命名空间字符串定义自有钩子，例如 `example.cache.lookup`。

### 停止传播

返回 `plugin.StopHook(payload)` 会保留 payload 并停止后续钩子：

```go
return plugin.StopHook(payload), nil
```

`Dispatch.Stopped` 同时变为 `true`。返回普通错误也会停止链，但错误代表操作失败，不应当作正常的“已经处理”信号。

### Triggered 与 Handled

两个概念不能混用：

- `Triggered`：至少调用了一个已启用钩子。
- payload 中的 `Handled`：插件已经提供默认实现所需的完整结果。

解析器、搜索、上传、替换、删除和附件数据钩子使用 `Handled`。仅记录日志的插件不能设置它，否则核心会跳过默认实现并得到空结果。

## 内置钩子总览

### 内容保存、删除和状态

| 常量 | 字符串 | Payload | 时机 |
|---|---|---|---|
| `HookContentBeforeSave` | `content.before_save` | `ContentSavePayload` | 任意内容写入前，可修改 Input 或拒绝 |
| `HookContentAfterSave` | `content.after_save` | `ContentSavePayload` | 任意保存完成后 |
| `HookContentAfterDraftSave` | `content.after_draft_save` | `ContentSavePayload` | 草稿/自动保存完成后 |
| `HookContentAfterPublish` | `content.after_publish` | `ContentSavePayload` | 发布或提交待审核完成后 |
| `HookContentBeforeDelete` | `content.before_delete` | `ContentDeletePayload` | 内容及附件删除前 |
| `HookContentAfterDelete` | `content.after_delete` | `ContentDeletePayload` | 删除完成后 |
| `HookContentBeforeStatus` | `content.before_status_change` | `ContentStatusPayload` | 状态写入前，可修改 Status |
| `HookContentAfterStatus` | `content.after_status_change` | `ContentStatusPayload` | 状态变更完成后 |

`ContentSavePayload.Operation` 的稳定值是 `draft`、`autosave`、`publish`。`PublishedID` 在编辑已发布内容时指向发布记录；完成钩子中的 `ID` 是实际写入/发布后的记录 ID。

### 内容查询、渲染和字段

| 常量 | 字符串 | Payload | 用途 |
|---|---|---|---|
| `HookContentFilter` | `content.filter` | `ContentFilterPayload` | 内容对象进入主题前过滤 |
| `HookContentBeforeRender` | `content.before_render` | `ContentRenderPayload` | 正文解析前修改内容/原文 |
| `HookContentAfterRender` | `content.after_render` | `ContentRenderPayload` | 过滤最终 HTML |
| `HookContentTitle` | `content.title` | `ContentTitlePayload` | 过滤输出标题 |
| `HookContentMarkdown` | `content.markdown` | `ContentParserPayload` | 接管 Markdown 解析 |
| `HookContentAutoParagraph` | `content.auto_paragraph` | `ContentParserPayload` | 接管纯文本/AutoP |
| `HookExcerpt` | `content.excerpt` | `ExcerptPayload` | 过滤摘要结果 |
| `HookContentSearch` | `content.search` | `ContentSearchPayload` | 查询前接管/修改，查询后过滤 |
| `HookContentFields` | `content.fields` | `ContentFieldsPayload` | 动态增减字段 Schema |
| `HookContentFieldReadOnly` | `content.field_read_only` | `ContentFieldReadOnlyPayload` | 动态决定字段只读 |

搜索 payload 的 `Stage` 为 `before` 或 `after`。当前 `Query` 是 `services.ContentQuery`，`Results` 是 `[]models.Content`。`before` 设置 `Handled=true` 后核心不执行默认 SQL LIKE 搜索。

### 评论保存和管理

| 常量 | 字符串 | Payload | 时机 |
|---|---|---|---|
| `HookCommentBeforeSave` | `comment.before_save` | `CommentSavePayload` | 所有评论类型写入前 |
| `HookCommentAfterSave` | `comment.after_save` | `CommentSavePayload` | 所有评论类型写入后 |
| `HookCommentBeforeReply` | `comment.before_reply` | `CommentSavePayload` | 后台回复前 |
| `HookCommentAfterReply` | `comment.after_reply` | `CommentSavePayload` | 后台回复后 |
| `HookCommentBeforeEdit` | `comment.before_edit` | `CommentSavePayload` | 后台编辑前 |
| `HookCommentAfterEdit` | `comment.after_edit` | `CommentSavePayload` | 后台编辑后 |
| `HookCommentBeforeMark` | `comment.before_mark` | `CommentActionPayload` | 审核状态变更前 |
| `HookCommentAfterMark` | `comment.after_mark` | `CommentActionPayload` | 状态和评论数同步后 |
| `HookCommentBeforeDelete` | `comment.before_delete` | `CommentActionPayload` | 删除前 |
| `HookCommentAfterDelete` | `comment.after_delete` | `CommentActionPayload` | 删除和子评论处理后 |

`CommentSavePayload.Operation` 为 `comment`、`reply`、`edit`、`pingback` 或 `trackback`。当前 `Input` 是 `services.SaveCommentInput`，`Content` 是 `models.Content`，`Comment` 是 `models.Comment`。

邮件提醒类插件通常同时监听 `HookCommentAfterSave` 和 `HookCommentAfterMark`：前者处理新评论和后台回复，后者只在 `PreviousStatus != "approved" && Status == "approved"` 时补发审核通过后的通知。父评论可通过 `Runtime.CommentByID` 查询，内容作者可通过 payload 中 `models.Content.AuthorID` 与 `Runtime.UserByID` 查询，邮件中的公开地址使用 `Runtime.CommentURL`，不要自行拼接固定链接。

### 评论查询和渲染

| 常量 | 字符串 | Payload | 用途 |
|---|---|---|---|
| `HookCommentFilter` | `comment.filter` | `CommentFilterPayload` | 评论进入后台/主题前过滤 |
| `HookCommentBeforeRender` | `comment.before_render` | `CommentRenderPayload` | 解析前修改原文 |
| `HookCommentAfterRender` | `comment.after_render` | `CommentRenderPayload` | 过滤最终 HTML |
| `HookCommentMarkdown` | `comment.markdown` | `CommentParserPayload` | 接管评论 Markdown |
| `HookCommentAutoParagraph` | `comment.auto_paragraph` | `CommentParserPayload` | 接管普通文本解析 |
| `HookCommentAvatar` | `comment.avatar` | `CommentAvatarPayload` | 修改头像 URL |

### 上传和附件

| 常量 | 字符串 | Payload | 用途 |
|---|---|---|---|
| `HookUploadBeforeSave` | `upload.before_save` | `UploadPayload` | 校验/处理前修改名称或父级 |
| `HookUploadHandle` | `upload.handle` | `UploadHandlePayload` | 接管实际存储 |
| `HookUploadAfterSave` | `upload.after_save` | `UploadPayload` | 写入后、创建附件记录前过滤 Meta |
| `HookAttachmentBeforeEdit` | `attachment.before_edit` | `AttachmentEditPayload` | 修改标题/描述前 |
| `HookAttachmentAfterEdit` | `attachment.after_edit` | `AttachmentEditPayload` | 信息保存后 |
| `HookAttachmentBeforeReplace` | `attachment.before_replace` | `AttachmentReplacePayload` | 替换前检查输入 |
| `HookAttachmentReplaceHandle` | `attachment.replace_handle` | `AttachmentReplacePayload` | 接管实际替换 |
| `HookAttachmentAfterReplace` | `attachment.after_replace` | `AttachmentReplacePayload` | 替换结果写入记录前 |
| `HookAttachmentBeforeDelete` | `attachment.before_delete` | `AttachmentPayload` | 删除前 |
| `HookAttachmentDeleteHandle` | `attachment.delete_handle` | `AttachmentDeleteHandlePayload` | 接管物理文件删除 |
| `HookAttachmentAfterDelete` | `attachment.after_delete` | `AttachmentPayload` | 文件和记录删除后 |
| `HookAttachmentURL` | `attachment.url` | `AttachmentURLPayload` | 动态改写公开 URL |
| `HookAttachmentData` | `attachment.data` | `AttachmentDataPayload` | 为缩略图等提供远程文件字节 |

当前 Meta 的具体类型是 `models.AttachmentMeta`，Content 为 `models.Content`。`Open` 返回暂存文件的新读取流，只能在当前同步调用中使用。

### 后台和前端

| 常量 | 字符串 | Payload | 用途 |
|---|---|---|---|
| `HookAdminMenu` | `admin.menu` | `[]plugin.AdminMenuItem` | 过滤最终后台插件菜单 |
| `HookFrontendHead` | `frontend.head` | `string` | 追加 head HTML |
| `HookFrontendFooter` | `frontend.footer` | `string` | 追加 body 底部 HTML |

Head/Footer 最终字符串会转换为 `template.HTML`，插件必须保证内容可信。

## Payload 字段速查

```go
type ContentSavePayload struct {
    ID, PublishedID, AuthorID int64
    Operation string
    Input, Content any
}

type ContentRenderPayload struct {
    Content any
    HTML template.HTML
    Data map[string]any
}

type ContentParserPayload struct {
    Content any
    Text string
    HTML template.HTML
    Handled bool
}

type CommentSavePayload struct {
    ID int64
    Operation string
    Input, Content, Comment any
}

type UploadHandlePayload struct {
    Name string
    ParentID int64
    Bucket string
    Size int64
    MIME string
    Open func() (io.ReadCloser, error)
    Meta any
    Handled bool
}
```

其他 payload 以 `core/plugin/plugin.go` 为最终定义。插件应始终使用带 `ok` 的类型断言，方便核心或其他插件返回意外类型时平稳跳过。

## 示例：修改保存输入

```go
func normalizeTitle(ctx context.Context, value any) (any, error) {
    payload, ok := value.(plugin.ContentSavePayload)
    if !ok {
        return value, nil
    }
    input, ok := payload.Input.(services.SaveContentInput)
    if !ok {
        return value, nil
    }
    input.Title = strings.TrimSpace(input.Title)
    if input.Title == "" {
        return value, errors.New("标题不能为空")
    }
    payload.Input = input
    return payload, nil
}
```

前置钩子返回错误会阻止保存。完成类钩子的错误也会返回调用方，但数据库操作可能已经完成，因此完成钩子应尽量幂等，不要用错误模拟事务回滚。

## 示例：接管 Markdown

```go
func renderMarkdown(ctx context.Context, value any) (any, error) {
    payload, ok := value.(plugin.ContentParserPayload)
    if !ok {
        return value, nil
    }
    output, err := myMarkdownRenderer(payload.Text)
    if err != nil {
        return value, err
    }
    payload.HTML = template.HTML(output) // output 必须已经安全处理
    payload.Handled = true
    return plugin.StopHook(payload), nil
}
```

只修改 `HTML` 而不设置 `Handled`，核心仍会执行默认解析。设置 `Handled` 后必须返回完整结果。

## 示例：评论反垃圾

```go
func moderateComment(ctx context.Context, value any) (any, error) {
    payload, ok := value.(plugin.CommentSavePayload)
    if !ok {
        return value, nil
    }
    input, ok := payload.Input.(services.SaveCommentInput)
    if !ok {
        return value, nil
    }
    if strings.Contains(strings.ToLower(input.Text), "buy followers") {
        input.Status = "spam"
        payload.Input = input
    }
    return payload, nil
}
```

普通评论、Pingback 和 Trackback 都会触发通用钩子。需要区分时检查 `payload.Operation`，不要仅根据输入字段猜测来源。

## 示例：动态内容字段

静态字段优先实现接口：

```go
func (Plugin) ContentFieldSchema() []plugin.FieldSchema {
    return []plugin.FieldSchema{
        {
            Name:     "source_url",
            Label:    "来源 URL",
            Type:     plugin.FieldText,
            Wide:     true,
            ForTypes: []string{"post"},
        },
    }
}
```

需要按内容动态添加时：

```go
func addFields(ctx context.Context, value any) (any, error) {
    payload, ok := value.(plugin.ContentFieldsPayload)
    if !ok || payload.Type != "post" {
        return value, nil
    }
    payload.Fields = append(payload.Fields, plugin.FieldSchema{
        Name: "campaign", Label: "活动代号", Type: plugin.FieldText,
    })
    return payload, nil
}
```

服务端会再次执行字段名称、适用类型和只读校验。

## 示例：改写附件 URL

```go
func useCDN(ctx context.Context, value any) (any, error) {
    payload, ok := value.(plugin.AttachmentURLPayload)
    if !ok {
        return value, nil
    }
    meta, ok := payload.Meta.(models.AttachmentMeta)
    if !ok || !strings.HasPrefix(meta.URL, "/uploads/") {
        return value, nil
    }
    payload.URL = "https://cdn.example.com" + meta.URL
    return payload, nil
}
```

签名 URL 插件应考虑页面缓存 TTL，避免缓存的 URL 在页面仍有效时提前过期。

## 示例：接管附件数据

```go
func provideAttachmentData(ctx context.Context, value any) (any, error) {
    payload, ok := value.(plugin.AttachmentDataPayload)
    if !ok {
        return value, nil
    }
    meta, ok := payload.Meta.(models.AttachmentMeta)
    if !ok || !strings.HasPrefix(meta.Path, "object://") {
        return value, nil
    }
    data, err := objectStoreGet(ctx, strings.TrimPrefix(meta.Path, "object://"))
    if err != nil {
        return value, err
    }
    payload.Data = data
    payload.Handled = true
    return payload, nil
}
```

此钩子会为后台远程图片缩略图提供数据。实现应限制最大对象大小，避免一次读入任意大文件。

## 示例：前端注入

```go
func injectHead(ctx context.Context, value any) (any, error) {
    current, _ := value.(string)
    current += `<meta name="example-plugin" content="enabled">`
    return current, nil
}

func injectFooter(ctx context.Context, value any) (any, error) {
    current, _ := value.(string)
    current += `<script src="/example/client.js" defer></script>`
    return current, nil
}
```

主题必须输出 `.FrontendHead` 和 `.FrontendFooter`。启用 PJAX 的主题中，脚本还要处理局部导航后的重复初始化。

## 示例：原子阅读计数

插件路由可以使用运行时字段原子增量：

```go
func countView(rt *plugin.Runtime, w http.ResponseWriter, r *http.Request) {
    cid, err := strconv.ParseInt(r.URL.Query().Get("cid"), 10, 64)
    if err != nil || cid <= 0 {
        http.Error(w, "invalid cid", http.StatusBadRequest)
        return
    }
    content, err := rt.ContentByID(r.Context(), cid)
    if err != nil || content.Type != "post" || content.Status != "publish" {
        http.NotFound(w, r)
        return
    }
    views, err := rt.IncrementIntField(r.Context(), cid, "views", 1)
    if err != nil {
        http.Error(w, "counter unavailable", http.StatusInternalServerError)
        return
    }
    _ = json.NewEncoder(w).Encode(map[string]int64{"views": views})
}
```

公开计数接口仍应考虑 WAF、去重和滥用；原子增量只保证数据库更新不会丢失并发加法，不验证一次访问的真实性。

## 开发检查清单

1. 插件 `Name()` 稳定、唯一，并设置最低 GopherInk 版本。
2. 所有 payload 使用安全类型断言，链式返回修改后的 payload。
3. 只有真正接管默认实现时设置 `Handled=true`。
4. 前置钩子错误用于拒绝操作；完成钩子保持幂等。
5. 解析器和 HTML 注入自行处理转义/净化。
6. 上传存储插件同时处理替换、删除、URL和缩略图数据。
7. 插件路由明确方法、输入上限、Content-Type、认证和 CSRF 边界。
8. 监听自动保存和发布时检查 `Operation`，避免重复外发任务。
9. 修改源码后先确认 `make list-plugins` 能发现插件，再重新编译；后台启停不能加载新代码。
10. 新公开路由在 WAF 开启状态下验证限流、404和缓存行为。
