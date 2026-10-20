package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
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
	AI       AI       `koanf:"ai"`
	Chat     Chat     `koanf:"chat"`
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
	ChatPerIP  Rate `koanf:"chat_per_ip"`
}

type Rate struct {
	Events int           `koanf:"events"`
	Per    time.Duration `koanf:"per"`
}

type Telegram struct {
	APIURL   string        `koanf:"api_url"`
	BotToken string        `koanf:"bot_token"`
	ChatIDs  []string      `koanf:"chat_ids"`
	Admins   []string      `koanf:"admins"`
	Timeout  time.Duration `koanf:"timeout"`
}

func (t Telegram) Enabled() bool {
	return t.BotToken != ""
}

type AI struct {
	BaseURL     string        `koanf:"base_url"`
	APIKey      string        `koanf:"api_key"`
	Model       string        `koanf:"model"`
	Timeout     time.Duration `koanf:"timeout"`
	Temperature float64       `koanf:"temperature"`
	MaxTokens   int           `koanf:"max_tokens"`
	Fallback    Fallback      `koanf:"fallback"`
}

func (a AI) Enabled() bool {
	return a.Primary() || a.Fallback.Enabled()
}

func (a AI) Primary() bool {
	return a.APIKey != ""
}

type Fallback struct {
	Command  string        `koanf:"command"`
	Model    string        `koanf:"model"`
	Effort   string        `koanf:"effort"`
	Token    string        `koanf:"token"`
	Dir      string        `koanf:"dir"`
	Timeout  time.Duration `koanf:"timeout"`
	Cooldown time.Duration `koanf:"cooldown"`
	Parallel int           `koanf:"parallel"`
}

func (f Fallback) Enabled() bool {
	return f.Command != ""
}

type Chat struct {
	MaxTurns      int           `koanf:"max_turns"`
	HistoryTurns  int           `koanf:"history_turns"`
	MaxTextLength int           `koanf:"max_text_length"`
	MaxBodyBytes  int64         `koanf:"max_body_bytes"`
	ReplyTimeout  time.Duration `koanf:"reply_timeout"`
	RetentionDays int           `koanf:"retention_days"`
}

func (c Chat) Retention() time.Duration {
	return time.Duration(c.RetentionDays) * 24 * time.Hour
}

const maxRetentionDays = 3650

var (
	fallbackEfforts      = []string{"low", "medium", "high", "xhigh", "max"}
	telegramTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{30,}$`)
	numericIDPattern     = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)
	usernamePattern      = regexp.MustCompile(`^@?[A-Za-z][A-Za-z0-9_]{4,31}$`)
)

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
			ChatPerIP:  Rate{Events: 40, Per: 10 * time.Minute},
		},
		Telegram: Telegram{
			APIURL:  "https://api.telegram.org",
			Timeout: 10 * time.Second,
		},
		AI: AI{
			BaseURL:     "https://llm.rate-cloud.com/v1",
			Model:       "qwen-3.8-max",
			Timeout:     60 * time.Second,
			Temperature: 0.4,
			MaxTokens:   600,
			Fallback: Fallback{
				Model:    "claude-sonnet-5-5",
				Effort:   "medium",
				Dir:      "/var/lib/tenzo/claude",
				Timeout:  90 * time.Second,
				Cooldown: 3 * time.Minute,
				Parallel: 2,
			},
		},
		Chat: Chat{
			MaxTurns:      40,
			HistoryTurns:  30,
			MaxTextLength: 1000,
			MaxBodyBytes:  4096,
			ReplyTimeout:  2 * time.Minute,
			RetentionDays: 30,
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
	c.Telegram.ChatIDs = compact(c.Telegram.ChatIDs)
	c.Telegram.Admins = compact(c.Telegram.Admins)
	c.AI.APIKey = strings.TrimSpace(c.AI.APIKey)
	c.AI.BaseURL = strings.TrimRight(c.AI.BaseURL, "/")
	c.AI.Fallback.Command = strings.TrimSpace(c.AI.Fallback.Command)
	c.AI.Fallback.Token = strings.TrimSpace(c.AI.Fallback.Token)
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

	check(c.Limits.ChatPerIP.Events > 0 && c.Limits.ChatPerIP.Per > 0, "limits.chat_per_ip must have positive events and per")

	if t := c.Telegram; t.Enabled() {
		check(telegramTokenPattern.MatchString(t.BotToken), "telegram.bot_token has an invalid format")
		check(isAbsoluteURL(t.APIURL), "telegram.api_url must be an absolute http(s) url")
		check(t.Timeout > 0, "telegram.timeout must be positive")
		check(len(t.ChatIDs)+len(t.Admins) > 0, "telegram.chat_ids or telegram.admins is required with telegram.bot_token")
	}
	for _, id := range c.Telegram.ChatIDs {
		check(numericIDPattern.MatchString(id), "telegram.chat_ids: %q must be a numeric user id", id)
	}
	for _, name := range c.Telegram.Admins {
		check(usernamePattern.MatchString(name), "telegram.admins: %q must be a telegram username", name)
	}

	check(!c.AI.Primary() || isAbsoluteURL(c.AI.BaseURL), "ai.base_url must be an absolute http(s) url")
	check(!c.AI.Primary() || c.AI.Model != "", "ai.model is required")
	check(c.AI.Timeout > 0, "ai.timeout must be positive")
	check(c.AI.Temperature >= 0 && c.AI.Temperature <= 2, "ai.temperature must be between 0 and 2")
	check(c.AI.MaxTokens >= 0, "ai.max_tokens must not be negative")
	if f := c.AI.Fallback; f.Enabled() {
		check(f.Model != "", "ai.fallback.model is required")
		check(f.Effort == "" || slices.Contains(fallbackEfforts, f.Effort), "ai.fallback.effort must be one of low, medium, high, xhigh, max")
		check(path.IsAbs(f.Dir), "ai.fallback.dir must be an absolute path")
		check(f.Timeout > 0 && f.Timeout < c.Chat.ReplyTimeout, "ai.fallback.timeout must be positive and less than chat.reply_timeout")
		check(f.Cooldown > 0, "ai.fallback.cooldown must be positive")
		check(f.Parallel >= 1 && f.Parallel <= 4, "ai.fallback.parallel must be between 1 and 4")
	}

	check(c.Chat.MaxTurns >= 1, "chat.max_turns must be positive")
	check(c.Chat.HistoryTurns >= 2, "chat.history_turns must be at least 2")
	check(c.Chat.MaxTextLength >= 100 && c.Chat.MaxTextLength <= 4000, "chat.max_text_length must be between 100 and 4000")
	check(c.Chat.MaxBodyBytes >= 1024, "chat.max_body_bytes must be at least 1024")
	check(c.Chat.ReplyTimeout > c.AI.Timeout, "chat.reply_timeout must be greater than ai.timeout")
	check(c.Chat.RetentionDays >= 1 && c.Chat.RetentionDays <= maxRetentionDays, "chat.retention_days must be between 1 and %d", maxRetentionDays)

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
