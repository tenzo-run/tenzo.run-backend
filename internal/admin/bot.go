package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"tenzo-run-backend/internal/chat"
	"tenzo-run-backend/internal/shared/postgres"
	"tenzo-run-backend/internal/shared/telegram"
)

const (
	pollTimeout   = 25 * time.Second
	pollBackoff   = 5 * time.Second
	handleTimeout = 3 * time.Minute
	setupTimeout  = 15 * time.Second
	purgeEvery    = 6 * time.Hour
	typingEvery   = 4 * time.Second
	clientSlots   = 8
	commandStart  = "/start"
	commandHelp   = "/help"
	commandNew    = "/new"
)

type BotAPI interface {
	Messenger
	Updates(ctx context.Context, offset int64, timeout time.Duration) ([]telegram.Update, error)
	Answer(ctx context.Context, callbackID, text string) error
	SetCommands(ctx context.Context, commands []telegram.Command) error
	Typing(ctx context.Context, chatID int64) error
}

type Chats interface {
	Reply(ctx context.Context, in chat.Input, out chat.Emitter) error
	Restart(ctx context.Context, telegramID int64, profile, contact, locale string) (chat.Session, error)
	AdminSay(ctx context.Context, id uuid.UUID, text string) (chat.Session, error)
	ByID(ctx context.Context, id int64) (chat.Session, error)
}

type Bot struct {
	api       BotAPI
	relay     *Relay
	repo      *Repository
	admins    *Admins
	chats     Chats
	assistant *Assistant
	log       *slog.Logger
	now       func() time.Time
	slots     chan struct{}
	wg        sync.WaitGroup
}

func NewBot(api BotAPI, relay *Relay, repo *Repository, admins *Admins, chats Chats, assistant *Assistant, log *slog.Logger) *Bot {
	return &Bot{
		api: api, relay: relay, repo: repo, admins: admins, chats: chats, assistant: assistant, log: log,
		now: time.Now, slots: make(chan struct{}, clientSlots),
	}
}

func (b *Bot) Run(ctx context.Context) {
	defer b.wg.Wait()
	b.setup(ctx)

	offset, err := b.repo.lastUpdate(ctx)
	if err != nil {
		b.log.ErrorContext(ctx, "load bot offset failed", slog.Any("error", err))
	}
	offset++
	lastPurge := b.now()

	for ctx.Err() == nil {
		updates, err := b.api.Updates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.log.WarnContext(ctx, "bot updates failed", slog.Any("error", err))
			sleep(ctx, pollBackoff)
			continue
		}
		for _, u := range updates {
			offset = max(offset, u.UpdateID+1)
			b.process(ctx, u)
		}
		if b.now().Sub(lastPurge) > purgeEvery {
			lastPurge = b.now()
			if err := b.repo.purgeUpdates(ctx, b.now().Add(-updatesRetention)); err != nil {
				b.log.WarnContext(ctx, "purge bot updates failed", slog.Any("error", err))
			}
		}
	}
}

func (b *Bot) setup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()

	if err := b.api.SetCommands(ctx, []telegram.Command{
		{Command: "start", Description: "О боте"},
		{Command: "new", Description: "Начать разговор заново"},
	}); err != nil {
		b.log.WarnContext(ctx, "set bot commands failed", slog.Any("error", err))
	}
}

func (b *Bot) process(ctx context.Context, u telegram.Update) {
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)

	fresh, err := b.repo.claimUpdate(hctx, u.UpdateID, b.now())
	if err != nil {
		cancel()
		b.log.ErrorContext(hctx, "claim bot update failed", slog.Any("error", err))
		return
	}
	if !fresh {
		cancel()
		return
	}

	if m := u.Message; m != nil && m.From != nil && m.Chat.Type == "private" {
		if code, ok := strings.CutPrefix(strings.TrimSpace(m.Text), commandStart+" "); ok {
			claimed, err := b.admins.Claim(hctx, *m.From, code)
			if err != nil {
				b.log.ErrorContext(hctx, "claim admin failed", slog.Any("error", err))
			}
			if claimed {
				defer cancel()
				b.guard(hctx, u.UpdateID, func() error {
					_, err := b.api.Send(hctx, m.Chat.ID, telegram.Outgoing{Text: claimedText + "\n\n" + welcomeText, HTML: true})
					return err
				})
				return
			}
		}
	}

	if m := u.Message; m != nil && m.From != nil && m.Chat.Type == "private" && !b.admin(hctx, *m.From) {
		select {
		case b.slots <- struct{}{}:
		case <-ctx.Done():
			cancel()
			return
		}
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			defer func() { <-b.slots }()
			defer cancel()
			b.guard(hctx, u.UpdateID, func() error { return b.onClient(hctx, *m) })
		}()
		return
	}

	defer cancel()
	b.guard(hctx, u.UpdateID, func() error {
		switch {
		case u.CallbackQuery != nil:
			return b.onCallback(hctx, *u.CallbackQuery)
		case u.Message != nil && u.Message.From != nil && u.Message.Chat.Type == "private":
			return b.onAdmin(hctx, *u.Message)
		}
		return nil
	})
}

