package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"tenzo-run-backend/internal/config"
)

const (
	ownerID       = 5001
	clientID      = 7001
	modelReply    = "Лендинг от 250 000 ₸, от 7 дней. Для какого бизнеса сайт?"
	waitStep      = 20 * time.Millisecond
	fakePollDelay = 100 * time.Millisecond
	testClaimCode = "owner-claim-code-0123456789"
	secondOwnerID = 6001
)

type sentMessage struct {
	Method  string
	ChatID  int64
	ID      int64
	Text    string
	Markup  string
	ReplyTo int64
}

type fakeBot struct {
	t       *testing.T
	mu      sync.Mutex
	updates []map[string]any
	nextUpd int64
	nextMsg int64
	sent    []sentMessage
}

func (f *fakeBot) push(update map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextUpd++
	update["update_id"] = f.nextUpd
	f.updates = append(f.updates, update)
}

func (f *fakeBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	switch method {
	case "getUpdates":
		offset := int64(body["offset"].(float64))
		select {
		case <-r.Context().Done():
			return
		case <-time.After(fakePollDelay):
		}
		f.mu.Lock()
		var out []map[string]any
		for _, u := range f.updates {
			if u["update_id"].(int64) >= offset {
				out = append(out, u)
			}
		}
		f.mu.Unlock()
		writeResult(w, out)
	case "sendMessage", "editMessageText":
		f.mu.Lock()
		m := sentMessage{Method: method, ChatID: int64(body["chat_id"].(float64)), Text: body["text"].(string)}
		if markup, ok := body["reply_markup"]; ok {
			b, _ := json.Marshal(markup)
			m.Markup = string(b)
		}
		if reply, ok := body["reply_parameters"].(map[string]any); ok {
			m.ReplyTo = int64(reply["message_id"].(float64))
		}
		if method == "sendMessage" {
			f.nextMsg++
			m.ID = f.nextMsg
		} else {
			m.ID = int64(body["message_id"].(float64))
		}
		f.sent = append(f.sent, m)
		f.mu.Unlock()
		writeResult(w, map[string]int64{"message_id": m.ID})
	default:
		writeResult(w, true)
	}
}

func writeResult(w http.ResponseWriter, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *fakeBot) waitFor(match func(sentMessage) bool) sentMessage {
	f.t.Helper()
	deadline := time.Now().Add(deliveryTimeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, m := range f.sent {
			if match(m) {
				f.mu.Unlock()
				return m
			}
		}
		f.mu.Unlock()
		time.Sleep(waitStep)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t.Fatalf("telegram message not sent; sent = %+v", f.sent)
	return sentMessage{}
}

func fakeModel(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("model request = %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var req struct {
			ToolChoice any `json:"tool_choice"`
			Messages   []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "text/event-stream")
		if req.ToolChoice != nil {
			args, _ := json.Marshal(`{"project":"Лендинг для кофейни"}`)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"save_lead\",\"arguments\":%s}}]}}]}\n\n", args)
		} else {
			for _, word := range strings.SplitAfter(modelReply, " ") {
				chunk, _ := json.Marshal(word)
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", chunk)
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
}

func startChatServer(t *testing.T) (*testServer, *fakeBot) {
	bot := &fakeBot{t: t}
	tg := httptest.NewServer(bot)
	t.Cleanup(tg.Close)
	model := httptest.NewServer(fakeModel(t))
	t.Cleanup(model.Close)

	s := startServer(t, func(c *config.Config) {
		c.Telegram = config.Telegram{
			APIURL: tg.URL, BotToken: testBotToken, Admins: []string{"tenzo_run"}, ClaimCode: testClaimCode, Timeout: time.Second,
		}
		c.AI.BaseURL, c.AI.APIKey = model.URL+"/v1", "test-key"
	})
	go s.svc.bot.Run(t.Context())
	return s, bot
}

type chatEvent struct {
	name string
	data map[string]any
}

func (s *testServer) chat(body map[string]any) []chatEvent {
	s.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(s.t.Context(), http.MethodPost, s.url+"/api/v1/chat", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrigin)
	res, err := s.client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		s.t.Fatalf("chat status = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}

	var events []chatEvent
	var name string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var data map[string]any
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data)
			events = append(events, chatEvent{name: name, data: data})
		}
	}
	return events
}

