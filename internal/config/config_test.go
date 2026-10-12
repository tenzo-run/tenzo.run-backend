package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	c := Default()
	c.Database.URL = "postgres://tenzo:tenzo@localhost:5472/tenzo?sslmode=disable"
	return c
}

func violations(err error) []error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}

func TestValidateAcceptsCompleteConfig(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
	}{
		{"production defaults", func(*Config) {}},
		{"development with text logs", func(c *Config) { c.Env, c.Log.Level, c.Log.Format = "development", "debug", "text" }},
		{"allowed origins", func(c *Config) { c.HTTP.AllowedOrigins = []string{"https://tenzo.run", "http://localhost:3000"} }},
		{"no idle database connections", func(c *Config) { c.Database.MinConns = 0 }},
		{"telegram notifications", func(c *Config) { c.Telegram.BotToken, c.Telegram.ChatID = "123:abc", "42" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(&c)
			if err := c.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

func TestValidateRejectsEachInvalidField(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
		field  string
	}{
		{"unknown env", func(c *Config) { c.Env = "staging" }, "env must be"},
		{"unknown log level", func(c *Config) { c.Log.Level = "trace" }, "log.level"},
		{"unknown log format", func(c *Config) { c.Log.Format = "xml" }, "log.format"},
		{"no listen address", func(c *Config) { c.HTTP.Addr = "" }, "http.addr"},
		{"relative public url", func(c *Config) { c.HTTP.PublicURL = "/api" }, "http.public_url"},
		{"public url without host", func(c *Config) { c.HTTP.PublicURL = "https://" }, "http.public_url"},
		{"no read header timeout", func(c *Config) { c.HTTP.ReadHeaderTimeout = 0 }, "http.read_header_timeout"},
		{"no read timeout", func(c *Config) { c.HTTP.ReadTimeout = 0 }, "http.read_timeout"},
		{"no write timeout", func(c *Config) { c.HTTP.WriteTimeout = 0 }, "http.write_timeout"},
		{"no shutdown timeout", func(c *Config) { c.HTTP.ShutdownTimeout = 0 }, "http.shutdown_timeout"},
		{"origin without scheme", func(c *Config) { c.HTTP.AllowedOrigins = []string{"https://tenzo.run", "tenzo.run"} }, "http.allowed_origins"},
		{"no database url", func(c *Config) { c.Database.URL = "" }, "database.url"},
		{"no database connections", func(c *Config) { c.Database.MaxConns, c.Database.MinConns = 0, 0 }, "database.max_conns"},
		{"more idle than total connections", func(c *Config) { c.Database.MinConns = c.Database.MaxConns + 1 }, "database.min_conns"},
		{"no statement timeout", func(c *Config) { c.Database.StatementTimeout = 0 }, "database.statement_timeout"},
		{"no leads per ip", func(c *Config) { c.Limits.LeadsPerIP.Events = 0 }, "limits.leads_per_ip"},
		{"no leads window", func(c *Config) { c.Limits.LeadsPerIP.Per = 0 }, "limits.leads_per_ip"},
		{"telegram token without chat", func(c *Config) { c.Telegram.BotToken = "123:abc" }, "telegram.bot_token"},
		{"telegram chat without token", func(c *Config) { c.Telegram.ChatID = "42" }, "telegram.bot_token"},
		{"telegram relative api url", func(c *Config) {
			c.Telegram.BotToken, c.Telegram.ChatID, c.Telegram.APIURL = "123:abc", "42", "api.telegram.org"
		}, "telegram.api_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(&c)
			errs := violations(c.Validate())
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.field) {
				t.Fatalf("errors = %v, want one about %q", errs, tc.field)
			}
		})
	}
}

func TestValidateReportsEveryViolation(t *testing.T) {
	c := validConfig()
	c.Env, c.HTTP.Addr, c.Database.URL = "staging", "", ""

	if errs := violations(c.Validate()); len(errs) != 3 {
		t.Fatalf("errors = %v, want three", errs)
	}
}

