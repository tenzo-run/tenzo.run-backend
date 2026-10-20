package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"tenzo-run-backend/internal/shared/llm"
)

type memoryStore struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]Session
	nextID   int64
}

func newMemoryStore() *memoryStore {
	return &memoryStore{sessions: map[uuid.UUID]Session{}}
}

func (m *memoryStore) find(_ context.Context, id uuid.UUID) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, pgx.ErrNoRows
	}
	s.Turns = append([]Turn(nil), s.Turns...)
	return s, nil
}

func (m *memoryStore) byID(_ context.Context, id int64) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.ID == id {
			return s, nil
		}
	}
	return Session{}, pgx.ErrNoRows
}

func (m *memoryStore) byTelegram(_ context.Context, telegramID int64) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found Session
	for _, s := range m.sessions {
		if s.TelegramID == telegramID && s.ID > found.ID {
			found = s
		}
	}
	if found.ID == 0 {
		return Session{}, pgx.ErrNoRows
	}
	return found, nil
}

func (m *memoryStore) create(_ context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	s.ID = m.nextID
	m.sessions[s.PublicID] = *s
	return nil
}

func (m *memoryStore) save(_ context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.PublicID] = *s
	return nil
}

func (m *memoryStore) list(context.Context, Filter) ([]Session, error) { return nil, nil }

func (m *memoryStore) stats(context.Context, time.Time) (Stats, error) { return Stats{}, nil }

func (m *memoryStore) purge(context.Context, time.Time) (int64, error) { return 0, nil }

type scriptedModel struct {
	mu        sync.Mutex
	reply     string
	replyErr  error
	extracted string
	prompts   []string
	last      []llm.Message
}

func (m *scriptedModel) Stream(_ context.Context, r llm.Request, onDelta func(string)) (llm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.ToolChoice != nil {
		if m.extracted == "" {
			return llm.Result{}, nil
		}
		return llm.Result{ToolCalls: []llm.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: llm.FunctionCall{Name: saveLeadTool, Arguments: m.extracted},
		}}}, nil
	}

	m.prompts = append(m.prompts, r.Messages[0].Content)
	m.last = r.Messages
	if m.replyErr != nil {
		return llm.Result{}, m.replyErr
	}
	for _, word := range strings.SplitAfter(m.reply, " ") {
		onDelta(word)
	}
	return llm.Result{Content: m.reply}, nil
}

type recordingRelay struct {
	mu       sync.Mutex
	incoming []Notice
	answered []Notice
}

func (r *recordingRelay) Incoming(_ context.Context, n Notice) (Ticket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.incoming = append(r.incoming, n)
	return Ticket{Posted: []Posted{{ChatID: 1, MessageID: int64(len(r.incoming))}}}, nil
}

func (r *recordingRelay) Answered(_ context.Context, t Ticket, n Notice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(t.Posted) != 1 {
		return errors.New("lost ticket")
	}
	r.answered = append(r.answered, n)
	return nil
}

type fixedRules []string

func (r fixedRules) Texts(context.Context) ([]string, error) { return r, nil }

type recordingEmitter struct {
	session uuid.UUID
	text    strings.Builder
	leads   []Lead
}

func (e *recordingEmitter) Session(id uuid.UUID) { e.session = id }
func (e *recordingEmitter) Delta(text string)    { e.text.WriteString(text) }
func (e *recordingEmitter) Lead(l Lead)          { e.leads = append(e.leads, l) }

func newTestService(model Model, relay Relay, rules Rules) (*Service, *memoryStore) {
	store := newMemoryStore()
	svc := NewService(store, model, relay, rules, Settings{
		MaxTurns:      3,
		HistoryTurns:  10,
		MaxTextLength: 200,
		Retention:     time.Hour,
		NoticeTimeout: time.Second,
	})
	return svc, store
}

