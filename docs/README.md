# GopherInk 开发文档

本目录记录 GopherInk v0.5.0 的开发接口和实现约束。项目简介、主要特性、快速启动、核心路由与数据表概览仍以根目录 [README](../README.md) 为入口；这里提供开发、扩展和运维时需要的深入说明。

文档内容以当前源码为准。GopherInk 的主题和插件采用构建时集成，新增或修改 Go 源码后必须重新编译并重启程序。

## 文档导航

| 文档 | 内容 |
|---|---|
| [架构与数据模型](architecture.md) | 启动流程、分层、数据库路由、核心表、请求处理链和实现边界 |
| [安装与配置](installation-and-configuration.md) | 编译要求、环境变量、数据库 DSN、目录、初始化和配置持久化 |
| [内容与分类体系](content-and-taxonomy.md) | 文章、独立页面、草稿、修订、Slug ID、分类、标签和自定义字段 |
| [评论与用户](comments-and-users.md) | 评论审核、访客身份、未审核评论自可见、用户角色、会话与头像 |
| [附件与图片处理](media-and-images.md) | 附件目录、绑定和删除策略、上传安全、WebP、缩略图及素材管理 |
| [主题开发](themes.md) | 主题注册、模板、静态资源、配置项、自定义字段和前端钩子 |
| [插件与钩子开发](plugins-and-hooks.md) | 插件注册、启停、配置、路由、后台菜单、50 个内置钩子和完整示例 |
| [安全与 WAF](security-and-waf.md) | WAF 请求链、限流、封禁、缓存、URL 索引、反向代理信任和安全边界 |
| [兼容接口与订阅](compatibility-apis.md) | RSS/Atom、Sitemap、XML-RPC、Pingback、Trackback、RSD 和外发通知 |

## 阅读建议

- 开发插件前先阅读 [插件与钩子开发](plugins-and-hooks.md)，特别是“加载模型”“路由安全边界”和“Handled 与 Triggered”。
- 开发主题前阅读 [主题开发](themes.md) 以及 [内容与分类体系](content-and-taxonomy.md)，确认模板数据和草稿语义。
- 修改请求处理、中间件或公开路由时同步检查 [安全与 WAF](security-and-waf.md)，避免公开 URL 索引和插件路由识别出现偏差。
- 修改附件存储时同时检查上传、替换、删除、缩略图和内容删除后的目录迁移，不要只替换上传入口。

## 源码入口

| 领域 | 主要代码 |
|---|---|
| 启动、CLI 与依赖组装 | `cmd/gopherink/main.go`、`cmd/gopherink/runtime_config.go`、`cmd/gopherink/user_commands.go`、`cmd/gopherink/plugins.go` |
| HTTP 和后台 | `core/handlers/app.go` |
| WAF | `core/handlers/waf.go` |
| 兼容接口 | `core/handlers/compat_api.go` |
| 数据模型和 Schema | `core/models/` |
| 业务服务 | `core/services/` |
| 插件和主题接口 | `core/plugin/plugin.go` |
| Markdown 和文本渲染 | `pkg/render/render.go` |
| 图片处理 | `pkg/imageproc/imageproc.go` |
| 默认主题 | `themes/default/` |
| 内置插件 | `plugins/` |
