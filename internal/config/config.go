package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const (
	envPrefix    = "TENZO_"
	envDelimiter = "__"
)

type Config struct {
	Env      string   `koanf:"env"`
	Log      Log      `koanf:"log"`
	HTTP     HTTP     `koanf:"http"`
	Database Database `koanf:"database"`
	Limits   Limits   `koanf:"limits"`
	Telegram Telegram `koanf:"telegram"`
}

type Log struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

type HTTP struct {
	Addr              string        `koanf:"addr"`
	PublicURL         string        `koanf:"public_url"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout"`
	ReadTimeout       time.Duration `koanf:"read_timeout"`
	WriteTimeout      time.Duration `koanf:"write_timeout"`
	IdleTimeout       time.Duration `koanf:"idle_timeout"`
	ShutdownTimeout   time.Duration `koanf:"shutdown_timeout"`
	AllowedOrigins    []string      `koanf:"allowed_origins"`
	TrustedProxies    []string      `koanf:"trusted_proxies"`
	Docs              bool          `koanf:"docs"`
}

type Database struct {
	URL               string        `koanf:"url"`
	MaxConns          int32         `koanf:"max_conns"`
	MinConns          int32         `koanf:"min_conns"`
	MaxConnLifetime   time.Duration `koanf:"max_conn_lifetime"`
	MaxConnIdleTime   time.Duration `koanf:"max_conn_idle_time"`
	HealthCheckPeriod time.Duration `koanf:"health_check_period"`
	ConnectTimeout    time.Duration `koanf:"connect_timeout"`
	StatementTimeout  time.Duration `koanf:"statement_timeout"`
	MigrateOnStart    bool          `koanf:"migrate_on_start"`
}

type Limits struct {
	LeadsPerIP Rate `koanf:"leads_per_ip"`
}

type Rate struct {
	Events int           `koanf:"events"`
	Per    time.Duration `koanf:"per"`
}

type Telegram struct {
	APIURL   string `koanf:"api_url"`
	BotToken string `koanf:"bot_token"`
	ChatID   string `koanf:"chat_id"`
}

func Default() Config {
	return Config{
		Env: "production",
		Log: Log{Level: "info", Format: "json"},
		HTTP: HTTP{
			Addr:              "127.0.0.1:8170",
			PublicURL:         "http://localhost:8170",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
			ShutdownTimeout:   20 * time.Second,
			TrustedProxies:    []string{"127.0.0.1/32"},
		},
		Database: Database{
			MaxConns:          10,
			MinConns:          1,
			MaxConnLifetime:   time.Hour,
			MaxConnIdleTime:   15 * time.Minute,
			HealthCheckPeriod: 30 * time.Second,
			ConnectTimeout:    5 * time.Second,
			StatementTimeout:  15 * time.Second,
		},
		Limits: Limits{
			LeadsPerIP: Rate{Events: 5, Per: 10 * time.Minute},
		},
		Telegram: Telegram{
			APIURL: "https://api.telegram.org",
		},
	}
}

func Load(path string, required bool) (Config, error) {
	k := koanf.New(".")

	if path != "" {
		if err := loadFile(k, path, required); err != nil {
			return Config{}, err
		}
	}

	if err := k.Load(env.Provider(".", env.Opt{Prefix: envPrefix, TransformFunc: envKey}), nil); err != nil {
		return Config{}, fmt.Errorf("load env: %w", err)
	}

	cfg := Default()
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{DecoderConfig: decoderConfig()}); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	cfg.normalize()

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

func loadFile(k *koanf.Koanf, path string, required bool) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil
		}
		return fmt.Errorf("config file: %w", err)
	}

	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}

	return nil
}

func envKey(key, value string) (string, any) {
	key = strings.TrimPrefix(key, envPrefix)
	key = strings.ReplaceAll(strings.ToLower(key), envDelimiter, ".")
	return key, value
}

func decoderConfig() *mapstructure.DecoderConfig {
	return &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
			mapstructure.TextUnmarshallerHookFunc(),
		),
		WeaklyTypedInput: true,
		TagName:          "koanf",
	}
}

func (c *Config) normalize() {
	c.HTTP.PublicURL = strings.TrimRight(c.HTTP.PublicURL, "/")
	c.HTTP.AllowedOrigins = compact(c.HTTP.AllowedOrigins)
	c.HTTP.TrustedProxies = compact(c.HTTP.TrustedProxies)
	c.Telegram.BotToken = strings.TrimSpace(c.Telegram.BotToken)
	c.Telegram.ChatID = strings.TrimSpace(c.Telegram.ChatID)
}

func compact(values []string) []string {
	out := values[:0]
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (c Config) Validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}

	check(oneOf(c.Env, "development", "production"), "env must be development or production")
	check(oneOf(c.Log.Level, "debug", "info", "warn", "error"), "log.level must be debug, info, warn or error")
	check(oneOf(c.Log.Format, "json", "text"), "log.format must be json or text")

	check(c.HTTP.Addr != "", "http.addr is required")
	check(isAbsoluteURL(c.HTTP.PublicURL), "http.public_url must be an absolute http(s) url")
	check(c.HTTP.ReadHeaderTimeout > 0, "http.read_header_timeout must be positive")
	check(c.HTTP.ReadTimeout > 0, "http.read_timeout must be positive")
	check(c.HTTP.WriteTimeout > 0, "http.write_timeout must be positive")
	check(c.HTTP.ShutdownTimeout > 0, "http.shutdown_timeout must be positive")
	for _, origin := range c.HTTP.AllowedOrigins {
		check(isAbsoluteURL(origin), "http.allowed_origins: %q is not a valid origin", origin)
	}

	check(c.Database.URL != "", "database.url is required")
	check(c.Database.MaxConns > 0, "database.max_conns must be positive")
	check(c.Database.MinConns >= 0 && c.Database.MinConns <= c.Database.MaxConns, "database.min_conns must be between 0 and max_conns")
	check(c.Database.StatementTimeout > 0, "database.statement_timeout must be positive")

	check(c.Limits.LeadsPerIP.Events > 0 && c.Limits.LeadsPerIP.Per > 0, "limits.leads_per_ip must have positive events and per")

	check((c.Telegram.BotToken == "") == (c.Telegram.ChatID == ""), "telegram.bot_token and telegram.chat_id must be both set or both empty")
	check(c.Telegram.BotToken == "" || isAbsoluteURL(c.Telegram.APIURL), "telegram.api_url must be an absolute http(s) url")

	return errors.Join(errs...)
}

func oneOf(value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

func isAbsoluteURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
