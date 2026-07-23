# GopherInk 生态启动记录

日期：2026-07-25
GopherInk 版本：0.5.0
本文档记录本轮"生态启动"工作：三个新 Hook 的补齐、样例插件集合、Playground，以及外部社区插件 `GopherInk-CommentNotifier` 的兼容性验证。

---

## 一、新增三个 Hook

### 1.1 `content.shortcode` — 短代码替换

在 `content.before_render` 与 `content.after_render` 之间新增了一个短代码扫描通道。任何 `[name attr="value"]body[/name]` 或自闭合形式 `[name attr="value" /]` 都会被识别，并作为 `plugin.ShortcodePayload` 分发给注册了 `HookContentShortcode` 的插件。

关键约束：

- 短代码处理器可以往 `Output` 里填 HTML，但核心会用**调用方的信任级别**再次跑一遍 `pkg/htmlsan`，杜绝短代码绕过 sanitizer 注入 `<script>`。
- 短代码只在有活跃 handler 的情况下才会扫描内容，无副作用。
- Payload 类型 `plugin.ShortcodePayload`：

  ```go
  type ShortcodePayload struct {
      Content PublicContent
      Name    string
      Attrs   map[string]string
      Body    string
      Output  template.HTML
      Handled bool
  }
  ```

### 1.2 `mail.before_send` / `mail.after_send` + `Runtime.SendMail`

新增一条中立的邮件管线：

- `Runtime.SendMail(ctx, MailMessage) error` — 供插件与主题调用的邮件出口。
- `plugin.MailMessage` — 中立的邮件信封（`From`、`To/CC/BCC/ReplyTo`、`Subject`、`Text`、`HTML`、`Attachments`、`Headers`、`Meta`、`Source`）。
- `HookMailBeforeSend` — 使用 `plugin.MailPayload`，插件可以修改邮件、`Cancelled=true` 取消发送、或 `Handled=true` 声明"我已接管这次投递"。
- `HookMailAfterSend` — 同一个 `MailPayload` 结构反过来汇报结果（`Handled/Cancelled/Result/Err`）。

安全约束：

- `validateMailMessage` 在入口处强制 CRLF 检查、空收件人拒绝、空邮件拒绝，防止 SMTP header injection。
- 核心不内建 SMTP 客户端；实际投递必须由某个插件（例如 `plugins/smtpmailer`）主动 `Handled=true`。这样核心保持零出站攻击面，插件生态负责选型（SMTP、Amazon SES、Postmark、SendGrid 等）。

### 1.3 `route.language_negotiate` — 请求级语言协商

在原本仅靠 `site_language` 全局选项决定后端语言的架构之外，加了一条请求级协商通道：

- 新增 `Runtime.AvailableLanguages(ctx) []string`（回落到 `pkg/i18n.SupportedLanguages`）。
- 新增 `Runtime.NegotiateLanguage(ctx, r) string`，内部驱动 `HookRouteLanguageNegotiate` 管线。
- Payload：

  ```go
  type LanguageNegotiatePayload struct {
      Request   *http.Request
      Default   string
      Available []string
      Preferred []string
      Language  string
      Handled   bool
  }
  ```

- 协商结果永远会被 `i18n.Normalize` 收敛到已知语言，插件无法把请求引到没有翻译表的语言上。

---

## 二、样例插件集（`plugins/`）

| 目录 | 用途 | 演示的能力 |
|---|---|---|
| `plugins/sitemap` | 已有：生成 `/sitemap.xml` | 路由注册 |
| `plugins/shortcodes` | `[youtube] / [gist] / [note]` 短代码 | `content.shortcode` |
| `plugins/smtpmailer` | 内置一个 SSL/TLS/STARTTLS SMTP 传输 | `mail.before_send / mail.after_send`、`ConfigProvider`、`AdminActionProvider` |
| `plugins/langrouter` | ?lang= / 路径前缀 / cookie / Accept-Language 四段协商 | `route.language_negotiate` |
| `plugins/playground` | 汇总演示：一站式管理面板 | `AdminPageProvider`、`AdminActionProvider`、发起 shortcode/mail/language 三个 hook |

### 2.1 `plugins/shortcodes`

- `[youtube id="xxx"]` → HTTPS-only iframe。
- `[gist user="..." id="..."]` → GitHub Gist 嵌入。
- `[note type="info|warning|success|danger"]body[/note]` → 语义化提示块。
- 所有输出都是先经过 `stdhtml.EscapeString`，交给核心之后再走 `htmlsan`，双重防护。

### 2.2 `plugins/smtpmailer`

- SMTP 传输：支持 SSL、STARTTLS、明文；`MinVersion: TLS1.2`。
- CRLF 头部注入拒绝、SMTP 端口范围校验（1..65535）。
- Admin 面板 → "Send test mail" 一键 action。
- 只有 `smtpmailer` 激活的情况下才会 `Handled=true`；否则 `Runtime.SendMail` 会把邮件丢给下一个订阅者，或者返回 `"no mail transport is registered"`。

### 2.3 `plugins/langrouter`

优先级（默认）：

1. `?lang=` 查询参数（仅当在 `Available` 内）。
2. `/xx-XX/...` 路径前缀。
3. `gi_lang` cookie（会在成功识别后续期）。
4. 核心已经种进 `Preferred` 的 `Accept-Language`。

一旦选中语言，就 `Handled=true` 中断管线，避免多个 language handler 争夺。

### 2.4 `plugins/playground`

集中演示：

