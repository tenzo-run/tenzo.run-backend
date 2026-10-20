package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxUpdateBytes = 1 << 20
	pollSlack      = 15 * time.Second
	parseModeHTML  = "HTML"
)

type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type Markup struct {
	InlineKeyboard [][]Button `json:"inline_keyboard,omitempty"`
	ForceReply     bool       `json:"force_reply,omitempty"`
	Placeholder    string     `json:"input_field_placeholder,omitempty"`
}

type Outgoing struct {
	Text    string
	Markup  *Markup
	ReplyTo int64
	HTML    bool
}

type User struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
}

func (u User) Name() string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" && u.Username != "" {
		return "@" + u.Username
	}
	return name
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	MessageID      int64    `json:"message_id"`
	From           *User    `json:"from"`
	Chat           Chat     `json:"chat"`
	Text           string   `json:"text"`
	ReplyToMessage *Message `json:"reply_to_message"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type Bot struct {
	base string
	http *http.Client
	poll *http.Client
}

func NewBot(apiURL, token string, timeout, pollTimeout time.Duration) *Bot {
	return &Bot{
		base: strings.TrimRight(apiURL, "/") + "/bot" + token + "/",
		http: &http.Client{Timeout: timeout},
		poll: &http.Client{Timeout: pollTimeout + pollSlack},
	}
}

type sendRequest struct {
	ChatID      int64    `json:"chat_id"`
	MessageID   int64    `json:"message_id,omitempty"`
	Text        string   `json:"text"`
	ParseMode   string   `json:"parse_mode,omitempty"`
	ReplyMarkup *Markup  `json:"reply_markup,omitempty"`
	Preview     preview  `json:"link_preview_options"`
	ReplyTo     *replyTo `json:"reply_parameters,omitempty"`
}

type replyTo struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
}

func (o Outgoing) request(chatID, messageID int64) sendRequest {
	r := sendRequest{
		ChatID:      chatID,
		MessageID:   messageID,
		Text:        truncate(o.Text),
		ReplyMarkup: o.Markup,
		Preview:     preview{Disabled: true},
	}
	if o.HTML {
		r.ParseMode = parseModeHTML
	}
	if o.ReplyTo != 0 {
		r.ReplyTo = &replyTo{MessageID: o.ReplyTo, AllowSendingWithoutReply: true}
	}
	return r
}

func (b *Bot) Send(ctx context.Context, chatID int64, o Outgoing) (int64, error) {
	var res struct {
		MessageID int64 `json:"message_id"`
	}
	if err := b.call(ctx, b.http, "sendMessage", o.request(chatID, 0), &res); err != nil {
		return 0, err
	}
	return res.MessageID, nil
}

func (b *Bot) Edit(ctx context.Context, chatID, messageID int64, o Outgoing) error {
	err := b.call(ctx, b.http, "editMessageText", o.request(chatID, messageID), nil)
	if err != nil && strings.Contains(err.Error(), notModified) {
		return nil
	}
	return err
}

func (b *Bot) Answer(ctx context.Context, callbackID, text string) error {
	return b.call(ctx, b.http, "answerCallbackQuery", map[string]string{"callback_query_id": callbackID, "text": text}, nil)
}

func (b *Bot) Updates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	var updates []Update
	err := b.call(ctx, b.poll, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout / time.Second),
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

func (b *Bot) SetCommands(ctx context.Context, commands []Command) error {
	return b.call(ctx, b.http, "setMyCommands", map[string]any{"commands": commands}, nil)
}

func (b *Bot) Typing(ctx context.Context, chatID int64) error {
	return b.call(ctx, b.http, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, nil)
}

func (b *Bot) call(ctx context.Context, client *http.Client, method string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode telegram %s: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("build telegram request")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("telegram %s: %w", method, urlErr.Err)
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()

	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxUpdateBytes)).Decode(&envelope); err != nil {
		return fmt.Errorf("telegram %s answered %d", method, resp.StatusCode)
	}
	if !envelope.OK {
		return fmt.Errorf("telegram %s answered %d: %s", method, resp.StatusCode, envelope.Description)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("decode telegram %s: %w", method, err)
	}
	return nil
}
