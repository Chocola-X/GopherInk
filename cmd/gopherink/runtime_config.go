package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
	"golang.org/x/term"
)

const (
	defaultDataDir    = "data"
	defaultListenHost = "0.0.0.0"
	defaultListenPort = 8086
	defaultTLSPort    = 443
)

type databaseSettings struct {
	Type       string `json:"type"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Name       string `json:"name"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	SQLitePath string `json:"sqlite_path"`
}

type listenSettings struct {
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
}

type tlsSettings struct {
	Enabled  bool   `json:"enabled"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

type storedConfig struct {
	Database  databaseSettings `json:"database"`
	UploadDir string           `json:"upload_dir"`
	Listen    listenSettings   `json:"listen"`
	TLS       tlsSettings      `json:"tls"`
}

type config struct {
	Addr          string
	ListenHost    string
	ListenPort    int
	AllowedCIDRs  []string
	TLSEnabled    bool
	TLSCertFile   string
	TLSKeyFile    string
	DataDir       string
	UploadDir     string
	DBDriver      string
	DBDSN         string
	DBReadDSN     string
	DBWriteDSN    string
	AdminUser     string
	AdminPassword string
	AdminMail     string
	WebInstall    bool
	AdminExplicit bool
}

type runtimeFlags struct {
	Settings   storedConfig
	DBDSN      string
	DBReadDSN  string
	DBWriteDSN string
	Visited    map[string]bool
}

type cidrListFlag struct {
	values []string
	set    bool
}

func (v *cidrListFlag) String() string {
	return strings.Join(v.values, ",")
}

func (v *cidrListFlag) Set(raw string) error {
	if !v.set {
		v.values = nil
		v.set = true
	}
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			v.values = append(v.values, item)
		}
	}
	return nil
}

func dataDir() string {
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_DATA_DIR")); value != "" {
		return value
	}
	return defaultDataDir
}

func configPath() string {
	return filepath.Join(dataDir(), "config.json")
}

func defaultStoredConfig(root string) storedConfig {
	return storedConfig{
		Database: databaseSettings{
			Type:       "sqlite",
			Host:       "127.0.0.1",
			Name:       "gopherink",
			SQLitePath: filepath.Join(root, "gopherink.db"),
		},
		UploadDir: filepath.Join(root, "uploads"),
		Listen: listenSettings{
			Host:         defaultListenHost,
			Port:         defaultListenPort,
			AllowedCIDRs: []string{"0.0.0.0/0"},
		},
	}
}

func readStoredConfig(path string) (storedConfig, bool, error) {
	cfg := defaultStoredConfig(filepath.Dir(path))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return storedConfig{}, false, fmt.Errorf("open startup config: %w", err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return storedConfig{}, true, fmt.Errorf("decode %s: %w", path, err)
	}
	normalizeStoredConfig(&cfg)
	return cfg, true, nil
}

func writeStoredConfig(path string, cfg storedConfig) error {
	normalizeStoredConfig(&cfg)
	if err := validateStoredConfig(cfg); err != nil {
		return err
	}
	var err error
	cfg.Listen.AllowedCIDRs, err = normalizeCIDRs(cfg.Listen.AllowedCIDRs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cfg); err != nil {
		temp.Close()
		return fmt.Errorf("encode startup config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace startup config: %w", err)
	}
	return nil
}

func normalizeStoredConfig(cfg *storedConfig) {
	cfg.Database.Type = normalizeDriver(cfg.Database.Type)
	if cfg.Database.Type == "" {
		cfg.Database.Type = "sqlite"
	}
	if strings.TrimSpace(cfg.Database.Host) == "" {
		cfg.Database.Host = "127.0.0.1"
	}
	if strings.TrimSpace(cfg.Database.Name) == "" {
		cfg.Database.Name = "gopherink"
	}
	if cfg.Database.Port <= 0 {
		switch cfg.Database.Type {
		case "mysql", "mariadb":
			cfg.Database.Port = 3306
		case "postgres":
			cfg.Database.Port = 5432
		}
	}
	if strings.TrimSpace(cfg.Listen.Host) == "" {
		cfg.Listen.Host = defaultListenHost
	}
	if cfg.Listen.Port <= 0 {
		cfg.Listen.Port = defaultPortForTLS(cfg.TLS.Enabled)
	}
	if len(cfg.Listen.AllowedCIDRs) == 0 {
		cfg.Listen.AllowedCIDRs = []string{"0.0.0.0/0"}
	}
	if strings.TrimSpace(cfg.UploadDir) == "" {
		cfg.UploadDir = filepath.Join(dataDir(), "uploads")
	}
	if strings.TrimSpace(cfg.Database.SQLitePath) == "" {
		cfg.Database.SQLitePath = filepath.Join(dataDir(), "gopherink.db")
	}
	cfg.TLS.CertFile = strings.TrimSpace(cfg.TLS.CertFile)
	cfg.TLS.KeyFile = strings.TrimSpace(cfg.TLS.KeyFile)
}

func normalizeDriver(driver string) string {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "sqlite", "sqlite3":
		return "sqlite"
	case "mysql":
		return "mysql"
	case "mariadb":
		return "mariadb"
	case "postgres", "postgresql", "pgx":
		return "postgres"
	default:
		return strings.ToLower(strings.TrimSpace(driver))
	}
}

func defaultPortForTLS(enabled bool) int {
	if enabled {
		return defaultTLSPort
	}
	return defaultListenPort
}

func adjustDefaultPortForTLS(cfg *storedConfig, previousEnabled, portExplicit bool) {
	if portExplicit || cfg.TLS.Enabled == previousEnabled {
		return
	}
	if cfg.Listen.Port == defaultPortForTLS(previousEnabled) {
		cfg.Listen.Port = defaultPortForTLS(cfg.TLS.Enabled)
	}
}

func validateStoredConfig(cfg storedConfig) error {
	switch cfg.Database.Type {
	case "sqlite":
		if strings.TrimSpace(cfg.Database.SQLitePath) == "" {
			return errors.New("SQLite 文件位置不能为空")
		}
	case "mysql", "mariadb", "postgres":
		if strings.TrimSpace(cfg.Database.Host) == "" {
			return errors.New("数据库 IP 地址不能为空")
		}
		if cfg.Database.Port < 1 || cfg.Database.Port > 65535 {
			return errors.New("数据库端口必须在 1 到 65535 之间")
		}
		if strings.TrimSpace(cfg.Database.Name) == "" {
			return errors.New("数据库名称不能为空")
		}
	default:
		return fmt.Errorf("不支持的数据库类型 %q", cfg.Database.Type)
	}
	if net.ParseIP(strings.TrimSpace(cfg.Listen.Host)) == nil {
		return errors.New("监听 IP 必须是有效的 IPv4 或 IPv6 地址")
	}
	if cfg.Listen.Port < 1 || cfg.Listen.Port > 65535 {
		return errors.New("监听端口必须在 1 到 65535 之间")
	}
	if strings.TrimSpace(cfg.UploadDir) == "" {
		return errors.New("上传目录不能为空")
	}
	if cfg.TLS.Enabled {
		if cfg.TLS.CertFile == "" {
			return errors.New("启用 TLS 时证书路径不能为空")
		}
		if cfg.TLS.KeyFile == "" {
			return errors.New("启用 TLS 时私钥路径不能为空")
		}
	}
	normalized, err := normalizeCIDRs(cfg.Listen.AllowedCIDRs)
	if err != nil {
		return err
	}
	if len(normalized) == 0 {
		return errors.New("至少需要设置一个允许访问的 IP 网段")
	}
	return nil
}

func normalizeCIDRs(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if ip := net.ParseIP(value); ip != nil {
			if ip.To4() != nil {
				value = ip.String() + "/32"
			} else {
				value = ip.String() + "/128"
			}
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("无效的监听 IP 网段 %q: %w", value, err)
		}
		canonical := network.String()
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result, nil
}

func bindPersistentFlags(fs *flag.FlagSet, cfg *storedConfig) *cidrListFlag {
	fs.StringVar(&cfg.Database.Type, "db-type", cfg.Database.Type, "数据库类型: sqlite/mysql/mariadb/postgres")
	fs.StringVar(&cfg.Database.Host, "db-host", cfg.Database.Host, "MySQL/PostgreSQL 数据库 IP 地址")
	fs.IntVar(&cfg.Database.Port, "db-port", cfg.Database.Port, "MySQL/PostgreSQL 数据库端口")
	fs.StringVar(&cfg.Database.Name, "db-name", cfg.Database.Name, "MySQL/PostgreSQL 数据库名称")
	fs.StringVar(&cfg.Database.Username, "db-user", cfg.Database.Username, "数据库用户名")
	fs.StringVar(&cfg.Database.Password, "db-password", cfg.Database.Password, "数据库密码")
	fs.StringVar(&cfg.Database.SQLitePath, "sqlite-path", cfg.Database.SQLitePath, "SQLite 数据库文件位置")
	fs.StringVar(&cfg.UploadDir, "upload-dir", cfg.UploadDir, "上传文件系统根目录")
	fs.StringVar(&cfg.Listen.Host, "host", cfg.Listen.Host, "HTTP/HTTPS 服务绑定 IP")
	fs.IntVar(&cfg.Listen.Port, "p", cfg.Listen.Port, "HTTP/HTTPS 服务端口")
	fs.IntVar(&cfg.Listen.Port, "port", cfg.Listen.Port, "HTTP/HTTPS 服务端口")
	cidrs := &cidrListFlag{values: append([]string(nil), cfg.Listen.AllowedCIDRs...)}
	fs.Var(cidrs, "allow-cidr", "允许访问服务的客户端 IP/CIDR，可重复或逗号分隔")
	fs.BoolVar(&cfg.TLS.Enabled, "tls", cfg.TLS.Enabled, "启用 HTTPS/TLS 监听")
	fs.StringVar(&cfg.TLS.CertFile, "tls-cert", cfg.TLS.CertFile, "TLS 证书链文件路径")
	fs.StringVar(&cfg.TLS.KeyFile, "tls-key", cfg.TLS.KeyFile, "TLS 私钥文件路径")
	return cidrs
}

func parsePersistentFlags(name string, args []string, base storedConfig) (storedConfig, map[string]bool, error) {
	previousTLSEnabled := base.TLS.Enabled
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	cidrs := bindPersistentFlags(fs, &base)
	hideFlagDefaults(fs, "db-password")
	if err := fs.Parse(args); err != nil {
		return storedConfig{}, nil, err
	}
	if len(fs.Args()) > 0 {
		return storedConfig{}, nil, fmt.Errorf("无法识别的参数: %s", strings.Join(fs.Args(), " "))
	}
	base.Listen.AllowedCIDRs = cidrs.values
	visited := map[string]bool{}
	fs.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	if visited["db-type"] && !visited["db-port"] {
		base.Database.Port = 0
	}
	adjustDefaultPortForTLS(&base, previousTLSEnabled, visited["p"] || visited["port"])
	normalizeStoredConfig(&base)
	return base, visited, nil
}

func parseRuntimeFlags(args []string, base storedConfig, envDSN, envReadDSN, envWriteDSN string) (runtimeFlags, error) {
	previousTLSEnabled := base.TLS.Enabled
	fs := flag.NewFlagSet("gopherink", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	cidrs := bindPersistentFlags(fs, &base)
	result := runtimeFlags{Settings: base, DBDSN: envDSN, DBReadDSN: envReadDSN, DBWriteDSN: envWriteDSN}
	fs.StringVar(&result.DBDSN, "db-dsn", result.DBDSN, "本次启动使用的主数据库 DSN")
	fs.StringVar(&result.DBReadDSN, "db-read-dsn", result.DBReadDSN, "本次启动使用的只读数据库 DSN")
	fs.StringVar(&result.DBWriteDSN, "db-write-dsn", result.DBWriteDSN, "本次启动使用的写数据库 DSN")
	hideFlagDefaults(fs, "db-password", "db-dsn", "db-read-dsn", "db-write-dsn")
	if err := fs.Parse(args); err != nil {
		return runtimeFlags{}, err
	}
	if len(fs.Args()) > 0 {
		return runtimeFlags{}, fmt.Errorf("无法识别的参数: %s", strings.Join(fs.Args(), " "))
	}
	result.Settings = base
	result.Settings.Listen.AllowedCIDRs = cidrs.values
	result.Visited = map[string]bool{}
	fs.Visit(func(item *flag.Flag) { result.Visited[item.Name] = true })
	if result.Visited["db-type"] && !result.Visited["db-port"] {
		result.Settings.Database.Port = 0
	}
	adjustDefaultPortForTLS(&result.Settings, previousTLSEnabled, result.Visited["p"] || result.Visited["port"])
	normalizeStoredConfig(&result.Settings)
	return result, nil
}

func hideFlagDefaults(fs *flag.FlagSet, names ...string) {
	for _, name := range names {
		if item := fs.Lookup(name); item != nil {
			item.DefValue = ""
		}
	}
}

func loadConfig(args []string, interactive bool) (config, error) {
	path := configPath()
	stored, exists, err := readStoredConfig(path)
	if err != nil {
		return config{}, err
	}
	if !exists && interactive && shouldRunFirstStartWizard(args, stored) {
		fmt.Fprintln(os.Stdout, "首次启动配置向导。以下设置将保存到 "+path+"；站点资料和管理员账户稍后在 Web 安装页中设置。")
		stored, err = promptStoredConfig(bufio.NewReader(os.Stdin), os.Stdout, stored)
		if err != nil {
			return config{}, err
		}
		if err := writeStoredConfig(path, stored); err != nil {
			return config{}, err
		}
		fmt.Fprintln(os.Stdout, "启动配置已保存。")
	}

	if err := applyEnvironment(&stored); err != nil {
		return config{}, err
	}
	envDSN := strings.TrimSpace(os.Getenv("GOPHERINK_DB_DSN"))
	envReadDSN := strings.TrimSpace(os.Getenv("GOPHERINK_DB_READ_DSN"))
	envWriteDSN := strings.TrimSpace(os.Getenv("GOPHERINK_DB_WRITE_DSN"))
	parsed, err := parseRuntimeFlags(args, stored, envDSN, envReadDSN, envWriteDSN)
	if err != nil {
		return config{}, err
	}
	if err := validateStoredConfig(parsed.Settings); err != nil {
		return config{}, err
	}
	parsed.Settings.Listen.AllowedCIDRs, err = normalizeCIDRs(parsed.Settings.Listen.AllowedCIDRs)
	if err != nil {
		return config{}, err
	}

	dsn := parsed.DBDSN
	if dsn == "" || structuredDatabaseFlagVisited(parsed.Visited) {
		dsn, err = databaseDSN(parsed.Settings.Database)
		if err != nil {
			return config{}, err
		}
	}
	if parsed.Visited["db-dsn"] {
		dsn = parsed.DBDSN
	}
	cliMainDatabase := parsed.Visited["db-dsn"] || structuredDatabaseFlagVisited(parsed.Visited)
	if parsed.DBWriteDSN != "" && (!cliMainDatabase || parsed.Visited["db-write-dsn"]) {
		dsn = parsed.DBWriteDSN
	}
	if strings.TrimSpace(dsn) == "" {
		return config{}, errors.New("数据库连接信息不能为空")
	}

	_, adminUserSet := os.LookupEnv("GOPHERINK_ADMIN_USER")
	_, adminPasswordSet := os.LookupEnv("GOPHERINK_ADMIN_PASSWORD")
	_, adminMailSet := os.LookupEnv("GOPHERINK_ADMIN_MAIL")
	return config{
		Addr:          net.JoinHostPort(parsed.Settings.Listen.Host, strconv.Itoa(parsed.Settings.Listen.Port)),
		ListenHost:    parsed.Settings.Listen.Host,
		ListenPort:    parsed.Settings.Listen.Port,
		AllowedCIDRs:  append([]string(nil), parsed.Settings.Listen.AllowedCIDRs...),
		TLSEnabled:    parsed.Settings.TLS.Enabled,
		TLSCertFile:   parsed.Settings.TLS.CertFile,
		TLSKeyFile:    parsed.Settings.TLS.KeyFile,
		DataDir:       dataDir(),
		UploadDir:     parsed.Settings.UploadDir,
		DBDriver:      parsed.Settings.Database.Type,
		DBDSN:         dsn,
		DBReadDSN:     parsed.DBReadDSN,
		DBWriteDSN:    parsed.DBWriteDSN,
		AdminUser:     env("GOPHERINK_ADMIN_USER", "admin"),
		AdminPassword: env("GOPHERINK_ADMIN_PASSWORD", "admin123"),
		AdminMail:     env("GOPHERINK_ADMIN_MAIL", "admin@example.com"),
		WebInstall:    envBool("GOPHERINK_WEB_INSTALL", true),
		AdminExplicit: adminUserSet || adminPasswordSet || adminMailSet,
	}, nil
}

func structuredDatabaseFlagVisited(visited map[string]bool) bool {
	for _, name := range []string{"db-type", "db-host", "db-port", "db-name", "db-user", "db-password", "sqlite-path"} {
		if visited[name] {
			return true
		}
	}
	return false
}

func applyEnvironment(cfg *storedConfig) error {
	previousTLSEnabled := cfg.TLS.Enabled
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_DB_DRIVER")); value != "" {
		cfg.Database.Type = value
	}
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_DB_HOST")); value != "" {
		cfg.Database.Host = value
	}
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_DB_PORT")); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("GOPHERINK_DB_PORT: %w", err)
		}
		cfg.Database.Port = port
	}
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_DB_NAME")); value != "" {
		cfg.Database.Name = value
	}
	if value, ok := os.LookupEnv("GOPHERINK_DB_USER"); ok {
		cfg.Database.Username = value
	}
	if value, ok := os.LookupEnv("GOPHERINK_DB_PASSWORD"); ok {
		cfg.Database.Password = value
	}
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_SQLITE_PATH")); value != "" {
		cfg.Database.SQLitePath = value
	}
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_UPLOAD_DIR")); value != "" {
		cfg.UploadDir = value
	}
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_LISTEN_HOST")); value != "" {
		cfg.Listen.Host = value
	}
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_LISTEN_CIDRS")); value != "" {
		cfg.Listen.AllowedCIDRs = strings.Split(value, ",")
	}
	addressExplicit := false
	if value := strings.TrimSpace(os.Getenv("GOPHERINK_ADDR")); value != "" {
		host, port, err := splitListenAddress(value)
		if err != nil {
			return fmt.Errorf("GOPHERINK_ADDR: %w", err)
		}
		cfg.Listen.Host = host
		cfg.Listen.Port = port
		addressExplicit = true
	}
	if _, ok := os.LookupEnv("GOPHERINK_TLS_ENABLED"); ok {
		cfg.TLS.Enabled = envBool("GOPHERINK_TLS_ENABLED", cfg.TLS.Enabled)
	}
	if value, ok := os.LookupEnv("GOPHERINK_TLS_CERT"); ok {
		cfg.TLS.CertFile = strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv("GOPHERINK_TLS_KEY"); ok {
		cfg.TLS.KeyFile = strings.TrimSpace(value)
	}
	adjustDefaultPortForTLS(cfg, previousTLSEnabled, addressExplicit)
	normalizeStoredConfig(cfg)
	var err error
	cfg.Listen.AllowedCIDRs, err = normalizeCIDRs(cfg.Listen.AllowedCIDRs)
	return err
}

func splitListenAddress(value string) (string, int, error) {
	if strings.HasPrefix(value, ":") {
		value = defaultListenHost + value
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func databaseDSN(cfg databaseSettings) (string, error) {
	switch cfg.Type {
	case "sqlite":
		return cfg.SQLitePath, nil
	case "mysql", "mariadb":
		mysqlConfig := mysql.NewConfig()
		mysqlConfig.User = cfg.Username
		mysqlConfig.Passwd = cfg.Password
		mysqlConfig.Net = "tcp"
		mysqlConfig.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
		mysqlConfig.DBName = cfg.Name
		mysqlConfig.ParseTime = true
		mysqlConfig.Params = map[string]string{"charset": "utf8mb4"}
		return mysqlConfig.FormatDSN(), nil
	case "postgres":
		var user *url.Userinfo
		if cfg.Password != "" {
			user = url.UserPassword(cfg.Username, cfg.Password)
		} else if cfg.Username != "" {
			user = url.User(cfg.Username)
		}
		dsn := &url.URL{
			Scheme: "postgres",
			User:   user,
			Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
			Path:   "/" + cfg.Name,
		}
		query := dsn.Query()
		query.Set("sslmode", "disable")
		dsn.RawQuery = query.Encode()
		return dsn.String(), nil
	default:
		return "", fmt.Errorf("不支持的数据库类型 %q", cfg.Type)
	}
}

func shouldRunFirstStartWizard(args []string, cfg storedConfig) bool {
	if len(args) > 0 || !stdinIsTerminal() || hasStartupEnvironment() {
		return false
	}
	_, err := os.Stat(cfg.Database.SQLitePath)
	return errors.Is(err, os.ErrNotExist)
}

func hasStartupEnvironment() bool {
	for _, key := range []string{
		"GOPHERINK_ADDR", "GOPHERINK_DB_DRIVER", "GOPHERINK_DB_DSN", "GOPHERINK_DB_HOST",
		"GOPHERINK_DB_PORT", "GOPHERINK_DB_NAME", "GOPHERINK_DB_USER", "GOPHERINK_DB_PASSWORD",
		"GOPHERINK_SQLITE_PATH", "GOPHERINK_UPLOAD_DIR", "GOPHERINK_LISTEN_HOST", "GOPHERINK_LISTEN_CIDRS",
		"GOPHERINK_TLS_ENABLED", "GOPHERINK_TLS_CERT", "GOPHERINK_TLS_KEY",
	} {
		if _, ok := os.LookupEnv(key); ok {
			return true
		}
	}
	return false
}

func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func promptStoredConfig(reader *bufio.Reader, out io.Writer, cfg storedConfig) (storedConfig, error) {
	normalizeStoredConfig(&cfg)
	cfg.Database.Type = normalizeDriver(prompt(reader, out, "数据库类型 [sqlite/mysql/mariadb/postgres]", cfg.Database.Type))
	if cfg.Database.Type == "sqlite" {
		cfg.Database.SQLitePath = prompt(reader, out, "SQLite 文件位置", cfg.Database.SQLitePath)
		cfg.Database.Port = 0
	} else {
		cfg.Database.Host = prompt(reader, out, "数据库 IP 地址", cfg.Database.Host)
		defaultPort := cfg.Database.Port
		if cfg.Database.Type == "postgres" && defaultPort == 3306 {
			defaultPort = 5432
		}
		if (cfg.Database.Type == "mysql" || cfg.Database.Type == "mariadb") && defaultPort == 5432 {
			defaultPort = 3306
		}
		if defaultPort == 0 {
			if cfg.Database.Type == "postgres" {
				defaultPort = 5432
			} else {
				defaultPort = 3306
			}
		}
		port, err := promptInt(reader, out, "数据库端口", defaultPort)
		if err != nil {
			return storedConfig{}, err
		}
		cfg.Database.Port = port
		cfg.Database.Name = prompt(reader, out, "数据库名称", cfg.Database.Name)
		cfg.Database.Username = prompt(reader, out, "数据库用户名", cfg.Database.Username)
		passwordLabel := "数据库密码"
		if cfg.Database.Password != "" {
			passwordLabel += "（留空保持现有密码）"
		}
		value, err := promptSecret(out, passwordLabel)
		if err != nil {
			return storedConfig{}, err
		}
		if value != "" || cfg.Database.Password == "" {
			cfg.Database.Password = value
		}
	}
	cfg.UploadDir = prompt(reader, out, "上传目录", cfg.UploadDir)
	cfg.Listen.Host = prompt(reader, out, "HTTP/HTTPS 服务绑定 IP", cfg.Listen.Host)
	previousTLSEnabled := cfg.TLS.Enabled
	tlsEnabled, err := promptBool(reader, out, "启用 HTTPS/TLS", cfg.TLS.Enabled)
	if err != nil {
		return storedConfig{}, err
	}
	cfg.TLS.Enabled = tlsEnabled
	adjustDefaultPortForTLS(&cfg, previousTLSEnabled, false)
	if cfg.TLS.Enabled {
		cfg.TLS.CertFile = prompt(reader, out, "TLS 证书链文件路径", cfg.TLS.CertFile)
		cfg.TLS.KeyFile = prompt(reader, out, "TLS 私钥文件路径", cfg.TLS.KeyFile)
	}
	port, err := promptInt(reader, out, "HTTP/HTTPS 服务端口", cfg.Listen.Port)
	if err != nil {
		return storedConfig{}, err
	}
	cfg.Listen.Port = port
	cidrText := prompt(reader, out, "允许访问的 IP/CIDR（多个使用逗号分隔）", strings.Join(cfg.Listen.AllowedCIDRs, ","))
	cfg.Listen.AllowedCIDRs = strings.Split(cidrText, ",")
	normalizeStoredConfig(&cfg)
	cfg.Listen.AllowedCIDRs, err = normalizeCIDRs(cfg.Listen.AllowedCIDRs)
	if err != nil {
		return storedConfig{}, err
	}
	if err := validateStoredConfig(cfg); err != nil {
		return storedConfig{}, err
	}
	return cfg, nil
}

func prompt(reader *bufio.Reader, out io.Writer, label, fallback string) string {
	if fallback != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback
	}
	return line
}

func promptInt(reader *bufio.Reader, out io.Writer, label string, fallback int) (int, error) {
	value := prompt(reader, out, label, strconv.Itoa(fallback))
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s必须是整数", label)
	}
	return number, nil
}

func promptBool(reader *bufio.Reader, out io.Writer, label string, fallback bool) (bool, error) {
	fallbackText := "no"
	if fallback {
		fallbackText = "yes"
	}
	fmt.Fprintf(out, "%s [yes/no，默认 %s]: ", label, fallbackText)
	line, _ := reader.ReadString('\n')
	value := strings.ToLower(strings.TrimSpace(line))
	if value == "" {
		value = fallbackText
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s必须填写 yes 或 no", label)
	}
}

func promptSecret(out io.Writer, label string) (string, error) {
	if !stdinIsTerminal() {
		return "", errors.New("密码交互输入需要终端")
	}
	fmt.Fprintf(out, "%s: ", label)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(out)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func runConfigCommand(args []string) error {
	path := configPath()
	stored, _, err := readStoredConfig(path)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if !stdinIsTerminal() {
			return errors.New("非交互环境请为 config 子命令提供需要保存的参数")
		}
		stored, err = promptStoredConfig(bufio.NewReader(os.Stdin), os.Stdout, stored)
	} else {
		stored, _, err = parsePersistentFlags("gopherink config", args, stored)
		if err == nil {
			err = validateStoredConfig(stored)
		}
	}
	if err != nil {
		return err
	}
	if err := writeStoredConfig(path, stored); err != nil {
		return err
	}
	fmt.Printf("启动配置已保存到 %s；服务未启动。\n", path)
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
