package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTruncateLongText(t *testing.T) {
	if got := truncate("короткий"); got != "короткий" {
		t.Fatalf("short text = %q", got)
	}
	if got := []rune(truncate(strings.Repeat("я", maxText+10))); len(got) != maxText+1 || got[maxText] != '…' {
		t.Fatalf("long text has %d runes", len(got))
	}
}

func TestSendReturnsMessageIDAndEditUsesIt(t *testing.T) {
	var paths []string
	var first struct {
		ChatID  int64  `json:"chat_id"`
		Text    string `json:"text"`
		Preview struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	}
	var edited struct {
		ChatID      int64           `json:"chat_id"`
		MessageID   int64           `json:"message_id"`
		Text        string          `json:"text"`
		ParseMode   string          `json:"parse_mode"`
		ReplyMarkup json.RawMessage `json:"reply_markup"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			_ = json.NewDecoder(r.Body).Decode(&edited)
		default:
			_ = json.NewDecoder(r.Body).Decode(&first)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":501}}`))
	}))
	defer srv.Close()

	b := NewBot(srv.URL+"/", "secret", time.Second, time.Second)
	id, err := b.Send(t.Context(), 42, Outgoing{Text: "первое"})
	if err != nil || id != 501 {
		t.Fatalf("id = %d, err = %v", id, err)
	}
	if first.ChatID != 42 || first.Text != "первое" || !first.Preview.Disabled {
		t.Fatalf("sent = %+v", first)
	}
	markup := &Markup{InlineKeyboard: [][]Button{{{Text: "Имя", CallbackData: "nm:1"}}}}
	if err := b.Edit(t.Context(), 42, id, Outgoing{Text: "<b>второе</b>", HTML: true, Markup: markup}); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/botsecret/sendMessage" || paths[1] != "/botsecret/editMessageText" ||
		edited.MessageID != 501 || edited.ChatID != 42 || edited.ParseMode != "HTML" ||
		!strings.Contains(string(edited.ReplyMarkup), `"callback_data":"nm:1"`) {
		t.Fatalf("paths = %v, edited = %+v", paths, edited)
	}
}

func TestSendReportsTelegramDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()

	_, err := NewBot(srv.URL, "secret", time.Second, time.Second).Send(t.Context(), 42, Outgoing{Text: "hello"})
	if err == nil || !strings.Contains(err.Error(), "400: Bad Request: chat not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestErrorsHideToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	_, err := NewBot(srv.URL, "secret-token", time.Second, time.Second).Send(t.Context(), 42, Outgoing{Text: "hello"})
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error = %v", err)
	}
}

func TestEditIgnoresNotModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: message is not modified"}`))
	}))
	defer srv.Close()

	if err := NewBot(srv.URL, "secret", time.Second, time.Second).Edit(t.Context(), 1, 1, Outgoing{Text: "same"}); err != nil {
		t.Fatal(err)
	}
}
