package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var saveTool = Tool{Type: "function", Function: Function{
	Name:        "save_client",
	Description: "Save the client.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"phone":{"type":"string"}}}`),
}}

func TestPlanRendersTranscriptForTextReplies(t *testing.T) {
	p, err := planCLI(Request{Messages: []Message{
		{Role: "system", Content: "Ты консультант."},
		{Role: "user", Content: "Муж пьёт"},
		{Role: "assistant", Content: "Сколько дней?"},
		{Role: "user", Content: " 5 дней "},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if p.schema != nil || p.forced != "" || !strings.HasPrefix(p.system, "Ты консультант.") || !strings.Contains(p.system, "next assistant message") {
		t.Fatalf("plan = %+v", p)
	}
	want := "<user>\nМуж пьёт\n</user>\n\n<assistant>\nСколько дней?\n</assistant>\n\n<user>\n5 дней\n</user>\n\n"
	if p.input != want {
		t.Fatalf("input = %q", p.input)
	}
}

func TestPlanUsesToolSchemaWhenToolIsRequired(t *testing.T) {
	p, err := planCLI(Request{
		Messages:   []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "8707"}},
		Tools:      []Tool{saveTool},
		ToolChoice: Require("save_client"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.forced != "save_client" || string(p.schema) != string(saveTool.Function.Parameters) || !strings.Contains(p.system, `"save_client": Save the client.`) {
		t.Fatalf("plan = %+v", p)
	}

	if _, err := planCLI(Request{Messages: []Message{{Role: "user", Content: "x"}}, ToolChoice: Require("nope")}); err == nil {
		t.Fatal("unknown required tool accepted")
	}
	if _, err := planCLI(Request{Messages: []Message{{Role: "system", Content: "x"}}}); err == nil {
		t.Fatal("empty conversation accepted")
	}
}

func TestPlanDescribesToolsAndRendersToolRounds(t *testing.T) {
	p, err := planCLI(Request{
		Messages: []Message{
			{Role: "user", Content: "сколько клиентов?"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Function: FunctionCall{Name: "stats", Arguments: `{}`}}}},
			{Role: "tool", ToolCallID: "1", Content: `{"clients":3}`},
		},
		Tools: []Tool{saveTool, {Type: "function", Function: Function{Name: "stats", Description: "Stats.", Parameters: json.RawMessage(`{"type": "object"}`)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.input, "<tool_call name=\"stats\">\n{}\n</tool_call>") || !strings.Contains(p.input, "<tool_result>\n{\"clients\":3}\n</tool_result>") {
		t.Fatalf("input = %q", p.input)
	}
	if !strings.Contains(p.system, `- stats: Stats. Arguments schema: {"type":"object"}`) || !strings.Contains(string(p.schema), `"enum":["save_client","stats"]`) {
		t.Fatalf("plan = %s\n%s", p.system, p.schema)
	}
}

const streamOutput = `{"type":"system","subtype":"init"}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Привет! "}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Чем помочь?"}}}
{"type":"assistant"}
{"type":"result","subtype":"success","is_error":false,"result":"Привет! Чем помочь?"}
`

func TestReadStreamsTextDeltas(t *testing.T) {
	var got []string
	res, err := readCLI(strings.NewReader(streamOutput), cliPlan{}, func(d string) { got = append(got, d) })
	if err != nil || res.Content != "Привет! Чем помочь?" || len(got) != 2 {
		t.Fatalf("res = %+v, deltas = %q, err = %v", res, got, err)
	}

	got = nil
	res, err = readCLI(strings.NewReader(`{"type":"result","is_error":false,"result":"Готово"}`+"\n"), cliPlan{}, func(d string) { got = append(got, d) })
	if err != nil || res.Content != "Готово" || len(got) != 1 {
		t.Fatalf("result only = %+v, %q, %v", res, got, err)
	}
}

func TestReadReportsErrorsAndMissingResult(t *testing.T) {
	_, err := readCLI(strings.NewReader(`{"type":"result","is_error":true,"result":"usage limit reached"}`+"\n"), cliPlan{}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "usage limit reached") {
		t.Fatalf("error = %v", err)
	}
	if _, err := readCLI(strings.NewReader("not json\n"), cliPlan{}, func(string) {}); err == nil {
		t.Fatal("missing result accepted")
	}
}

func TestReadTurnsStructuredOutputIntoToolCalls(t *testing.T) {
	forced := cliPlan{schema: saveTool.Function.Parameters, forced: "save_client"}
	res, err := readCLI(strings.NewReader(`{"type":"result","is_error":false,"result":"","structured_output":{"phone":"+77071234567"}}`+"\n"), forced, func(string) {})
	if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "save_client" || res.ToolCalls[0].Function.Arguments != `{"phone":"+77071234567"}` {
		t.Fatalf("forced = %+v, %v", res, err)
	}

	tools := cliPlan{schema: json.RawMessage(`{}`)}
	res, err = readCLI(strings.NewReader(`{"type":"result","is_error":false,"structured_output":{"tool_calls":[{"name":"stats","arguments":null},{"name":"list_rules","arguments":{"x":1}}]}}`+"\n"), tools, func(string) {})
	if err != nil || len(res.ToolCalls) != 2 || res.ToolCalls[0].Function.Arguments != "{}" || res.ToolCalls[1].ID != "cli_2" {
		t.Fatalf("tools = %+v, %v", res, err)
	}

	var deltas []string
	res, err = readCLI(strings.NewReader(`{"type":"result","is_error":false,"result":"{\"content\":\" Готово \"}"}`+"\n"), tools, func(d string) { deltas = append(deltas, d) })
	if err != nil || res.Content != "Готово" || len(deltas) != 1 {
		t.Fatalf("content = %+v, %q, %v", res, deltas, err)
	}
}

func TestEnvDoesNotLeakServiceSecrets(t *testing.T) {
	t.Setenv("TENZO_AI__API_KEY", "secret")
	t.Setenv("ANTHROPIC_API_KEY", "other")
	env := strings.Join(NewCLI(CLISettings{Dir: "/var/lib/tenzo/claude", Token: "tok"}).env(), "\n")
	if strings.Contains(env, "secret") || strings.Contains(env, "ANTHROPIC_API_KEY") || !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=tok") || !strings.Contains(env, "HOME=/var/lib/tenzo/claude") {
		t.Fatalf("env = %s", env)
	}
}

type scripted struct {
	mu     sync.Mutex
	calls  int
	deltas []string
	res    Result
	err    error
}

func (s *scripted) Stream(_ context.Context, _ Request, onDelta func(string)) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	for _, d := range s.deltas {
		onDelta(d)
	}
	return s.res, s.err
}

func newTestFallback(primary, backup Streamer) *Fallback {
	return NewFallback(primary, backup, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestFallbackSwitchesToBackupAndComesBack(t *testing.T) {
	primary := &scripted{err: errors.New("llm answered 503")}
	backup := &scripted{deltas: []string{"ok"}, res: Result{Content: "ok"}}
	f := newTestFallback(primary, backup)

	var got strings.Builder
	res, err := f.Stream(t.Context(), Request{}, func(d string) { got.WriteString(d) })
	if err != nil || res.Content != "ok" || got.String() != "ok" || primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("res = %+v, err = %v, calls %d/%d", res, err, primary.calls, backup.calls)
	}

	if _, err := f.Stream(t.Context(), Request{}, func(string) {}); err != nil || primary.calls != 1 || backup.calls != 2 {
		t.Fatalf("while down: calls %d/%d, %v", primary.calls, backup.calls, err)
	}

	if err := f.Probe(t.Context()); err == nil {
		t.Fatal("probe of a dead primary must fail")
	}
	primary.mu.Lock()
	primary.err, primary.res = nil, Result{Content: "снова"}
	primary.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for f.isDown() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if res, _ := f.Stream(t.Context(), Request{}, func(string) {}); res.Content != "снова" || backup.calls != 2 {
		t.Fatalf("recovered: %+v, backup calls %d", res, backup.calls)
	}
}

func TestFallbackKeepsPartialAnswersAndCancellations(t *testing.T) {
	primary := &scripted{deltas: []string{"Здрав"}, err: errors.New("stream broke")}
	backup := &scripted{res: Result{Content: "x"}}
	f := newTestFallback(primary, backup)
	if _, err := f.Stream(t.Context(), Request{}, func(string) {}); err == nil || backup.calls != 0 {
		t.Fatalf("partial answer was retried: %v, backup %d", err, backup.calls)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	primary.deltas = nil
	if _, err := f.Stream(ctx, Request{}, func(string) {}); err == nil || backup.calls != 0 {
		t.Fatalf("canceled request was retried: %v", err)
	}
}

func TestFallbackTreatsEmptyAnswerAsFailure(t *testing.T) {
	primary := &scripted{}
	backup := &scripted{res: Result{ToolCalls: []ToolCall{{ID: "cli_1"}}}}
	f := newTestFallback(primary, backup)
	res, err := f.Stream(t.Context(), Request{}, func(string) {})
	if err != nil || len(res.ToolCalls) != 1 || backup.calls != 1 {
		t.Fatalf("res = %+v, %v", res, err)
	}

	backup.err = errors.New("limit")
	if _, err := f.Stream(t.Context(), Request{}, func(string) {}); err == nil || !strings.Contains(err.Error(), "backup model") {
		t.Fatalf("error = %v", err)
	}
}

func TestCLIAgainstInstalledClaude(t *testing.T) {
	bin := os.Getenv("TENZO_TEST_CLAUDE_BIN")
	if bin == "" {
		t.Skip("TENZO_TEST_CLAUDE_BIN is not set")
	}
	c := NewCLI(CLISettings{Command: bin, Model: "claude-sonnet-5-5", Effort: "medium", Dir: os.Getenv("TENZO_TEST_CLAUDE_DIR"), Token: os.Getenv(cliTokenEnv), Timeout: 90 * time.Second})

	var deltas int
	res, err := c.Stream(t.Context(), Request{Messages: []Message{
		{Role: "system", Content: "Ты консультант клиники. Отвечай одним коротким предложением по-русски."},
		{Role: "user", Content: "Вы работаете ночью?"},
	}}, func(string) { deltas++ })
	if err != nil || res.Content == "" || deltas == 0 {
		t.Fatalf("text: %+v, deltas %d, %v", res, deltas, err)
	}
	t.Logf("text: %s", res.Content)

	res, err = c.Stream(t.Context(), Request{
		Messages:   []Message{{Role: "system", Content: "Извлеки данные клиента из переписки."}, {Role: "user", Content: "Меня зовут Арман, номер 8 707 123 45 67"}},
		Tools:      []Tool{{Type: "function", Function: Function{Name: "save_client", Description: "Сохранить имя и телефон.", Parameters: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"phone":{"type":"string"}}}`)}}},
		ToolChoice: Require("save_client"),
	}, func(string) {})
	if err != nil || len(res.ToolCalls) != 1 || !strings.Contains(res.ToolCalls[0].Function.Arguments, "Арман") {
		t.Fatalf("forced: %+v, %v", res, err)
	}
	t.Logf("forced: %s", res.ToolCalls[0].Function.Arguments)

	stats := Tool{Type: "function", Function: Function{Name: "stats", Description: "Сколько клиентов в базе.", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}
	res, err = c.Stream(t.Context(), Request{
		Messages: []Message{{Role: "system", Content: "Ты помощник администратора. Данные бери только из инструментов."}, {Role: "user", Content: "Сколько у нас клиентов?"}},
		Tools:    []Tool{stats},
	}, func(string) {})
	if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "stats" {
		t.Fatalf("tool call: %+v, %v", res, err)
	}
	res, err = c.Stream(t.Context(), Request{
		Messages: []Message{
			{Role: "system", Content: "Ты помощник администратора. Данные бери только из инструментов."},
			{Role: "user", Content: "Сколько у нас клиентов?"},
			{Role: "assistant", ToolCalls: res.ToolCalls},
			{Role: "tool", ToolCallID: res.ToolCalls[0].ID, Content: `{"clients":17}`},
		},
		Tools: []Tool{stats},
	}, func(string) {})
	if err != nil || !strings.Contains(res.Content, "17") {
		t.Fatalf("tool answer: %+v, %v", res, err)
	}
	t.Logf("tool answer: %s", res.Content)
}

func TestFallbackReachesInstalledClaude(t *testing.T) {
	bin := os.Getenv("TENZO_TEST_CLAUDE_BIN")
	if bin == "" {
		t.Skip("TENZO_TEST_CLAUDE_BIN is not set")
	}
	primary := New(Settings{BaseURL: "http://127.0.0.1:9/v1", APIKey: "x", Model: "qwen", Timeout: 5 * time.Second})
	backup := NewCLI(CLISettings{Command: bin, Model: "claude-sonnet-5-5", Effort: "medium", Dir: os.Getenv("TENZO_TEST_CLAUDE_DIR"), Timeout: 90 * time.Second, Parallel: 2})
	f := NewFallback(primary, backup, time.Minute, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	started := time.Now()
	var streamed strings.Builder
	res, err := f.Stream(t.Context(), Request{Messages: []Message{
		{Role: "system", Content: "Ты консультант Tenzo: разработка сайтов и ИИ-ассистентов в Алматы. Отвечай одним предложением."},
		{Role: "user", Content: "Сколько стоит лендинг?"},
	}}, func(d string) { streamed.WriteString(d) })
	if err != nil || res.Content == "" || streamed.String() != res.Content {
		t.Fatalf("fallback: %+v, %v", res, err)
	}
	t.Logf("answer in %s: %s", time.Since(started).Round(time.Millisecond), res.Content)
}
