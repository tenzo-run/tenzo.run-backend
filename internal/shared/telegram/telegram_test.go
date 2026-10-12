package telegram

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type sent struct {
	ChatID  string `json:"chat_id"`
	Text    string `json:"text"`
	Preview struct {
		Disabled bool `json:"is_disabled"`
	} `json:"link_preview_options"`
}

func receive(t *testing.T, ch <-chan sent) sent {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("telegram message was not delivered")
		return sent{}
	}
}

func TestSendDeliversInOrder(t *testing.T) {
	ch := make(chan sent, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/botsecret/sendMessage" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var m sent
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Error(err)
		}
		ch <- m
	}))
	defer srv.Close()

	b := New(srv.URL+"/", "secret", "42", discard)
	go b.Run(t.Context())
	b.Send("first")
	b.Send("second")

	if m := receive(t, ch); m.ChatID != "42" || m.Text != "first" || !m.Preview.Disabled {
		t.Fatalf("first message = %+v", m)
	}
	if m := receive(t, ch); m.Text != "second" {
		t.Fatalf("second message = %+v", m)
	}
}

func TestSendRetriesFailures(t *testing.T) {
	var calls atomic.Int32
	ch := make(chan sent, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var m sent
		_ = json.NewDecoder(r.Body).Decode(&m)
		ch <- m
	}))
	defer srv.Close()

	b := New(srv.URL, "secret", "42", discard)
	b.retry = []time.Duration{time.Millisecond}
	go b.Run(t.Context())
	b.Send("again")

	if m := receive(t, ch); m.Text != "again" || calls.Load() != 2 {
		t.Fatalf("message = %+v after %d calls", m, calls.Load())
	}
}

func TestErrorsHideToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	b := New(srv.URL, "secret-token", "42", discard)
	err := b.post(t.Context(), "hello")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error = %v", err)
	}
}

func TestDisabledWithoutTokenOrChat(t *testing.T) {
	for _, b := range []*Bot{New("https://api.telegram.org", "", "42", discard), New("https://api.telegram.org", "token", "", discard)} {
		if b != nil {
			t.Fatalf("bot = %+v", b)
		}
		b.Send("ignored")
		b.Run(t.Context())
	}
}

func TestTruncateLongText(t *testing.T) {
	if got := truncate("короткий"); got != "короткий" {
		t.Fatalf("short text = %q", got)
	}
	if got := []rune(truncate(strings.Repeat("я", maxText+10))); len(got) != maxText+1 || got[maxText] != '…' {
		t.Fatalf("long text has %d runes", len(got))
	}
}
