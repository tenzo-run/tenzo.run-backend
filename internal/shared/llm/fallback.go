package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	probeText    = "ping"
	probeTimeout = 15 * time.Second
	fastProbe    = 10 * time.Second
	recoverAfter = 3

	fromPrimary = 0
	fromBackup  = 1
	nobody      = -1
)

var (
	errEmptyAnswer = errors.New("empty answer")
	errSlow        = errors.New("primary model is slower than the hedge delay")
)

type Streamer interface {
	Stream(ctx context.Context, r Request, onDelta func(string)) (Result, error)
}

type Fallback struct {
	primary Streamer
	backup  Streamer
	hedge   time.Duration
	check   time.Duration
	log     *slog.Logger

	mu      sync.Mutex
	down    bool
	healthy int
}

func NewFallback(primary, backup Streamer, hedge, check time.Duration, log *slog.Logger) *Fallback {
	return &Fallback{primary: primary, backup: backup, hedge: hedge, check: check, log: log}
}

type outcome struct {
	res  Result
	err  error
	from int
}

type race struct {
	mu    sync.Mutex
	owner int
}

func (r *race) claim(who int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owner == nobody {
		r.owner = who
	}
	return r.owner == who
}

func (r *race) owns(who int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owner == who
}

func (f *Fallback) Stream(ctx context.Context, r Request, onDelta func(string)) (Result, error) {
	if f.isDown() {
		return f.viaBackup(ctx, r, onDelta)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	won := &race{owner: nobody}
	results := make(chan outcome, 2)
	start := func(who int, s Streamer) {
		go func() {
			res, err := s.Stream(ctx, r, func(d string) {
				if won.claim(who) {
					onDelta(d)
				}
			})
			if err == nil && res.Content == "" && len(res.ToolCalls) == 0 {
				err = errEmptyAnswer
			}
			results <- outcome{res: res, err: err, from: who}
		}()
	}

	start(fromPrimary, f.primary)
	hedge := time.NewTimer(f.hedge)
	defer hedge.Stop()
	backupStarted, pending := false, 1
	startBackup := func(reason error) {
		if !backupStarted {
			backupStarted = true
			pending++
			f.markDown(ctx, reason)
			start(fromBackup, f.backup)
		}
	}

	var errs []error
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-hedge.C:
			if !won.owns(fromPrimary) {
				startBackup(errSlow)
			}
		case o := <-results:
			pending--
			switch {
			case o.err == nil && won.claim(o.from):
				return o.res, nil
			case o.err != nil && won.owns(o.from):
				return o.res, o.err
			case o.err != nil:
				errs = append(errs, o.err)
				if o.from == fromPrimary && ctx.Err() == nil {
					startBackup(o.err)
				}
			}
			if pending == 0 {
				return Result{}, fmt.Errorf("both models failed: %w", errors.Join(errs...))
			}
		}
	}
}

func (f *Fallback) viaBackup(ctx context.Context, r Request, onDelta func(string)) (Result, error) {
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
		if f.isDown() {
			f.recheck(ctx)
		}
	}
}

func (f *Fallback) recheck(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	started := time.Now()
	err := f.Probe(pctx)
	cancel()

	f.mu.Lock()
	if err == nil && time.Since(started) <= fastProbe {
		f.healthy++
	} else {
		f.healthy = 0
	}
	stable := f.healthy >= recoverAfter
	f.mu.Unlock()
	if stable {
		f.markUp(ctx)
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
	f.down, f.healthy = true, 0
	f.mu.Unlock()
	if !was {
		f.log.WarnContext(ctx, "primary model failed, switching to backup", slog.Any("error", err), slog.Duration("check_every", f.check))
	}
}

func (f *Fallback) markUp(ctx context.Context) {
	f.mu.Lock()
	was := f.down
	f.down, f.healthy = false, 0
	f.mu.Unlock()
	if was {
		f.log.InfoContext(ctx, "primary model is back")
	}
}
