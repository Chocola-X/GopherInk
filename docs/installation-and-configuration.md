# 安装与配置

## 编译要求

- Go 版本以根目录 `go.mod` 的 `go` 指令为准。
- 默认 SQLite 驱动需要 CGO 和可用的 C 编译器。
- 新增或修改主题、插件后需要重新编译。

```bash
go build -o gopherink ./cmd/gopherink
./gopherink
```

默认监听 `0.0.0.0:8086`，SQLite 数据库位于 `data/gopherink.db`。首次在交互式终端启动且没有配置文件、默认数据库或外部启动参数时，程序依次询问数据库、上传目录、绑定地址、端口和允许网段，并把结果写入 `data/config.json`。数据库连接成功后才会创建 Schema，然后进入浏览器 Web 安装流程设置站点资料和管理员。

终端启动配置与后台设置完全分离：

- `data/config.json`：数据库连接、上传文件系统位置、HTTP 监听和客户端网段，只由启动层读取。
- `gb_options`：站点、阅读、评论、WAF、后台个性化、主题和插件设置，由后台管理。
- CLI 临时参数：只覆盖当前进程，不反写 JSON 或数据库。

## 临时参数与持久化配置

直接传参会启动服务，参数只在本次运行中生效：

```bash
./gopherink -p 8848
./gopherink --host 127.0.0.1 --allow-cidr 127.0.0.1/32
./gopherink --sqlite-path /srv/gopherink/site.db --upload-dir /srv/gopherink/uploads
```

使用 `config` 子命令时，参数写入 `data/config.json` 后进程退出，不会打开数据库或启动 HTTP 服务：

```bash
./gopherink config -p 8848
./gopherink config --db-type postgres --db-host 10.0.0.8 --db-port 5432 \
  --db-name gopherink --db-user gopherink --db-password 'replace-me'
./gopherink config --allow-cidr 127.0.0.1 --allow-cidr 10.0.0.0/8
```

不带参数执行 `./gopherink config` 会打开交互式配置向导，密码输入不会在终端回显。配置文件以 `0600` 权限写入，因为其中可能包含数据库密码。直接使用 `--db-password` 会把密码留在 Shell 历史和进程参数中，生产环境优先使用交互向导或受控环境变量。部署系统仍应限制 `data/` 的文件系统访问和备份读取权限。

| 参数 | 持久化字段 | 说明 |
|---|---|---|
| `-p` / `--port` | `listen.port` | HTTP 端口，默认 `8086` |
| `--host` | `listen.host` | 实际绑定的 IPv4/IPv6 地址，默认 `0.0.0.0` |
| `--allow-cidr` | `listen.allowed_cidrs` | 允许建立 HTTP 请求的客户端网段；可重复或逗号分隔 |
| `--db-type` | `database.type` | `sqlite`、`mysql`、`mariadb`、`postgres` |
| `--db-host` / `--db-port` | `database.host` / `database.port` | MySQL、MariaDB、PostgreSQL 的 IP 和端口 |
| `--db-name` | `database.name` | 外部数据库名称，默认 `gopherink` |
| `--db-user` / `--db-password` | 对应数据库凭据 | 外部数据库账户 |
| `--sqlite-path` | `database.sqlite_path` | SQLite 文件位置 |
| `--upload-dir` | `upload_dir` | `/uploads/` 映射到的文件系统根目录 |
| `--db-dsn` | 不持久化 | 本次启动主 DSN，供特殊驱动参数使用 |
| `--db-read-dsn` / `--db-write-dsn` | 不持久化 | 本次启动读写分离 DSN |

单个 IP 也可传给 `--allow-cidr`，程序会规范化为 `/32` 或 `/128`。该规则检查 TCP 直接来源地址，发生在 WAF 之前，不信任 `X-Forwarded-For`；反向代理部署应把代理节点网段加入这里，再在后台单独配置可信代理规则。`listen.host` 是操作系统套接字绑定地址，CIDR 是访问允许列表，两者语义不同。

默认配置文件结构如下：

```json
{
  "database": {
    "type": "sqlite",
    "host": "127.0.0.1",
    "port": 0,
    "name": "gopherink",
    "username": "",
    "password": "",
    "sqlite_path": "data/gopherink.db"
  },
  "upload_dir": "data/uploads",
  "listen": {
    "host": "0.0.0.0",
    "port": 8086,
    "allowed_cidrs": ["0.0.0.0/0"]
  }
}
```