func TestNormalize(t *testing.T) {
	c := validConfig()
	c.HTTP.PublicURL = "https://api.tenzo.run/"
	c.HTTP.AllowedOrigins = []string{" https://tenzo.run ", "", "  ", "http://localhost:3000"}
	c.HTTP.TrustedProxies = []string{"10.0.0.0/8 ", ""}
	c.Telegram.BotToken, c.Telegram.ChatID = " 123:abc ", " 42\n"

	c.normalize()

	if c.HTTP.PublicURL != "https://api.tenzo.run" {
		t.Errorf("public url = %q", c.HTTP.PublicURL)
	}
	if !slices.Equal(c.HTTP.AllowedOrigins, []string{"https://tenzo.run", "http://localhost:3000"}) {
		t.Errorf("origins = %q", c.HTTP.AllowedOrigins)
	}
	if !slices.Equal(c.HTTP.TrustedProxies, []string{"10.0.0.0/8"}) {
		t.Errorf("proxies = %q", c.HTTP.TrustedProxies)
	}
	if c.Telegram.BotToken != "123:abc" || c.Telegram.ChatID != "42" {
		t.Errorf("telegram = %q, %q", c.Telegram.BotToken, c.Telegram.ChatID)
	}
}

func TestEnvKey(t *testing.T) {
	cases := []struct {
		name string
		env  string
		key  string
	}{
		{"top level", "TENZO_ENV", "env"},
		{"nested section", "TENZO_HTTP__PUBLIC_URL", "http.public_url"},
		{"rate limit", "TENZO_LIMITS__LEADS_PER_IP__EVENTS", "limits.leads_per_ip.events"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, value := envKey(tc.env, "42")
			if key != tc.key || value != "42" {
				t.Fatalf("key = %q, value = %v; want %q", key, value, tc.key)
			}
		})
	}
}

func TestLoadMergesFileAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "env: development\nhttp:\n  addr: \"127.0.0.1:9000\"\n  allowed_origins:\n    - \"http://localhost:3000\"\n" +
		"database:\n  url: \"postgres://file\"\nlimits:\n  leads_per_ip:\n    events: 3\n    per: 1m\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TENZO_DATABASE__URL", "postgres://env")
	t.Setenv("TENZO_HTTP__ALLOWED_ORIGINS", "https://tenzo.run,https://www.tenzo.run")
	t.Setenv("TENZO_TELEGRAM__BOT_TOKEN", "123:abc")
	t.Setenv("TENZO_TELEGRAM__CHAT_ID", "42")

	c, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}

	if c.Env != "development" || c.HTTP.Addr != "127.0.0.1:9000" || c.Database.URL != "postgres://env" {
		t.Errorf("env = %q, addr = %q, database = %q", c.Env, c.HTTP.Addr, c.Database.URL)
	}
	if !slices.Equal(c.HTTP.AllowedOrigins, []string{"https://tenzo.run", "https://www.tenzo.run"}) {
		t.Errorf("origins = %q", c.HTTP.AllowedOrigins)
	}
	if c.Limits.LeadsPerIP != (Rate{Events: 3, Per: time.Minute}) {
		t.Errorf("leads per ip = %+v", c.Limits.LeadsPerIP)
	}
	if c.Telegram.BotToken != "123:abc" || c.Telegram.ChatID != "42" || c.HTTP.ReadTimeout != Default().HTTP.ReadTimeout {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadRequiresExplicitFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	t.Setenv("TENZO_DATABASE__URL", "postgres://env")

	if _, err := Load(missing, true); err == nil {
		t.Fatal("explicit missing file must fail")
	}
	if _, err := Load(missing, false); err != nil {
		t.Fatalf("implicit missing file: %v", err)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	t.Setenv("TENZO_DATABASE__URL", "postgres://env")
	t.Setenv("TENZO_TELEGRAM__BOT_TOKEN", "123:abc")

	if _, err := Load("", false); err == nil || !strings.Contains(err.Error(), "telegram.bot_token") {
		t.Fatalf("error = %v", err)
	}
}
