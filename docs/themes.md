# 主题开发

## 加载模型

主题是编译进 GopherInk 的 Go 包。主题包在 `init` 中调用 `plugin.RegisterTheme`，统一构建器会扫描 `themes/` 的直接子目录并生成临时空白导入。新增、删除或修改主题后必须重新编译并重启。

`themes/` 必须至少包含一个可构建主题包；可以删除默认主题，只保留第三方主题进行编译。主题目录可以属于主项目 module，也可以自带独立 `go.mod`。

后台“主题”页面只能在已编译主题之间切换，不支持上传压缩包后热加载源码。

主题的后台扩展页、数据调整回调、运行时初始化和评论增强回调会收到带主题名 owner 的 `plugin.Runtime`。确实需要保存主题私有的大量结构化数据时，可以复用 [插件数据库](plugins-and-hooks.md#插件数据库) 接口；常规主题设置仍应优先使用 `ConfigSchema`。

核心会把当前 CMS 语言注入主题运行时和模板数据：

- `rt.Language(ctx)`：返回规范化语言码，例如 `zh-CN` 或 `en-US`。
- `.Lang`：前台模板当前语言码。
- `.HTMLLang`：可直接用于 `<html lang="{{.HTMLLang}}">`。
- `T` / `t`：当前主题自己的模板翻译函数。主题实现 `Theme.Translate` 时由该函数处理；未实现时返回原文。

CMS 核心只负责暴露当前语言，不维护主题翻译表。主题需要语言适配时，应在主题包内维护自己的翻译逻辑和回落策略，例如中文未命中时回落英文；主题不适配某种语言时，注册时写入的原始文案会原样显示。主题的 `ConfigSchema`、`AdminPages`、`AdminNotices`、前台模板 `T/t` 都遵循这个边界：核心统一渲染后台外壳，但扩展文案由主题自己翻译。

## 最小主题结构

```text
themes/example/
├── theme.go
├── templates/
│   ├── base.html
│   ├── index.html
│   ├── post.html
│   └── 404.html
└── static/
    └── theme.css
```

注册示例：

```go
package exampletheme

import (
    "embed"
    "html/template"
    "io/fs"
    "strings"

    "github.com/Chocola-X/GopherInk/core/plugin"
)

//go:embed templates/* static/*
var themeFS embed.FS

func init() {
    staticFS, _ := fs.Sub(themeFS, "static")
    plugin.RegisterTheme(plugin.Theme{
        Name:         "example",
        DisplayName:  "Example Theme",
        Version:      "0.1.0",
        Author:       "Example Author",
        Description:  "Example theme.",
        TemplateList: []string{"index.html", "post.html", "404.html"},
        Templates:    themeFS,
        Static:       staticFS,
        Embedded:     true,
        Translate:    tr,
        Funcs: template.FuncMap{
            "upper": strings.ToUpper,
        },
    })
}

var zhCN = map[string]string{
    "Example Theme": "示例主题",
    "Example theme.": "示例主题。",
}

func tr(lang, key string) string {
    if strings.HasPrefix(strings.ToLower(lang), "zh") {
        if value := zhCN[key]; value != "" {
            return value
        }
    }
    return key
}
```

把主题目录放入 `themes/` 后执行 `make build` 或 `go run ./cmd/gopherink-builder -o gopherink` 即可参与构建，不需要手动修改 `cmd/gopherink/plugins.go`。

## 主题设置提示

主题可以通过 `Theme.AdminNotices` 在自己的原生设置页展示提示。回调会收到插件运行时和当前主题配置副本，适合说明缺失的必填资源或兼容性要求：

```go
plugin.RegisterTheme(plugin.Theme{
    Name: "example",
    // 其他主题字段省略。
    AdminNotices: func(ctx context.Context, rt *plugin.Runtime, values map[string]string) []plugin.AdminNotice {
        if values["background_image"] != "" {
            return nil
        }
        return []plugin.AdminNotice{{
            Type:    plugin.NoticeInfo,
            Mode:    plugin.NoticeCard,
            Message: "尚未设置背景图，前台将使用 MDUI 主题色背景。",
        }}
    },
})
```

`NoticeCard` 是设置页顶部的持续卡片，`NoticeSnackbar` 是底部短提示，类型和安全边界与 [插件后台提示](plugins-and-hooks.md#后台提示信息) 相同。主题设置提示只影响后台，不会自动注入前台页面；前台提示仍由主题模板自行设计。

`Name` 是配置键和内部标识，应保持稳定且只使用简单名称；`DisplayName` 用于后台展示，可以调整。

## Theme 字段

| 字段 | 用途 |
|---|---|
| `Name` | 技术名称和配置键 |
| `DisplayName` | 后台展示名称 |
| `Version`、`Author`、`Description`、`Homepage` | 主题元信息 |
| `Screenshot` | 主题预览图标识/地址 |
| `TemplateList` | 后台可选模板名称 |
| `Templates` | 包含模板的 `fs.FS` |
| `Static` | 静态资源 `fs.FS` |
| `Funcs` | 解析模板时加入的函数 |
| `Translate` | 主题自己的翻译函数；核心只传入当前语言和原始 key |
| `ConfigSchema` | 主题设置表单 Schema |
| `ContentFields` | 文章/页面自定义字段 Schema |
| `ConfigValidator` | 保存主题设置前执行跨字段校验 |
| `ConfigHandler` | 保存主题设置前执行额外处理或外部同步 |
| `AdminNotices` | 为主题原生设置页提供持续提示信息 |
| `AdminPages` | 主题设置页中的原生附加选项卡 |
| `RenderAdminPage` | 渲染附加选项卡的可信后台 HTML |
| `HandleAdminPageAction` | 处理附加选项卡经过鉴权和 CSRF 校验的 POST 操作 |
| `EnrichComments` | 批量生成主题评论增强数据，例如头像标志、CSS 类和额外字段 |
| `Capabilities` | 声明主题实现的核心协议能力 |
| `InitRuntime` | 每次前台渲染前调整主题运行时参数 |
| `AdjustData` | 渲染模板前补充或修改数据 |
| `EditableDir` | 非嵌入主题允许编辑的目录 |
| `Embedded` | 标记资源是否嵌入二进制 |

内置默认主题使用 `embed.FS`，因此后台不显示无意义的“文件”编辑按钮。

## 主题设置附加页面

复杂主题配置不适合压缩进固定 Schema 时，可以注册原生附加选项卡。核心负责管理员鉴权、CSRF 校验、后台外壳、选项卡导航、配置合并和统一通知；主题只负责渲染页面内容与校验业务字段：

```go
plugin.RegisterTheme(plugin.Theme{
    Name: "example",
    AdminPages: []plugin.AdminPage{{
        Name:        "friends",
        Label:       "友链",
        Icon:        "link",
        Title:       "友链设置",
        Description: "管理主题使用的友链数据。",
    }},
    RenderAdminPage: func(ctx context.Context, rt *plugin.Runtime, page string, rc plugin.AdminPageRenderContext) (template.HTML, error) {
        // rc.Config 包含 theme:example 的完整配置，rc.CSRF 用于 POST 表单。
        return renderFriends(rc.Config, rc.CSRF)
    },
    HandleAdminPageAction: func(ctx context.Context, rt *plugin.Runtime, page string, form map[string][]string) (plugin.AdminPageActionResult, error) {
        return plugin.AdminPageActionResult{
            ConfigPatch: map[string]string{"friends": encodedFriends},
            Notice: plugin.AdminNotice{
                Type: plugin.NoticeSuccess, Mode: plugin.NoticeSnackbar, Message: "友链已保存。",
            },
        }, nil
    },
})
```

选项卡地址由核心生成，为 `/admin/themes/<主题名>/config?tab=<页面名>`。`ConfigPatch` 只覆盖返回的键，普通 Schema 设置保存时也会保留附加页面管理的键。回调输出属于编译进程序的可信主题代码，但仍应使用 `html/template` 渲染外部数据；不要把用户输入直接拼进 `template.HTML`。

默认主题的友链功能使用这一接口，将目标独立页面、是否乱序和友链 JSON 保存在 `theme:default` 配置中，不修改内容自定义字段或核心数据库结构。目标页面支持 CID、自定义 slug、`/page/slug.html` 和完整页面 URL，且保存时必须解析为已发布独立页面。每条友链必须填写邮箱或图标 URL；显式图标优先，留空时通过统一邮箱头像接口生成。邮箱还用于匹配评论身份，不应写入公开页面。

## 统一邮箱头像

主题模板通过核心保留函数 `emailAvatarURL` 把邮箱转换为头像地址：

```gotemplate
<img src="{{emailAvatarURL .ProfileEmail 160}}" alt="">
```

该函数与插件的 `Runtime.AvatarURL(ctx, email, size)` 使用同一实现，会统一应用后台“自定义邮箱获取头像的替换链接”、`{hash}`、`{size}` 和头像等级设置。主题不应自行计算 MD5 或硬编码 Gravatar 域名。需要允许用户直接指定图片时，先判断显式图片 URL，留空后再调用 `emailAvatarURL`。

`EnrichComments` 在一次请求中接收当前页面的评论副本、当前主题配置和插件运行时，可以按邮箱、作者身份或主题配置返回以评论 ID 为键的 `CommentEnrichment`。回调签名为 `func(context.Context, *plugin.Runtime, map[string]string, []plugin.PublicComment) map[int64]plugin.CommentEnrichment`，第三个参数为当前主题配置副本。`CommentEnrichment.Badges` 用于显示博主、友链好友等头像标志，`CSSClasses` 用于追加主题样式类，`Extra` 可携带主题模板自用的数据。默认主题使用它显示博主和友链好友标志。回调应批量处理，避免逐条评论查询数据库。

## 运行时初始化

`InitRuntime` 在前台模板渲染前执行，适合让主题按自身配置调整运行时行为，例如每页文章数、评论排序、阅读模式或主题侧边栏数据准备。回调收到的 Runtime 已绑定到当前主题：

```go
InitRuntime: func(ctx context.Context, rt *plugin.Runtime) error {
    lang := "en-US"
    if rt.Language != nil {
        lang = rt.Language(ctx)
    }
    mode := "markdown"
    if rt.ContentRenderMode != nil {
        mode = rt.ContentRenderMode(ctx)
    }
    _, _ = lang, mode
    return nil
},
```

这不是后台保存钩子，也不应执行昂贵的一次性迁移。需要持久配置时仍使用 `ConfigSchema`、`ConfigHandler` 或主题附加页面。

## 评论守卫

主题可以使用核心提供的评论守卫，阻止只解析 HTML 表单并直接请求 `/comment` 的通用垃圾评论程序。主题只负责交互，token 签发、访客 Cookie、内容 ID 绑定、有效期和重放校验均由核心执行。

注册主题时声明能力：

```go
Capabilities: plugin.ThemeCapabilities{
    CommentGuard: true,
},
```

声明后，核心会向模板注入：

- `CommentGuardEnabled`：当前主题是否启用评论守卫。
- `CommentGuardEndpoint`：守卫签发地址，当前为 `/comment/guard`。

匿名评论表单必须包含守卫字段：

```gotemplate
<form id="comment-form" method="post" action="/comment"
      data-comment-guard-endpoint="{{.CommentGuardEndpoint}}">
  <input type="hidden" name="_csrf" value="{{.CommentCSRF}}">
  <input type="hidden" name="cid" value="{{.Post.CID}}">
  {{if not .CommentIdentity.LoggedIn}}
  <input type="hidden" name="_comment_guard" value="">
  {{end}}
</form>
```

提交前使用同源 `fetch` 请求 `GET {{.CommentGuardEndpoint}}?cid=<内容ID>`，同时发送请求头 `X-Requested-With: XMLHttpRequest` 和 `X-GopherInk-Comment: guard`，把 JSON 响应中的 `token` 写入 `_comment_guard`。随后通过 `fetch` 提交表单，并发送 `X-Requested-With: XMLHttpRequest` 和 `X-GopherInk-Comment: submit`。

最小前端接入示例：

```js
const form = document.querySelector("#comment-form");

form?.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!form.reportValidity()) return;

  const guardInput = form.querySelector('[name="_comment_guard"]');
  if (guardInput) {
    const guardURL = new URL(form.dataset.commentGuardEndpoint, location.href);
    guardURL.searchParams.set("cid", form.elements.cid.value);
    const response = await fetch(guardURL, {
      cache: "no-store",
      credentials: "same-origin",
      headers: {
        "X-Requested-With": "XMLHttpRequest",
        "X-GopherInk-Comment": "guard",
      },
    });
    if (!response.ok) throw new Error("comment guard failed");
    guardInput.value = (await response.json()).token;
  }

  const response = await fetch(form.action, {
    method: "POST",
    body: new FormData(form),
    cache: "no-store",
    credentials: "same-origin",
    redirect: "follow",
    headers: {
      "X-Requested-With": "XMLHttpRequest",
      "X-GopherInk-Comment": "submit",
    },
  });
  if (!response.ok) throw new Error("comment submit failed");
  location.assign(response.url);
});
```

启用该能力后，核心会拒绝缺少完整守卫流程的匿名评论。不要只在 JavaScript 中检查请求，也不要自行生成可预测 token。未声明 `CommentGuard` 的主题继续兼容普通表单提交。可直接复用 `themes/default/static/app.js` 中的 `initCommentSubmit` 实现。

## 模板解析

前台渲染会解析主题的 `base.html` 和目标模板，并注入：

- 处理器准备的站点、内容、分页、归档、评论和用户数据。
- 当前主题配置 `ThemeConfig map[string]string`。
- 主题 `Funcs`。
- `AdjustData` 修改后的附加数据。
- 插件生成的 `FrontendHead` 和 `FrontendFooter`。

目标模板通常定义供基础模板调用的 block。可直接参考 `themes/default/templates/` 的 `base.html`、`index.html`、`post.html` 和 `404.html`。

模板函数若返回 `template.HTML`，输入必须是可信内容。默认主题的 `safeHTML` 仅用于已经过服务端或插件明确处理的 HTML，不应直接包裹查询参数、评论原文等访客输入。

核心还提供 `isArchiveType` 模板函数，用于判断当前归档类型，作用接近 Typecho 的 `$this->is()`：

```gotemplate
{{if isArchiveType "post"}}...{{end}}
```

单篇文章和独立页面会额外提供一组评论和权限相关数据：

- `PostAllow.Comment`、`PostAllow.Ping`、`PostAllow.Feed`：当前内容是否允许评论、引用和 Feed 输出。
- `PostPasswordProtected`：当前内容是否设置了访问密码。
- `CommentAction`：评论提交地址，当前为 `/comment`。
- `CommentRespondID`：评论表单锚点 ID，默认是 `comment-form`。
- `CommentCSRF`：评论表单 CSRF 令牌。
- `CommentIdentity`：当前评论者身份信息，包含 `LoggedIn`（是否已登录）、`Name`（昵称）和 `AvatarURL`（头像地址）。

评论表单可以使用 `remember "author"`、`remember "mail"` 和 `remember "url"` 读取匿名访客上次评论时保存的称呼、邮箱和网址。该函数只读取核心评论记忆 Cookie，不做身份认证判断。

## 主题静态资源

当前核心按主题技术名称动态挂载主题静态文件：

```text
/theme/<theme-name>/
```

例如技术名为 `default` 的主题仍然使用 `/theme/default/`，第三方主题 `minimal` 会使用 `/theme/minimal/`。主题只要注册 `Theme.Static`，就可以通过自己的技术名访问静态资源；嵌入主题和非嵌入主题使用同一 URL 规则。

## 主题配置

主题通过 `ConfigSchema` 声明设置，保存为 `theme:<name>` JSON。支持字段类型：

```text
text, password, textarea, radio, checkbox, select, number, color, image
```

示例：

```go
ConfigSchema: []plugin.FieldSchema{
    {
        Name:    "primary_color",
        Label:   "主题色",
        Group:   "配色",
        Type:    plugin.FieldColor,
        Default: "#6750a4",
    },
    {
        Name:        "card_opacity",
        Label:       "卡片透明度",
        Group:       "配色",
        Type:        plugin.FieldNumber,
        Default:     "0.80",
        Min:         "0",
        Max:         "1",
        Step:        "0.01",
        Description: "只调整背景透明度。",
    },
    {
        Name:    "fallback_no_cover",
        Label:   "未设置封面时使用无封面样式",
        Group:   "站点资源",
        Type:    plugin.FieldCheckbox,
        Default: "1",
    },
    {
        Name:          "default_cover",
        Label:         "默认文章封面 URL",
        Group:         "站点资源",
        Type:          plugin.FieldImage,
        Required:      true,
        ShowWhenField: "fallback_no_cover",
        ShowWhenValue: "0",
        Wide:          true,
    },
}
```

`Group` 控制后台分组；`Wide` 适合 URL、长文本等需要整行宽度的输入。`ShowWhenField` 与 `ShowWhenValue` 控制字段在依赖值匹配时显示，`Required` 会对当前可见字段同时启用浏览器和服务端必填校验。数字字段应同时给出 `Min`、`Max` 和 `Step`，避免浏览器按默认整数步长拒绝小数。

模板读取配置时必须提供合理回退值，因为旧配置或空值不一定包含 Schema 默认值：

```gotemplate
<link rel="icon" href="{{themeValue .ThemeConfig "favicon" "/theme/default/favicon.svg"}}">
```

默认主题的 `enable_infinite_scroll` 配置默认关闭。启用后，浏览器接近文章列表底部时会读取分页中的下一页 URL，把返回页面的 `.article .post` 追加到当前列表，并继续观察新的下一页入口；关闭时保留常规分页和手动加载。服务端分页仍然存在，因此搜索引擎和禁用 JavaScript 的访问不会失去后续页面入口。

默认主题的文章过期横幅由 `show_stale_notice` 控制，默认开启。`stale_notice_days` 默认 30，只在开关启用时显示和参与必填校验；模板根据文章最后修改时间计算是否展示横幅。

## 图片和相对 URL

`FieldImage` 允许填写 URL或通过后台上传。主题必须兼容：

- `/uploads/...` 站内绝对路径。
- `uploads/...` 相对输入。
- `https://...`、`http://...` 和 `//...` 外部地址。

默认主题的 `assetURL` 会保留站内根路径、协议相对地址、片段和完整 URL，并为普通相对值补 `/`。第三方主题可实现等价函数，避免把相对 URL 当成无效值。

默认主题的图片 URL 还支持 `{random}` 占位符。每次生成资源地址时都会替换为不同的纯数字令牌，适合绕过随机图片 API 的中间缓存：

```text
https://api.mikupara.com/h?id={random}
```

首页使用默认封面时会为每篇文章分别调用 `assetURL`，因此同一页中的随机封面也不会共用令牌。

`fallback_no_cover` 只处理类型为 `article` 且没有 `cover` 字段值的内容。`normal` 是主题作者或编辑者明确指定的无封面类型，`daily` 使用独立的日常卡片，两者不会被默认封面覆盖。关闭回落后，缺少封面的普通文章才会使用必填的 `default_cover`。

当前主题设置素材专用上传和清理界面围绕内置 `default` 主题实现。第三方主题的图片字段会使用通用 Schema 上传入口；若需要独立素材目录和引用清理，应同时扩展后台处理器，而不是只增加 `FieldImage`。

## 内容字段

`Theme.ContentFields` 是主题注册文章/独立页面字段的入口。后台会按照 Schema 直接生成 MDUI 2 控件；这些预设字段与用户手动新增的通用自定义字段分区显示，但统一保存到 `gb_fields`：

```go
ContentFields: []plugin.FieldSchema{
    {
        Name:    "articleType",
        Label:   "文章类型",
        Group:   "主题显示",
        Type:    plugin.FieldSelect,
        Default: "article",
        Options: []plugin.FieldOption{
            {Label: "文章", Value: "article"},
            {Label: "无封面", Value: "normal"},
            {Label: "日常", Value: "daily"},
        },
        ForTypes: []string{"post", "page"},
    },
    {
        Name:        "cover",
        Label:       "文章/独立页面封面图",
        Type:        plugin.FieldImage,
        Description: "文章或页面封面 URL",
        ForTypes:    []string{"post", "page"},
        Wide:        true,
    },
}
```

`Type` 支持 `text`、`password`、`textarea`、`radio`、`checkbox`、`select`、`number`、`color` 和 `image`。`select`、`radio` 通过 `Options` 自定义候选项；`Default` 设置新内容的默认值；`Group` 控制编辑页分组；`ForTypes` 可限定为 `post` 或 `page`；`ReadOnly` 会同时作用于界面和服务端保护。

主题字段与插件字段经过统一校验和保存，模板数据中的 `.Fields` 映射可由主题函数读取。Schema 不再注册后，已有数据会作为普通自定义字段显示，用户可以继续编辑或删除。

内容详情页的字段位于 `.Fields`。首页、分类、标签、搜索和归档等聚合页为了避免逐篇查询，核心会批量读取字段并提供按 CID 索引的 `.PostFields`：

```gotemplate
{{$postFields := .PostFields}}
{{range .Posts}}
  {{$fields := index $postFields .CID}}
  {{$articleType := fieldString $fields "articleType"}}
{{end}}
```

## AdjustData

`AdjustData` 在模板执行前运行，适合计算主题专用数据：

```go
AdjustData: func(ctx context.Context, data map[string]any) error {
    data["ShowReadingProgress"] = true
    return nil
},
```

它没有直接数据库服务句柄。需要数据库数据时，应优先由核心提供稳定模板数据，或通过插件命名服务扩展，不要在主题中打开第二套数据库连接。渲染请求的 context 包含只读插件 Runtime，可按需获取：

```go
AdjustData: func(ctx context.Context, data map[string]any) error {
    rt, ok := plugin.RuntimeFromContext(ctx)
    if !ok || rt.ServiceAvailable == nil || !rt.ServiceAvailable("links.list") {
        return nil
    }
    links, err := rt.CallService(ctx, "links.list", 12, "friends")
    if err != nil {
        return err
    }
    data["FriendLinks"] = links
    return nil
},
```

这里取得的 Runtime 与插件收到的 Runtime 使用相同启停和权限边界，不会暴露核心数据库对象。

## 调用插件服务

主题模板可以调用已启用插件通过 `RegisterService` 公开的命名服务。核心保留两个模板函数：

- `pluginServiceAvailable "name"`：服务存在且所属插件已启用时返回 `true`。
- `pluginCall "name" arg...`：调用服务并返回结构化结果；失败会终止模板渲染。
- `pluginConfig "plugin-name"`：读取指定插件的站点配置，返回 `map[string]string`；插件不存在、未配置或读取失败时返回空 map。

以友情链接插件提供的 `links.list` 为例：

```gotemplate
{{if pluginServiceAvailable "links.list"}}
  <ul class="friend-links">
    {{range pluginCall "links.list" 12 "friends"}}
      <li>
        <a href="{{.URL}}" target="_blank" rel="noopener noreferrer">
          {{.Name}}
        </a>
        {{with .Description}}<span>{{.}}</span>{{end}}
      </li>
    {{end}}
  </ul>
{{end}}
```

主题应始终先检查服务是否可用，使插件停用或未编译时页面仍可渲染。服务返回的普通字符串和结构字段继续由 `html/template` 自动转义；只有插件明确返回 `template.HTML` 的内容才会作为 HTML 输出，因此主题不应再用 `safeHTML` 绕过未知插件数据的转义。

这套接口适合查询型能力，不取代 `frontend.head`、`frontend.footer` 等事件钩子。命名服务不会产生新 URL，也不会改变主题的静态资源挂载方式。

## 前端注入钩子

已启用插件可通过 `frontend.head` 和 `frontend.footer` 返回字符串，核心把最终值转换为 `template.HTML` 放入模板数据。主题需要在适当位置输出：

```gotemplate
<head>
  ...
  {{.FrontendHead}}
</head>
<body>
  ...
  {{.FrontendFooter}}
</body>
```

不输出这些字段会让依赖前端脚本或样式注入的插件失效。

## PJAX 注意事项

默认主题支持 PJAX。第三方主题启用局部导航时应保证：

- 浏览器历史、标题、canonical 和需要更新的 head 信息得到同步。
- 页面脚本使用可重复初始化和销毁的入口。
- 普通整页导航仍然可用，JS 失败时不影响内容访问。
- 评论、搜索、分页、归档和插件路由明确决定是否走 PJAX。

插件只通过 `frontend.head/footer` 注入一次性脚本时，必须自行处理 PJAX 后新内容的初始化。