设置 `GOPHERINK_DATA_DIR` 后，配置文件位置变为 `<GOPHERINK_DATA_DIR>/config.json`，默认 SQLite 和上传目录也随之变化。优先级固定为：

```text
内置默认值 < config.json < 环境变量 < 本次 CLI 参数
```

`config` 会在现有文件上修改明确给出的字段，例如 `config -p 8848` 不会清空已有数据库凭据。直接启动时使用 `-p 8848` 则不会修改文件。

`./gopherink -help`、`--help`、`-h` 和 `help` 会输出完整使用说明；`./gopherink config -help` 只列出可持久化的配置参数，`./gopherink user help` 列出用户应急命令。

## 环境变量

| 变量 | 作用 | 默认/回退行为 |
|---|---|---|
| `GOPHERINK_ADDR` | HTTP 绑定地址和端口 | `0.0.0.0:8086` |
| `GOPHERINK_LISTEN_HOST` | HTTP 绑定 IP；`GOPHERINK_ADDR` 存在时以后者为准 | `0.0.0.0` |
| `GOPHERINK_LISTEN_CIDRS` | 允许客户端 CIDR，逗号分隔 | `0.0.0.0/0` |
| `GOPHERINK_DB_DRIVER` | `sqlite3`、`mysql`、`mariadb` 或 `postgres` | `sqlite` |
| `GOPHERINK_DB_DSN` | 主数据库 DSN | `data/gopherink.db` |
| `GOPHERINK_DB_WRITE_DSN` | 独立写库 DSN | 回退到 `GOPHERINK_DB_DSN` |
| `GOPHERINK_DB_READ_DSN` | 读库 DSN | 未设置时使用写库 |
| `GOPHERINK_DB_HOST` | 外部数据库 IP | `127.0.0.1` |
| `GOPHERINK_DB_PORT` | 外部数据库端口 | MySQL/MariaDB `3306`，PostgreSQL `5432` |
| `GOPHERINK_DB_NAME` | 外部数据库名称 | `gopherink` |
| `GOPHERINK_DB_USER` | 外部数据库用户名 | 空 |
| `GOPHERINK_DB_PASSWORD` | 外部数据库密码 | 空 |
| `GOPHERINK_SQLITE_PATH` | SQLite 文件位置 | `<GOPHERINK_DATA_DIR>/gopherink.db` |
| `GOPHERINK_ADMIN_USER` | 初始管理员用户名 | `admin` |
| `GOPHERINK_ADMIN_PASSWORD` | 初始管理员密码 | `admin123` |
| `GOPHERINK_ADMIN_MAIL` | 初始管理员邮箱 | `admin@example.com` |
| `GOPHERINK_WEB_INSTALL` | 空库时启用浏览器安装向导 | `true` |
| `GOPHERINK_DATA_DIR` | 数据目录 | `data` |
| `GOPHERINK_UPLOAD_DIR` | 附件文件系统根目录 | `<GOPHERINK_DATA_DIR>/uploads` |

生产环境必须显式设置初始管理员密码，或通过安装向导立即改为强密码。不要继续使用示例默认凭据。

## DSN 示例

SQLite：

```text
data/gopherink.db
```

MySQL/MariaDB：

```text
user:password@tcp(127.0.0.1:3306)/gopherink?charset=utf8mb4&parseTime=true
```

PostgreSQL：

```text
postgres://user:password@127.0.0.1:5432/gopherink?sslmode=disable
```

结构化参数会生成上述 DSN。外部数据库本身必须预先创建，GopherInk 负责在目标数据库内创建当前 Schema，不负责创建数据库实例或配置网络账户。若需要 TLS、Unix Socket 或其他驱动专用参数，使用临时 `--db-dsn` 或对应 DSN 环境变量。

读写分离时，读写 DSN 必须使用同一数据库方言。复制、故障切换和只读权限由数据库基础设施负责。

## 用户应急命令

应急命令读取同一套启动配置连接数据库，但不会初始化 Schema、加载插件或启动 HTTP 服务。

```bash
# 查看 ID、用户名、显示名、角色和邮箱
./gopherink user list

# 按 ID 或用户名重置，省略密码参数时在交互终端询问两次
./gopherink user reset-password --id 1
./gopherink user reset-password --name admin

# 自动化环境从标准输入读取，避免密码出现在进程参数中
printf 'new-strong-password\n' | ./gopherink user reset-password --id 1 --password-stdin
```

