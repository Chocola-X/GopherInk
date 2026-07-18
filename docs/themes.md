# 主题开发

## 加载模型

主题是编译进 GopherInk 的 Go 包。主题包在 `init` 中调用 `plugin.RegisterTheme`，并在 `cmd/gopherink/plugins.go` 通过空白导入参与构建。新增或修改主题后必须重新编译并重启。

后台“主题”页面只能在已编译主题之间切换，不支持上传压缩包后热加载源码。

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
        Description:  "示例主题。",
        TemplateList: []string{"index.html", "post.html", "404.html"},
        Templates:    themeFS,
        Static:       staticFS,
        Embedded:     true,
        Funcs: template.FuncMap{
            "upper": strings.ToUpper,
        },
    })
}
```

然后在 `cmd/gopherink/plugins.go` 加入：

```go
_ "github.com/Chocola-X/GopherInk/themes/example"
```

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
| `ConfigSchema` | 主题设置表单 Schema |
| `ContentFields` | 文章/页面自定义字段 Schema |
| `AdjustData` | 渲染模板前补充或修改数据 |
| `EditableDir` | 非嵌入主题允许编辑的目录 |
| `Embedded` | 标记资源是否嵌入二进制 |

内置默认主题使用 `embed.FS`，因此后台不显示无意义的“文件”编辑按钮。

## 模板解析

前台渲染会解析主题的 `base.html` 和目标模板，并注入：

- 处理器准备的站点、内容、分页、归档、评论和用户数据。
- 当前主题配置 `ThemeConfig map[string]string`。
- 主题 `Funcs`。
- `AdjustData` 修改后的附加数据。
- 插件生成的 `FrontendHead` 和 `FrontendFooter`。

目标模板通常定义供基础模板调用的 block。可直接参考 `themes/default/templates/` 的 `base.html`、`index.html`、`post.html` 和 `404.html`。

模板函数若返回 `template.HTML`，输入必须是可信内容。默认主题的 `safeHTML` 仅用于已经过服务端或插件明确处理的 HTML，不应直接包裹查询参数、评论原文等访客输入。

## 主题静态资源

当前核心在启动时把内置主题静态文件挂载到：

```text
/theme/default/
```

这是当前 v0.5.0 的实际实现边界：静态挂载仍针对技术名称 `default`。新增第三方主题若需要独立 `/theme/<name>/` 静态目录，应同步扩展 `core/handlers/app.go` 的主题静态路由，或在主题方案中使用已有公开资源。仅注册 `Theme.Static` 不会自动得到任意名称的静态 URL。

不要把这个限制误解成推荐所有主题复用 `/theme/default/`；后续若通用化路由，应以主题名称动态挂载并同步 WAF 静态资源分类。

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
        Name:  "favicon",
        Label: "Favicon URL",
        Group: "站点资源",
        Type:  plugin.FieldImage,
        Wide:  true,
    },
}
```

`Group` 控制后台分组；`Wide` 适合 URL、长文本等需要整行宽度的输入。数字字段应同时给出 `Min`、`Max` 和 `Step`，避免浏览器按默认整数步长拒绝小数。

模板读取配置时必须提供合理回退值，因为旧配置或空值不一定包含 Schema 默认值：

```gotemplate
<link rel="icon" href="{{themeValue .ThemeConfig "favicon" "/theme/default/favicon.svg"}}">
```

## 图片和相对 URL

`FieldImage` 允许填写 URL或通过后台上传。主题必须兼容：

- `/uploads/...` 站内绝对路径。
- `uploads/...` 相对输入。
- `https://...`、`http://...` 和 `//...` 外部地址。

默认主题的 `assetURL` 会保留站内根路径、协议相对地址、片段和完整 URL，并为普通相对值补 `/`。第三方主题可实现等价函数，避免把相对 URL 当成无效值。

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

## AdjustData

`AdjustData` 在模板执行前运行，适合计算主题专用数据：

```go
AdjustData: func(ctx context.Context, data map[string]any) error {
    data["ShowReadingProgress"] = true
    return nil
},
```

它没有直接数据库服务句柄。需要数据库数据时，应优先由核心提供稳定模板数据，或通过插件钩子/运行时接口扩展，不要在主题中打开第二套数据库连接。

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
