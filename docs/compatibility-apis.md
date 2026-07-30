# 兼容接口与订阅

## RSS 和 Atom

核心提供：

| 路径 | 格式/内容 |
|---|---|
| `/feed.xml` | 文章 RSS 2.0 |
| `/atom.xml` | 文章 Atom |
| `/comments/feed.xml` | 最新评论 RSS 2.0 |
| `/category/<slug>/feed.xml` | 分类文章 RSS |
| `/tag/<slug>/feed.xml` | 标签文章 RSS |

实际分类和标签前缀会跟随固定链接配置。Feed 只包含已发布、允许进入 Feed且不属于未来时间的内容。

`feed_full_text` 控制输出全文还是摘要。非全文模式会优先处理正文分隔标记/摘要逻辑。所有绝对链接依赖 `base_url`，生产环境必须正确配置协议、域名和可能的路径前缀。

## Sitemap 插件

内置 Sitemap 是插件而非硬编码核心路由，源码位于 `plugins/sitemap/`：

- 通过 `plugin.Register` 编译登记。
- 在 `Init` 中注册 GET 路由。
- 通过 `Runtime.ListContents` 读取最多 1000 篇已发布、非未来文章。
- 使用文章的自定义 Slug，空 Slug 时回退 `SlugID`，并生成 `.html` 固定链接。
- 输出 XML `urlset`、首页和每项内容的 `lastmod`。

Sitemap 依赖 `base_url` 生成绝对 `<loc>`。插件启停只控制当前二进制中的路由，不会卸载代码。

开发自定义 Sitemap 时应：

- 使用 XML encoder 或正确转义 URL。
- 只包含规范公开 URL，排除草稿、隐藏、私密、未来内容和编辑草稿。
- 每个 URL 使用绝对地址。
- 使用 RFC 3339/W3C 日期格式支持的 `lastmod`。
- 大规模站点应分页读取并在超过协议单文件限制时拆分 Sitemap index；内置插件当前是最多 1000 篇文章的轻量单文件实现，不包含独立页面。

## 虚拟文件插件

内置 Virtual Files 插件用于管理 `/robots.txt`、搜索引擎验证文件和域名所有权验证文本：

- 新安装默认启用，并预置允许全站抓取的 `/robots.txt`。
- 后台可以添加、修改路径和文本内容，或删除不再使用的文件。
- 文件保存在插件配置中，不写入网站根目录，也不创建额外数据库表。
- 通过 `request.fallback` 提供响应，只有在 CMS、主题和其他插件路由都未匹配时才生效，不能覆盖已有页面或静态资源。
- 自动根据扩展名返回常见文本 MIME，未知扩展名回落到 `text/plain; charset=utf-8`。
- 单文件限制为 512 KiB，最多 100 个，总文本大小不超过 4 MiB。

这类文件应保持内容简短。需要发布可下载的大文件或图片时，应使用附件系统，而不是虚拟文件插件。

## XML-RPC

入口：

```text
/xmlrpc.php
/action/xmlrpc
```

当前支持的方法：

```text
mt.supportedMethods
pingback.ping
blogger.getUsersBlogs
metaWeblog.getRecentPosts
wp.getPosts
metaWeblog.getPost
wp.getPost
mt.getRecentPostTitles
mt.getPostCategories
mt.setPostCategories
mt.publishPost
metaWeblog.newPost
wp.newPost
metaWeblog.editPost
wp.editPost
blogger.deletePost
wp.deletePost
metaWeblog.getCategories
mt.getCategoryList
wp.getCategories
wp.getTags
metaWeblog.newMediaObject
wp.uploadFile
```

后台“基本设置 > 兼容性接口”提供三种运行模式：

| 模式 | 行为 |
|---|---|
| 关闭 | 禁用 XML-RPC 入口，并停止输出 RSD、WLW Manifest 和 Pingback 发现信息 |
| 仅关闭 Pingback 接口 | 保留 MetaWeblog、WordPress 和 Blogger 方法，但不宣告或接受 `pingback.ping` |
| 打开 | 启用全部受支持的 XML-RPC 方法和 Pingback 接收能力 |

配置项 `xmlrpc_mode` 对应值依次为 `off`、`no_pingback`、`on`，默认 `on`。Trackback 是独立协议，不受该模式影响。

方法经过用户密码认证和角色/资源所有权检查。贡献者可管理自己的文章，但不能通过 API 绕过页面和发布权限；页面创建/编辑要求编辑权限。

上传方法复用核心附件安全校验和上传钩子。内容保存复用草稿/发布服务和内容钩子，不应形成一套独立数据语义。

WAF 对 XML-RPC 有独立限流，默认 60 秒 30 次，可单独关闭。禁用 XML-RPC 相关选项时，客户端应把失败视为功能关闭而不是重试风暴。

## RSD 和 WLW Manifest

兼容发现接口：

| 路径 | 用途 |
|---|---|
| `/rsd.xml` | 宣告 MetaWeblog、Blogger 和 WordPress XML-RPC端点 |
| `/wlwmanifest.xml` | Windows Live Writer 兼容信息 |

前台模板数据包含对应 URL，主题应在 `<head>` 输出发现链接；默认主题已经处理。

## Pingback

XML-RPC 的 `pingback.ping` 接收来源 URL 和目标 URL：

1. 检查 Pingback 开关和目标内容 `allowPing`。
2. 验证目标属于本站公开内容。
3. 通过 SSRF 防护客户端抓取来源页面。
4. 确认来源页面确实链接目标。
5. 检查同一来源 URL是否已存在 Pingback。
6. 通过评论保存钩子写入 `type=pingback` 的评论。

Pingback 默认保存为已通过评论，但插件仍可以在 `comment.before_save` 根据 `Operation=pingback` 调整状态或拒绝。

## Trackback

接收端路径：

```text
/trackback/<content-cid>
```

Trackback 会检查全局开关、目标内容、`allowPing`、来源 URL和重复记录，然后通过评论保存钩子写入 `type=trackback` 的评论。响应使用 Trackback XML约定而不是普通 HTML 状态页。

`<content-cid>` 是内部内容 ID，不是公开 `slugId`。主题如果输出 Trackback 地址，应使用模板数据/核心生成值，不要从公开 URL末段推导。

## 发布后的外发通知

文章允许 Ping 时，发布流程可扫描正文中的 HTTP/HTTPS链接：

- 使用安全 HTTP 客户端访问目标。
- 优先发现 Pingback 响应头或页面标记。
- 也可发现 Trackback RDF/标记端点。
- 记录最近通知状态，避免无提示失败。

外发请求受 SSRF、重定向和响应大小限制。正文中包含站内网地址不会导致服务访问私网。

插件监听 `content.after_save`（`Operation == "publish"`）或 `content.after_status_change`（状态变为 `publish`）时要注意 XML-RPC、后台发布和草稿合并都会触发发布生命周期。需要去重的外部任务应按发布内容 ID 和修改时间建立幂等键。

## 固定链接一致性

兼容接口、Feed、Sitemap 和主题必须统一调用核心 URL生成逻辑：

- 自定义 `slug` 优先。
- 没有自定义值时使用 `slugId`。
- 文章和页面默认以 `.html` 结尾。
- 分类使用分类固定链接模板。
- `base_url` 提供绝对地址前缀。

不要直接用 `Content.Slug` 拼接 URL；它可能为空，这正是早期 Sitemap 出现 `/post/` 重复地址的原因。
