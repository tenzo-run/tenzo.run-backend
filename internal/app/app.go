package app

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"tenzo-run-backend/internal/admin"
	"tenzo-run-backend/internal/chat"
	"tenzo-run-backend/internal/config"
	"tenzo-run-backend/internal/lead"
	"tenzo-run-backend/internal/rules"
	"tenzo-run-backend/internal/shared/httpx"
	"tenzo-run-backend/internal/shared/llm"
	"tenzo-run-backend/internal/shared/postgres"
	"tenzo-run-backend/internal/shared/ratelimit"
	"tenzo-run-backend/internal/shared/telegram"
)

const (
	Version = "1.1.0"

	limiterCleanupInterval = time.Minute
	chatNoticeTimeout      = 15 * time.Second
	chatPurgeInterval      = time.Hour
	botPollTimeout         = 25 * time.Second
	updatesPerReply        = 20
	fallbackDirMode        = 0o700
)

type services struct {
	leads          *lead.Service
	leadLimiter    *ratelimit.Limiter
	chat           *chat.Service
	chatLimiter    *ratelimit.Limiter
	updatesLimiter *ratelimit.Limiter
	relay          *admin.Relay
	bot            *admin.Bot
	log            *slog.Logger
}

func newServices(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) *services {
	chatRate := cfg.Limits.ChatPerIP
	svc := &services{
		leadLimiter:    ratelimit.New(cfg.Limits.LeadsPerIP.Events, cfg.Limits.LeadsPerIP.Per),
		chatLimiter:    ratelimit.New(chatRate.Events, chatRate.Per),
		updatesLimiter: ratelimit.New(chatRate.Events*updatesPerReply, chatRate.Per),
		log:            log,
	}

	var (
		api     *telegram.Bot
		repo    *admin.Repository
		admins  *admin.Admins
		chatRel chat.Relay
		notify  lead.Notifier
	)
	if tg := cfg.Telegram; tg.Enabled() {
		api = telegram.NewBot(tg.APIURL, tg.BotToken, tg.Timeout, botPollTimeout)
		repo = admin.NewRepository(pool)
		admins = admin.NewAdmins(repo, tg.ChatIDs, tg.Admins)
		svc.relay = admin.NewRelay(api, repo, admins, log)
		chatRel, notify = svc.relay, svc.relay
	}
	svc.leads = lead.NewService(lead.NewRepository(pool), notify)

	if !cfg.AI.Enabled() {
		return svc
	}
	model := newModel(cfg.AI, log)
	consultantRules := rules.NewService(rules.NewRepository(pool))
	svc.chat = chat.NewService(chat.NewRepository(pool), model, chatRel, consultantRules, chat.Settings{
		MaxTurns:      cfg.Chat.MaxTurns,
		HistoryTurns:  cfg.Chat.HistoryTurns,
		MaxTextLength: cfg.Chat.MaxTextLength,
		Retention:     cfg.Chat.Retention(),
		NoticeTimeout: chatNoticeTimeout,
	})
	if api != nil {
		assistant := admin.NewAssistant(model, consultantRules, svc.chat, api)
		svc.bot = admin.NewBot(api, svc.relay, repo, admins, svc.chat, assistant, log)
	}
	return svc
}

func newModel(ai config.AI, log *slog.Logger) llm.Streamer {
	var primary llm.Streamer
	if ai.Primary() {
		primary = llm.New(llm.Settings{
			BaseURL:     ai.BaseURL,
			APIKey:      ai.APIKey,
			Model:       ai.Model,
			Timeout:     ai.Timeout,
			Temperature: ai.Temperature,
			MaxTokens:   ai.MaxTokens,
		})
	}
	f := ai.Fallback
	if !f.Enabled() {
		return primary
	}
	if err := os.MkdirAll(f.Dir, fallbackDirMode); err != nil {
		log.Warn("backup model dir is not available", slog.Any("error", err))
	}
	backup := llm.NewCLI(llm.CLISettings{
		Command: f.Command, Model: f.Model, Effort: f.Effort, Token: f.Token, Dir: f.Dir,
		Timeout: f.Timeout, Parallel: f.Parallel,
	})
	if primary == nil {
		return backup
	}
	return llm.NewFallback(primary, backup, f.Cooldown, log)
}

func (s *services) background(ctx context.Context, g *errgroup.Group) {
	for _, l := range []*ratelimit.Limiter{s.leadLimiter, s.chatLimiter, s.updatesLimiter} {
		g.Go(func() error { l.Run(ctx, limiterCleanupInterval); return nil })
	}
	if s.relay != nil {
		g.Go(func() error { s.relay.Run(ctx); return nil })
	}
	if s.bot != nil {
		g.Go(func() error { s.bot.Run(ctx); return nil })
	}
	if s.chat != nil {
		g.Go(func() error { s.purgeChats(ctx); return nil })
	}
}

func (s *services) purgeChats(ctx context.Context) {
	t := time.NewTicker(chatPurgeInterval)
	defer t.Stop()
	for {
		if n, err := s.chat.Purge(ctx); err != nil {
			s.log.WarnContext(ctx, "purge chats failed", slog.Any("error", err))
		} else if n > 0 {
			s.log.InfoContext(ctx, "old chats purged", slog.Int64("count", n))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if cfg.Database.MigrateOnStart {
		if err := postgres.Migrate(ctx, cfg.Database.URL, log); err != nil {
			return err
		}
	}

	pool, err := postgres.Connect(ctx, cfg.Database, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	svc := newServices(cfg, pool, log)
	log.Info("features",
		slog.Bool("chat", svc.chat != nil),
		slog.Bool("telegram", svc.relay != nil),
		slog.Bool("bot", svc.bot != nil))

	handler, err := newRouter(cfg, log, pool, svc)
	if err != nil {
		return err
	}

	srv := httpx.NewServer(cfg.HTTP, handler, log)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Serve(gctx, srv, cfg.HTTP.ShutdownTimeout, log) })
	svc.background(gctx, g)

	err = g.Wait()
	if svc.chat != nil {
		wctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
		svc.chat.Wait(wctx)
		cancel()
	}
	return err
}
