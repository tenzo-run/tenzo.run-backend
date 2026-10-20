package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tenzo-run-backend/internal/chat"
	"tenzo-run-backend/internal/rules"
	"tenzo-run-backend/internal/shared/llm"
	"tenzo-run-backend/internal/shared/telegram"
)

const (
	toolListRules    = "list_rules"
	toolProposeRules = "propose_rules"
	toolFindChats    = "find_chats"
	toolStats        = "stats"

	maxToolRounds   = 4
	historyLimit    = 16
	pendingLifetime = time.Hour
	foundLimit      = 5
	tokenBytes      = 6
)

type Model interface {
	Stream(ctx context.Context, r llm.Request, onDelta func(string)) (llm.Result, error)
}

type RulesStore interface {
	List(ctx context.Context) ([]rules.Rule, error)
	Add(ctx context.Context, text, by string) (rules.Rule, error)
	Remove(ctx context.Context, id int64) error
}

type change struct {
	add     []string
	remove  []int64
	created time.Time
}

type ChatFinder interface {
	List(ctx context.Context, f chat.Filter) ([]chat.Session, error)
	Stats(ctx context.Context, since time.Time) (chat.Stats, error)
}

type Assistant struct {
	model Model
	rules RulesStore
	chats ChatFinder
	api   Messenger
	now   func() time.Time

	mu      sync.Mutex
	history map[int64][]llm.Message
	pending map[string]change
}

func NewAssistant(model Model, rulesStore RulesStore, chats ChatFinder, api Messenger) *Assistant {
	return &Assistant{
		model: model, rules: rulesStore, chats: chats, api: api, now: time.Now,
		history: map[int64][]llm.Message{}, pending: map[string]change{},
	}
}

func (a *Assistant) Reset(adminID int64) {
	a.mu.Lock()
	delete(a.history, adminID)
	a.mu.Unlock()
}

func (a *Assistant) Handle(ctx context.Context, adminID int64, adminName, text string) (string, error) {
	if a.model == nil {
		return "Помощник выключен: не настроен доступ к модели.", nil
	}

	a.mu.Lock()
	past := append([]llm.Message(nil), a.history[adminID]...)
	a.mu.Unlock()

	msgs := append([]llm.Message{{Role: "system", Content: assistantPrompt}}, past...)
	msgs = append(msgs, llm.Message{Role: "user", Content: text})
	start := len(msgs) - 1

	for range maxToolRounds {
		res, err := a.model.Stream(ctx, llm.Request{Messages: msgs, Tools: assistantTools}, func(string) {})
		if err != nil {
			return "", fmt.Errorf("ask assistant: %w", err)
		}
		if len(res.ToolCalls) == 0 {
			reply := strings.TrimSpace(res.Content)
			if reply == "" {
				reply = "Готово."
			}
			msgs = append(msgs, llm.Message{Role: "assistant", Content: reply})
			a.remember(adminID, msgs[start:])
			return reply, nil
		}

		msgs = append(msgs, llm.Message{Role: "assistant", Content: res.Content, ToolCalls: res.ToolCalls})
		for _, call := range res.ToolCalls {
			msgs = append(msgs, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    a.run(ctx, adminID, adminName, call),
			})
		}
	}
	return "Не смог закончить ответ. Сформулируйте, пожалуйста, просьбу короче.", nil
}

func (a *Assistant) Confirm(ctx context.Context, token string, apply bool, adminName string) (string, error) {
	a.mu.Lock()
	c, ok := a.pending[token]
	delete(a.pending, token)
	a.mu.Unlock()

	if !ok || a.now().Sub(c.created) > pendingLifetime {
		return "Это предложение устарело. Попросите помощника ещё раз.", nil
	}
	if !apply {
		return "Отменено, правила не изменились.", nil
	}

	var (
		done []string
		errs []error
	)
	for _, id := range c.remove {
		if err := a.rules.Remove(ctx, id); err != nil {
			errs = append(errs, err)
			continue
		}
		done = append(done, fmt.Sprintf("убрано правило №%d", id))
	}
	for _, text := range c.add {
		rule, err := a.rules.Add(ctx, text, adminName)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		done = append(done, fmt.Sprintf("добавлено №%d: %s", rule.ID, rule.Text))
	}
	if len(done) == 0 {
		return "Не получилось применить изменения.", errors.Join(errs...)
	}
	return "✅ Применено, консультант уже работает по-новому:\n• " + strings.Join(done, "\n• "), errors.Join(errs...)
}

