# GoBlog

一个用 Go 编写的极简博客系统，功能对标 Typecho。

> **注意：** 项目仍在早期开发阶段，架构和代码可能会有较大变动。本文档现阶段仅作参考。

## 特性

- **多数据库支持** — SQLite（零配置）、MySQL/MariaDB、PostgreSQL，支持读写分离
- **完整后台管理** — 文章、页面、分类、标签、评论、用户、附件、备份、升级
- **编辑草稿机制** — 已发布内容的修改先保存为草稿，发布时合并回原文，支持自动保存
- **修订版本** — 自动保留历史修订（最多 20 条），可随时回滚
- **插件/主题系统** — 钩子驱动的插件架构，支持主题配置、自定义字段、模板函数扩展
- **兼容性 API** — XML-RPC（MetaWeblog/WordPress/Blogger）、Pingback、Trackback、RSD
- **安全防护** — CSRF 令牌、HMAC-SHA256 Cookie 会话、登录限速、SSRF 防护、上传安全校验、评论反垃圾
- **国际化** — 中英文后台界面
- **Markdown 渲染** — 基于 Goldmark，支持 GFM 扩展（表格、任务列表、删除线等）
- **RSS/Atom 订阅** — 文章和评论的 RSS/Atom Feed
- **Sitemap** — 内置 Sitemap 插件

## 快速开始

### 编译

```bash
go build -o goblog ./cmd/goblog
```

> SQLite 驱动依赖 CGO，编译时需要安装 C 编译器（如 `gcc`）。

### 运行

最简方式（SQLite，零配置）：

```bash
./goblog
```

程序会在 `data/goblog.db` 创建 SQLite 数据库，监听 `:8080`，默认管理员账号 `admin` / `admin123`。

访问 `http://localhost:8080/admin/` 进入后台。

### 环境变量配置

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `GOBLOG_ADDR` | 监听地址 | `:8080` |
| `GOBLOG_DB_DRIVER` | 数据库驱动（`sqlite3`/`mysql`/`postgres`） | `sqlite3` |
| `GOBLOG_DB_DSN` | 数据库连接字符串 | `data/goblog.db` |
| `GOBLOG_DB_READ_DSN` | 读库 DSN（读写分离） | — |
| `GOBLOG_DB_WRITE_DSN` | 写库 DSN（读写分离） | — |
| `GOBLOG_ADMIN_USER` | 初始管理员用户名 | `admin` |
| `GOBLOG_ADMIN_PASSWORD` | 初始管理员密码 | `admin123` |
| `GOBLOG_ADMIN_MAIL` | 初始管理员邮箱 | — |
| `GOBLOG_WEB_INSTALL` | 启用 Web 安装向导 | — |
| `GOBLOG_UPLOAD_DIR` | 上传文件存储目录 | — |

### 数据库连接字符串示例

**SQLite：**

```
data/goblog.db
```

**MySQL：**

```
user:password@tcp(127.0.0.1:3306)/goblog?charset=utf8mb4&parseTime=true
```

**PostgreSQL：**

```
postgres://user:password@127.0.0.1:5432/goblog?sslmode=disable
```

## 项目结构

```
goblog/
├── cmd/goblog/          # 程序入口
│   ├── main.go          # 启动、配置加载、服务初始化
│   └── plugins.go       # 内置插件/主题注册（空白导入）
├── admin/               # 后台管理界面
│   ├── embed.go         # embed.FS 嵌入声明
│   ├── assets/          # 后台静态资源（CSS/JS/MDUI2）
│   └── templates/       # 后台 HTML 模板
├── core/                # 核心业务逻辑
│   ├── handlers/        # HTTP 路由与处理器
│   │   ├── app.go       # 核心路由、中间件、所有前后台处理器
│   │   └── compat_api.go # XML-RPC/Pingback/Trackback 兼容层
│   ├── models/          # 数据模型与迁移
│   │   ├── models.go    # Content, User, Option, Field, Revision
│   │   ├── more.go      # Meta, Comment, AttachmentMeta, Relationship, Stats
│   │   ├── migrate.go   # 数据库 Schema 与版本化迁移（V1-V6）
│   │   └── query.go     # SQL 方言抽象（Rebind, UpsertOptionSQL）
│   ├── plugin/          # 插件/主题管理器与钩子系统
│   ├── services/        # 业务服务层
│   │   ├── contents.go  # 内容 CRUD、草稿、修订、slugID
│   │   ├── comments.go  # 评论管理
│   │   ├── metas.go     # 分类/标签管理
│   │   ├── users.go     # 用户管理、bcrypt 认证
│   │   ├── options.go   # 站点配置管理
│   │   └── dbrouter.go  # 读写分离路由
│   └── validate/        # 表单验证器
├── pkg/                 # 公共工具包
│   ├── auth/            # HMAC-SHA256 Cookie 会话
│   ├── httpclient/      # HTTP 客户端（重试、代理、SSRF 防护）
│   ├── i18n/            # 中英文国际化
│   ├── render/          # Markdown/纯文本/HTML 渲染
│   └── slug/            # URL slug 生成
├── plugins/             # 内置插件
│   └── sitemap/         # Sitemap 生成插件
├── themes/              # 内置主题
│   └── default/         # 默认 Cuckoo 主题（MDUI2）
│       ├── theme.go     # 主题注册
│       ├── static/      # 主题静态资源
│       └── templates/   # 主题模板（base/index/post/404）
└── refer/               # 参考文档
```

