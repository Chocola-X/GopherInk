# GoBlog Typecho 对标功能说明

本文件用于记录“用 Go 重写一个 Typecho”阶段的后台基础功能范围，后续开发以此为索引。

## 不实现网页安装向导

- Typecho 的 `install.php` / `install/` 不做网页复刻。
- GoBlog 首次启动时在命令行选择数据库后端，默认 `sqlite`；也可用环境变量跳过交互：
  - `GOBLOG_DB_DRIVER=sqlite|mariadb|mysql`
  - `GOBLOG_DB_DSN=...`

## 后台菜单与页面

- 控制台：`index.php`、`welcome.php`
- 撰写：`write-post.php`、`write-page.php`
- 管理：`manage-posts.php`、`manage-pages.php`、`manage-comments.php`、`manage-categories.php`、`manage-tags.php`、`manage-users.php`
- 独立编辑页：`category.php`、`user.php`、`profile.php`
- 设置：`options-general.php`、`options-reading.php`、`options-discussion.php`、`options-permalink.php`
- 外观和扩展：`themes.php`、`plugins.php`、`options-theme.php`、`options-plugin.php`、`theme-editor.php`
- 文件和维护：`manage-medias.php`、`media.php`、`backup.php`

## 当前基础实现目标

- 登录、登出、Cookie 会话
- 控制台统计
- 文章管理：列表、搜索、按状态筛选、按分类筛选、新建、编辑、删除、状态切换
- 页面管理：列表、新建、编辑、删除、父级页面、排序字段、模板字段、隐藏/发布
- 分类管理：列表、新建、编辑、删除、默认分类、层级父类
- 标签管理：列表、新建、编辑、删除
- 评论管理：列表、搜索、按状态筛选、编辑、回复、通过、待审、垃圾、删除
- 用户管理：列表、新建、编辑、删除、角色字段
- 个人资料：昵称、邮箱、网址、密码修改
- 设置：基本、阅读、评论、永久链接
- 主题：已编译主题列表、激活主题
- 插件：已编译插件列表
- 备份/附件/主题编辑：先提供后台入口和说明页面，后续实现文件级能力

## 数据模型

核心表参考 Typecho：

- `gb_contents`：文章、页面、草稿等内容类型
- `gb_metas`：分类和标签
- `gb_relationships`：内容与分类/标签关系
- `gb_comments`：评论、引用、回复
- `gb_users`：后台用户
- `gb_options`：站点配置

Go 版为跨 SQLite/MariaDB 简化，保留语义但避免保留字：

- Typecho `order` -> GoBlog `sortOrder`
- Typecho `group` -> GoBlog `role`

## 后续增强项

- 附件上传、附件归属和媒体库
- 自定义字段完整 UI 和存储
- 主题文件编辑器的安全写入
- 插件/主题配置表单协议
- 备份导入导出
- 预览、自动保存、修订版本
- 完整权限系统和 CSRF 防护
- Pingback、Trackback、XML-RPC
