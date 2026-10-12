package app

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tenzo-run-backend/internal/config"
	"tenzo-run-backend/internal/shared/postgres"
)

const (
	testOrigin      = "http://localhost:3000"
	testBotToken    = "test-bot-token"
	testChatID      = "42"
	deliveryTimeout = 5 * time.Second
)

type testServer struct {
	t        *testing.T
	url      string
	client   *http.Client
	svc      *services
	pool     *pgxpool.Pool
	messages chan string
}

func startServer(t *testing.T, options ...func(*config.Config)) *testServer {
	dbURL := os.Getenv("TENZO_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TENZO_TEST_DATABASE_URL is not set")
	}

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `drop schema public cascade; create schema public`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, dbURL, log); err != nil {
		t.Fatal(err)
	}

	messages := make(chan string, 16)
	bot := httptest.NewServer(fakeTelegram(t, messages))
	t.Cleanup(bot.Close)

	srv := httptest.NewUnstartedServer(nil)
	t.Cleanup(srv.Close)

	cfg := config.Default()
	cfg.Env = "development"
	cfg.HTTP.PublicURL = "http://" + srv.Listener.Addr().String()
	cfg.HTTP.AllowedOrigins = []string{testOrigin}
	cfg.HTTP.Docs = true
	cfg.Database.URL = dbURL
	cfg.Limits.LeadsPerIP = config.Rate{Events: 100, Per: time.Minute}
	cfg.Telegram = config.Telegram{APIURL: bot.URL, BotToken: testBotToken, ChatID: testChatID}
	for _, option := range options {
		option(&cfg)
	}

	svc := newServices(cfg, pool, log)
	go svc.telegram.Run(ctx)
	handler, err := newRouter(cfg, log, pool, svc)
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = handler
	srv.Start()

	return &testServer{t: t, url: srv.URL, client: &http.Client{}, svc: svc, pool: pool, messages: messages}
}

func fakeTelegram(t *testing.T, messages chan<- string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+testBotToken+"/sendMessage" {
			t.Errorf("telegram path = %s", r.URL.Path)
		}
		var m struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m.ChatID != testChatID {
			t.Errorf("telegram message = %+v, %v", m, err)
		}
		messages <- m.Text
	})
}

func (s *testServer) message() string {
	s.t.Helper()
	select {
	case m := <-s.messages:
		return m
	case <-time.After(deliveryTimeout):
		s.t.Fatal("telegram message was not delivered")
		return ""
	}
}

type request struct {
	method string
	path   string
	body   any
	raw    string
	header http.Header
}

type response struct {
	status int
	body   []byte
	header http.Header
}

func (s *testServer) do(r request) response {
	s.t.Helper()

	var body io.Reader
	switch {
	case r.body != nil:
		b, err := json.Marshal(r.body)
		if err != nil {
			s.t.Fatal(err)
		}
		body = bytes.NewReader(b)
	case r.raw != "":
		body = strings.NewReader(r.raw)
	}

	req, err := http.NewRequestWithContext(s.t.Context(), r.method, s.url+r.path, body)
	if err != nil {
		s.t.Fatal(err)
	}
	for k, v := range r.header {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := s.client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()

	b, err := io.ReadAll(res.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return response{status: res.StatusCode, body: b, header: res.Header}
}

func (s *testServer) expect(r request, status int, out any) response {
	s.t.Helper()

	res := s.do(r)
	if res.status != status {
		s.t.Fatalf("%s %s: status %d, want %d: %s", r.method, r.path, res.status, status, res.body)
	}
	if out != nil {
		if err := json.Unmarshal(res.body, out); err != nil {
			s.t.Fatalf("%s %s: decode: %v: %s", r.method, r.path, err, res.body)
		}
	}
	return res
}

func (s *testServer) countLeads() int {
	s.t.Helper()
	var n int
	if err := s.pool.QueryRow(s.t.Context(), `select count(*) from leads`).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func TestHealthAndDocs(t *testing.T) {
	s := startServer(t)

	s.expect(request{method: "GET", path: "/healthz"}, 200, nil)
	s.expect(request{method: "GET", path: "/readyz"}, 200, nil)

	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	s.expect(request{method: "GET", path: "/openapi.json"}, 200, &spec)
	if op := spec.Paths["/api/v1/leads"]["post"].OperationID; op != "create-lead" {
		t.Fatalf("operation = %q", op)
	}
}
