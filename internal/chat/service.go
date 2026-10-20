package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"tenzo-run-backend/internal/shared/llm"
	"tenzo-run-backend/internal/shared/logger"
	"tenzo-run-backend/internal/shared/postgres"
)

const (
	persistBudget = 5 * time.Second
	newContactGap = 12 * time.Hour
	lockPoll      = 200 * time.Millisecond
	lockWait      = 2 * time.Minute
	replyWait     = 90 * time.Second
)

type Store interface {
	find(ctx context.Context, id uuid.UUID) (Session, error)
	byID(ctx context.Context, id int64) (Session, error)
	byTelegram(ctx context.Context, telegramID int64) (Session, error)
	create(ctx context.Context, s *Session) error
	save(ctx context.Context, s *Session) error
	purge(ctx context.Context, before time.Time) (int64, error)
	list(ctx context.Context, f Filter) ([]Session, error)
	stats(ctx context.Context, since time.Time) (Stats, error)
}

type Model interface {
	Stream(ctx context.Context, r llm.Request, onDelta func(string)) (llm.Result, error)
}

type Emitter interface {
	Session(id uuid.UUID)
	Delta(text string)
	Lead(l Lead)
}

type Stats struct {
	Chats    int
	Contacts int
	Waiting  int
}

type Settings struct {
	MaxTurns      int
	HistoryTurns  int
	MaxTextLength int
	Retention     time.Duration
	NoticeTimeout time.Duration
}

type Service struct {
	store    Store
	model    Model
	relay    Relay
	rules    Rules
	settings Settings
	busy     sync.Map
	jobs     sync.WaitGroup
	now      func() time.Time
	newID    func() (uuid.UUID, error)
}

func NewService(store Store, model Model, relay Relay, rules Rules, settings Settings) *Service {
	return &Service{
		store: store, model: model, relay: relay, rules: rules, settings: settings,
		now: time.Now, newID: uuid.NewV7,
	}
}

func (s *Service) Reply(ctx context.Context, in Input, out Emitter) error {
	if err := in.normalize(s.settings.MaxTextLength); err != nil {
		return err
	}

	sess, err := s.open(ctx, in)
	if err != nil {
		return err
	}
	unlock, err := s.lock(ctx, sess.PublicID, replyWait)
	if err != nil {
		return err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			unlock()
		}
	}()
	if sess.ID != 0 {
		fresh, err := s.store.find(ctx, sess.PublicID)
		if err != nil {
			return fmt.Errorf("find chat: %w", err)
		}
		fresh.Profile = sess.Profile
		sess = &fresh
	}

	if sess.UserTurns() >= s.settings.MaxTurns {
		return ErrTooLong
	}

	now := s.clock()
	returning := sess.ID != 0 && sess.UserTurns() > 0 && now.Sub(sess.UpdatedAt) >= newContactGap
	sess.Turns = append(sess.Turns, Turn{Role: RoleUser, Text: in.Text, At: now})
	sess.UpdatedAt = now
	if sess.Lead.Contact == "" && in.Contact != "" {
		sess.Lead.Contact = in.Contact
	}
	if contact := FindContact(in.Text); contact != "" && contact != sess.Lead.Contact {
		sess.newContact = true
		sess.Lead.Contact = contact
	}
	if err := s.persist(ctx, sess); err != nil {
		return err
	}

	out.Session(sess.PublicID)
	if sess.newContact {
		out.Lead(sess.Lead)
	}

	ticket := s.announce(ctx, Notice{Session: snapshot(sess), Returning: returning, NewContact: sess.newContact, Text: in.Text}, logger.From(ctx))

	reply, answerErr := s.answer(ctx, sess, out)
	if reply != "" {
		sess.Turns = append(sess.Turns, Turn{Role: RoleAssistant, Text: reply, At: s.clock()})
	}
	sess.UpdatedAt = s.clock()
	if err := s.persist(ctx, sess); err != nil {
		return err
	}

	handedOff = true
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		defer unlock()
		s.followUp(context.WithoutCancel(ctx), sess, ticket, Notice{Returning: returning, Text: in.Text, Reply: reply})
	}()
	return answerErr
}

func (s *Service) followUp(ctx context.Context, sess *Session, ticket <-chan Ticket, n Notice) {
	log := logger.From(ctx)
	contact := sess.Lead.Contact
	for _, call := range s.extract(ctx, snapshot(sess)) {
		res := apply(&sess.Lead, call)
		if q := res.question; q != "" && q != sess.Question {
			sess.Question, sess.Waiting, n.Escalated = q, true, true
		}
	}
	if sess.Lead.Contact != contact {
		sess.newContact = true
	}
	n.Session, n.NewContact = snapshot(sess), sess.newContact
	s.finish(ctx, ticket, n, log)

	if err := s.persist(ctx, sess); err != nil {
		log.ErrorContext(ctx, "save chat lead failed", slog.Any("error", err))
	}
}

