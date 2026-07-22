# 内容与分类体系

## 统一内容模型

文章、独立页面和附件共用 `gb_contents`，通过 `type` 区分：

- `post`：文章，可进入首页、归档、分类、标签和 Feed。
- `page`：独立页面，不进入普通文章聚合。
- `attachment`：附件记录，正文列保存 `AttachmentMeta` JSON。

文章和页面共用编辑器、自动保存、修订、自定义字段和大部分权限逻辑。页面要求至少 `editor` 权限；文章编辑入口要求至少 `contributor` 权限。

## 状态

核心使用以下状态值：

| 值 | 含义 |
|---|---|
| `publish` | 已发布 |
| `draft` | 草稿，或已发布内容的编辑草稿 |
| `waiting` | 待审核，贡献者提交发布时使用 |
| `private` | 私密内容 |
| `hidden` | 已隐藏内容 |

公开查询只返回符合公开条件的已发布内容，并排除未来发布日期；后台查询可以显式包含草稿和其他状态。

## 草稿模型

### 未发布内容

新建文章或页面在第一次保存时形成一条独立草稿：

- `status=draft`
- `draftOf=0`
- 后续手动保存和自动保存更新同一条记录。
- 发布时这条记录直接转为发布状态，不另建一个公开副本。

删除未发布草稿后，其尚未正式发布的 Slug ID 可以被后续内容复用。数据库主键 `cid` 仍由数据库管理，不承诺复用。

### 编辑已发布内容

已发布内容保持固定快照，编辑时最多存在一条独立编辑草稿：

- 发布记录保持 `draftOf=0`。
- 编辑草稿为 `status=draft`、`draftOf=<已发布 CID>`。
- 编辑草稿与发布记录共享同一个 `slugId`。
- 重复自动保存更新这条编辑草稿；服务层会清理同一发布内容的多余编辑草稿。
- “丢弃草稿”只删除编辑草稿，不修改已发布内容。
- 再次发布时，草稿内容、分类标签和字段合并回原发布记录，然后删除草稿。

后台列表因此以一个公开内容实例为一个列表项，通过“编辑草稿”入口提示该项目存在未发布修改，而不是把草稿显示成另一篇文章。

## Slug ID 与固定链接

`cid`、`slugId` 和 `slug` 是三个不同概念：

- `cid` 是数据库内容主键。
- `slugId` 是文章或页面的稳定递增数字固定链接标识。
- `slug` 是用户可选的自定义路径片段。

公开链接使用固定链接模板生成，默认：

```text
/post/{slug}.html
/page/{slug}.html
```

生成 `{slug}` 时，自定义 `slug` 非空则优先使用；否则使用十进制 `slugId`。因此一篇没有自定义别名、`slugId=5` 的文章路径为 `/post/5.html`，设置 `slug=hello` 后为 `/post/hello.html`，底层 `slugId` 仍保留为 5。

文章和页面分别维护各自的 Slug ID 分配下限。已发布内容使用过的 Slug ID 不因删除而回收；仅未发布草稿占用且尚未提交为发布下限的 ID 可以被后续同类型内容复用。已发布内容及其编辑草稿始终属于同一个 Slug ID。

固定链接模板由后台设置控制，支持的占位符和合法性由 `core/handlers/app.go` 的固定链接校验逻辑约束。修改模板时必须保留可唯一定位资源的占位信息。

## 保存输入

服务层使用 `services.SaveContentInput` 传递标题、别名、正文、时间、作者相关字段、模板、类型、状态、密码、评论/Ping/Feed 开关、父级、分类、标签和自定义字段等信息。

插件的 `content.before_save` 钩子中，`ContentSavePayload.Input` 的当前具体类型就是 `services.SaveContentInput`。修改时应重新赋回 payload：

```go
payload, ok := value.(plugin.ContentSavePayload)
if !ok {
    return value, nil
}
input, ok := payload.Input.(services.SaveContentInput)
if !ok {
    return value, nil
}
input.Title = strings.TrimSpace(input.Title)
payload.Input = input
return payload, nil
```

完整插件结构和导入见 [插件与钩子开发](plugins-and-hooks.md)。

## 自动保存

后台编辑器通过独立自动保存入口提交当前表单。服务端根据当前内容 ID 和 `published_id` 判断更新未发布草稿还是已发布内容的编辑草稿。自动保存由基本设置中的 `content_autosave_enabled` 控制，默认开启；关闭后编辑过程不会定时保存正文，但附件上传前仍可显式创建当前内容草稿，用于获得稳定的内容 ID 和附件目录。

