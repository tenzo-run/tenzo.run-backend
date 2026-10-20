package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	cliStderrBytes = 2 << 10
	cliPromptFile  = "prompt-*.txt"
	cliToolPrefix  = "cli_"
	cliTokenEnv    = "CLAUDE_CODE_OAUTH_TOKEN"
)

const cliFrame = `

The conversation so far is in the user message as a transcript of <user>, <assistant>, <tool_call> and <tool_result> blocks. Write only the next assistant message: plain text in the same language, without tags and without mentioning the transcript.`

const cliToolsFrame = `

You can use these tools:
%s
Answer with the JSON object only. To call tools, list them in "tool_calls" with their arguments; their results will come back in the transcript. When no tool is needed, put the message for the user in "content".`

const cliForcedFrame = `

Answer only by filling the arguments of the tool %q: %s`

type CLISettings struct {
	Command  string
	Model    string
	Effort   string
	Token    string
	Dir      string
	Timeout  time.Duration
	Parallel int
}

type CLI struct {
	s     CLISettings
	slots chan struct{}
}

func NewCLI(s CLISettings) *CLI {
	return &CLI{s: s, slots: make(chan struct{}, max(1, s.Parallel))}
}

type cliLine struct {
	Type  string `json:"type"`
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	IsError    bool            `json:"is_error"`
	Result     string          `json:"result"`
	Structured json.RawMessage `json:"structured_output"`
}