交互输入不会在终端回显。也支持 `--password`，但密码可能出现在 Shell 历史和进程列表中，不建议在生产环境使用。新密码至少 6 个字符。重置成功后会同时轮换用户 `authCode`，使现有登录 Cookie 立即失效。

## 持久化目录

部署时至少持久化以下位置：

| 路径 | 内容 |
|---|---|
| `data/config.json` | 独立启动配置，默认权限 `0600` |
| `data/gopherink.db` | 默认 SQLite 数据库 |
| `data/waf.log` | WAF 独立事件日志 |
| `data/uploads/posts/` | 默认文章附件目录 |
| `data/uploads/pages/` | 默认独立页面附件目录 |
| `data/uploads/unattached/` | 默认未绑定附件及保留策略迁出目录 |
| `data/uploads/admin-settings/` | 默认后台个性化素材目录 |
| `data/uploads/theme-settings/` | 默认主题设置素材目录 |

上表使用默认 `GOPHERINK_DATA_DIR=data`。通过 `config --upload-dir`、临时 `--upload-dir` 或 `GOPHERINK_UPLOAD_DIR` 更改上传根目录后，五个子目录都位于该自定义根目录；浏览器公开 URL仍以 `/uploads/` 开头。使用外部数据库时仍需持久化 `data/waf.log` 和本地上传目录。使用插件接管附件存储时，应按插件实现备份远程对象及其元数据。

## 后台配置存储

后台设置最终保存到 `gb_options`。`core/services/options.go` 负责补全缺失默认值，其中认证密钥 `auth_secret` 在不存在时随机生成。

配置分为几类：

- 站点、阅读、评论、附件、API、HTTP 和 WAF 等核心选项。
- `theme:<theme-name>`：主题配置 JSON。
- `plugin:<plugin-name>`：插件站点配置 JSON。
- `plugin:<plugin-name>:personal`：插件按用户保存的个人配置 JSON。
- 后台个性化选项：背景、颜色和各组件透明度等。

不要在多实例间使用不同的 `auth_secret` 数据库副本，否则会话 Cookie 和 CSRF 令牌无法互认。

## 基础 URL 和反向代理

`base_url` 用于固定链接、订阅、Sitemap、Pingback 等绝对 URL。生产环境应填写浏览器实际访问的 HTTPS 地址，避免生成 `localhost` 或错误协议链接。

反向代理环境中的客户端 IP 信任是独立安全设置：

- 未启用信任时，以直接连接地址为准。
- 启用后可选择白名单或黑名单模式，并逐行填写 IP/CIDR。
- 只有满足信任规则的代理来源才能影响 `X-Forwarded-For` / `X-Real-IP` 解析。

不要无条件信任来自公网的转发头，否则攻击者可以伪造 IP 绕过限流和登录封禁。详细规则见 [安全与 WAF](security-and-waf.md)。

## 上传和图片内存

上传大小在后台以 MB 配置，默认 16 MB。图片处理内存预算默认 256 MB，最低允许 64 MB。预算用于在处理超大图片前做估算保护，不是 Go 进程总内存硬限制。

图片转换失败时，上传流程会回退保存原始文件并向后台返回提示；缩略图可选择 JPG、WebP 或禁用。详细行为见 [附件与图片处理](media-and-images.md)。

## 数据库备份

后台备份能力与数据库驱动和当前处理器实现相关。无论是否使用后台入口，建议生产部署执行一致性备份：

- SQLite：在停写或使用 SQLite 在线备份机制时复制数据库文件。
- MySQL/MariaDB：使用 `mysqldump` 或物理备份。
- PostgreSQL：使用 `pg_dump` 或物理备份。
- 同时备份本地 `uploads/` 和必要的 `data/` 文件。

附件数据库记录只保存路径/URL及元数据，不包含本地文件本体，只备份数据库无法恢复媒体资源。

## 部署检查

1. 设置真实 `base_url` 和强管理员密码。
2. 持久化数据库、上传目录和 WAF 日志目录。
3. HTTPS 终止后再按需启用 HSTS；证书和 HTTPS 链路未稳定前保持关闭。
4. 仅信任明确的反向代理 IP/CIDR。
5. 根据服务器资源调整上传大小、图片处理预算和缩略图策略。
6. 根据访问量设置公开缓存 TTL 和各类 WAF 限流。
7. 变更插件或主题后重新编译，并在上线前验证其路由和钩子。