func TestChatEndToEnd(t *testing.T) {
	s, bot := startChatServer(t)

	bot.push(map[string]any{"message": map[string]any{
		"message_id": 1, "text": "/start",
		"from": map[string]any{"id": ownerID, "first_name": "Санжар", "username": "Tenzo_Run"},
		"chat": map[string]any{"id": ownerID, "type": "private"},
	}})
	bot.waitFor(func(m sentMessage) bool { return m.ChatID == ownerID && strings.Contains(m.Text, "Бот tenzo.run") })

	events := s.chat(map[string]any{"text": "Сколько стоит лендинг? Мой телеграм @coffee_owner", "locale": "ru", "page": "/ru/ceny"})
	var session, reply string
	var lead bool
	for _, e := range events {
		switch e.name {
		case "session":
			session = e.data["id"].(string)
		case "delta":
			reply += e.data["text"].(string)
		case "lead":
			lead = lead || e.data["contact"] == true
		}
	}
	if session == "" || reply != modelReply || !lead || events[len(events)-1].name != "done" {
		t.Fatalf("events = %+v", events)
	}

	notice := bot.waitFor(func(m sentMessage) bool {
		return m.Method == "editMessageText" && m.ChatID == ownerID && strings.Contains(m.Text, "Лендинг для кофейни")
	})
	for _, want := range []string{"Оставил контакт", "@coffee_owner", "/ru/ceny", modelReply} {
		if !strings.Contains(notice.Text, want) {
			t.Errorf("notice has no %q: %s", want, notice.Text)
		}
	}
	if !strings.Contains(notice.Markup, "https://t.me/coffee_owner") || !strings.Contains(notice.Markup, `"rp:`) {
		t.Errorf("keyboard = %s", notice.Markup)
	}

	bot.push(map[string]any{"message": map[string]any{
		"message_id": 2, "text": "Добрый день! Пришлю смету сегодня.",
		"from":             map[string]any{"id": ownerID, "first_name": "Санжар", "username": "tenzo_run"},
		"chat":             map[string]any{"id": ownerID, "type": "private"},
		"reply_to_message": map[string]any{"message_id": notice.ID, "chat": map[string]any{"id": ownerID, "type": "private"}},
	}})
	bot.waitFor(func(m sentMessage) bool { return m.ChatID == ownerID && strings.HasPrefix(m.Text, "↪️") })

	var updates struct {
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
		Total   int  `json:"total"`
		Waiting bool `json:"waiting"`
	}
	s.expect(request{method: "GET", path: "/api/v1/chat/" + session + "/updates?after=0"}, 200, &updates)
	if updates.Total != 1 || updates.Messages[0].Text != "Добрый день! Пришлю смету сегодня." {
		t.Fatalf("updates = %+v", updates)
	}

	bot.push(map[string]any{"message": map[string]any{
		"message_id": 3, "text": "Нужен сайт для кофейни",
		"from": map[string]any{"id": clientID, "first_name": "Айдар", "username": "aidar_kz", "language_code": "ru"},
		"chat": map[string]any{"id": clientID, "type": "private"},
	}})
	bot.waitFor(func(m sentMessage) bool { return m.ChatID == clientID && m.Text == modelReply })
	tgNotice := bot.waitFor(func(m sentMessage) bool {
		return m.Method == "editMessageText" && m.ChatID == ownerID && strings.Contains(m.Text, "Telegram-бот")
	})
	if !strings.Contains(tgNotice.Text, "Айдар (@aidar_kz)") {
		t.Fatalf("telegram notice = %s", tgNotice.Text)
	}

	bot.push(map[string]any{"message": map[string]any{
		"message_id": 4, "text": "Созвонимся завтра?",
		"from":             map[string]any{"id": ownerID, "first_name": "Санжар", "username": "tenzo_run"},
		"chat":             map[string]any{"id": ownerID, "type": "private"},
		"reply_to_message": map[string]any{"message_id": tgNotice.ID, "chat": map[string]any{"id": ownerID, "type": "private"}},
	}})
	bot.waitFor(func(m sentMessage) bool {
		return m.ChatID == clientID && strings.Contains(m.Text, "Созвонимся завтра?")
	})

	bot.push(map[string]any{"callback_query": map[string]any{
		"id": "cb1", "data": "rp:1",
		"from": map[string]any{"id": clientID, "first_name": "Айдар", "username": "aidar_kz"},
	}})
	time.Sleep(fakePollDelay * 3)
	bot.mu.Lock()
	for _, m := range bot.sent {
		if m.ChatID == clientID && strings.Contains(m.Text, "Ответ для") {
			t.Errorf("stranger opened the admin reply prompt: %+v", m)
		}
	}
	bot.mu.Unlock()
}