func (s *Service) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		s.jobs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *Service) Restart(ctx context.Context, telegramID int64, profile, contact, locale string) (Session, error) {
	id, err := s.newID()
	if err != nil {
		return Session{}, fmt.Errorf("generate chat id: %w", err)
	}
	now := s.clock()
	sess := Session{
		PublicID:   id,
		Channel:    ChannelTelegram,
		TelegramID: telegramID,
		Profile:    clip(profile, maxProfileLength),
		Locale:     ParseLocale(locale),
		Lead:       Lead{Contact: clip(contact, maxContactLength)},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.persist(ctx, &sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

func (s *Service) AdminSay(ctx context.Context, id uuid.UUID, text string) (Session, error) {
	text = cleanText(text)
	switch n := utf8.RuneCountInString(text); {
	case n == 0:
		return Session{}, ErrEmptyText
	case n > s.settings.MaxTextLength:
		return Session{}, ErrLongText
	}

	unlock, err := s.lock(ctx, id, lockWait)
	if err != nil {
		return Session{}, err
	}
	defer unlock()

	sess, err := s.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	sess.Turns = append(sess.Turns, Turn{Role: RoleAssistant, Text: text, At: s.clock(), From: FromAdmin})
	sess.Waiting, sess.Question = false, ""
	sess.UpdatedAt = s.clock()
	if err := s.persist(ctx, &sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

func (s *Service) Updates(ctx context.Context, id uuid.UUID, after int) (Updates, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return Updates{}, err
	}
	admin := sess.adminTurns()
	if after < 0 || after > len(admin) {
		after = len(admin)
	}
	return Updates{Messages: admin[after:], Total: len(admin), Waiting: sess.Waiting}, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Session, error) {
	return s.found(s.store.find(ctx, id))
}

func (s *Service) ByID(ctx context.Context, id int64) (Session, error) {
	return s.found(s.store.byID(ctx, id))
}

func (s *Service) found(sess Session, err error) (Session, error) {
	if postgres.IsNoRows(err) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("find chat: %w", err)
	}
	return sess, nil
}

func (s *Service) List(ctx context.Context, f Filter) ([]Session, error) {
	if err := f.normalize(); err != nil {
		return nil, err
	}
	items, err := s.store.list(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	return items, nil
}

func (s *Service) Stats(ctx context.Context, since time.Time) (Stats, error) {
	st, err := s.store.stats(ctx, since)
	if err != nil {
		return Stats{}, fmt.Errorf("chat stats: %w", err)
	}
	return st, nil
}

func (s *Service) Purge(ctx context.Context) (int64, error) {
	n, err := s.store.purge(ctx, s.clock().Add(-s.settings.Retention))
	if err != nil {
		return 0, fmt.Errorf("purge chats: %w", err)
	}
	return n, nil
}

func (s *Service) lock(ctx context.Context, id uuid.UUID, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		if _, taken := s.busy.LoadOrStore(id, struct{}{}); !taken {
			return func() { s.busy.Delete(id) }, nil
		}
		if time.Now().After(deadline) {
			return nil, ErrBusy
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

func (s *Service) announce(ctx context.Context, n Notice, log *slog.Logger) <-chan Ticket {
	done := make(chan Ticket, 1)
	if s.relay == nil {
		done <- Ticket{}
		return done
	}
	go func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.settings.NoticeTimeout)
		defer cancel()
		t, err := s.relay.Incoming(rctx, n)
		if err != nil {
			log.WarnContext(rctx, "chat notice failed", slog.Any("error", err))
		}
		done <- t
	}()
	return done
}

func (s *Service) finish(ctx context.Context, ticket <-chan Ticket, n Notice, log *slog.Logger) {
	t := <-ticket
	if s.relay == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.settings.NoticeTimeout)
	defer cancel()
	if err := s.relay.Answered(rctx, t, n); err != nil {
		log.WarnContext(rctx, "chat notice update failed", slog.Any("error", err))
	}
}

func (s *Service) open(ctx context.Context, in Input) (*Session, error) {
	var (
		sess Session
		err  error
	)
	switch {
	case in.Telegram != 0:
		sess, err = s.store.byTelegram(ctx, in.Telegram)
	case in.Session != uuid.Nil:
		sess, err = s.store.find(ctx, in.Session)
	default:
		err = postgres.ErrNoRows
	}
	if err == nil {
		if in.Profile != "" {
			sess.Profile = in.Profile
		}
		return &sess, nil
	}
	if !postgres.IsNoRows(err) {
		return nil, fmt.Errorf("find chat: %w", err)
	}

	id, err := s.newID()
	if err != nil {
		return nil, fmt.Errorf("generate chat id: %w", err)
	}
	now := s.clock()
	channel := ChannelSite
	if in.Telegram != 0 {
		channel = ChannelTelegram
	}
	return &Session{
		PublicID:   id,
		Channel:    channel,
		TelegramID: in.Telegram,
		Profile:    in.Profile,
		Locale:     in.Locale,
		Page:       in.Page,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

func (s *Service) persist(ctx context.Context, sess *Session) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistBudget)
	defer cancel()

	if sess.ID == 0 {
		if err := s.store.create(ctx, sess); err != nil {
			return fmt.Errorf("create chat: %w", err)
		}
		return nil
	}
	if err := s.store.save(ctx, sess); err != nil {
		return fmt.Errorf("save chat: %w", err)
	}
	return nil
}

func (s *Service) answer(ctx context.Context, sess *Session, out Emitter) (string, error) {
	var reply strings.Builder
	_, err := s.model.Stream(ctx, llm.Request{Messages: s.history(ctx, sess)}, func(delta string) {
		reply.WriteString(delta)
		out.Delta(delta)
	})
	if err != nil && reply.Len() == 0 {
		return "", fmt.Errorf("ask model: %w", err)
	}

	text := strings.TrimSpace(reply.String())
	if text == "" {
		text = fallback(sess)
		out.Delta(text)
	}
	return text, nil
}

func (s *Service) extract(ctx context.Context, sess Session) []llm.ToolCall {
	res, err := s.model.Stream(ctx, llm.Request{
		Messages:   extractMessages(&sess, s.settings.HistoryTurns),
		Tools:      tools,
		ToolChoice: llm.Require(saveLeadTool),
	}, func(string) {})
	if err != nil {
		logger.From(ctx).WarnContext(ctx, "chat extraction failed", slog.Any("error", err))
	}
	return res.ToolCalls
}

func (s *Service) history(ctx context.Context, sess *Session) []llm.Message {
	turns := sess.Turns
	if len(turns) > s.settings.HistoryTurns {
		turns = turns[len(turns)-s.settings.HistoryTurns:]
	}

	var custom []string
	if s.rules != nil {
		list, err := s.rules.Texts(ctx)
		if err != nil {
			logger.From(ctx).WarnContext(ctx, "load assistant rules failed", slog.Any("error", err))
		}
		custom = list
	}

	msgs := make([]llm.Message, 0, len(turns)+1)
	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt(sess, custom, s.clock())})
	for _, t := range turns {
		msgs = append(msgs, llm.Message{Role: t.Role, Content: t.Text})
	}
	return msgs
}

func (s *Service) clock() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

func snapshot(sess *Session) Session {
	c := *sess
	c.Turns = slices.Clone(sess.Turns)
	return c
}

type leadArgs struct {
	Contact  string `json:"contact"`
	Name     string `json:"name"`
	Project  string `json:"project"`
	Budget   string `json:"budget"`
	Deadline string `json:"deadline"`
	Question string `json:"question_for_admin"`
}

type applied struct {
	question string
}

func apply(l *Lead, call llm.ToolCall) applied {
	if call.Function.Name != saveLeadTool {
		return applied{}
	}

	var args leadArgs
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return applied{}
	}

	if contact := parseContact(args.Contact); contact != "" && l.Contact == "" {
		l.Contact = contact
	}
	for _, f := range []struct {
		dst   *string
		value string
		max   int
	}{
		{&l.Name, args.Name, maxNameLength},
		{&l.Project, args.Project, maxProjectLength},
		{&l.Budget, args.Budget, maxBudgetLength},
		{&l.Deadline, args.Deadline, maxDeadlineLength},
	} {
		if v := clip(f.value, f.max); v != "" {
			*f.dst = v
		}
	}
	return applied{question: clip(args.Question, maxQuestionLength)}
}

func fallback(sess *Session) string {
	known := sess.Lead.Contact != ""
	switch {
	case sess.Locale == LocaleEN && known:
		return "Thank you, Sanzhar has your contact and will write to you within a working day."
	case sess.Locale == LocaleEN:
		return "Tell me a bit more about your project, and I will suggest how Sanzhar can help."
	case sess.Locale == LocaleZH && known:
		return "谢谢，Sanzhar 已收到您的联系方式，会在一个工作日内与您联系。"
	case sess.Locale == LocaleZH:
		return "请再详细介绍一下您的项目，我会告诉您 Sanzhar 能怎样帮助您。"
	case known:
		return "Спасибо, Санжар получил ваш контакт и напишет в течение рабочего дня."
	default:
		return "Расскажите чуть подробнее о задаче — подскажу, чем Санжар может помочь."
	}
}
