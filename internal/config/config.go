package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const Timezone = "Asia/Shanghai"

// Config contains boot settings only. Business settings belong to platform.
// There is intentionally no database-driver or configurable-timezone setting.
type Config struct {
	HTTP     HTTP     `yaml:"http"`
	Database Database `yaml:"database"`
	Redis    Redis    `yaml:"redis"`
	Worker   Worker   `yaml:"worker"`
	Log      Log      `yaml:"log"`
	Auth     Auth     `yaml:"auth"`
	Secrets  Secrets  `yaml:"secrets"`
	Cloud    Cloud    `yaml:"cloud"`
}
type Cloud struct {
	WritesEnabled bool `yaml:"writes_enabled"`
}
type Secrets struct {
	KeyringFile string `yaml:"keyring_file"`
}

type HTTP struct {
	Address         string        `yaml:"address"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	TrustedProxies  []string      `yaml:"trusted_proxies"`
}

type Auth struct {
	PublicURL         string        `yaml:"public_url"`
	SessionTTL        time.Duration `yaml:"session_ttl"`
	LoginWindow       time.Duration `yaml:"login_window"`
	LoginIPLimit      int           `yaml:"login_ip_limit"`
	LoginAccountLimit int           `yaml:"login_account_limit"`
}

type Database struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	Name         string `yaml:"name"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
	SSLMode      string `yaml:"ssl_mode"`
	MaxOpen      int    `yaml:"max_open"`
	MaxIdle      int    `yaml:"max_idle"`
}

type Redis struct {
	Address      string `yaml:"address"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
	DB           int    `yaml:"db"`
}

type Worker struct {
	Concurrency       int           `yaml:"concurrency"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	Diagnostics       bool          `yaml:"diagnostics"`
}

type Log struct {
	Level string `yaml:"level"`
}