func (b *Bot) guard(ctx context.Context, updateID int64, fn func() error) {
	defer func() {
		if r := recover(); r != nil {
			b.log.ErrorContext(ctx, "bot update panicked", slog.Any("panic", r))
		}
	}()
	if err := fn(); err != nil {
		b.log.ErrorContext(ctx, "bot update failed", slog.Int64("update_id", updateID), slog.Any("error", err))
	}
}

func (b *Bot) admin(ctx context.Context, u telegram.User) bool {
	allowed, err := b.admins.Seen(ctx, u)
	if err != nil {
		b.log.WarnContext(ctx, "remember admin failed", slog.Any("error", err))
	}
	return allowed
}

func (b *Bot) onAdmin(ctx context.Context, m telegram.Message) error {
	text := strings.TrimSpace(m.Text)
	switch {
	case text == "":
		return nil
	case strings.HasPrefix(text, commandStart), strings.HasPrefix(text, commandHelp):
		_, err := b.api.Send(ctx, m.Chat.ID, telegram.Outgoing{Text: welcomeText, HTML: true})
		return err
	case strings.HasPrefix(text, commandNew):
		b.assistant.Reset(m.From.ID)
		_, err := b.api.Send(ctx, m.Chat.ID, telegram.Outgoing{Text: "Начали заново. Что поменяем в работе консультанта?"})
		return err
	}

	if m.ReplyToMessage != nil {
		sessionID, err := b.repo.sessionOf(ctx, m.Chat.ID, m.ReplyToMessage.MessageID)
		switch {
		case err == nil:
			return b.sayToClient(ctx, sessionID, text, m.From.Name())
		case !postgres.IsNoRows(err):
			return fmt.Errorf("find replied session: %w", err)
		}
	}

	stop := b.typing(ctx, m.Chat.ID)
	reply, err := b.assistant.Handle(ctx, m.From.ID, m.From.Name(), text)
	stop()
	if err != nil {
		b.log.WarnContext(ctx, "admin assistant failed", slog.Any("error", err))
		reply = "Не получилось ответить: модель сейчас недоступна. Попробуйте через минуту."
	}
	_, err = b.api.Send(ctx, m.Chat.ID, telegram.Outgoing{Text: reply})
	return err
}

func (b *Bot) onClient(ctx context.Context, m telegram.Message) error {
	locale := chat.ParseLocale(m.From.LanguageCode)
	text := strings.TrimSpace(m.Text)
	switch {
	case text == "":
		return b.say(ctx, m.Chat.ID, clientText(textOnly, locale))
	case strings.HasPrefix(text, commandStart), strings.HasPrefix(text, commandHelp):
		return b.say(ctx, m.Chat.ID, clientText(greeting, locale))
	case strings.HasPrefix(text, commandNew):
		if _, err := b.chats.Restart(ctx, m.Chat.ID, profile(*m.From), contactOf(*m.From), locale); err != nil {
			return errors.Join(err, b.say(ctx, m.Chat.ID, clientText(failed, locale)))
		}
		return b.say(ctx, m.Chat.ID, clientText(restarted, locale))
	}

	stop := b.typing(ctx, m.Chat.ID)
	out := &collector{}
	err := b.chats.Reply(ctx, chat.Input{
		Telegram: m.Chat.ID,
		Profile:  profile(*m.From),
		Contact:  contactOf(*m.From),
		Text:     text,
		Locale:   locale,
	}, out)
	stop()

	reply := strings.TrimSpace(out.text.String())
	switch {
	case errors.Is(err, chat.ErrBusy):
		return b.say(ctx, m.Chat.ID, clientText(busy, locale))
	case errors.Is(err, chat.ErrTooLong):
		return b.say(ctx, m.Chat.ID, clientText(tooLong, locale))
	case errors.Is(err, chat.ErrLongText):
		return b.say(ctx, m.Chat.ID, clientText(longText, locale))
	case err != nil && reply == "":
		return errors.Join(err, b.say(ctx, m.Chat.ID, clientText(failed, locale)))
	}
	if reply == "" {
		return err
	}
	return errors.Join(err, b.say(ctx, m.Chat.ID, reply))
}

func (b *Bot) say(ctx context.Context, chatID int64, text string) error {
	_, err := b.api.Send(ctx, chatID, telegram.Outgoing{Text: text})
	return err
}

