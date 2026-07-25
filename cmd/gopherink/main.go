package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Chocola-X/GopherInk/core/handlers"
	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
	"github.com/Chocola-X/GopherInk/core/services"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "config":
			return runConfigCommand(args[1:])
		case "user", "users":
			return runUserCommand(args[1:])
		case "help", "-help", "--help", "-h":
			printUsage()
			return nil
		}
	}
	cfg, err := loadConfig(args, true)
	if err != nil {
		return err
	}
	return serve(cfg)
}

func serve(cfg config) error {
	if cfg.TLSEnabled {
		if _, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil {
			return fmt.Errorf("加载 TLS 证书和私钥失败: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	serviceDB := services.DB(services.NewSQLDB(db, cfg.DBDriver))
	if cfg.DBReadDSN != "" {
		readDB, err := openDB(config{DBDriver: cfg.DBDriver, DBDSN: cfg.DBReadDSN})
		if err != nil {
			return fmt.Errorf("read database health check failed: %w", err)
		}
		defer readDB.Close()
		serviceDB = services.NewDBRouter(db, readDB, cfg.DBDriver)
	}

	if err := models.InitializeSchema(ctx, db, cfg.DBDriver); err != nil {
		return err
	}
	setupCtx := services.WithWriter(ctx)

	options := services.NewOptionService(serviceDB)
	if err := options.EnsureDefaults(setupCtx); err != nil {
		return err
	}

	users := services.NewUserService(serviceDB)
	userCount, err := users.Count(setupCtx)
	if err != nil {
		return err
	}
	defaultAdminReady := false
	if shouldCreateDefaultAdmin(userCount, cfg) {
		if err := users.EnsureDefaultAdmin(setupCtx, cfg.AdminUser, cfg.AdminPassword, cfg.AdminMail); err != nil {
			return err
		}
		defaultAdminReady = true
	} else if userCount == 0 {
		log.Printf("web install is available at %s/install", localServiceURL(cfg))
	} else {
		defaultAdminReady = true
	}

	contents := services.NewContentService(serviceDB)
	metas := services.NewMetaService(serviceDB)
	if err := metas.EnsureDefaultCategory(setupCtx); err != nil {
		return err
	}
	comments := services.NewCommentService(serviceDB)
	app := handlers.NewWithPaths(contents, metas, comments, users, options, plugin.Default, cfg.DataDir, cfg.UploadDir)
	handler, err := allowNetworks(app.Handler(), cfg.AllowedCIDRs)
	if err != nil {
		return err
	}

	protocol := "HTTP"
	if cfg.TLSEnabled {
		protocol = "HTTPS"
	}
	log.Printf("GopherInk %s listening on %s; allowed client networks: %s", protocol, cfg.Addr, strings.Join(cfg.AllowedCIDRs, ", "))
	if defaultAdminReady {
		log.Printf("admin: %s/admin", localServiceURL(cfg))
	}
	server := &http.Server{
		Addr:      cfg.Addr,
		Handler:   handler,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	var serveErr error
	if cfg.TLSEnabled {
		serveErr = server.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
	} else {
		serveErr = server.ListenAndServe()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func shouldCreateDefaultAdmin(userCount int, cfg config) bool {
	if userCount > 0 {
		return false
	}
	return !cfg.WebInstall || cfg.AdminExplicit
}

func openDB(cfg config) (*sql.DB, error) {
	driver := cfg.DBDriver
	if driver == "sqlite" {
		driver = "sqlite3"
		if err := os.MkdirAll(filepath.Dir(cfg.DBDSN), 0755); err != nil {
			return nil, err
		}
	}
	if driver == "mariadb" {
		driver = "mysql"
	}
	if driver == "postgresql" || driver == "pgx" {
		driver = "postgres"
	}
	db, err := sql.Open(driver, cfg.DBDSN)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite3" && !strings.Contains(cfg.DBDSN, "?") {
		db.SetMaxOpenConns(1)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func allowNetworks(next http.Handler, cidrs []string) (http.Handler, error) {
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, item := range cidrs {
		_, network, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("parse allowed client network %q: %w", item, err)
		}
		networks = append(networks, network)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(strings.TrimSpace(host))
		for _, network := range networks {
			if ip != nil && network.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "client network is not allowed", http.StatusForbidden)
	}), nil
}

func localServiceURL(cfg config) string {
	host := cfg.ListenHost
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	scheme := "http"
	if cfg.TLSEnabled {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(cfg.ListenPort))
}

func printUsage() {
	fmt.Println(`GopherInk 0.5.0

用法
  gopherink [启动参数]
      启动 HTTP 或 HTTPS 服务。参数只对本次进程生效，不写入配置文件。

  gopherink config [配置参数]
      将设置保存到 data/config.json 后退出，不启动服务。
      不带配置参数时进入交互式配置向导。

  gopherink user list
      列出数据库中的用户 ID、用户名、显示名、角色和邮箱。

  gopherink user reset-password --id ID
  gopherink user reset-password --name USERNAME
      重置指定用户密码并使其现有登录会话失效。

启动与持久化配置参数
  -p, --port PORT          HTTP/HTTPS 监听端口；HTTP 默认 8086，TLS 默认 443
  --host IP                HTTP/HTTPS 服务绑定 IP，默认 0.0.0.0
  --allow-cidr CIDR        允许访问的客户端 IP/CIDR；可重复或逗号分隔
  --tls                     启用 HTTPS/TLS；关闭时可使用 --tls=false
  --tls-cert PATH           TLS 证书链文件路径
  --tls-key PATH            TLS 私钥文件路径
  --db-type TYPE           sqlite、mysql、mariadb 或 postgres
  --db-host IP             MySQL/PostgreSQL 数据库地址
  --db-port PORT           外部数据库端口
  --db-name NAME           外部数据库名称，默认 gopherink
  --db-user USER           数据库用户名
  --db-password PASSWORD   数据库密码；使用交互向导可避免终端回显
  --sqlite-path PATH       SQLite 数据库文件，默认 data/gopherink.db
  --upload-dir PATH        上传文件系统根目录，默认 data/uploads

仅限本次启动的高级数据库参数
  --db-dsn DSN             直接指定主数据库 DSN
  --db-read-dsn DSN        指定只读数据库 DSN
  --db-write-dsn DSN       指定写数据库 DSN

用户密码重置参数
  --id ID                  按用户 ID 定位
  --name USERNAME          按用户名定位
  --password-stdin         从标准输入第一行读取密码，适合自动化
  --password PASSWORD      直接传入密码，不推荐，可能留在 Shell 历史中

配置优先级
  内置默认值 < data/config.json < 环境变量 < 本次 CLI 参数
  GOPHERINK_DATA_DIR 可改变 data 目录及 config.json 所在位置。

常用环境变量
  GOPHERINK_ADDR                    HTTP/HTTPS 绑定地址，例如 127.0.0.1:8086
  GOPHERINK_LISTEN_CIDRS            允许网段，多个使用逗号分隔
  GOPHERINK_TLS_ENABLED             是否启用 HTTPS/TLS
  GOPHERINK_TLS_CERT                TLS 证书链文件路径
  GOPHERINK_TLS_KEY                 TLS 私钥文件路径
  GOPHERINK_DB_DRIVER               数据库类型
  GOPHERINK_DB_DSN                  主数据库 DSN
  GOPHERINK_DB_HOST / GOPHERINK_DB_PORT
                                      结构化数据库地址和端口
  GOPHERINK_DB_NAME / GOPHERINK_DB_USER
                                      数据库名称和用户名
  GOPHERINK_DB_PASSWORD             数据库密码
  GOPHERINK_SQLITE_PATH             SQLite 文件位置
  GOPHERINK_UPLOAD_DIR              上传目录
  GOPHERINK_DATA_DIR                数据根目录，默认 data

示例
  gopherink -p 8848
  gopherink config -p 8848
  gopherink --tls --tls-cert /etc/gopherink/fullchain.pem --tls-key /etc/gopherink/privkey.pem
  gopherink config --tls --tls-cert /etc/gopherink/fullchain.pem --tls-key /etc/gopherink/privkey.pem
  gopherink config --allow-cidr 127.0.0.1 --allow-cidr 10.0.0.0/8
  gopherink config --db-type postgres --db-host 10.0.0.8 --db-port 5432 \
    --db-name gopherink --db-user blog
  gopherink user list
  gopherink user reset-password --id 1
  printf 'new-password\n' | gopherink user reset-password --id 1 --password-stdin

更多说明请查看 docs/installation-and-configuration.md。`)
}
