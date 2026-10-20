package rules

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"tenzo-run-backend/internal/shared/apperr"
	"tenzo-run-backend/internal/shared/postgres"
)

const (
	maxTextLength = 500
	maxRules      = 60
)

var (
	ErrEmpty    = apperr.Invalid("rule is empty")
	ErrTooLong  = apperr.Invalid("rule is too long")
	ErrTooMany  = apperr.Conflict("too many rules, remove some first")
	ErrNotFound = apperr.NotFound("rule not found")
)

type Rule struct {
	ID        int64
	Text      string
	CreatedBy string
	CreatedAt time.Time
}

type Repository struct {
	db postgres.DB
}

func NewRepository(db postgres.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) list(ctx context.Context) ([]Rule, error) {
	rows, err := r.db.Query(ctx, `select id, text, created_by, created_at from assistant_rules order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		var rule Rule
		if err := rows.Scan(&rule.ID, &rule.Text, &rule.CreatedBy, &rule.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

func (r *Repository) add(ctx context.Context, rule Rule) (Rule, error) {
	err := r.db.QueryRow(ctx, `
		insert into assistant_rules (text, created_by, created_at)
		select $1, $2, $3 where (select count(*) from assistant_rules) < $4
		returning id`, rule.Text, rule.CreatedBy, rule.CreatedAt, maxRules).Scan(&rule.ID)
	if postgres.IsNoRows(err) {
		return Rule{}, ErrTooMany
	}
	return rule, err
}

func (r *Repository) remove(ctx context.Context, id int64) error {
	tag, err := r.db.Exec(ctx, `delete from assistant_rules where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type Store interface {
	list(ctx context.Context) ([]Rule, error)
	add(ctx context.Context, rule Rule) (Rule, error)
	remove(ctx context.Context, id int64) error
}

type Service struct {
	store Store
	now   func() time.Time

	mu     sync.Mutex
	cached []Rule
	loaded bool
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

func (s *Service) List(ctx context.Context) ([]Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return append([]Rule(nil), s.cached...), nil
	}
	list, err := s.store.list(ctx)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	s.cached, s.loaded = list, true
	return append([]Rule(nil), list...), nil
}

func (s *Service) Texts(ctx context.Context) ([]string, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, r := range list {
		out = append(out, r.Text)
	}
	return out, nil
}

func (s *Service) Add(ctx context.Context, text, by string) (Rule, error) {
	text = strings.Join(strings.Fields(text), " ")
	switch n := utf8.RuneCountInString(text); {
	case n == 0:
		return Rule{}, ErrEmpty
	case n > maxTextLength:
		return Rule{}, ErrTooLong
	}

	rule, err := s.store.add(ctx, Rule{Text: text, CreatedBy: strings.TrimSpace(by), CreatedAt: s.now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		if err == ErrTooMany {
			return Rule{}, err
		}
		return Rule{}, fmt.Errorf("add rule: %w", err)
	}
	s.invalidate()
	return rule, nil
}

func (s *Service) Remove(ctx context.Context, id int64) error {
	if err := s.store.remove(ctx, id); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("remove rule: %w", err)
	}
	s.invalidate()
	return nil
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.loaded = false
	s.cached = nil
	s.mu.Unlock()
}