func TestChatRejectsBadRequests(t *testing.T) {
	s, _ := startChatServer(t)

	s.expect(request{method: "POST", path: "/api/v1/chat", raw: "{", header: http.Header{"Origin": {testOrigin}}}, 400, nil)
	s.expect(request{method: "POST", path: "/api/v1/chat", body: map[string]string{"text": "hi", "session": "nope"}, header: http.Header{"Origin": {testOrigin}}}, 400, nil)
	s.expect(request{method: "POST", path: "/api/v1/chat", body: map[string]string{"text": "  "}, header: http.Header{"Origin": {testOrigin}}}, 422, nil)
	s.expect(request{method: "GET", path: "/api/v1/chat/0190a3c0-0000-7000-8000-000000000000/updates"}, 404, nil)

	events := s.chat(map[string]any{"text": "spam", "website": "http://spam"})
	if len(events) != 1 || events[0].name != "done" {
		t.Fatalf("honeypot events = %+v", events)
	}
	var n int
	if err := s.pool.QueryRow(t.Context(), `select count(*) from chat_sessions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("sessions = %d, %v", n, err)
	}
}

func TestChatDisabledWithoutModel(t *testing.T) {
	s := startServer(t)
	s.expect(request{method: "POST", path: "/api/v1/chat", body: map[string]string{"text": "hi"}, header: http.Header{"Origin": {testOrigin}}}, 503, nil)
}

func TestOwnerClaimsBotWithStartCode(t *testing.T) {
	s, bot := startChatServer(t)

	stranger := map[string]any{"id": secondOwnerID, "first_name": "Санжар", "username": "sanzhar_personal"}
	chatOf := map[string]any{"id": secondOwnerID, "type": "private"}
	bot.push(map[string]any{"message": map[string]any{"message_id": 1, "text": "/start wrong-code-0000000000", "from": stranger, "chat": chatOf}})
	bot.waitFor(func(m sentMessage) bool {
		return m.ChatID == secondOwnerID && strings.Contains(m.Text, "ИИ-ассистент Tenzo")
	})

	bot.push(map[string]any{"message": map[string]any{"message_id": 2, "text": "/start " + testClaimCode, "from": stranger, "chat": chatOf}})
	bot.waitFor(func(m sentMessage) bool {
		return m.ChatID == secondOwnerID && strings.Contains(m.Text, "теперь владелец")
	})

	s.chat(map[string]any{"text": "Нужен сайт", "locale": "ru", "page": "/ru"})
	bot.waitFor(func(m sentMessage) bool {
		return m.Method == "editMessageText" && m.ChatID == secondOwnerID && strings.Contains(m.Text, "Нужен сайт")
	})
}
