package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tenzo-run-backend/internal/config"
)

const (
	applicationName = "tenzo-run-backend"
	connectAttempts = 10
	maxConnectDelay = 10 * time.Second
	pingTimeout     = 3 * time.Second
)

type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func Connect(ctx context.Context, cfg config.Database, log *slog.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnLifetimeJitter = cfg.MaxConnLifetime / 10
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	params := poolCfg.ConnConfig.RuntimeParams
	params["application_name"] = applicationName
	params["timezone"] = "UTC"
	params["statement_timeout"] = strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
	params["idle_in_transaction_session_timeout"] = strconv.FormatInt((cfg.StatementTimeout * 2).Milliseconds(), 10)

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := waitReady(ctx, pool, log); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}

func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	return pool.Ping(ctx)
}

func waitReady(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	delay := 500 * time.Millisecond

	for attempt := 1; ; attempt++ {
		err := Ping(ctx, pool)
		if err == nil {
			return nil
		}
		if attempt == connectAttempts {
			return fmt.Errorf("database is unreachable: %w", err)
		}

		log.WarnContext(ctx, "database is not ready", slog.Int("attempt", attempt), slog.Any("error", err))

		if err := sleep(ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, maxConnectDelay)
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
