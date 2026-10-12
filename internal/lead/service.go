package lead

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"tenzo-run-backend/internal/shared/logger"
)

type Store interface {
	insert(ctx context.Context, l Lead) error
	latest(ctx context.Context, limit int) ([]Lead, error)
}

type Notifier interface {
	Send(text string)
}

type Service struct {
	store    Store
	notifier Notifier
	now      func() time.Time
}

func NewService(store Store, notifier Notifier) *Service {
	return &Service{store: store, notifier: notifier, now: time.Now}
}

func (s *Service) Submit(ctx context.Context, d Draft) (uuid.UUID, error) {
	d.normalize()

	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generate lead id: %w", err)
	}

	if d.Spam() {
		logger.From(ctx).InfoContext(ctx, "spam lead discarded")
		return id, nil
	}

	if err := d.Validate(); err != nil {
		return uuid.Nil, err
	}

	l := d.lead(id, s.now().UTC())
	if err := s.store.insert(ctx, l); err != nil {
		return uuid.Nil, fmt.Errorf("insert lead: %w", err)
	}

	s.notify(l)
	return id, nil
}

func (s *Service) Latest(ctx context.Context, limit int) ([]Lead, error) {
	if !validLimit(limit) {
		return nil, ErrInvalidLimit
	}

	leads, err := s.store.latest(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list latest leads: %w", err)
	}
	return leads, nil
}

func (s *Service) notify(l Lead) {
	if s.notifier != nil {
		s.notifier.Send(notice(l))
	}
}