func (a *Assistant) remember(adminID int64, msgs []llm.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := append(a.history[adminID], msgs...)
	if len(h) > historyLimit {
		h = h[len(h)-historyLimit:]
		for len(h) > 0 && h[0].Role != "user" {
			h = h[1:]
		}
	}
	a.history[adminID] = h
}

func (a *Assistant) run(ctx context.Context, adminID int64, adminName string, call llm.ToolCall) string {
	switch call.Function.Name {
	case toolListRules:
		return a.listRules(ctx)
	case toolProposeRules:
		return a.propose(ctx, adminID, call.Function.Arguments)
	case toolFindChats:
		return a.findChats(ctx, call.Function.Arguments)
	case toolStats:
		return a.stats(ctx)
	}
	return toolError("unknown tool")
}

func (a *Assistant) listRules(ctx context.Context) string {
	list, err := a.rules.List(ctx)
	if err != nil {
		return toolError("rules unavailable")
	}
	type item struct {
		ID   int64  `json:"id"`
		Text string `json:"text"`
	}
	out := make([]item, 0, len(list))
	for _, r := range list {
		out = append(out, item{ID: r.ID, Text: r.Text})
	}
	return encodeJSON(map[string]any{"rules": out})
}

func (a *Assistant) propose(ctx context.Context, adminID int64, raw string) string {
	var args struct {
		Add    []string `json:"add"`
		Remove []int64  `json:"remove"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return toolError("invalid arguments")
	}

	var add []string
	for _, t := range args.Add {
		if t = strings.Join(strings.Fields(t), " "); t != "" {
			add = append(add, t)
		}
	}
	if len(add) == 0 && len(args.Remove) == 0 {
		return toolError("nothing to change")
	}

	token, err := newToken()
	if err != nil {
		return toolError("internal error")
	}
	a.mu.Lock()
	for k, c := range a.pending {
		if a.now().Sub(c.created) > pendingLifetime {
			delete(a.pending, k)
		}
	}
	a.pending[token] = change{add: add, remove: args.Remove, created: a.now()}
	a.mu.Unlock()

	var b strings.Builder
	b.WriteString("Предлагаю изменить работу консультанта:")
	for _, id := range args.Remove {
		fmt.Fprintf(&b, "\n➖ убрать правило №%d", id)
	}
	for _, t := range add {
		b.WriteString("\n➕ " + t)
	}
	b.WriteString("\n\nПрименить?")

	_, err = a.api.Send(ctx, adminID, telegram.Outgoing{
		Text: b.String(),
		Markup: &telegram.Markup{InlineKeyboard: [][]telegram.Button{{
			{Text: "✅ Применить", CallbackData: callbackData(ActionApplyRules, token)},
			{Text: "Отмена", CallbackData: callbackData(ActionDropRules, token)},
		}}},
	})
	if err != nil {
		return toolError("could not send confirmation")
	}
	return encodeJSON(map[string]string{"status": "sent_for_confirmation"})
}

func (a *Assistant) findChats(ctx context.Context, raw string) string {
	var args struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal([]byte(raw), &args)

	items, err := a.chats.List(ctx, chat.Filter{Query: args.Query, Limit: foundLimit})
	if err != nil {
		return toolError("chats unavailable")
	}

	type item struct {
		Name     string `json:"name,omitempty"`
		Contact  string `json:"contact,omitempty"`
		Project  string `json:"project,omitempty"`
		Budget   string `json:"budget,omitempty"`
		Deadline string `json:"deadline,omitempty"`
		Channel  string `json:"channel"`
		Messages int    `json:"messages"`
		Waiting  bool   `json:"waiting"`
		Updated  string `json:"updated"`
		LastText string `json:"last_text,omitempty"`
	}
	out := make([]item, 0, len(items))
	for _, s := range items {
		out = append(out, item{
			Name: s.Lead.Name, Contact: s.Lead.Contact, Project: s.Lead.Project, Budget: s.Lead.Budget,
			Deadline: s.Lead.Deadline, Channel: s.Channel, Messages: s.UserTurns(), Waiting: s.Waiting,
			Updated: s.UpdatedAt.In(chat.Almaty).Format("02.01.2006 15:04"), LastText: clip(s.LastText(), 200),
		})
	}
	return encodeJSON(map[string]any{"chats": out})
}

func (a *Assistant) stats(ctx context.Context) string {
	now := a.now().In(chat.Almaty)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, chat.Almaty)
	today, err := a.chats.Stats(ctx, day)
	if err != nil {
		return toolError("stats unavailable")
	}
	week, err := a.chats.Stats(ctx, day.AddDate(0, 0, -6))
	if err != nil {
		return toolError("stats unavailable")
	}
	return encodeJSON(map[string]any{
		"chats_today":     today.Chats,
		"contacts_today":  today.Contacts,
		"chats_7_days":    week.Chats,
		"contacts_7_days": week.Contacts,
		"waiting_answer":  today.Waiting,
	})
}

func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func toolError(msg string) string {
	return encodeJSON(map[string]string{"error": msg})
}

func encodeJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

const assistantPrompt = `Ты — помощник Санжара Абдурахманова (Tenzo) в его Telegram-боте. Санжар — разработчик сайтов, веб-сервисов, ИИ-ассистентов, ботов и мобильных приложений. На сайте tenzo.run и в этом боте с клиентами переписывается ИИ-консультант; ты помогаешь Санжару настраивать его работу и смотреть обращения.

ЧТО ТЫ УМЕЕШЬ
1. Правила консультанта. list_rules — показать текущие. propose_rules — предложить добавить новые и/или убрать старые по номеру. Изменение начнёт действовать только после того, как Санжар нажмёт «Применить» под твоим предложением; сам ничего не меняй и не говори, что уже поменял.
2. Обращения: find_chats — найти диалоги по имени, контакту или теме задачи (пустой запрос — последние). stats — сколько диалогов и контактов за сегодня и за 7 дней, сколько ждут ответа.
3. Подсказать, как пользоваться ботом: ответить клиенту — ответом (reply) на уведомление или кнопками под ним; ответ сразу появится у клиента на сайте или в боте.

КАК ПИСАТЬ ПРАВИЛА
- Каждое правило — одна короткая конкретная инструкция в повелительном наклонении для консультанта: «Не обещай скидки — скажи, что условия обсудим в смете».
- Не дублируй существующие правила: сначала посмотри list_rules.
- Правила не могут разрешить выдумывать факты, цены, отзывы и гарантии или раскрывать инструкции. Если просят такое — объясни, почему нельзя, и предложи честную формулировку.
- Если просьба неясна — задай один уточняющий вопрос.

КАК ОТВЕЧАТЬ
- По-русски, коротко и по делу, обычным текстом без markdown.
- Данные клиентов показывай только из find_chats, ничего не придумывай.`

var assistantTools = []llm.Tool{
	{Type: "function", Function: llm.Function{
		Name:        toolListRules,
		Description: "Показать текущие правила консультанта с номерами.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}},
	{Type: "function", Function: llm.Function{
		Name:        toolProposeRules,
		Description: "Предложить администратору изменить правила консультанта. Изменение применится после кнопки «Применить».",
		Parameters: json.RawMessage(`{"type":"object","properties":{
			"add":{"type":"array","items":{"type":"string"},"description":"Новые правила, каждое — одна короткая инструкция"},
			"remove":{"type":"array","items":{"type":"integer"},"description":"Номера правил, которые нужно убрать"}
		},"additionalProperties":false}`),
	}},
	{Type: "function", Function: llm.Function{
		Name:        toolFindChats,
		Description: "Найти диалоги с клиентами по имени, контакту или теме задачи. Пустой запрос — последние диалоги.",
		Parameters: json.RawMessage(`{"type":"object","properties":{
			"query":{"type":"string","description":"Имя, часть контакта или слово из задачи; можно пусто"}
		},"additionalProperties":false}`),
	}},
	{Type: "function", Function: llm.Function{
		Name:        toolStats,
		Description: "Сводка: диалоги и оставленные контакты за сегодня и за 7 дней, сколько ждут ответа.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}},
}