func (b *Bot) typing(ctx context.Context, chatID int64) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(typingEvery)
		defer t.Stop()
		for {
			_ = b.api.Typing(ctx, chatID)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (b *Bot) onCallback(ctx context.Context, q telegram.CallbackQuery) error {
	if !b.admin(ctx, q.From) {
		return b.api.Answer(ctx, q.ID, "Нет доступа")
	}
	action, raw, ok := parseCallback(q.Data)
	if !ok {
		return b.api.Answer(ctx, q.ID, "")
	}

	switch action {
	case ActionApplyRules, ActionDropRules:
		result, err := b.assistant.Confirm(ctx, raw, action == ActionApplyRules, q.From.Name())
		if err != nil {
			return errors.Join(err, b.api.Answer(ctx, q.ID, "Не получилось"))
		}
		if q.Message != nil {
			_ = b.api.Edit(ctx, q.Message.Chat.ID, q.Message.MessageID, telegram.Outgoing{Text: result})
		}
		return b.api.Answer(ctx, q.ID, "")
	}

	sessionID, ok := parseID(raw)
	if !ok {
		return b.api.Answer(ctx, q.ID, "")
	}
	if action == ActionReply {
		return b.promptReply(ctx, q, sessionID)
	}

	sess, err := b.chats.ByID(ctx, sessionID)
	if err != nil {
		return errors.Join(err, b.api.Answer(ctx, q.ID, "Диалог не найден"))
	}
	text, ok := quickReply(action, sess.Locale)
	if !ok {
		return b.api.Answer(ctx, q.ID, "")
	}
	if err := b.sayToClient(ctx, sessionID, text, q.From.Name()); err != nil {
		return errors.Join(err, b.api.Answer(ctx, q.ID, "Не получилось отправить"))
	}
	return b.api.Answer(ctx, q.ID, "Отправлено клиенту")
}

func (b *Bot) sayToClient(ctx context.Context, sessionID int64, text, adminName string) error {
	sess, err := b.chats.ByID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("find session: %w", err)
	}
	sess, err = b.chats.AdminSay(ctx, sess.PublicID, text)
	if err != nil {
		return fmt.Errorf("deliver admin reply: %w", err)
	}
	if sess.Channel == chat.ChannelTelegram {
		signature := signatures[sess.Locale]
		if _, err := b.api.Send(ctx, sess.TelegramID, telegram.Outgoing{Text: "<b>" + esc(signature) + "</b>\n" + esc(text), HTML: true}); err != nil {
			b.relay.Tell(ctx, sess.ID, "⚠️ Не удалось доставить ответ в Telegram: "+esc(label(sess)))
			return fmt.Errorf("send admin reply to telegram: %w", err)
		}
	}
	b.relay.Tell(ctx, sess.ID, "↪️ <b>"+esc(adminName)+"</b> → "+esc(label(sess))+": "+esc(clip(text, maxPreview)))
	return nil
}

func (b *Bot) promptReply(ctx context.Context, q telegram.CallbackQuery, sessionID int64) error {
	sess, err := b.chats.ByID(ctx, sessionID)
	if err != nil {
		return errors.Join(err, b.api.Answer(ctx, q.ID, "Диалог не найден"))
	}
	where := "на сайте"
	if sess.Channel == chat.ChannelTelegram {
		where = "в боте"
	}
	id, err := b.api.Send(ctx, q.From.ID, telegram.Outgoing{
		Text:   "✏️ Ответ для " + label(sess) + "\nНапишите текст ответом на это сообщение — он сразу появится у клиента " + where + ".",
		Markup: &telegram.Markup{ForceReply: true, Placeholder: "Ответ клиенту"},
	})
	if err != nil {
		return errors.Join(err, b.api.Answer(ctx, q.ID, "Не получилось"))
	}
	if err := b.repo.remember(ctx, sessionID, []chat.Posted{{ChatID: q.From.ID, MessageID: id}}, b.now()); err != nil {
		return err
	}
	return b.api.Answer(ctx, q.ID, "")
}

type collector struct {
	text strings.Builder
}

func (c *collector) Session(uuid.UUID) {}
func (c *collector) Lead(chat.Lead)    {}
func (c *collector) Delta(text string) { c.text.WriteString(text) }

func profile(u telegram.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	switch {
	case name != "" && u.Username != "":
		return name + " (@" + u.Username + ")"
	case u.Username != "":
		return "@" + u.Username
	default:
		return name
	}
}

func contactOf(u telegram.User) string {
	if u.Username != "" {
		return "@" + u.Username
	}
	return ""
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

const claimedText = "✅ Готово: этот аккаунт теперь владелец бота. Сюда будут приходить все обращения с сайта и из бота."

const welcomeText = `<b>Бот tenzo.run</b>

🔔 Каждое сообщение из чата на сайте и из этого бота приходит сюда сразу. Под ним — кто пишет, суть задачи, контакт и что ответил ИИ-консультант. Заявки из формы на сайте тоже приходят сюда.

Как ответить клиенту:
• кнопки «Контакт», «Имя», «Созвон» — готовая фраза от вашего имени уйдёт клиенту;
• «Ответить» или ответ (свайп) на уведомление — ваш текст сразу появится у клиента в чате на сайте или в боте;
• «Написать напрямую» — откроет Telegram или WhatsApp клиента, если он оставил контакт.

❓ Если консультант не знает ответа, он пишет клиенту «уточню у Санжара», а здесь появляется вопрос — просто ответьте на него.

🧠 Напишите сюда обычным сообщением, что поменять в работе консультанта: «не обещай скидки», «отвечай короче», «предлагай созвон». Помощник предложит правило, оно начнёт действовать после кнопки «Применить». Ещё он покажет последние обращения и статистику.`