func TestReplyStreamsAnswerAndRelaysEveryMessage(t *testing.T) {
	model := &scriptedModel{
		reply:     "Лендинг от 250 000 ₸, от 7 дней. Для какого бизнеса сайт?",
		extracted: `{"project":"Лендинг для кофейни","budget":"300 тысяч"}`,
	}
	relay := &recordingRelay{}
	svc, store := newTestService(model, relay, fixedRules{"Предлагай созвон"})

	out := &recordingEmitter{}
	if err := svc.Reply(t.Context(), Input{Text: "Сколько стоит лендинг?", Locale: "ru", Page: "/ru/uslugi/razrabotka-sajtov"}, out); err != nil {
		t.Fatal(err)
	}
	if out.session == uuid.Nil || out.text.String() != model.reply {
		t.Fatalf("reply = %q, session %s", out.text.String(), out.session)
	}
	if len(relay.incoming) != 1 || relay.incoming[0].Text != "Сколько стоит лендинг?" {
		t.Fatalf("incoming = %+v", relay.incoming)
	}
	if len(relay.answered) != 1 || relay.answered[0].Reply != model.reply || relay.answered[0].Session.Lead.Project != "Лендинг для кофейни" {
		t.Fatalf("answered = %+v", relay.answered)
	}

	prompt := model.prompts[0]
	for _, want := range []string{"Предлагай созвон", "разработка сайтов", "Язык страницы: русский", "контакт — пока нет"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt has no %q", want)
		}
	}

	saved, _ := store.find(t.Context(), out.session)
	if len(saved.Turns) != 2 || saved.Lead.Budget != "300 тысяч" || saved.Channel != ChannelSite {
		t.Fatalf("saved = %+v", saved)
	}
}

func TestReplyCatchesContactInText(t *testing.T) {
	model := &scriptedModel{reply: "Спасибо, Санжар напишет сегодня."}
	relay := &recordingRelay{}
	svc, store := newTestService(model, relay, nil)

	out := &recordingEmitter{}
	if err := svc.Reply(t.Context(), Input{Text: "Пишите в WhatsApp 8 700 060 00 54", Locale: "ru"}, out); err != nil {
		t.Fatal(err)
	}
	if len(out.leads) == 0 || out.leads[0].Contact != "+77000600054" {
		t.Fatalf("leads = %+v", out.leads)
	}
	if !strings.Contains(model.prompts[0], "Контакт человек прислал только что") {
		t.Fatal("prompt does not mention the fresh contact")
	}
	if !relay.incoming[0].NewContact || !relay.answered[0].NewContact {
		t.Fatalf("notices = %+v / %+v", relay.incoming, relay.answered)
	}
	saved, _ := store.find(t.Context(), out.session)
	if saved.Lead.Contact != "+77000600054" {
		t.Fatalf("contact = %q", saved.Lead.Contact)
	}
}

func TestExtractionAddsContactTheRegexMissed(t *testing.T) {
	model := &scriptedModel{reply: "Принято.", extracted: `{"contact":"sanzhar_dev","name":"Санжар"}`}
	relay := &recordingRelay{}
	svc, _ := newTestService(model, relay, nil)

	out := &recordingEmitter{}
	if err := svc.Reply(t.Context(), Input{Text: "мой телеграм sanzhar_dev", Locale: "ru"}, out); err != nil {
		t.Fatal(err)
	}
	last := relay.answered[0]
	if last.Session.Lead.Contact != "@sanzhar_dev" || last.Session.Lead.Name != "Санжар" || !last.NewContact {
		t.Fatalf("answered = %+v", last)
	}
}

func TestTelegramSessionKnowsContactAndKeepsHistory(t *testing.T) {
	model := &scriptedModel{reply: "Бот от 150 000 ₸."}
	svc, _ := newTestService(model, nil, nil)

	in := Input{Telegram: 777, Profile: "Айдар (@aidar_kz)", Contact: "@aidar_kz", Text: "Нужен бот", Locale: "ru"}
	first := &recordingEmitter{}
	if err := svc.Reply(t.Context(), in, first); err != nil {
		t.Fatal(err)
	}
	in.Text = "Для записи клиентов"
	second := &recordingEmitter{}
	if err := svc.Reply(t.Context(), in, second); err != nil {
		t.Fatal(err)
	}
	if first.session != second.session {
		t.Fatal("telegram messages must continue one chat")
	}
	prompt := model.prompts[1]
	if !strings.Contains(prompt, "Telegram-бот") || !strings.Contains(prompt, "контакт — @aidar_kz") {
		t.Fatalf("prompt = %s", prompt)
	}
	if len(first.leads) != 0 {
		t.Fatal("automatic telegram contact is not a new lead event")
	}

	restarted, err := svc.Restart(t.Context(), 777, "Айдар", "@aidar_kz", "ru")
	if err != nil {
		t.Fatal(err)
	}
	third := &recordingEmitter{}
	if err := svc.Reply(t.Context(), in, third); err != nil {
		t.Fatal(err)
	}
	if third.session != restarted.PublicID {
		t.Fatal("restart must begin a new chat")
	}
}

