package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTranslateRequestWithTools(t *testing.T) {
	in := anthropicRequest{
		Model: "z-ai/glm-5.2", MaxTokens: 100,
		System: json.RawMessage(`"Be concise"`),
		Messages: []message{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"call_1","name":"weather","input":{"city":"Istanbul"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"call_1","content":"sunny"}]`)},
		},
		Tools: []anthropicTool{{Name: "weather", InputSchema: map[string]any{"type": "object"}}},
	}

	out, err := translateRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 3 || out.Messages[1].ToolCalls[0].Function.Name != "weather" || out.Messages[2].Role != "tool" {
		t.Fatalf("unexpected translation: %#v", out.Messages)
	}
	if len(out.Tools) != 1 || out.Tools[0].Function.Name != "weather" {
		t.Fatalf("unexpected tools: %#v", out.Tools)
	}
}

func TestMessagesStopsWhenUpstreamSendsNoHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer upstream.Close()

	s := &server{baseURL: upstream.URL, apiKey: "test", client: upstreamClient(20 * time.Millisecond)}
	body := bytes.NewBufferString(`{"model":"z-ai/glm-5.2","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	response := httptest.NewRecorder()
	s.messages(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", response.Code, response.Body.String())
	}
}

func TestTranslateResponse(t *testing.T) {
	var in openAIResponse
	if err := json.Unmarshal([]byte(`{"id":"chat_1","model":"glm","choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Istanbul\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`), &in); err != nil {
		t.Fatal(err)
	}
	out := translateResponse(in, "fallback")
	if out["stop_reason"] != "tool_use" {
		t.Fatalf("unexpected response: %#v", out)
	}
	content := out["content"].([]any)
	if content[0].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("unexpected content: %#v", content)
	}
}
