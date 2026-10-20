package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"tenzo-run-backend/internal/chat"
	"tenzo-run-backend/internal/shared/telegram"
)

const (
	noticeQueue   = 64
	noticeTimeout = 15 * time.Second
)

var noticeRetry = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second}

type Messenger interface {
	Send(ctx context.Context, chatID int64, o telegram.Outgoing) (int64, error)
	Edit(ctx context.Context, chatID, messageID int64, o telegram.Outgoing) error
}

type Relay struct {
	bot    Messenger
	repo   *Repository
	admins *Admins
	queue  chan string
	retry  []time.Duration
	log    *slog.Logger
	now    func() time.Time
}

func NewRelay(bot Messenger, repo *Repository, admins *Admins, log *slog.Logger) *Relay {
	return &Relay{
		bot: bot, repo: repo, admins: admins, queue: make(chan string, noticeQueue), retry: noticeRetry,
		log: log, now: time.Now,
	}
}

func (r *Relay) Incoming(ctx context.Context, n chat.Notice) (chat.Ticket, error) {
	text := headline(n) + "\n<b>" + esc(label(n.Session)) + ":</b> " + esc(clip(n.Text, maxPreview))
	posted, err := r.broadcast(ctx, telegram.Outgoing{Text: text, HTML: true})
	if len(posted) > 0 {
		if rerr := r.repo.remember(ctx, n.Session.ID, posted, r.now()); rerr != nil {
			err = errors.Join(err, fmt.Errorf("remember bot messages: %w", rerr))
		}
	}
	return chat.Ticket{Posted: posted}, err
}

func (r *Relay) Answered(ctx context.Context, t chat.Ticket, n chat.Notice) error {
	out := telegram.Outgoing{Text: summary(n), HTML: true, Markup: keyboard(n)}
	var errs []error
	for _, p := range t.Posted {
		if err := r.bot.Edit(ctx, p.ChatID, p.MessageID, out); err != nil {
			errs = append(errs, fmt.Errorf("chat %d: %w", p.ChatID, err))
		}
	}
	if len(t.Posted) == 0 {
		posted, err := r.broadcast(ctx, out)
		if err != nil {
			errs = append(errs, err)
		}
		if len(posted) > 0 {
			if err := r.repo.remember(ctx, n.Session.ID, posted, r.now()); err != nil {
				errs = append(errs, fmt.Errorf("remember bot messages: %w", err))
			}
		}
	}
	return errors.Join(errs...)
}

func (r *Relay) Tell(ctx context.Context, sessionID int64, text string) {
	for _, chatID := range r.admins.IDs(ctx) {
		replyTo, _ := r.repo.latestMessage(ctx, chatID, sessionID)
		_, _ = r.bot.Send(ctx, chatID, telegram.Outgoing{Text: text, HTML: true, ReplyTo: replyTo})
	}
}

func (r *Relay) Send(text string) {
	select {
	case r.queue <- text:
	default:
		r.log.Warn("telegram notice queue is full, notice dropped")
	}
}

func (r *Relay) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-r.queue:
			r.deliver(ctx, text)
		}
	}
}

func (r *Relay) deliver(ctx context.Context, text string) {
	pending := r.admins.IDs(ctx)
	if len(pending) == 0 {
		r.log.WarnContext(ctx, "no telegram admins yet, notice kept only in the database")
		return
	}
	for attempt := 0; ; attempt++ {
		var failed []int64
		var errs []error
		for _, id := range pending {
			sctx, cancel := context.WithTimeout(ctx, noticeTimeout)
			_, err := r.bot.Send(sctx, id, telegram.Outgoing{Text: text})
			cancel()
			if err != nil {
				failed = append(failed, id)
				errs = append(errs, err)
			}
		}
		if len(failed) == 0 {
			return
		}
		if attempt == len(r.retry) {
			r.log.WarnContext(ctx, "telegram notice not sent", slog.Any("error", errors.Join(errs...)))
			return
		}
		pending = failed
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.retry[attempt]):
		}
	}
}

func (r *Relay) broadcast(ctx context.Context, o telegram.Outgoing) ([]chat.Posted, error) {
	var (
		posted []chat.Posted
		errs   []error
	)
	for _, chatID := range r.admins.IDs(ctx) {
		id, err := r.bot.Send(ctx, chatID, o)
		if err != nil {
			errs = append(errs, fmt.Errorf("chat %d: %w", chatID, err))
			continue
		}
		posted = append(posted, chat.Posted{ChatID: chatID, MessageID: id})
	}
	return posted, errors.Join(errs...)
}

func summary(n chat.Notice) string {
	s := n.Session
	var b strings.Builder
	b.WriteString(headline(n))
	b.WriteString("\n<b>" + esc(label(s)) + "</b>")
	if c := s.Lead.Contact; c != "" && label(s) != chat.FormatContact(c) {
		b.WriteString(" · " + esc(chat.FormatContact(c)))
	}
	if p := s.Lead.Project; p != "" {
		b.WriteString("\n<b>Задача:</b> " + esc(clip(p, maxSummary)))
	}
	var terms []string
	if v := s.Lead.Budget; v != "" {
		terms = append(terms, "<b>Бюджет:</b> "+esc(v))
	}
	if v := s.Lead.Deadline; v != "" {
		terms = append(terms, "<b>Срок:</b> "+esc(v))
	}
	if len(terms) > 0 {
		b.WriteString("\n" + strings.Join(terms, " · "))
	}

	b.WriteString("\n\n👤 " + esc(clip(n.Text, maxPreview)))
	if n.Reply != "" {
		b.WriteString("\n💬 <i>" + esc(clip(n.Reply, maxPreview)) + "</i>")
	}
	if n.Escalated {
		b.WriteString("\n\n❓ <b>Ждёт вашего ответа:</b> " + esc(s.Question))
		b.WriteString("\nОтветьте на это сообщение — ответ сразу появится у клиента.")
	}
	return b.String()
}

func keyboard(n chat.Notice) *telegram.Markup {
	s := n.Session
	id := strconv.FormatInt(s.ID, 10)
	var rows [][]telegram.Button

	var ask []telegram.Button
	if s.Lead.Contact == "" {
		ask = append(ask, telegram.Button{Text: "📇 Контакт", CallbackData: callbackData(ActionAskContact, id)})
	}
	if s.Lead.Name == "" {
		ask = append(ask, telegram.Button{Text: "🙂 Имя", CallbackData: callbackData(ActionAskName, id)})
	}
	if len(ask) > 0 {
		rows = append(rows, ask)
	}
	rows = append(rows, []telegram.Button{
		{Text: "📞 Созвон", CallbackData: callbackData(ActionOfferCall, id)},
		{Text: "✏️ Ответить", CallbackData: callbackData(ActionReply, id)},
	})
	if url := contactURL(s); url != "" {
		rows = append(rows, []telegram.Button{{Text: "↗️ Написать напрямую", URL: url}})
	}
	return &telegram.Markup{InlineKeyboard: rows}
}

func contactURL(s chat.Session) string {
	c := s.Lead.Contact
	switch {
	case strings.HasPrefix(c, "@"):
		return "https://t.me/" + strings.TrimPrefix(c, "@")
	case strings.HasPrefix(c, "+"):
		return "https://wa.me/" + strings.TrimPrefix(c, "+")
	}
	return ""
}
