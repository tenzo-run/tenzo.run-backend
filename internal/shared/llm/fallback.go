package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

var errEmptyAnswer = errors.New("empty answer")

type Streamer interface {
	Stream(ctx context.Context, r Request, onDelta func(string)) (Result, error)
}

type Fallback struct {
	primary  Streamer
	backup   Streamer
	cooldown time.Duration
	log      *slog.Logger
	now      func() time.Time

	mu    sync.Mutex
	until time.Time
}

func NewFallback(primary, backup Streamer, cooldown time.Duration, log *slog.Logger) *Fallback {
	return &Fallback{primary: primary, backup: backup, cooldown: cooldown, log: log, now: time.Now}
}

func (f *Fallback) Stream(ctx context.Context, r Request, onDelta func(string)) (Result, error) {
	if !f.tripped() {
		sent := false
		res, err := f.primary.Stream(ctx, r, func(d string) {
			sent = true
			onDelta(d)
		})
		if err == nil && !sent && res.Content == "" && len(res.ToolCalls) == 0 {
			err = errEmptyAnswer
		}
		if err == nil {
			f.recover(ctx)
			return res, nil
		}
		if sent || ctx.Err() != nil {
			return res, err
		}
		f.trip(ctx, err)
	}

	res, err := f.backup.Stream(ctx, r, onDelta)
	if err != nil {
		return res, fmt.Errorf("backup model: %w", err)
	}
	return res, nil
}

func (f *Fallback) tripped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now().Before(f.until)
}

func (f *Fallback) trip(ctx context.Context, err error) {
	f.mu.Lock()
	first := f.until.IsZero()
	f.until = f.now().Add(f.cooldown)
	f.mu.Unlock()
	if first {
		f.log.WarnContext(ctx, "primary model failed, switching to backup", slog.Any("error", err), slog.Duration("for", f.cooldown))
	}
}

func (f *Fallback) recover(ctx context.Context) {
	f.mu.Lock()
	was := !f.until.IsZero()
	f.until = time.Time{}
	f.mu.Unlock()
	if was {
		f.log.InfoContext(ctx, "primary model is back")
	}
}
