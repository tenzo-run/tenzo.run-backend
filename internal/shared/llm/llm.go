package llm

import (
	"bufio"
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
	maxErrorBytes = 2 << 10
	maxLineBytes  = 1 << 20
	dataPrefix    = "data:"
	doneMarker    = "[DONE]"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Request struct {
	Messages   []Message
	Tools      []Tool
	ToolChoice any
}

func Require(tool string) any {
	return map[string]any{"type": "function", "function": map[string]string{"name": tool}}
}

type Result struct {
	Content   string
	ToolCalls []ToolCall
}

type Settings struct {
	BaseURL     string
	APIKey      string
	Model       string
	Timeout     time.Duration
	Temperature float64
	MaxTokens   int
}

type Client struct {
	endpoint string
	settings Settings
	http     *http.Client
}

func New(s Settings) *Client {
	return &Client{
		endpoint: strings.TrimRight(s.BaseURL, "/") + "/chat/completions",
		settings: s,
		http:     &http.Client{},
	}
}

type body struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  any       `json:"tool_choice,omitempty"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

type chunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) Stream(ctx context.Context, r Request, onDelta func(string)) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.settings.Timeout)
	defer cancel()

	payload, err := json.Marshal(body{
		Model:       c.settings.Model,
		Messages:    r.Messages,
		Tools:       r.Tools,
		ToolChoice:  r.ToolChoice,
		Temperature: c.settings.Temperature,
		MaxTokens:   c.settings.MaxTokens,
		Stream:      true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("encode llm request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, errors.New("build llm request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.settings.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return Result{}, fmt.Errorf("llm request: %w", urlErr.Err)
		}
		return Result{}, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		return Result{}, fmt.Errorf("llm answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	return read(resp.Body, onDelta)
}

func read(r io.Reader, onDelta func(string)) (Result, error) {
	var (
		content strings.Builder
		calls   []ToolCall
	)

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxLineBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, dataPrefix) {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, dataPrefix))
		if data == doneMarker {
			break
		}

		var ch chunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			continue
		}
		if ch.Error != nil {
			return Result{}, fmt.Errorf("llm stream error: %s", ch.Error.Code)
		}

		for _, choice := range ch.Choices {
			if text := choice.Delta.Content; text != "" {
				content.WriteString(text)
				onDelta(text)
			}
			for _, tc := range choice.Delta.ToolCalls {
				for len(calls) <= tc.Index {
					calls = append(calls, ToolCall{Type: "function"})
				}
				call := &calls[tc.Index]
				if tc.ID != "" {
					call.ID = tc.ID
				}
				call.Function.Name += tc.Function.Name
				call.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Result{}, fmt.Errorf("read llm stream: %w", err)
	}

	return Result{Content: content.String(), ToolCalls: calls}, nil
}
