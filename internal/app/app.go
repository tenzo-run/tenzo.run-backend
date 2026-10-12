package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"tenzo-run-backend/internal/config"
	"tenzo-run-backend/internal/lead"
	"tenzo-run-backend/internal/shared/httpx"
	"tenzo-run-backend/internal/shared/postgres"
	"tenzo-run-backend/internal/shared/ratelimit"
	"tenzo-run-backend/internal/shared/telegram"
)

const (
	Version = "1.0.0"

	limiterCleanupInterval = time.Minute
)

type services struct {
	leads       *lead.Service
	leadLimiter *ratelimit.Limiter
	telegram    *telegram.Bot
}

func newServices(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) *services {
	bot := telegram.New(cfg.Telegram.APIURL, cfg.Telegram.BotToken, cfg.Telegram.ChatID, log)

	return &services{
		leads:       lead.NewService(lead.NewRepository(pool), bot),
		leadLimiter: ratelimit.New(cfg.Limits.LeadsPerIP.Events, cfg.Limits.LeadsPerIP.Per),
		telegram:    bot,
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

	handler, err := newRouter(cfg, log, pool, svc)
	if err != nil {
		return err
	}

	srv := httpx.NewServer(cfg.HTTP, handler, log)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Serve(gctx, srv, cfg.HTTP.ShutdownTimeout, log) })
	g.Go(func() error { svc.leadLimiter.Run(gctx, limiterCleanupInterval); return nil })
	g.Go(func() error { svc.telegram.Run(gctx); return nil })

	return g.Wait()
}
