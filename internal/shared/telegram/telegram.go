package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	queueSize   = 256
	sendTimeout = 10 * time.Second
	maxText     = 4000
)

type Bot struct {
	endpoint string
	chatID   string
	client   *http.Client
	queue    chan string
	retry    []time.Duration
	log      *slog.Logger
}

func New(apiURL, token, chatID string, log *slog.Logger) *Bot {
	if token == "" || chatID == "" {
		return nil
	}
	return &Bot{
		endpoint: strings.TrimRight(apiURL, "/") + "/bot" + token + "/sendMessage",
		chatID:   chatID,
		client:   &http.Client{Timeout: sendTimeout},
		queue:    make(chan string, queueSize),
		retry:    []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second},
		log:      log,
	}
}

func (b *Bot) Send(text string) {
	if b == nil {
		return
	}
	select {
	case b.queue <- truncate(text):
	default:
		b.log.Warn("telegram queue is full, message dropped")
	}
}

func (b *Bot) Run(ctx context.Context) {
	if b == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-b.queue:
			b.deliver(ctx, text)
		}
	}
}

func (b *Bot) deliver(ctx context.Context, text string) {
	for attempt := 0; ; attempt++ {
		err := b.post(ctx, text)
		if err == nil {
			return
		}
		if attempt == len(b.retry) {
			b.log.WarnContext(ctx, "telegram message not sent", slog.Any("error", err))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(b.retry[attempt]):
		}
	}
}

func (b *Bot) post(ctx context.Context, text string) error {
	payload, err := json.Marshal(map[string]any{
		"chat_id":              b.chatID,
		"text":                 text,
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("build telegram request")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return urlErr.Err
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram answered %d: %s", resp.StatusCode, body)
	}
	return nil
}

func truncate(text string) string {
	runes := []rune(text)
	if len(runes) <= maxText {
		return text
	}
	return string(runes[:maxText]) + "…"
}