自动保存不是修订版本，也不应创建多个草稿。修改编辑器前端时必须保留当前草稿 ID/发布 ID 的后续回写，否则下一次自动保存会被误认为新建内容。

自动保存由核心写入编排层处理，会先触发 `autosave.before_save`，再触发内容保存通用钩子，`ContentSavePayload.Operation` 为 `autosave`，保存完成后触发 `content.after_save` 和 `autosave.after_save`。插件进行外发通知时通常应忽略该操作，避免每次输入都触发昂贵任务。

## 修订版本

修订快照保存在 `gb_revisions`，与编辑草稿分离。`gb_contents` 不再使用 revision 类型保存快照。修订记录包括标题、别名、正文、状态、密码、排序、模板、父级和三个内容开关等可恢复字段。

- 修订功能默认开启，可在阅读设置中关闭；关闭后不再写入新的快照。
- 默认每项内容最多保留 20 条，后台可配置修订上限，填写 0 表示无限制。
- 编辑页面底部提供手动保存快照按钮，用于主动记录当前表单内容。
- 已发布文章或页面再次发布更新前，会自动保存旧的已发布版本快照。
- 可以查看全文、恢复或删除单条修订。
- 删除内容时同步删除它的修订。
- 修订不参与文章/页面数量统计，也不生成公开 URL。

## 查询与归档

`services.ContentQuery` 支持按类型、状态、关键字、分类、标签、作者、父级、年月日、未来时间、草稿、分页等条件组合查询。

`ArchiveMonths` 对已发布、非未来文章按站点时区聚合月份。它不是把日期字符串交给模板临时分组，而是服务层按查询结果生成归档月份及计数。

插件可通过 `content.list` 在 `Stage=before` 修改 `ContentQuery`，或设置 `Handled=true` 返回自己的结果；`Stage=after` 可继续过滤默认结果。该钩子覆盖后台文章/页面列表和前台首页、分类、标签、作者、搜索、归档等内容集合。

## 正文格式

`pkg/render.ContentHTML` 支持：

- Markdown：Goldmark + GFM，启用表格、任务列表、删除线、自动标题 ID和硬换行。
- 纯文本：HTML 转义后生成段落与 `<br>`。
- HTML：按受信任正文输出。
- 内容头标记 `<!--markdown-->` / `<!--plaintext-->` 可覆盖站点默认模式。

后台 Markdown 编辑器提供格式工具栏、预览和按需加载的语法高亮脚本。前端展示仍以服务端渲染结果为准。

## 分类与标签

分类和标签共用 `gb_metas`：

- `type=category` 为分类，支持父分类、排序和描述。
- `type=tag` 为标签，通常不使用父级结构。
- `gb_relationships` 保存内容与 Meta 的多对多关系。
- `count` 是关联数量的缓存字段，关系变化和内容删除后会同步更新。

分类和标签的名称、Slug 由服务层校验唯一性。删除或移动分类时应通过服务接口执行，以保证关系和计数同步；不要只删除 `gb_metas` 行。

## 自定义字段

字段保存在 `gb_fields`，支持 `str`、`int`、`float` 和服务层可识别的 `json` 类型。主题和插件通过 `plugin.FieldSchema` 声明编辑界面：

```go
func (MyPlugin) ContentFieldSchema() []plugin.FieldSchema {
    return []plugin.FieldSchema{
        {
            Name:        "reading_minutes",
            Label:       "预计阅读分钟",
            Type:        plugin.FieldNumber,
            Default:     "5",
            Min:         "1",
            Max:         "120",
            ForTypes:    []string{models.ContentTypePost},
            Description: "仅显示在文章编辑页。",
        },
    }
}
```

字段名称必须通过服务端校验。`ForTypes` 限制适用内容类型；`ReadOnly` 或 `content.field_readonly` 可保护已有字段，服务端会拒绝伪造表单覆盖只读值。

## 删除内容

删除文章或页面会在事务中处理关系、评论、字段、修订和关联编辑草稿。附件文件如何处理由 `attachment_delete_policy` 决定，并在内容删除处理器中配合附件钩子和目录迁移执行。详见 [附件与图片处理](media-and-images.md)。
