package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"tenzo-run-backend/migrations"
)

type Migrator struct {
	provider *goose.Provider
	log      *slog.Logger
}

func NewMigrator(databaseURL string, log *slog.Logger) (*Migrator, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create migration locker: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create migration provider: %w", err)
	}

	return &Migrator{provider: provider, log: log}, nil
}

func (m *Migrator) Close() error {
	return m.provider.Close()
}

func (m *Migrator) Up(ctx context.Context) error {
	results, err := m.provider.Up(ctx)
	m.report(ctx, results...)
	if err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

func (m *Migrator) Down(ctx context.Context) error {
	result, err := m.provider.Down(ctx)
	if result != nil {
		m.report(ctx, result)
	}
	if err != nil {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}

func (m *Migrator) Status(ctx context.Context) error {
	statuses, err := m.provider.Status(ctx)
	if err != nil {
		return fmt.Errorf("migration status: %w", err)
	}

	for _, s := range statuses {
		m.log.InfoContext(ctx, "migration",
			slog.Int64("version", s.Source.Version),
			slog.String("file", s.Source.Path),
			slog.String("state", string(s.State)),
		)
	}
	return nil
}

func (m *Migrator) report(ctx context.Context, results ...*goose.MigrationResult) {
	for _, r := range results {
		m.log.InfoContext(ctx, "migration applied",
			slog.Int64("version", r.Source.Version),
			slog.String("file", r.Source.Path),
			slog.String("direction", r.Direction),
			slog.Duration("duration", r.Duration),
		)
	}
}

func Migrate(ctx context.Context, databaseURL string, log *slog.Logger) error {
	m, err := NewMigrator(databaseURL, log)
	if err != nil {
		return err
	}
	defer m.Close()

	return m.Up(ctx)
}
