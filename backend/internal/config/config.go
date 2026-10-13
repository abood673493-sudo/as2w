// Package config loads and validates the panel's runtime configuration from
// environment variables.
//
// It has no external dependencies so that configuration can be unit tested
// without a database or a running server. Every value is validated here, once,
// so the rest of the application can trust the Config struct.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Recognised values for APP_ENV.
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Defaults are declared once so that .env.example, the docs and the tests stay
// in sync with the code.
const (
	defaultHTTPAddr       = "127.0.0.1:8080"
	defaultLogLevel       = "info"
	defaultCookieName     = "sshmp_session"
	defaultCSRFHeader     = "X-CSRF-Token"
	defaultSSHDPath       = "/etc/ssh/sshd_config"
	defaultSSHService     = "ssh"
	defaultAccountShell   = "/bin/bash"
	defaultHomePrefix     = "/home"
	defaultAccountGroup   = "sshmp-users"
	defaultStateDir       = "/var/lib/ssh-manager-panel"
	defaultPAMBinary      = "/usr/local/bin/sshmp-pam-check"
	defaultWSPath         = "/ssh"
	defaultWSListenAddr   = "127.0.0.1:8081"
	defaultWSUpstreamAddr = "127.0.0.1:22"
	defaultACMEDirectory  = "https://acme-v02.api.letsencrypt.org/directory"
	defaultACMEStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
	defaultACMEWebRoot    = "/var/www/acme"
	defaultProcRoot       = "/proc"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	App       App
	Database  Database
	Session   Session
	Security  Security
	Bootstrap Bootstrap
	SSH       SSH
	Gateway   Gateway
	TLS       TLS
	Metrics   Metrics
	Notify    Notify
}

// App holds process level settings.
type App struct {
	Env             string
	HTTPAddr        string
	PublicBaseURL   string
	LogLevel        string
	ShutdownTimeout time.Duration
	TrustedProxies  []string
}

// Database holds PostgreSQL connection settings.
type Database struct {
	URL            string
	MaxConns       int32
	MinConns       int32
	ConnTimeout    time.Duration
	MigrateOnStart bool
}

// Session holds dashboard session/cookie settings.
type Session struct {
	CookieName     string
	TTL            time.Duration
	IdleTimeout    time.Duration
	CookieSecure   bool
	CookieDomain   string
	CookieSameSite string
	CSRFHeader     string
}

// Security holds brute-force protection settings.
type Security struct {
	LoginMaxAttempts int
	LoginWindow      time.Duration
	LoginLockout     time.Duration
}

// Bootstrap holds the optional first-administrator credentials.
type Bootstrap struct {
	Username string
	Password string
}

// SSH holds Linux/OpenSSH integration settings.
type SSH struct {
	// Enabled mirrors SYSTEM_ENABLE_NATIVE. When false the panel still works,
	// but every operation that would touch the host is refused with a clear
	// error instead of being executed.
	Enabled           bool
	Host              string
	Port              int
	SSHDConfigPath    string
	ServiceName       string
	AccountShell      string
	HomePrefix        string
	AccountGroup      string
	UIDMin            int
	UIDMax            int
	StateDir          string
	BackupDir         string
	PAMCheckBinary    string
	ManagedBlockBegin string
	ManagedBlockEnd   string
}

// Gateway holds SSH-over-WebSocket settings.
type Gateway struct {
	Enabled          bool
	ListenAddr       string
	Path             string
	UpstreamSSHAddr  string
	MaxConnections   int
	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
	ReadBufferSize   int
	WriteBufferSize  int
	AllowedOrigins   []string
	TrustProxy       bool
}

// TLS holds ACME / reverse proxy settings.
type TLS struct {
	ACMEEmail        string
	DirectoryURL     string
	StagingDirectory string
	WebRoot          string
	RenewBefore      time.Duration
	CaddyAdminAPI    string
}

// Metrics holds accounting and sampling settings.
type Metrics struct {
	SampleInterval       time.Duration
	ProcRoot             string
	TCPAccountingEnabled bool
	RetentionDays        int
}