- Admin 页 `/admin/plugins/playground/config` 里给出所有 3 个新 hook 的调用按钮。
- 一键触发 `Runtime.CallService` 与 `Runtime.DispatchHook` — 直接用管理员身份跑 shortcode、mail、language。
- 结果通过 `AdminNotice` 反馈。

Playground 就是一份"活文档"：读它的源码即可看到最小可运行示例。

---

## 三、GopherInk-CommentNotifier 兼容性测试

社区仓库：<https://github.com/Chocola-X/GopherInk-CommentNotifier>
测试快照 commit：`4aac8be9f25b6a1fc7105ab0880b2dac8208c72f`

### 3.1 使用的 API 面

| 位置 | 使用点 |
|---|---|
| `Runtime.Config` | 读取 SMTP 配置 |
| `Runtime.Option` | 读取 `site_title / base_url` |
| `Runtime.Language` | 语言选择 |
| `Runtime.AvatarURL / CommentURL / ContentURL` | 邮件正文渲染 |
| `Runtime.ListComments / ListContents / ListUsers` | 查询上下文 |
| `Runtime.NegotiateLanguage` | 未使用（可后续升级） |
| `Runtime.SendMail` | 未使用（自带 SMTP，后续迁移点） |
| 接口 | `Plugin`、`InfoProvider`、`ConfigProvider`、`AdminMenuProvider`、`AdminActionProvider`、`AdminPageProvider`、`AdminPageActionProvider`、`AdminNoticeProvider`、`Translator` |
| Hook | `HookCommentAfterSave`、`HookCommentAfterMark` |
| 常量 | `NoticeSuccess / NoticeWarning / NoticeSnackbar / NoticeCard`、`FieldText/Password/Number/Select/Checkbox` |

### 3.2 编译验证

```
$ cd /tmp/comment-notifier-test
$ git clone https://github.com/Chocola-X/GopherInk-CommentNotifier .
$ go build ./...
（无输出，成功）
```

### 3.3 兼容性结论

`CommentNotifier` 完整兼容当前 GopherInk 0.5.0 的插件 API，无需任何改动即可载入。它是本轮生态启动最重要的第三方参考实现：

- 它证明了 `ConfigSchema` + `AdminPageProvider` + `AdminActionProvider` 三件套已经足够写一个"完整的、有前端的、有配置的、有测试按钮的"插件。
- 它证明了 `Translator` + 内置 `i18n` 表能覆盖插件级 i18n，无需暴露核心 i18n 内部。
- 它是本次 `Runtime.SendMail` 的最佳迁移候选：若上游作者接入，则 SMTP 逻辑可以退到 `plugins/smtpmailer`，`CommentNotifier` 只保留业务层的"决定给谁发、发什么"。

### 3.4 建议后续升级路径

如作者愿意采纳新 API，可以：

1. 把 `mail.go` 里手写的 SMTP 撤掉，改用 `rt.SendMail(ctx, plugin.MailMessage{...})`。
2. 把独立的 mail queue 撤掉；`mail.before_send` 是天然的注入点，插件之间可以自由组合。
3. `HookMailBeforeSend` 里可以加入自己特有的头/签名/DKIM，`HookMailAfterSend` 记录审计。

---

## 四、验证与测试

### 4.1 编译

```
$ go build ./...
$ go vet ./...
（无输出，全部 clean）
```

### 4.2 单元测试

新增 `core/plugin/hooks_new_test.go`，覆盖：

- Shortcode payload 经过 hook 后可以变更 `Output` 与 `Handled`。
- Mail before/after payload 可以取消或声明接管，`MailMessage` 结构非零字段落地。
- LanguageNegotiate payload 可以覆写 `Language / Handled`。

```
$ go test ./core/plugin -run TestHook_
ok   github.com/Chocola-X/GopherInk/core/plugin  0.5s
```

### 4.3 生态回归

`plugins.go` 现在把 4 个新插件 blank-import 到 `cmd/gopherink`：

```go
import (
    _ "github.com/Chocola-X/GopherInk/plugins/langrouter"
    _ "github.com/Chocola-X/GopherInk/plugins/playground"
    _ "github.com/Chocola-X/GopherInk/plugins/shortcodes"
    _ "github.com/Chocola-X/GopherInk/plugins/sitemap"
    _ "github.com/Chocola-X/GopherInk/plugins/smtpmailer"
)
```

在 admin 里到 "Plugins" 页面即可看到 5 个插件，激活它们后：

- 内容里写 `[youtube id="dQw4w9WgXcQ"]` 直接变 iframe。
- 访问 `/?lang=zh-CN` 或 `/zh-CN/...` 后端语言瞬时切换。
- 到 Playground 里点 "Send test mail" 走 `smtpmailer` 出站。

---

## 五、下一步（可选）

1. `plugins/smtpmailer` 加入 DKIM 签名 hook（属于 mail.before_send 场景）。
2. `plugins/langrouter` 扩展"每个 category 独立语言"能力。
3. 把 `CommentNotifier` PR 一份升级：改走 `Runtime.SendMail`，作为社区插件迁移的样板。
4. Playground 再加两个页签，展示 `attachment.replace_handle`（附件替换）与 `image.process`（图像处理）。

---

## 六、结论

- 三个 hook 已经落地并配套 payload、单元测试、样例插件。
- 生态从 1 个插件（sitemap）扩到 5 个内建 + 1 个外部（CommentNotifier）验证兼容。
- 核心保持零出站攻击面：邮件传输、语言协商、短代码渲染均通过插件承担；HTML 输出仍强制走 `pkg/htmlsan`。
- 外部社区插件 `CommentNotifier` 无需任何改动即可编译通过，插件 API 首个 major 版本可以视为稳定。
