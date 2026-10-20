package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const probeText = "ping"

var errEmptyAnswer = errors.New("empty answer")

type Streamer interface {
	Stream(ctx context.Context, r Request, onDelta func(string)) (Result, error)
}

type Fallback struct {
	primary Streamer
	backup  Streamer
	check   time.Duration
	log     *slog.Logger

	mu   sync.Mutex
	down bool
}

func NewFallback(primary, backup Streamer, check time.Duration, log *slog.Logger) *Fallback {
	return &Fallback{primary: primary, backup: backup, check: check, log: log}
}

func (f *Fallback) Stream(ctx context.Context, r Request, onDelta func(string)) (Result, error) {
	if !f.isDown() {
		sent := false
		res, err := f.primary.Stream(ctx, r, func(d string) {
			sent = true
			onDelta(d)
		})
		if err == nil && !sent && res.Content == "" && len(res.ToolCalls) == 0 {
			err = errEmptyAnswer
		}
		if err == nil {
			return res, nil
		}
		if sent || ctx.Err() != nil {
			return res, err
		}
		f.markDown(ctx, err)
	}

	res, err := f.backup.Stream(ctx, r, onDelta)
	if err != nil {
		return res, fmt.Errorf("backup model: %w", err)
	}
	return res, nil
}

func (f *Fallback) Run(ctx context.Context) {
	t := time.NewTicker(f.check)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if f.isDown() && f.Probe(ctx) == nil {
			f.markUp(ctx)
		}
	}
}

func (f *Fallback) Probe(ctx context.Context) error {
	res, err := f.primary.Stream(ctx, Request{Messages: []Message{{Role: "user", Content: probeText}}}, func(string) {})
	if err == nil && res.Content == "" && len(res.ToolCalls) == 0 {
		err = errEmptyAnswer
	}
	return err
}

func (f *Fallback) isDown() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.down
}

func (f *Fallback) markDown(ctx context.Context, err error) {
	f.mu.Lock()
	was := f.down
	f.down = true
	f.mu.Unlock()
	if !was {
		f.log.WarnContext(ctx, "primary model failed, switching to backup", slog.Any("error", err), slog.Duration("check_every", f.check))
	}
}

func (f *Fallback) markUp(ctx context.Context) {
	f.mu.Lock()
	was := f.down
	f.down = false
	f.mu.Unlock()
	if was {
		f.log.InfoContext(ctx, "primary model is back")
	}
}