func TestEscalationAdminReplyAndUpdates(t *testing.T) {
	model := &scriptedModel{reply: "Уточню у Санжара и напишу здесь же.", extracted: `{"question_for_admin":"Есть ли рассрочка"}`}
	relay := &recordingRelay{}
	svc, _ := newTestService(model, relay, nil)

	out := &recordingEmitter{}
	if err := svc.Reply(t.Context(), Input{Text: "Есть рассрочка?", Locale: "ru"}, out); err != nil {
		t.Fatal(err)
	}
	if !relay.answered[0].Escalated {
		t.Fatal("question must be escalated")
	}
	u, err := svc.Updates(t.Context(), out.session, 0)
	if err != nil || !u.Waiting || u.Total != 0 {
		t.Fatalf("updates = %+v, %v", u, err)
	}

	if _, err := svc.AdminSay(t.Context(), out.session, "Да, можно разбить на этапы."); err != nil {
		t.Fatal(err)
	}
	u, err = svc.Updates(t.Context(), out.session, 0)
	if err != nil || u.Waiting || u.Total != 1 || u.Messages[0].Text != "Да, можно разбить на этапы." {
		t.Fatalf("updates = %+v, %v", u, err)
	}
	if u, _ = svc.Updates(t.Context(), out.session, 1); len(u.Messages) != 0 {
		t.Fatalf("seen updates = %+v", u)
	}

	if err := svc.Reply(t.Context(), Input{Session: out.session, Text: "Спасибо", Locale: "ru"}, &recordingEmitter{}); err != nil {
		t.Fatal(err)
	}
	history := model.last
	if got := history[len(history)-2]; got.Role != RoleAssistant || got.Content != "Да, можно разбить на этапы." {
		t.Fatalf("admin turn in history = %+v", got)
	}
}

func TestReplyKeepsUserTurnWhenModelFails(t *testing.T) {
	model := &scriptedModel{replyErr: errors.New("down")}
	svc, store := newTestService(model, nil, nil)

	out := &recordingEmitter{}
	if err := svc.Reply(t.Context(), Input{Text: "Привет", Locale: "ru"}, out); err == nil {
		t.Fatal("model failure must surface")
	}
	saved, _ := store.find(t.Context(), out.session)
	if len(saved.Turns) != 1 || saved.Turns[0].Role != RoleUser {
		t.Fatalf("turns = %+v", saved.Turns)
	}
}

func TestEmptyAnswerFallsBackByLocale(t *testing.T) {
	svc, _ := newTestService(&scriptedModel{}, nil, nil)

	out := &recordingEmitter{}
	if err := svc.Reply(t.Context(), Input{Text: "Hi", Locale: "en-US"}, out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.text.String(), "Tell me a bit more") {
		t.Fatalf("fallback = %q", out.text.String())
	}
}

func TestChatLimits(t *testing.T) {
	svc, _ := newTestService(&scriptedModel{reply: "ok"}, nil, nil)

	if err := svc.Reply(t.Context(), Input{Text: "  \n "}, &recordingEmitter{}); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("empty = %v", err)
	}
	if err := svc.Reply(t.Context(), Input{Text: strings.Repeat("я", 201)}, &recordingEmitter{}); !errors.Is(err, ErrLongText) {
		t.Fatalf("long = %v", err)
	}

	out := &recordingEmitter{}
	for range 3 {
		if err := svc.Reply(t.Context(), Input{Session: out.session, Text: "ещё"}, out); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Reply(t.Context(), Input{Session: out.session, Text: "ещё"}, out); !errors.Is(err, ErrTooLong) {
		t.Fatalf("too long = %v", err)
	}
}