func Defaults() Config {
	return Config{
		HTTP:     HTTP{Address: "127.0.0.1:8080", ShutdownTimeout: 10 * time.Second},
		Database: Database{Host: "127.0.0.1", Port: 5432, Name: "aws_panel", User: "aws_panel", SSLMode: "disable", MaxOpen: 12, MaxIdle: 4},
		Redis:    Redis{Address: "127.0.0.1:6379"},
		Worker:   Worker{Concurrency: 4, ShutdownTimeout: 15 * time.Second, HeartbeatInterval: time.Minute},
		Log:      Log{Level: "info"},
		Auth:     Auth{PublicURL: "http://127.0.0.1:8080", SessionTTL: 12 * time.Hour, LoginWindow: 15 * time.Minute, LoginIPLimit: 60, LoginAccountLimit: 10},
	}
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return Config{}, errors.New("cannot open configuration file")
		}
		defer file.Close()
		decoder := yaml.NewDecoder(io.LimitReader(file, 1<<20))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, errors.New("invalid configuration: check YAML fields and value types")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return Config{}, errors.New("configuration must contain exactly one YAML document")
		}
	}
	override := func(key string, target *string) {
		if value, ok := os.LookupEnv(key); ok {
			*target = value
		}
	}
	override("AWS_PANEL_HTTP_ADDRESS", &cfg.HTTP.Address)
	override("AWS_PANEL_PUBLIC_URL", &cfg.Auth.PublicURL)
	override("AWS_PANEL_KEYRING_FILE", &cfg.Secrets.KeyringFile)
	if value, ok := os.LookupEnv("AWS_PANEL_CLOUD_WRITES_ENABLED"); ok {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, errors.New("AWS_PANEL_CLOUD_WRITES_ENABLED must be a boolean")
		}
		cfg.Cloud.WritesEnabled = enabled
	}
	if value, ok := os.LookupEnv("AWS_PANEL_DIAGNOSTICS"); ok {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, errors.New("AWS_PANEL_DIAGNOSTICS must be a boolean")
		}
		cfg.Worker.Diagnostics = enabled
	}
	if value, ok := os.LookupEnv("AWS_PANEL_TRUSTED_PROXIES"); ok {
		cfg.HTTP.TrustedProxies = nil
		if strings.TrimSpace(value) != "" {
			for _, part := range strings.Split(value, ",") {
				cfg.HTTP.TrustedProxies = append(cfg.HTTP.TrustedProxies, strings.TrimSpace(part))
			}
		}
	}
	override("AWS_PANEL_DB_HOST", &cfg.Database.Host)
	override("AWS_PANEL_DB_NAME", &cfg.Database.Name)
	override("AWS_PANEL_DB_USER", &cfg.Database.User)
	override("AWS_PANEL_DB_PASSWORD", &cfg.Database.Password)
	override("AWS_PANEL_DB_PASSWORD_FILE", &cfg.Database.PasswordFile)
	override("AWS_PANEL_DB_SSL_MODE", &cfg.Database.SSLMode)
	override("AWS_PANEL_REDIS_ADDRESS", &cfg.Redis.Address)
	override("AWS_PANEL_REDIS_PASSWORD", &cfg.Redis.Password)
	override("AWS_PANEL_REDIS_PASSWORD_FILE", &cfg.Redis.PasswordFile)
	if value, ok := os.LookupEnv("AWS_PANEL_DB_PORT"); ok {
		port, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, errors.New("AWS_PANEL_DB_PORT must be an integer")
		}
		cfg.Database.Port = port
	}
	var err error
	cfg.Database.Password, err = readPassword(cfg.Database.Password, cfg.Database.PasswordFile)
	if err != nil {
		return Config{}, fmt.Errorf("database: %w", err)
	}
	cfg.Redis.Password, err = readPassword(cfg.Redis.Password, cfg.Redis.PasswordFile)
	if err != nil {
		return Config{}, fmt.Errorf("redis: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func readPassword(value, path string) (string, error) {
	if path == "" {
		return value, nil
	}
	if value != "" {
		return "", errors.New("set either password or password_file, not both")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read password_file")
	}
	if len(data) > 4096 {
		return "", errors.New("password_file is too large")
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r"), nil
}

func (c Config) Validate() error {
	origin, err := url.Parse(c.Auth.PublicURL)
	if err != nil || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return errors.New("auth.public_url must be an HTTP(S) origin without credentials, path or query")
	}
	if origin.Scheme != "https" {
		ip, parseErr := netip.ParseAddr(origin.Hostname())
		if origin.Scheme != "http" || (origin.Hostname() != "localhost" && (parseErr != nil || !ip.IsLoopback())) {
			return errors.New("auth.public_url requires HTTPS; HTTP is only permitted on loopback hosts")
		}
	}
	if c.Auth.SessionTTL < 5*time.Minute || c.Auth.SessionTTL > 7*24*time.Hour ||
		c.Auth.LoginWindow < time.Minute || c.Auth.LoginWindow > 24*time.Hour ||
		c.Auth.LoginIPLimit < 1 || c.Auth.LoginAccountLimit < 1 {
		return errors.New("invalid authentication limits or session lifetime")
	}
	for _, value := range c.HTTP.TrustedProxies {
		if _, err := netip.ParsePrefix(value); err != nil {
			if _, err := netip.ParseAddr(value); err != nil {
				return errors.New("http.trusted_proxies must contain IP addresses or CIDRs")
			}
		}
	}
	if _, _, err := net.SplitHostPort(c.HTTP.Address); err != nil {
		return errors.New("http.address must contain host and port")
	}
	if _, _, err := net.SplitHostPort(c.Redis.Address); err != nil {
		return errors.New("redis.address must contain host and port")
	}
	if c.Database.Host == "" || c.Database.Name == "" || c.Database.User == "" || c.Database.Port < 1 || c.Database.Port > 65535 {
		return errors.New("database host, name, user and valid port are required")
	}
	switch c.Database.SSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return errors.New("unsupported PostgreSQL ssl_mode")
	}
	if c.Database.MaxOpen < 2 || c.Database.MaxIdle < 0 || c.Database.MaxIdle > c.Database.MaxOpen {
		return errors.New("invalid database connection pool size")
	}
	if c.Redis.DB < 0 || c.Redis.DB > 15 {
		return errors.New("redis.db must be between 0 and 15")
	}
	if c.Worker.Concurrency < 1 || c.Worker.Concurrency >= c.Database.MaxOpen {
		return errors.New("worker.concurrency must be positive and less than database.max_open")
	}
	if c.HTTP.ShutdownTimeout <= 0 || c.Worker.ShutdownTimeout <= 0 || c.Worker.HeartbeatInterval < time.Second {
		return errors.New("shutdown timeouts must be positive; heartbeat interval must be at least one second")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("unsupported log level")
	}
	return nil
}

// DSN sets TimeZone in the startup parameters of every pooled connection.
// Passwords are URL-escaped and the resulting string must never be logged.
func (c Database) DSN(scheme string) string {
	u := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:   "/" + c.Name,
		User:   url.UserPassword(c.User, c.Password),
	}
	query := url.Values{
		"sslmode":          {c.SSLMode},
		"TimeZone":         {Timezone},
		"search_path":      {"public"},
		"application_name": {"aws-panel"},
		"connect_timeout":  {"5"},
	}
	u.RawQuery = query.Encode()
	return u.String()
}
