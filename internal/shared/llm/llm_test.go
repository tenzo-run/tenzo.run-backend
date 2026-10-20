package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const stream = `: waiting for complete response

data: {"choices":[{"delta":{"role":"assistant"}}]}

data: {"choices":[{"delta":{"reasoning_content":"think"}}]}

data: {"choices":[{"delta":{"content":"При"}}]}

data: {"choices":[{"delta":{"content":"вет"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"save_client","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\"phone\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"87011234567\"}"}}]}}]}

data: [DONE]
`

func TestStreamCollectsContentAndToolCalls(t *testing.T) {
	var got body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("request = %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, stream)
	}))
	defer srv.Close()

	c := New(Settings{BaseURL: srv.URL + "/v1/", APIKey: "key", Model: "m", Timeout: time.Second, Temperature: 0.4})
	var deltas []string
	res, err := c.Stream(t.Context(), Request{
		Messages:   []Message{{Role: "user", Content: "hi"}},
		ToolChoice: Require("save_client"),
	}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}

	if res.Content != "Привет" || strings.Join(deltas, "|") != "При|вет" {
		t.Fatalf("content = %q, deltas = %q", res.Content, deltas)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "call_1" || res.ToolCalls[0].Function.Name != "save_client" ||
		res.ToolCalls[0].Function.Arguments != `{"phone":"87011234567"}` {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if !got.Stream || got.Model != "m" || got.ToolChoice == nil {
		t.Fatalf("body = %+v", got)
	}
}

func TestStreamReportsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":"subscription_expired"}}`)
	}))
	defer srv.Close()

	_, err := New(Settings{BaseURL: srv.URL, Timeout: time.Second}).Stream(t.Context(), Request{}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "subscription_expired") {
		t.Fatalf("error = %v", err)
	}

	mid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "data: {\"error\":{\"code\":\"provider_reply_invalid\"}}\n\n")
	}))
	defer mid.Close()

	_, err = New(Settings{BaseURL: mid.URL, Timeout: time.Second}).Stream(t.Context(), Request{}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "provider_reply_invalid") {
		t.Fatalf("stream error = %v", err)
	}
}