// Notify holds outbound notification settings.
type Notify struct {
	WebhookURL string
}

// IsProduction reports whether the panel runs in production mode.
func (c *Config) IsProduction() bool { return c.App.Env == EnvProduction }

// Load reads, parses and validates the configuration from the environment.
func Load() (*Config, error) {
	cfg := &Config{}
	var err error

	// ---- App ------------------------------------------------------------
	cfg.App.Env = strings.ToLower(getString("APP_ENV", EnvProduction))
	switch cfg.App.Env {
	case EnvDevelopment, EnvProduction:
	default:
		return nil, fmt.Errorf("APP_ENV must be %q or %q, got %q", EnvDevelopment, EnvProduction, cfg.App.Env)
	}

	cfg.App.HTTPAddr = getString("APP_HTTP_ADDR", defaultHTTPAddr)
	if _, _, err := net.SplitHostPort(cfg.App.HTTPAddr); err != nil {
		return nil, fmt.Errorf("APP_HTTP_ADDR %q is not host:port: %w", cfg.App.HTTPAddr, err)
	}

	cfg.App.PublicBaseURL = strings.TrimRight(getString("APP_PUBLIC_BASE_URL", ""), "/")
	if cfg.App.PublicBaseURL != "" {
		if err := validateHTTPURL("APP_PUBLIC_BASE_URL", cfg.App.PublicBaseURL); err != nil {
			return nil, err
		}
	}

	cfg.App.LogLevel = strings.ToLower(getString("APP_LOG_LEVEL", defaultLogLevel))
	switch cfg.App.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return nil, fmt.Errorf("APP_LOG_LEVEL must be one of debug|info|warn|error, got %q", cfg.App.LogLevel)
	}

	if cfg.App.ShutdownTimeout, err = getDuration("APP_SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return nil, err
	}
	if cfg.App.ShutdownTimeout <= 0 {
		return nil, errors.New("APP_SHUTDOWN_TIMEOUT must be positive")
	}

	cfg.App.TrustedProxies = getCSV("TRUSTED_PROXY_CIDRS", []string{"127.0.0.1/32", "::1/128"})
	for _, cidr := range cfg.App.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: %q is not a valid CIDR", cidr)
		}
	}

	// ---- Database -------------------------------------------------------
	cfg.Database.URL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if cfg.Database.URL == "" {
		return nil, errors.New("DATABASE_URL is required (see .env.example)")
	}
	if _, err := url.Parse(cfg.Database.URL); err != nil {
		return nil, fmt.Errorf("DATABASE_URL is not a valid URL: %w", err)
	}

	maxConns, err := getInt("DB_MAX_CONNS", 10)
	if err != nil {
		return nil, err
	}
	minConns, err := getInt("DB_MIN_CONNS", 1)
	if err != nil {
		return nil, err
	}
	if maxConns < 1 {
		return nil, errors.New("DB_MAX_CONNS must be at least 1")
	}
	if minConns < 0 || minConns > maxConns {
		return nil, fmt.Errorf("DB_MIN_CONNS must be between 0 and DB_MAX_CONNS (%d)", maxConns)
	}
	cfg.Database.MaxConns = int32(maxConns)
	cfg.Database.MinConns = int32(minConns)

	if cfg.Database.ConnTimeout, err = getDuration("DB_CONN_TIMEOUT", 5*time.Second); err != nil {
		return nil, err
	}
	if cfg.Database.ConnTimeout <= 0 {
		return nil, errors.New("DB_CONN_TIMEOUT must be positive")
	}
	cfg.Database.MigrateOnStart = getBool("DB_MIGRATE_ON_START", true)

	// ---- Session --------------------------------------------------------
	cfg.Session.CookieName = getString("SESSION_COOKIE_NAME", defaultCookieName)
	if cfg.Session.TTL, err = getDuration("SESSION_TTL", 7*24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.Session.IdleTimeout, err = getDuration("SESSION_IDLE_TIMEOUT", 24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.Session.TTL <= 0 || cfg.Session.IdleTimeout <= 0 {
		return nil, errors.New("SESSION_TTL and SESSION_IDLE_TIMEOUT must be positive")
	}
	if cfg.Session.IdleTimeout > cfg.Session.TTL {
		return nil, errors.New("SESSION_IDLE_TIMEOUT must not exceed SESSION_TTL")
	}
	cfg.Session.CookieDomain = getString("SESSION_COOKIE_DOMAIN", "")
	// Secure cookies default to true in production and false in development so
	// that plain-HTTP localhost workflows keep working.
	cfg.Session.CookieSecure = getBool("SESSION_COOKIE_SECURE", cfg.IsProduction())
	cfg.Session.CookieSameSite = strings.ToLower(getString("SESSION_COOKIE_SAMESITE", "lax"))
	switch cfg.Session.CookieSameSite {
	case "lax", "strict", "none":
	default:
		return nil, fmt.Errorf("SESSION_COOKIE_SAMESITE must be lax|strict|none, got %q", cfg.Session.CookieSameSite)
	}
	if cfg.Session.CookieSameSite == "none" && !cfg.Session.CookieSecure {
		return nil, errors.New("SESSION_COOKIE_SAMESITE=none requires SESSION_COOKIE_SECURE=true")
	}
	cfg.Session.CSRFHeader = getString("CSRF_HEADER_NAME", defaultCSRFHeader)

	// ---- Security -------------------------------------------------------
	if cfg.Security.LoginMaxAttempts, err = getInt("LOGIN_RATE_LIMIT_ATTEMPTS", 5); err != nil {
		return nil, err
	}
	if cfg.Security.LoginMaxAttempts < 1 {
		return nil, errors.New("LOGIN_RATE_LIMIT_ATTEMPTS must be at least 1")
	}
	if cfg.Security.LoginWindow, err = getDuration("LOGIN_RATE_LIMIT_WINDOW", 15*time.Minute); err != nil {
		return nil, err
	}
	if cfg.Security.LoginLockout, err = getDuration("LOGIN_RATE_LIMIT_LOCKOUT", 15*time.Minute); err != nil {
		return nil, err
	}
	if cfg.Security.LoginWindow <= 0 || cfg.Security.LoginLockout <= 0 {
		return nil, errors.New("login rate limit window/lockout must be positive")
	}

	// ---- Bootstrap ------------------------------------------------------
	cfg.Bootstrap.Username = strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_USERNAME"))
	cfg.Bootstrap.Password = os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")

	// ---- SSH ------------------------------------------------------------
	cfg.SSH.Enabled = getBool("SYSTEM_ENABLE_NATIVE", false)
	cfg.SSH.Host = strings.TrimSpace(os.Getenv("SSH_HOST"))
	cfg.SSH.Port, err = getInt("SSH_PORT", 22)
	if err != nil {
		return nil, err
	}
	if cfg.SSH.Port < 1 || cfg.SSH.Port > 65535 {
		return nil, fmt.Errorf("SSH_PORT must be within 1-65535, got %d", cfg.SSH.Port)
	}
	cfg.SSH.SSHDConfigPath = getString("SSHD_CONFIG_PATH", defaultSSHDPath)
	cfg.SSH.ServiceName = getString("SSHD_SERVICE_NAME", defaultSSHService)
	cfg.SSH.AccountShell = getString("SSH_ACCOUNT_SHELL", defaultAccountShell)
	if !strings.HasPrefix(cfg.SSH.AccountShell, "/") {
		return nil, fmt.Errorf("SSH_ACCOUNT_SHELL must be an absolute path, got %q", cfg.SSH.AccountShell)
	}
	cfg.SSH.HomePrefix = strings.TrimRight(getString("SSH_ACCOUNT_HOME_PREFIX", defaultHomePrefix), "/")
	if !strings.HasPrefix(cfg.SSH.HomePrefix, "/") {
		return nil, fmt.Errorf("SSH_ACCOUNT_HOME_PREFIX must be an absolute path, got %q", cfg.SSH.HomePrefix)
	}
	cfg.SSH.AccountGroup = getString("SSH_ACCOUNT_GROUP", defaultAccountGroup)
	if cfg.SSH.AccountGroup == "root" {
		return nil, errors.New("SSH_ACCOUNT_GROUP must not be root")
	}
	if cfg.SSH.UIDMin, err = getInt("SSH_ACCOUNT_UID_MIN", 2000); err != nil {
		return nil, err
	}
	if cfg.SSH.UIDMax, err = getInt("SSH_ACCOUNT_UID_MAX", 59000); err != nil {
		return nil, err
	}
	if cfg.SSH.UIDMin < 1000 || cfg.SSH.UIDMax > 60000 || cfg.SSH.UIDMin >= cfg.SSH.UIDMax {
		return nil, fmt.Errorf("SSH_ACCOUNT_UID_MIN/UID_MAX must satisfy 1000 <= min < max <= 60000 (got %d/%d)", cfg.SSH.UIDMin, cfg.SSH.UIDMax)
	}
	cfg.SSH.StateDir = strings.TrimRight(getString("SSH_STATE_DIR", defaultStateDir), "/")
	cfg.SSH.BackupDir = strings.TrimRight(getString("SSH_BACKUP_DIR", cfg.SSH.StateDir+"/backups"), "/")
	cfg.SSH.PAMCheckBinary = getString("SSH_PAM_CHECK_BINARY", defaultPAMBinary)
	cfg.SSH.ManagedBlockBegin = getString("SSHD_MANAGED_BLOCK_BEGIN", "# >>> ssh-manager-panel >>>")
	cfg.SSH.ManagedBlockEnd = getString("SSHD_MANAGED_BLOCK_END", "# <<< ssh-manager-panel <<<")

	// ---- WebSocket gateway ---------------------------------------------
	cfg.Gateway.Enabled = getBool("WS_ENABLED", true)
	cfg.Gateway.ListenAddr = getString("WS_LISTEN_ADDR", defaultWSListenAddr)
	if _, _, err := net.SplitHostPort(cfg.Gateway.ListenAddr); err != nil {
		return nil, fmt.Errorf("WS_LISTEN_ADDR %q is not host:port: %w", cfg.Gateway.ListenAddr, err)
	}
	cfg.Gateway.Path = getString("WS_PATH", defaultWSPath)
	if !strings.HasPrefix(cfg.Gateway.Path, "/") || strings.ContainsAny(cfg.Gateway.Path, " \t") {
		return nil, fmt.Errorf("WS_PATH must start with '/' and contain no whitespace, got %q", cfg.Gateway.Path)
	}
	cfg.Gateway.UpstreamSSHAddr = getString("WS_UPSTREAM_SSH_ADDR", defaultWSUpstreamAddr)
	if _, _, err := net.SplitHostPort(cfg.Gateway.UpstreamSSHAddr); err != nil {
		return nil, fmt.Errorf("WS_UPSTREAM_SSH_ADDR %q is not host:port: %w", cfg.Gateway.UpstreamSSHAddr, err)
	}
	if cfg.Gateway.MaxConnections, err = getInt("WS_MAX_CONNECTIONS", 0); err != nil {
		return nil, err
	}
	if cfg.Gateway.MaxConnections < 0 {
		return nil, errors.New("WS_MAX_CONNECTIONS must be >= 0 (0 means unlimited)")
	}
	if cfg.Gateway.HandshakeTimeout, err = getDuration("WS_HANDSHAKE_TIMEOUT", 10*time.Second); err != nil {
		return nil, err
	}
	if cfg.Gateway.IdleTimeout, err = getDuration("WS_IDLE_TIMEOUT", 5*time.Minute); err != nil {
		return nil, err
	}
	if cfg.Gateway.HandshakeTimeout <= 0 || cfg.Gateway.IdleTimeout <= 0 {
		return nil, errors.New("WS_HANDSHAKE_TIMEOUT and WS_IDLE_TIMEOUT must be positive")
	}
	if cfg.Gateway.ReadBufferSize, err = getInt("WS_READ_BUFFER_SIZE", 32768); err != nil {
		return nil, err
	}
	if cfg.Gateway.WriteBufferSize, err = getInt("WS_WRITE_BUFFER_SIZE", 32768); err != nil {
		return nil, err
	}
	if cfg.Gateway.ReadBufferSize < 1024 || cfg.Gateway.WriteBufferSize < 1024 {
		return nil, errors.New("WS read/write buffer sizes must be at least 1024 bytes")
	}
	cfg.Gateway.AllowedOrigins = getCSV("WS_ALLOWED_ORIGINS", nil)
	cfg.Gateway.TrustProxy = getBool("WS_TRUST_PROXY", true)

	// ---- TLS / ACME -----------------------------------------------------
	cfg.TLS.ACMEEmail = strings.TrimSpace(os.Getenv("ACME_EMAIL"))
	if cfg.TLS.ACMEEmail != "" && !strings.Contains(cfg.TLS.ACMEEmail, "@") {
		return nil, fmt.Errorf("ACME_EMAIL %q does not look like an email address", cfg.TLS.ACMEEmail)
	}
	cfg.TLS.DirectoryURL = getString("ACME_DIRECTORY_URL", defaultACMEDirectory)
	cfg.TLS.StagingDirectory = getString("ACME_STAGING_DIRECTORY_URL", defaultACMEStaging)
	for name, value := range map[string]string{
		"ACME_DIRECTORY_URL":         cfg.TLS.DirectoryURL,
		"ACME_STAGING_DIRECTORY_URL": cfg.TLS.StagingDirectory,
	} {
		if err := validateHTTPURL(name, value); err != nil {
			return nil, err
		}
	}
	cfg.TLS.WebRoot = getString("ACME_WEBROOT", defaultACMEWebRoot)
	if cfg.TLS.RenewBefore, err = getDuration("CERT_RENEW_BEFORE", 30*24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.TLS.RenewBefore <= 0 {
		return nil, errors.New("CERT_RENEW_BEFORE must be positive")
	}
	cfg.TLS.CaddyAdminAPI = strings.TrimRight(getString("CADDY_ADMIN_API", ""), "/")
	if cfg.TLS.CaddyAdminAPI != "" {
		if err := validateHTTPURL("CADDY_ADMIN_API", cfg.TLS.CaddyAdminAPI); err != nil {
			return nil, err
		}
	}

	// ---- Metrics --------------------------------------------------------
	if cfg.Metrics.SampleInterval, err = getDuration("METRICS_SAMPLE_INTERVAL", 15*time.Second); err != nil {
		return nil, err
	}
	if cfg.Metrics.SampleInterval < time.Second {
		return nil, errors.New("METRICS_SAMPLE_INTERVAL must be at least 1s")
	}
	cfg.Metrics.ProcRoot = strings.TrimRight(getString("PROC_ROOT", defaultProcRoot), "/")
	cfg.Metrics.TCPAccountingEnabled = getBool("SSH_TCP_ACCOUNTING_ENABLED", false)
	if cfg.Metrics.RetentionDays, err = getInt("ACCOUNTING_RETENTION_DAYS", 90); err != nil {
		return nil, err
	}
	if cfg.Metrics.RetentionDays < 1 {
		return nil, errors.New("ACCOUNTING_RETENTION_DAYS must be at least 1")
	}

	// ---- Notifications --------------------------------------------------
	cfg.Notify.WebhookURL = strings.TrimSpace(os.Getenv("NOTIFICATIONS_WEBHOOK_URL"))
	if cfg.Notify.WebhookURL != "" {
		if err := validateHTTPURL("NOTIFICATIONS_WEBHOOK_URL", cfg.Notify.WebhookURL); err != nil {
			return nil, err
		}
	}

	// Cross-field safety checks.
	if cfg.IsProduction() {
		if !cfg.Session.CookieSecure {
			return nil, errors.New("SESSION_COOKIE_SECURE must be true when APP_ENV=production")
		}
	}

	return cfg, nil
}

// Redacted returns a map suitable for debug logging: secrets are replaced with
// a fixed marker so that configuration dumps never leak credentials.
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"env":                 c.App.Env,
		"http_addr":           c.App.HTTPAddr,
		"public_base_url":     c.App.PublicBaseURL,
		"log_level":           c.App.LogLevel,
		"trusted_proxies":     c.App.TrustedProxies,
		"database_url":        redactURL(c.Database.URL),
		"db_max_conns":        c.Database.MaxConns,
		"db_migrate_on_start": c.Database.MigrateOnStart,
		"cookie_name":         c.Session.CookieName,
		"cookie_secure":       c.Session.CookieSecure,
		"system_native":       c.SSH.Enabled,
		"ws_enabled":          c.Gateway.Enabled,
		"ws_listen_addr":      c.Gateway.ListenAddr,
		"ws_path":             c.Gateway.Path,
		"tls_acme_email":      redactEmail(c.TLS.ACMEEmail),
	}
}

