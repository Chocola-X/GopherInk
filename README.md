# GopherInk

<p align="center"><img src="logo.svg" alt="GopherInk Logo" width="160"></p>

一个用 Go 编写的极简博客系统，功能对标 Typecho。

项目仓库：[github.com/Chocola-X/GopherInk](https://github.com/Chocola-X/GopherInk)

项目官网：[gopherink.nekopara.uk](https://gopherink.nekopara.uk)

> **注意：** 当前项目版本为 v0.5.0，仍在持续完善中。本文档现阶段仅作参考。

## 特性

- **多数据库支持** — SQLite（零配置）、MySQL/MariaDB、PostgreSQL，支持读写分离
- **完整后台管理** — 文章、页面、分类、标签、评论、用户、附件与备份
- **界面个性化** — 默认主题和管理后台分别支持 MDUI 2 动态配色、自定义桌面/移动端背景，以及卡片、输入框、顶栏、侧边栏和背景遮罩透明度调节
- **编辑草稿机制** — 已发布内容的修改先保存为草稿，发布时合并回原文，支持自动保存
- **修订版本** — 自动保留历史修订（默认最多 20 条），可随时回滚
- **插件/主题系统** — 钩子驱动的插件架构（50 个预定义钩子点），支持主题配置、自定义字段、模板函数扩展
- **兼容性 API** — XML-RPC（MetaWeblog/WordPress/Blogger）、Pingback、Trackback、RSD
- **WAF 安全防护** — 内置 Web 应用防火墙，IP 限速、登录暴力破解防护、无效路径封禁、页面缓存、安全响应头
- **图片处理** — 上传自动转 WebP（无损/有损可选）、缩略图生成、GIF 动画转 WebP
- **安全认证** — CSRF 令牌、HMAC-SHA256 Cookie 会话、登录限速、SSRF 防护、上传安全校验、评论反垃圾
- **命令行应急恢复** — 无需启动网站即可列出用户并按 ID 或用户名重置密码，重置后自动撤销该账户的现有登录会话
- **国际化** — 中英文后台界面
- **Markdown 渲染** — 基于 Goldmark，支持 GFM 扩展（表格、任务列表、删除线等）
- **RSS/Atom 订阅** — 文章和评论的 RSS/Atom Feed
- **Sitemap** — 内置 Sitemap 插件
- **单二进制部署** — 前后台模板与静态资源通过 `embed.FS` 嵌入，无需额外分发前端依赖文件

插件优先级、接管信号，以及内容、评论、附件的生命周期和字段扩展接口见 [插件与钩子开发](docs/plugins-and-hooks.md)。架构、配置、主题、安全和兼容接口等开发说明见 [开发文档目录](docs/README.md)。

插件和主题采用构建时集成：每次新增或修改插件、主题源码，都需要重新编译并重启 GopherInk；后台启停不会热加载代码。

## 快速开始

### 编译

```bash
go build -o gopherink ./cmd/gopherink
```

> SQLite 驱动依赖 CGO，编译时需要安装 C 编译器（如 `gcc`）。

### 运行

首次在终端中运行会进入启动配置向导，数据库、上传目录、监听规则和 HTTPS 选择保存到 `data/config.json`；随后服务使用 SQLite 启动，并在空库上提供 Web 安装页：

```bash
./gopherink
```

默认数据库为 `data/gopherink.db`，默认不启用 HTTPS，并监听 `0.0.0.0:8086`，访问 `http://localhost:8086/install` 完成站点和管理员初始化。交互向导可选择启用 HTTPS；启用后默认端口为 `443`，并要求填写证书链和私钥路径。非交互环境不执行终端向导，直接使用配置文件、环境变量或默认值。

启动参数仅对本次进程生效：

```bash
./gopherink -p 8848
./gopherink --db-type mysql --db-host 127.0.0.1 --db-port 3306 --db-name gopherink --db-user blog
./gopherink --tls --tls-cert /etc/gopherink/fullchain.pem --tls-key /etc/gopherink/privkey.pem
```

在参数前使用 `config` 会写入 `data/config.json` 后直接退出，不会启动网站服务：

```bash
./gopherink config -p 8848
./gopherink config --upload-dir /srv/gopherink/uploads --allow-cidr 127.0.0.1 --allow-cidr 10.0.0.0/8
./gopherink config --tls --tls-cert /etc/gopherink/fullchain.pem --tls-key /etc/gopherink/privkey.pem
```

配置优先级为“内置默认值 < `data/config.json` < 环境变量 < 本次 CLI 参数”。完整参数、JSON 格式和数据库示例见 [安装与配置](docs/installation-and-configuration.md)。

运行 `./gopherink -help` 可查看启动参数、持久化配置、环境变量和用户应急命令的完整提示。

### 用户应急命令

```bash
./gopherink user list
./gopherink user reset-password --id 1
printf 'new-password\n' | ./gopherink user reset-password --id 1 --password-stdin
```

密码重置会更新 bcrypt 哈希并撤销该用户现有会话。应急命令只访问启动层配置指向的数据库，不启动网站。

### 环境变量配置

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `GOPHERINK_ADDR` | 本次启动监听地址 | HTTP `0.0.0.0:8086`；TLS 默认端口 `443` |
| `GOPHERINK_LISTEN_CIDRS` | 允许访问的客户端 CIDR，逗号分隔 | `0.0.0.0/0` |
| `GOPHERINK_TLS_ENABLED` | 启用内置 HTTPS/TLS 监听 | `false` |
| `GOPHERINK_TLS_CERT` / `GOPHERINK_TLS_KEY` | TLS 证书链和私钥文件路径 | 空 |
| `GOPHERINK_DB_DRIVER` | 数据库驱动（`sqlite3`/`mysql`/`mariadb`/`postgres`） | `sqlite` |
| `GOPHERINK_DB_DSN` | 数据库连接字符串 | `data/gopherink.db` |
| `GOPHERINK_DB_READ_DSN` | 读库 DSN（读写分离） | — |
| `GOPHERINK_DB_WRITE_DSN` | 写库 DSN（读写分离） | — |
| `GOPHERINK_DB_HOST` / `GOPHERINK_DB_PORT` | 外部数据库 IP 和端口 | 按数据库类型 |
| `GOPHERINK_DB_NAME` / `GOPHERINK_DB_USER` / `GOPHERINK_DB_PASSWORD` | 外部数据库名称和凭据 | `gopherink` / 空 |
| `GOPHERINK_SQLITE_PATH` | SQLite 文件位置 | `<GOPHERINK_DATA_DIR>/gopherink.db` |
| `GOPHERINK_ADMIN_USER` | 初始管理员用户名 | `admin` |
| `GOPHERINK_ADMIN_PASSWORD` | 初始管理员密码 | `admin123` |
| `GOPHERINK_ADMIN_MAIL` | 初始管理员邮箱 | `admin@example.com` |
| `GOPHERINK_WEB_INSTALL` | 启用 Web 安装向导 | `true` |
| `GOPHERINK_DATA_DIR` | 数据、默认 SQLite、WAF 日志和默认上传目录的根目录 | `data` |
| `GOPHERINK_UPLOAD_DIR` | 单独指定附件文件系统根目录 | `<GOPHERINK_DATA_DIR>/uploads` |

### 数据库连接字符串示例

**SQLite：**

```
data/gopherink.db
```

**MySQL / MariaDB：**

```
user:password@tcp(127.0.0.1:3306)/gopherink?charset=utf8mb4&parseTime=true
```

**PostgreSQL：**

```
postgres://user:password@127.0.0.1:5432/gopherink?sslmode=disable
```

### 读写分离

设置 `GOPHERINK_DB_READ_DSN` 和 `GOPHERINK_DB_WRITE_DSN` 后，读操作走读库、写操作走写库。也可在后台"基本设置"中配置 `db_read_dsn` / `db_write_dsn`。读库为空时自动回退到写库。

## 项目结构

```
GopherInk/
├── cmd/gopherink/              # 程序入口
│   ├── main.go                 # 命令分发、服务初始化和监听
│   ├── runtime_config.go       # JSON 配置、环境变量、CLI 和首次引导
│   ├── user_commands.go        # 用户列表和密码重置应急命令
│   └── plugins.go              # 内置插件/主题注册（空白导入）
├── admin/                   # 后台管理界面
│   ├── embed.go             # embed.FS 嵌入声明
│   ├── assets/              # 后台静态资源
│   │   ├── app.css          # 后台样式
│   │   ├── app.js           # 后台逻辑
│   │   ├── markdown-editor.js # Markdown 编辑器
│   │   └── vendor/          # 第三方库（MDUI2、Material Icons）
│   └── templates/           # 后台 HTML 模板（32 个）
├── core/                    # 核心业务逻辑
│   ├── handlers/            # HTTP 路由与处理器
│   │   ├── app.go           # 核心路由、中间件、所有前后台处理器
│   │   ├── compat_api.go    # XML-RPC/Pingback/Trackback 兼容层
│   │   ├── images.go        # 图片缩略图处理
│   │   └── waf.go           # WAF 安全中间件
│   ├── models/              # 数据模型与数据库初始化
│   │   ├── models.go        # Content, User, Option, Field, Revision
│   │   ├── more.go          # Meta, Comment, AttachmentMeta, Relationship, Stats
│   │   ├── schema.go        # 数据库 Schema 初始化（版本 1）
│   │   └── query.go         # SQL 方言抽象（Rebind, UpsertOptionSQL, LimitOffset）
│   ├── plugin/              # 插件/主题管理器与钩子系统
│   │   └── plugin.go        # Plugin 接口、Manager、Runtime、50 个钩子常量
│   ├── services/            # 业务服务层
│   │   ├── contents.go      # 内容 CRUD、草稿、修订、slugID、搜索、附件
│   │   ├── comments.go      # 评论管理
│   │   ├── metas.go         # 分类/标签管理
│   │   ├── users.go         # 用户管理、bcrypt 认证
│   │   ├── options.go       # 站点配置管理（80+ 默认选项）
│   │   └── dbrouter.go      # 读写分离路由（DB 接口、SQLDB、DBRouter）
│   └── validate/            # 表单验证器（链式调用）
├── pkg/                     # 公共工具包
│   ├── auth/                # HMAC-SHA256 Cookie 会话
│   ├── httpclient/          # HTTP 客户端（重试、代理、SSRF 防护）
│   ├── i18n/                # 中英文国际化
│   ├── imageproc/           # 图片处理（WebP 转换、缩略图、GIF 动画）
│   ├── render/              # Markdown/纯文本/HTML 渲染
│   └── slug/                # URL slug 生成
├── plugins/                 # 内置插件
│   └── sitemap/             # Sitemap 生成插件
├── themes/                  # 内置主题
│   └── default/             # Default Theme（MDUI2）
│       ├── theme.go         # 主题注册、配置 Schema、模板函数
│       ├── static/          # 主题静态资源
│       └── templates/       # 主题模板（base/index/post/404）
└── docs/                    # 分类开发文档
    ├── README.md            # 开发文档目录
    ├── architecture.md      # 架构与数据模型
    ├── plugins-and-hooks.md # 插件与钩子开发
    ├── themes.md            # 主题开发
    └── ...                  # 内容、评论、附件、安全和兼容接口
```

## 数据模型

系统使用 8 张核心表：

| 表 | 用途 |
|----|------|
| `gb_contents` | 文章、页面、附件和编辑草稿（通过 `type`、`status`、`draftOf` 区分） |
| `gb_users` | 用户（角色：administrator/editor/contributor/subscriber/visitor） |
| `gb_options` | 站点配置（name+user 联合主键，支持每用户配置） |
| `gb_metas` | 分类和标签（通过 `type` 区分） |
| `gb_relationships` | 内容与分类/标签的多对多关系 |
| `gb_comments` | 评论、Pingback、Trackback |
| `gb_fields` | 自定义字段（支持 str/int/float 类型） |
| `gb_revisions` | 内容修订版本 |

### 关键设计

- **编辑草稿**：`gb_contents.draftOf` 字段实现已发布内容的修改先保存为草稿，发布时合并回原文
- **Slug ID 映射**：`gb_contents.slugId` 字段支持 `/post/{slug}.html` 和 `/post/{id}.html` 两种 URL 格式
- **Schema 版本**：当前数据库结构版本为 1，新安装时直接初始化最终表结构
- **自定义字段**：`gb_fields` 表支持 str/int/float 三种值类型，插件可通过 `ContentFieldsProvider` 声明字段

### 表结构详情

#### gb_contents

| 列 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| cid | PK AUTO | — | 内容 ID |
| title | varchar(150) | NULL | 标题 |
| slug | varchar(150) | NULL | 别名 |
| slugId | int | 0 | 别名 ID |
| created | int | 0 | 创建时间（Unix） |
| modified | int | 0 | 修改时间（Unix） |
| text | text | — | 正文 |
| sortOrder | int | 0 | 排序 |
| authorId | int | 0 | 作者 ID |
| template | varchar(32) | NULL | 自定义模板 |
| type | varchar(16) | 'post' | 类型（post/page/attachment/revision） |
| status | varchar(16) | 'publish' | 状态（publish/draft/private/hidden/waiting） |
| password | varchar(64) | NULL | 密码保护 |
| commentsNum | int | 0 | 评论数 |
| allowComment | char(1) | '1' | 允许评论 |
| allowPing | char(1) | '0' | 允许 Ping |
| allowFeed | char(1) | '1' | 允许 Feed |
| parent | int | 0 | 父级 |
| draftOf | int | 0 | 草稿所属（0 为独立内容） |

索引：`slug`、`slugId`、`created`、`draftOf`

#### gb_users

| 列 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| uid | PK AUTO | — | 用户 ID |
| name | varchar(32) | NULL | 用户名 |
| password | varchar(128) | NULL | 密码（bcrypt） |
| mail | varchar(150) | NULL | 邮箱 |
| url | varchar(150) | NULL | 主页 |
| screenName | varchar(32) | NULL | 昵称 |
| created | int | 0 | 创建时间 |
| activated | int | 0 | 激活时间 |
| logged | int | 0 | 登录时间 |
| role | varchar(16) | 'visitor' | 角色 |
| authCode | varchar(64) | NULL | 授权码 |

唯一索引：`name`、`mail`

#### gb_comments

| 列 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| coid | PK AUTO | — | 评论 ID |
| cid | int | 0 | 所属内容 ID |
| created | int | 0 | 创建时间 |
| author | varchar(150) | NULL | 作者 |
| authorId | int | 0 | 作者 ID（注册用户） |
| ownerId | int | 0 | 内容所有者 ID |
| mail | varchar(150) | NULL | 邮箱 |
| url | varchar(255) | NULL | URL |
| ip | varchar(64) | NULL | IP 地址 |
| agent | varchar(511) | NULL | User-Agent |
| text | text | — | 内容 |
| type | varchar(16) | 'comment' | 类型（comment/pingback/trackback） |
| status | varchar(16) | 'approved' | 状态 |
| parent | int | 0 | 父评论 ID |

索引：`cid`

## 路由

### 前台

| 路径 | 说明 |
|------|------|
| `/` | 首页（文章列表或指定页面） |
| `/post/{slug}.html` | 文章详情 |
| `/post/{id}.html` | 文章详情（ID 格式） |
| `/page/{slug}.html` | 页面详情 |
| `/category/{slug}` | 分类归档 |
| `/tag/{slug}` | 标签归档 |
| `/author/{id}` | 作者归档 |
| `/archive/` | 时间归档 |
| `/search` | 搜索 |
| `/comment` | 提交评论 |
| `/preview/` | 内容预览 |
| `/feed.xml` | RSS Feed |
| `/atom.xml` | Atom Feed |
| `/comments/feed.xml` | 评论 RSS Feed |

### 后台

| 路径 | 说明 |
|------|------|
| `/admin/` | 控制台 |
| `/admin/login` | 登录 |
| `/admin/register` | 注册 |
| `/admin/posts/` | 文章管理 |
| `/admin/pages/` | 页面管理 |
| `/admin/categories/` | 分类管理 |
| `/admin/tags/` | 标签管理 |
| `/admin/comments/` | 评论管理 |
| `/admin/users/` | 用户管理 |
| `/admin/profile` | 个人设置 |
| `/admin/profile/revoke-sessions` | 撤销所有会话 |
| `/admin/profile/plugins/` | 个人插件设置 |
| `/admin/medias/` | 附件管理 |
| `/admin/options/` | 基本设置 |
| `/admin/options/general` | 基本设置 |
| `/admin/options/reading` | 阅读设置 |
| `/admin/options/discussion` | 评论设置 |
| `/admin/options/permalink` | 永久链接设置 |
| `/admin/options/waf` | WAF 设置 |
| `/admin/themes/` | 主题管理 |
| `/admin/plugins/` | 插件管理 |
| `/admin/management/` | 系统管理 |
| `/admin/management/upload` | 上传管理 |
| `/admin/backup` | 备份导入/导出 |
| `/admin/autosave` | 自动保存 |
| `/admin/markdown/preview` | Markdown 预览 |
| `/admin/thumbnail` | 缩略图 |
| `/admin/medias/editor` | 编辑器附件上传 |

### 兼容性 API

| 路径 | 说明 |
|------|------|
| `/xmlrpc.php` | XML-RPC（MetaWeblog/WordPress/Blogger API） |
| `/action/xmlrpc` | XML-RPC（备用路径） |
| `/action/pingback` | Pingback |
| `/trackback/` | Trackback |
| `/rsd.xml` | RSD 发现 |
| `/wlwmanifest.xml` | WLW 清单 |

## 插件系统

GopherInk 采用钩子驱动的插件架构。插件通过实现 `Plugin` 接口并调用 `init()` 注册：

```go
type Plugin interface {
    Name() string
    Version() string
    Description() string
    Init(*Manager)
}
```

### 扩展接口

插件可选择实现以下接口以获得更多能力：

| 接口 | 方法 | 说明 |
|------|------|------|
| `InfoProvider` | `Info() PluginInfo` | 提供详细插件信息 |
| `Activator` | `Activate(ctx, *Runtime) error` | 插件激活回调 |
| `Deactivator` | `Deactivate(ctx, *Runtime) error` | 插件停用回调 |
| `ConfigProvider` | `ConfigSchema() []FieldSchema` | 提供站点级配置 Schema |
| `PersonalConfigProvider` | `PersonalConfigSchema() []FieldSchema` | 提供用户级配置 Schema |
| `ContentFieldsProvider` | `ContentFieldSchema() []FieldSchema` | 提供内容自定义字段 Schema |

### 插件运行时 API

插件路由和处理函数可通过 `Runtime` 访问以下能力：

| 方法 | 说明 |
|------|------|
| `ListPublished(ctx, limit, offset)` | 分页获取已发布文章 |
| `ContentByID(ctx, id)` | 按内部 ID 获取精简内容；调用方需检查状态和权限 |
| `IncrementIntField(ctx, id, name, delta)` | 原子增加整数自定义字段（适合阅读计数） |
| `Option(ctx, name)` | 读取站点选项 |
| `Config(ctx, pluginName)` | 读取插件站点级配置 |
| `PersonalConfig(ctx, pluginName, uid)` | 读取插件用户级配置 |
| `DispatchHook(ctx, name, payload)` | 调度已启用插件的自定义钩子 |

### 钩子优先级

| 常量 | 值 | 说明 |
|------|-----|------|
| `HookPriorityEarly` | -100 | 早期优先 |
| `HookPriorityNormal` | 0 | 正常优先（默认） |
| `HookPriorityLate` | 100 | 延迟优先 |

数值越小越先执行；相同优先级保持注册顺序。钩子返回 `StopHook(payload)` 可停止后续回调传播。

### 可用钩子点

#### 内容生命周期

| 钩子 | 说明 |
|------|------|
| `content.before_save` | 内容保存前，可修改输入 |
| `content.after_save` | 内容保存后 |
| `content.after_draft_save` | 草稿/自动保存后 |
| `content.after_publish` | 发布操作后 |
| `content.before_delete` | 内容删除前，可阻止 |
| `content.after_delete` | 内容删除后 |
| `content.before_status_change` | 状态变更前，可修改状态 |
| `content.after_status_change` | 状态变更后 |

#### 内容查询与渲染

| 钩子 | 说明 |
|------|------|
| `content.filter` | 内容对象进入主题前的通用过滤 |
| `content.title` | 标题输出过滤 |
| `content.before_render` | 正文解析前过滤 |
| `content.markdown` | 替换 Markdown 解析器（设置 `Handled=true`） |
| `content.auto_paragraph` | 替换纯文本解析器（设置 `Handled=true`） |
| `content.after_render` | 正文 HTML 生成后过滤 |
| `content.excerpt` | 摘要生成后过滤 |
| `content.search` | 搜索查询过滤或接管（设置 `Handled=true`） |
| `content.fields` | 动态增减内容字段 |
| `content.field_read_only` | 标记字段为只读 |

#### 评论生命周期

| 钩子 | 说明 |
|------|------|
| `comment.before_save` | 评论写入前，可修改输入或阻止 |
| `comment.after_save` | 评论写入后 |
| `comment.before_reply` / `comment.after_reply` | 后台回复前/后 |
| `comment.before_edit` / `comment.after_edit` | 后台编辑前/后 |
| `comment.before_mark` / `comment.after_mark` | 审核状态变更前/后 |
| `comment.before_delete` / `comment.after_delete` | 删除前/后 |

#### 评论渲染

| 钩子 | 说明 |
|------|------|
| `comment.filter` | 评论过滤 |
| `comment.before_render` | 评论正文解析前 |
| `comment.markdown` | 替换评论 Markdown 解析器 |
| `comment.auto_paragraph` | 替换评论纯文本解析器 |
| `comment.after_render` | 评论 HTML 生成后 |
| `comment.avatar` | 修改或清空头像 URL |

#### 上传与附件

| 钩子 | 说明 |
|------|------|
| `upload.before_save` | 上传校验前修改名称或父内容 ID |
| `upload.handle` | 接管实际写入（设置 `Handled=true`） |
| `upload.after_save` | 文件写入完成后过滤元数据 |
| `attachment.before_edit` / `attachment.after_edit` | 附件信息修改前/后 |
| `attachment.before_replace` / `attachment.after_replace` | 附件替换前/后 |
| `attachment.replace_handle` | 接管实际替换（设置 `Handled=true`） |
| `attachment.before_delete` / `attachment.after_delete` | 附件删除前/后 |
| `attachment.delete_handle` | 接管实际文件删除（设置 `Handled=true`） |
| `attachment.url` | 动态生成公开 URL（适合签名 URL 或 CDN） |
| `attachment.data` | 提供文件内容（供远程图片缩略图） |

#### 前端

| 钩子 | 说明 |
|------|------|
| `admin.menu` | 后台菜单扩展 |
| `frontend.head` | 前台 `<head>` 注入 |
| `frontend.footer` | 前台页脚注入 |

### 接管默认实现

搜索、Markdown/AutoP 解析、上传保存、附件替换、附件删除和附件数据读取的 payload 均包含 `Handled` 字段。插件设置 `Handled=true` 并返回完整结果后，核心不会执行默认实现。存储插件不能绕过文件大小、危险扩展名和 MIME 一致性等安全校验。

### 主题开发

主题通过 `plugin.Manager.RegisterTheme()` 注册，支持：

- 自定义模板（`html/template`）
- 静态资源（`embed.FS` 或外部目录）
- 配置 Schema（`FieldSchema`，后台自动生成设置表单）
- 自定义字段定义（可限定 `post`/`page` 类型）
- 模板函数（`FuncMap`）
- 数据调整回调（`AdjustData`）
- 可编辑目录（`EditableDir`，允许后台编辑主题文件）

### 字段类型

配置 Schema 和内容字段支持以下类型：

| 类型常量 | 说明 |
|----------|------|
| `text` | 单行文本 |
| `password` | 密码 |
| `textarea` | 多行文本 |
| `radio` | 单选 |
| `checkbox` | 复选 |
| `select` | 下拉选择 |
| `number` | 数字 |
| `color` | 颜色选择器 |
| `image` | 图片 URL |

## 默认主题

内置 Default Theme，是 GopherInk 基于 MDUI2 设计的默认主题，特性包括：

- 明暗模式切换（auto/light/dark）
- 主题色自定义（10 种预设色 + 自定义色）
- PJAX 无刷新导航
- 文章目录（tocbot）
- 代码高亮（Highlight.js）
- 背景图配置（桌面/移动端/侧栏/评论框/文章底部）
- 侧栏组件（资料卡、最新回复、标签云）
- 阅读时间估算
- Gravatar / 自定义头像
- 回到顶部按钮

### 主题配置项

| 分组 | 配置项 | 说明 |
|------|--------|------|
| 资料卡 | `display_name` | 资料卡名称（留空用站点标题） |
| | `profile_email` | Gravatar 邮箱 |
| | `profile_avatar` | 自定义头像 URL |
| | `bio` | 资料卡描述（留空用站点描述） |
| 配色和透明度 | `primary_preset` | 预设主题色（10 种） |
| | `custom_primary` | 自定义主题色（优先于预设） |
| | `theme_mode` | 明暗模式（auto/light/dark） |
| | `card_opacity` | 卡片背景透明度（0-1） |
| | `input_opacity` | 输入框背景透明度（0-1） |
| | `background_mask_opacity` | 背景遮罩透明度（0-1） |
| 背景和装饰 | `enable_decor` | 启用装饰图片 |
| | `background_image` | 桌面背景图 |
| | `mobile_background_image` | 移动端背景图 |
| | `sidebar_image` | 侧栏封面图 |
| | `default_cover` | 默认文章封面 |
| | `comment_bg_image` | 评论框装饰图 |
| | `post_end_image` | 文章底部装饰图 |
| | `favicon` | Favicon |
| 侧栏和导航 | `show_profile` | 显示侧栏资料卡 |
| | `show_recent_comments` | 显示最新回复 |
| | `show_tag_cloud` | 显示标签云 |
| | `enable_pjax` | 启用 PJAX 导航 |
| | `enable_toc` | 文章页显示目录 |
| | `enable_back_to_top` | 显示回到顶部按钮 |
| 页脚 | `footer_html` | 底部自定义 HTML |

### 内容字段

| 字段 | 类型 | 适用 | 说明 |
|------|------|------|------|
| `cover` | image | post, page | 封面图 URL |
| `catalog` | checkbox | post, page | 显示文章目录 |

### 模板函数

| 函数 | 说明 |
|------|------|
| `themeValue(values, key, fallback...)` | 获取主题配置值 |
| `themeOpacity(values, key, fallback)` | 获取透明度值（0-1 钳制） |
| `gravatar(email, size)` | 生成 Gravatar URL（MD5, identicon 回退） |
| `avatarURL(values, email, size)` | 优先自定义头像，否则 Gravatar |
| `assetURL(value)` | 资源 URL 处理 |
| `safeHTML(value)` | 标记为安全 HTML |
| `readingTime(text)` | 阅读时间估算（400 字/分钟） |
| `daysSince(ts)` | 距今天数 |
| `staleDays(ts, threshold)` | 是否超过阈值天数 |
| `fieldString(fields, name)` | 获取自定义字段字符串值 |

## WAF 安全中间件

GopherInk 内置 Web 应用防火墙（WAF），提供多层安全防护。已认证管理员的后台请求自动跳过 WAF 检查。

### 请求限速

| 策略 | 窗口 | 阈值 | 说明 |
|------|------|------|------|
| 动态请求 | 60s | 300 次 | 默认路径 |
| 静态请求 | 60s | 1200 次 | `/admin/assets/`、`/theme/` |
| 上传请求 | 60s | 600 次 | `/uploads/` |
| 搜索请求 | 60s | 20 次 | `/search` |
| XML-RPC 请求 | 60s | 30 次 | `/xmlrpc.php` |

### IP 封禁

| 策略 | 触发条件 | 封禁时长 |
|------|----------|----------|
| 登录暴力破解 | 300s 内 5 次失败 | 900s |
| 附件下载滥用 | 60s 内 120 次 | 600s |
| 无效路径扫描 | 60s 内 20 次 | 600s |

### 安全响应头

| 头 | 值 | 说明 |
|----|-----|------|
| `X-Content-Type-Options` | `nosniff` | 禁止 MIME 嗅探 |
| `X-Frame-Options` | `DENY` | 禁止 iframe 嵌入 |
| `Referrer-Policy` | `strict-origin-when-cross-origin` | Referer 策略 |
| `X-XSS-Protection` | `0` | 禁用旧版 XSS 过滤器 |
| `Strict-Transport-Security` | `max-age=31536000; includeSubDomains` | HSTS（仅 HTTPS + 启用时） |

### 页面缓存

WAF 内置公开页面缓存，默认开启：

| 配置 | 默认值 | 说明 |
|------|--------|------|
| `waf_cache_enabled` | 1 | 缓存开关 |
| `waf_cache_ttl` | 30 | 缓存 TTL（秒） |
| `waf_cache_max_entries` | 512 | 最大缓存条目 |

### URL 索引

WAF 维护公开 URL 索引，不存在的路径会被记录为无效路径，超过阈值后触发 IP 封禁。

| 配置 | 默认值 | 说明 |
|------|--------|------|
| `waf_url_index_enabled` | 1 | URL 索引开关 |
| `waf_url_index_ttl` | 60 | 索引 TTL（秒） |
| `waf_index_max_items` | 10000 | 最大索引条目 |

## 图片处理

### 上传模式

| 模式 | 说明 |
|------|------|
| `original` | 保留原始格式（默认） |
| `webp_lossless` | 无损 WebP 转换 |
| `webp_quality` | 有损 WebP 转换（默认质量 85） |

GIF 动画上传时自动转换为 WebP 动画。SVG 文件不进行转换处理。

### 缩略图

- 后台缩略图默认尺寸：320×200
- 缩略图格式：JPEG（默认）或 WebP，由 `thumbnail_format` 选项控制
- 缩略图质量由 `thumbnail_quality` 选项控制（默认 82）
- 缩略图缓存到 `.thumbnails/` 子目录，缓存有效期 24 小时
- 图片处理内存限制默认 256MB，由 `image_processing_memory_mb` 选项控制

## 站点配置

GopherInk 提供 80+ 项站点配置，均可在后台管理界面修改。以下为主要配置分类：

### 基本设置

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `site_title` | GopherInk | 站点标题 |
| `site_description` | 一个极简的 Go 博客 | 站点描述 |
| `site_keywords` | — | 站点关键词 |
| `base_url` | http://localhost:8086 | 站点 URL |
| `site_language` | zh-CN | 站点语言 |
| `site_timezone` | Local | 站点时区 |
| `active_theme` | default | 当前主题 |
| `default_category` | 1 | 默认分类 ID |
| `allow_register` | 0 | 是否允许注册 |
| `register_default_role` | subscriber | 注册默认角色 |

### 阅读设置

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `post_date_format` | 2006-01-02 | 文章日期格式 |
| `page_size` | 10 | 每页条数 |
| `posts_list_size` | 10 | 文章列表条数 |
| `content_render_mode` | markdown | 内容渲染模式 |
| `feed_full_text` | 0 | Feed 全文输出 |
| `front_page_type` | posts | 首页类型（posts/page） |
| `front_page_cid` | 0 | 首页指定内容 ID |
| `revision_limit` | 20 | 修订版本上限 |

### 评论设置

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `comments_require_moderation` | 0 | 评论需审核 |
| `comments_require_mail` | 1 | 评论需邮箱 |
| `comments_order` | ASC | 评论排序 |
| `comments_max_nesting_levels` | 3 | 评论嵌套层级 |
| `comments_antispam` | 1 | 反垃圾评论 |
| `comments_check_referer` | 1 | 检查 Referer |
| `comments_post_interval` | 60 | 评论间隔（秒） |
| `comments_stop_words` | — | 停用词 |
| `comments_ip_blacklist` | — | IP 黑名单 |
| `comments_markdown` | 0 | 评论 Markdown 渲染 |
| `comments_avatar` | 1 | 评论头像 |

### 上传设置

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `upload_allowed_exts` | jpg,jpeg,png,gif,webp,svg,pdf,txt,md,zip | 允许上传扩展名 |
| `upload_max_size` | 16777216 (16MB) | 上传最大字节数 |
| `upload_image_processing` | original | 图片处理模式 |
| `upload_webp_quality` | 85 | WebP 质量 |
| `upload_replace_same_ext_only` | 1 | 替换附件同扩展名限制 |
| `attachment_delete_policy` | keep | 附件删除策略（keep/delete_records/delete_all） |

### 永久链接

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `permalink_post` | /post/{slug}.html | 文章永久链接 |
| `permalink_page` | /page/{slug}.html | 页面永久链接 |
| `permalink_category` | /category/{slug} | 分类永久链接 |

### 兼容性 API

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `enable_xmlrpc` | 1 | 启用 XML-RPC |
| `enable_pingback` | 1 | 启用 Pingback |
| `enable_trackback` | 1 | 启用 Trackback |

### HTTP 客户端

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `http_client_timeout` | 5 | 超时（秒） |
| `http_client_user_agent` | GopherInk/0.5.0 | User-Agent |
| `http_client_proxy` | — | 代理地址 |
| `http_client_retries` | 1 | 重试次数 |

## 安全

| 机制 | 说明 |
|------|------|
| Cookie 会话 | HMAC-SHA256 签名，7 天有效期，支持版本号、SameSite、Secure、前缀 |
| CSRF 防护 | 基于日期的 HMAC 令牌，支持跨天验证 |
| 登录限速 | IP+用户名维度 3 秒冷却，WAF 层 300s/5 次失败封禁 900s |
| SSRF 防护 | HTTP 客户端默认禁止访问私有 IP（loopback/private/link-local 等），DNS 解析后二次校验 |
| 上传安全 | 扩展名白名单、MIME 校验、危险文件检测 |
| 评论反垃圾 | IP 黑名单、停用词、频率限制、蜜罐字段、Referer 检查 |
| WAF 防火墙 | 多层限速、IP 封禁、URL 索引、页面缓存、安全响应头 |

## 国际化

后台界面支持中英文切换：

| 语言 | 代码 |
|------|------|
| 简体中文 | `zh-CN`（默认） |
| 英语 | `en-US` |

翻译通过 `i18n.T(lang, key)` 函数调用，空语言默认回退到 `zh-CN`，找不到翻译时回退到 key 本身。

## 内容渲染

| 模式 | 说明 |
|------|------|
| `markdown` | Markdown 渲染（默认），基于 Goldmark + GFM 扩展 |
| `autop` / `plaintext` / `plain` | 纯文本自动段落 |
| `html` | 原始 HTML 直出 |

内容正文支持 `<!--markdown-->` 和 `<!--plaintext-->` 前缀标记覆盖渲染模式。`<!--more-->` 标记用于分割摘要。

## 依赖

| 依赖 | 版本 | 说明 |
|------|------|------|
| `github.com/yuin/goldmark` | v1.8.2 | Markdown 解析器 |
| `github.com/mattn/go-sqlite3` | v1.14.47 | SQLite3 驱动（CGO） |
| `github.com/go-sql-driver/mysql` | v1.10.0 | MySQL 驱动 |
| `github.com/lib/pq` | v1.10.9 | PostgreSQL 驱动 |
| `github.com/gen2brain/webp` | v0.6.4 | WebP 图片编解码 |
| `golang.org/x/crypto` | v0.53.0 | bcrypt 密码哈希 |
| `golang.org/x/image` | v0.44.0 | 图片缩放处理 |

## 许可证

[AGPL-3.0](LICENSE)