type cliAnswer struct {
	Content   string `json:"content"`
	ToolCalls []struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"tool_calls"`
}

type cliPlan struct {
	system string
	input  string
	schema json.RawMessage
	forced string
}

func (c *CLI) Stream(ctx context.Context, r Request, onDelta func(string)) (Result, error) {
	plan, err := planCLI(r)
	if err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.s.Timeout)
	defer cancel()

	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return Result{}, fmt.Errorf("claude is busy: %w", ctx.Err())
	}

	prompt, err := os.CreateTemp(c.s.Dir, cliPromptFile)
	if err != nil {
		return Result{}, fmt.Errorf("claude prompt file: %w", err)
	}
	defer os.Remove(prompt.Name())
	_, werr := prompt.WriteString(plan.system)
	if cerr := prompt.Close(); werr != nil || cerr != nil {
		return Result{}, fmt.Errorf("claude prompt file: %w", errors.Join(werr, cerr))
	}

	cmd := exec.CommandContext(ctx, c.s.Command, c.args(plan, prompt.Name())...)
	cmd.Dir = c.s.Dir
	cmd.Env = c.env()
	cmd.Stdin = strings.NewReader(plan.input)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("claude stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start claude: %w", err)
	}

	res, readErr := readCLI(stdout, plan, onDelta)
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	switch {
	case readErr != nil:
		return res, readErr
	case ctx.Err() != nil:
		return res, fmt.Errorf("claude: %w", ctx.Err())
	case waitErr != nil:
		return res, fmt.Errorf("claude exited: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return res, nil
}

func (c *CLI) args(p cliPlan, promptFile string) []string {
	args := []string{
		"-p",
		"--model", c.s.Model,
		"--tools", "",
		"--setting-sources", "",
		"--strict-mcp-config",
		"--no-session-persistence",
		"--disable-slash-commands",
		"--system-prompt-file", promptFile,
	}
	if c.s.Effort != "" {
		args = append(args, "--effort", c.s.Effort)
	}
	if p.schema != nil {
		return append(args, "--output-format", "json", "--json-schema", string(p.schema))
	}
	return append(args, "--output-format", "stream-json", "--verbose", "--include-partial-messages")
}

func (c *CLI) env() []string {
	env := []string{
		"HOME=" + c.s.Dir,
		"TMPDIR=" + c.s.Dir,
		"PATH=" + os.Getenv("PATH"),
		"LANG=C.UTF-8",
		"DISABLE_AUTOUPDATER=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	}
	if c.s.Token != "" {
		env = append(env, cliTokenEnv+"="+c.s.Token)
	}
	return env
}

func planCLI(r Request) (cliPlan, error) {
	var system []string
	var input strings.Builder
	for _, m := range r.Messages {
		switch m.Role {
		case "system":
			system = append(system, m.Content)
		case "user":
			block(&input, "user", "", m.Content)
		case "assistant":
			if strings.TrimSpace(m.Content) != "" {
				block(&input, "assistant", "", m.Content)
			}
			for _, call := range m.ToolCalls {
				block(&input, "tool_call", call.Function.Name, call.Function.Arguments)
			}
		case "tool":
			block(&input, "tool_result", "", m.Content)
		}
	}
	p := cliPlan{system: strings.Join(system, "\n\n") + cliFrame, input: input.String()}
	if p.input == "" {
		return cliPlan{}, errors.New("claude: empty conversation")
	}

	if name := requiredTool(r.ToolChoice); name != "" {
		for _, t := range r.Tools {
			if t.Function.Name == name {
				p.forced, p.schema = name, t.Function.Parameters
				p.system += fmt.Sprintf(cliForcedFrame, name, t.Function.Description)
				return p, nil
			}
		}
		return cliPlan{}, fmt.Errorf("claude: unknown required tool %q", name)
	}
	if len(r.Tools) == 0 {
		return p, nil
	}

	names := make([]string, 0, len(r.Tools))
	var list strings.Builder
	for _, t := range r.Tools {
		names = append(names, t.Function.Name)
		fmt.Fprintf(&list, "- %s: %s Arguments schema: %s\n", t.Function.Name, t.Function.Description, compactJSON(t.Function.Parameters))
	}
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]string{"type": "string"},
			"tool_calls": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":      map[string]any{"type": "string", "enum": names},
						"arguments": map[string]string{"type": "object"},
					},
					"required": []string{"name", "arguments"},
				},
			},
		},
	})
	p.schema = schema
	p.system += fmt.Sprintf(cliToolsFrame, list.String())
	return p, nil
}

func block(b *strings.Builder, tag, name, text string) {
	b.WriteString("<" + tag)
	if name != "" {
		b.WriteString(" name=" + strconv.Quote(name))
	}
	b.WriteString(">\n" + strings.TrimSpace(text) + "\n</" + tag + ">\n\n")
}

func requiredTool(choice any) string {
	m, ok := choice.(map[string]any)
	if !ok {
		return ""
	}
	fn, ok := m["function"].(map[string]string)
	if !ok {
		return ""
	}
	return fn["name"]
}

func readCLI(r io.Reader, p cliPlan, onDelta func(string)) (Result, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	var text strings.Builder
	for sc.Scan() {
		var line cliLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		switch {
		case line.Type == "stream_event" && line.Event.Type == "content_block_delta" && line.Event.Delta.Type == "text_delta":
			text.WriteString(line.Event.Delta.Text)
			onDelta(line.Event.Delta.Text)
		case line.Type == "result":
			if line.IsError {
				return Result{Content: text.String()}, fmt.Errorf("claude: %s", clipText(line.Result))
			}
			return finishCLI(line, p, text.String(), onDelta)
		}
	}
	if err := sc.Err(); err != nil {
		return Result{Content: text.String()}, fmt.Errorf("read claude: %w", err)
	}
	return Result{Content: text.String()}, errors.New("claude: no result")
}

func finishCLI(line cliLine, p cliPlan, streamed string, onDelta func(string)) (Result, error) {
	if p.schema == nil {
		if streamed == "" && line.Result != "" {
			onDelta(line.Result)
			streamed = line.Result
		}
		return Result{Content: streamed}, nil
	}

	structured := line.Structured
	if len(structured) == 0 || string(structured) == "null" {
		structured = json.RawMessage(line.Result)
	}
	if !json.Valid(structured) {
		return Result{}, errors.New("claude: structured output is not json")
	}
	if p.forced != "" {
		return Result{ToolCalls: []ToolCall{{
			ID: cliToolPrefix + "1", Type: "function",
			Function: FunctionCall{Name: p.forced, Arguments: string(structured)},
		}}}, nil
	}

	var a cliAnswer
	if err := json.Unmarshal(structured, &a); err != nil {
		return Result{}, fmt.Errorf("claude: decode answer: %w", err)
	}
	res := Result{Content: strings.TrimSpace(a.Content)}
	for i, call := range a.ToolCalls {
		args := string(call.Arguments)
		if args == "" || args == "null" {
			args = "{}"
		}
		res.ToolCalls = append(res.ToolCalls, ToolCall{
			ID: cliToolPrefix + strconv.Itoa(i+1), Type: "function",
			Function: FunctionCall{Name: call.Name, Arguments: args},
		})
	}
	if len(res.ToolCalls) == 0 && res.Content != "" {
		onDelta(res.Content)
	}
	return res, nil
}

func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

func clipText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxErrorBytes {
		return s[:maxErrorBytes]
	}
	return s
}

type limitedBuffer struct {
	bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := cliStderrBytes - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