// ---- helpers ---------------------------------------------------------------

func getString(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, raw)
	}
	return v, nil
}

func getBool(key string, def bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return def
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

func getDuration(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration (e.g. 15m), got %q", key, raw)
	}
	return v, nil
}

func getCSV(key string, def []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func validateHTTPURL(name, value string) error {
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", name, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s must use http or https, got %q", name, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%s must include a host", name)
	}
	return nil
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "REDACTED")
		}
	}
	return u.String()
}

func redactEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "REDACTED"
	}
	return email[:1] + "***" + email[at:]
}

// EnvVarNames lists every environment variable the panel reads. Used by the
// `config` CLI command and by the documentation tests.
func EnvVarNames() []string {
	return []string{
		"APP_ENV", "APP_HTTP_ADDR", "APP_PUBLIC_BASE_URL", "APP_LOG_LEVEL",
		"APP_SHUTDOWN_TIMEOUT", "TRUSTED_PROXY_CIDRS",
		"DATABASE_URL", "DB_MAX_CONNS", "DB_MIN_CONNS", "DB_CONN_TIMEOUT", "DB_MIGRATE_ON_START",
		"SESSION_COOKIE_NAME", "SESSION_TTL", "SESSION_IDLE_TIMEOUT", "SESSION_COOKIE_SECURE",
		"SESSION_COOKIE_DOMAIN", "SESSION_COOKIE_SAMESITE", "CSRF_HEADER_NAME",
		"LOGIN_RATE_LIMIT_ATTEMPTS", "LOGIN_RATE_LIMIT_WINDOW", "LOGIN_RATE_LIMIT_LOCKOUT",
		"BOOTSTRAP_ADMIN_USERNAME", "BOOTSTRAP_ADMIN_PASSWORD",
		"SYSTEM_ENABLE_NATIVE", "SSH_HOST", "SSH_PORT", "SSHD_CONFIG_PATH", "SSHD_SERVICE_NAME",
		"SSH_ACCOUNT_SHELL", "SSH_ACCOUNT_HOME_PREFIX", "SSH_ACCOUNT_GROUP",
		"SSH_ACCOUNT_UID_MIN", "SSH_ACCOUNT_UID_MAX", "SSH_STATE_DIR", "SSH_BACKUP_DIR",
		"SSH_PAM_CHECK_BINARY", "SSHD_MANAGED_BLOCK_BEGIN", "SSHD_MANAGED_BLOCK_END",
		"WS_ENABLED", "WS_LISTEN_ADDR", "WS_PATH", "WS_UPSTREAM_SSH_ADDR", "WS_MAX_CONNECTIONS",
		"WS_HANDSHAKE_TIMEOUT", "WS_IDLE_TIMEOUT", "WS_READ_BUFFER_SIZE", "WS_WRITE_BUFFER_SIZE",
		"WS_ALLOWED_ORIGINS", "WS_TRUST_PROXY",
		"ACME_EMAIL", "ACME_DIRECTORY_URL", "ACME_STAGING_DIRECTORY_URL", "ACME_WEBROOT",
		"CERT_RENEW_BEFORE", "CADDY_ADMIN_API",
		"METRICS_SAMPLE_INTERVAL", "PROC_ROOT", "SSH_TCP_ACCOUNTING_ENABLED", "ACCOUNTING_RETENTION_DAYS",
		"NOTIFICATIONS_WEBHOOK_URL",
	}
}