## 数据模型

系统使用 8 张核心表：

| 表 | 用途 |
|----|------|
| `gb_contents` | 文章、页面、附件、修订版本（通过 `type` 和 `status` 区分） |
| `gb_users` | 用户（角色：administrator/editor/contributor/subscriber/visitor） |
| `gb_options` | 站点配置（name+user 联合主键，支持每用户配置） |
| `gb_metas` | 分类和标签（通过 `type` 区分） |
| `gb_relationships` | 内容与分类/标签的多对多关系 |
| `gb_comments` | 评论、Pingback、Trackback |
| `gb_fields` | 自定义字段（支持 str/int/float/json 类型） |
| `gb_revisions` | 内容修订版本 |

### 关键设计

- **编辑草稿**：`gb_contents.draftOf` 字段实现已发布内容的修改先保存为草稿，发布时合并回原文
- **Slug ID 映射**：`gb_contents.slugId` 字段支持 `/post/{slug}.html` 和 `/post/{id}.html` 两种 URL 格式
- **版本化迁移**：Schema 版本号存储在 `gb_options`（`schema_version`），当前版本为 6

## 路由

### 前台

| 路径 | 说明 |
|------|------|
| `/` | 首页（文章列表或指定页面） |
| `/post/{slug}.html` | 文章详情 |
| `/page/{slug}.html` | 页面详情 |
| `/category/{slug}` | 分类归档 |
| `/tag/{slug}` | 标签归档 |
| `/author/{id}` | 作者归档 |
| `/archive/` | 时间归档 |
| `/search` | 搜索 |
| `/comment` | 提交评论 |
| `/feed.xml` | RSS Feed |
| `/atom.xml` | Atom Feed |

### 后台

| 路径 | 说明 |
|------|------|
| `/admin/` | 控制台 |
| `/admin/posts/` | 文章管理 |
| `/admin/pages/` | 页面管理 |
| `/admin/categories/` | 分类管理 |
| `/admin/tags/` | 标签管理 |
| `/admin/comments/` | 评论管理 |
| `/admin/users/` | 用户管理 |
| `/admin/medias/` | 附件管理 |
| `/admin/options/` | 站点设置 |
| `/admin/themes/` | 主题管理 |
| `/admin/plugins/` | 插件管理 |
| `/admin/backup` | 备份导入/导出 |
| `/admin/upgrade` | 数据库升级 |

### 兼容性 API

| 路径 | 说明 |
|------|------|
| `/xmlrpc.php` | XML-RPC（MetaWeblog/WordPress/Blogger API） |
| `/trackback/` | Trackback |
| `/rsd.xml` | RSD 发现 |
| `/wlwmanifest.xml` | WLW 清单 |

## 插件系统

GoBlog 采用钩子驱动的插件架构。插件通过实现 `Plugin` 接口并调用 `init()` 注册：

```go
type Plugin interface {
    Name() string
    Version() string
    Description() string
    Init(*Manager)
}
```

### 可用钩子点

- `content.before_save` / `content.after_save` — 内容保存前后
- `comment.before_save` / `comment.after_save` — 评论保存前后
- `content.before_delete` / `content.after_delete` — 内容删除前后
- `comment.before_delete` / `comment.after_delete` — 评论删除前后
- `template.render` — 模板渲染
- `route.register` — 路由注册
- `admin.register` — 后台页面注册

### 主题开发

主题通过 `plugin.Manager.RegisterTheme()` 注册，支持：

- 自定义模板（`html/template`）
- 静态资源（`embed.FS` 或外部目录）
- 配置 Schema（JSON Schema 格式，后台自动生成设置表单）
- 自定义字段定义
- 模板函数（`FuncMap`）

## 默认主题

内置 Cuckoo 风格主题，基于 MDUI2 框架，特性包括：

- 明暗模式切换
- 主题色自定义
- PJAX 无刷新导航
- 文章目录（tocbot）
- 代码高亮（Highlight.js）
- 背景图配置
- 侧栏组件

## 安全

| 机制 | 说明 |
|------|------|
| Cookie 会话 | HMAC-SHA256 签名，7 天有效期 |
| CSRF 防护 | 基于日期的 HMAC 令牌，支持跨天验证 |
| 登录限速 | IP+用户名维度 3 秒冷却 |
| SSRF 防护 | HTTP 客户端默认禁止访问私有 IP |
| 上传安全 | 扩展名白名单、MIME 校验、危险文件检测 |
| 评论反垃圾 | IP 黑名单、停用词、频率限制、蜜罐字段、Referer 检查 |

## 许可证

[AGPL-3.0](LICENSE)
