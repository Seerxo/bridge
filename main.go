package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type server struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

type anthropicRequest struct {
	Model       string          `json:"model"`
	System      json.RawMessage `json:"system"`
	Messages    []message       `json:"messages"`
	Tools       []anthropicTool `json:"tools,omitempty"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        json.RawMessage `json:"stop_sequences,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []openAITool    `json:"tools,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        any             `json:"stop,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAICallFunction `json:"function"`
}

type openAICallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func main() {
	baseURL := strings.TrimRight(env("BRIDGE_UPSTREAM_URL", "https://integrate.api.nvidia.com/v1"), "/")
	s := &server{
		baseURL: baseURL,
		apiKey:  os.Getenv("BRIDGE_UPSTREAM_API_KEY"),
		client:  upstreamClient(envDuration("BRIDGE_FIRST_BYTE_TIMEOUT", 60*time.Second)),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/messages", s.messages)
	mux.HandleFunc("GET /v1/models", s.models)

	addr := env("BRIDGE_ADDR", "127.0.0.1:8080")
	log.Printf("Seerxo Bridge listening on %s -> %s", addr, baseURL)
	log.Fatal(http.ListenAndServe(addr, requestLog(mux)))
}

func (s *server) messages(w http.ResponseWriter, r *http.Request) {
	if s.apiKey == "" {
		writeError(w, http.StatusServiceUnavailable, "BRIDGE_UPSTREAM_API_KEY is not configured")
		return
	}

	var in anthropicRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if in.Model == "" || in.MaxTokens <= 0 || len(in.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "model, max_tokens, and messages are required")
		return
	}

	out, err := translateRequest(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, _ := json.Marshal(out)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		writeError(w, resp.StatusCode, "upstream: "+strings.TrimSpace(string(data)))
		return
	}

	if in.Stream {
		s.stream(w, resp.Body, in.Model)
		return
	}
	var upstream openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		writeError(w, http.StatusBadGateway, "invalid upstream response: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, translateResponse(upstream, in.Model))
}

func translateRequest(in anthropicRequest) (openAIRequest, error) {
	out := openAIRequest{Model: in.Model, MaxTokens: in.MaxTokens, Temperature: in.Temperature, TopP: in.TopP, Stream: in.Stream}
	if len(in.Stop) > 0 && string(in.Stop) != "null" {
		if err := json.Unmarshal(in.Stop, &out.Stop); err != nil {
			return out, errors.New("invalid stop_sequences")
		}
	}
	if system := contentText(in.System); system != "" {
		out.Messages = append(out.Messages, openAIMessage{Role: "system", Content: system})
	}
	for _, m := range in.Messages {
		converted, err := convertMessage(m)
		if err != nil {
			return out, err
		}
		out.Messages = append(out.Messages, converted...)
	}
	for _, tool := range in.Tools {
		out.Tools = append(out.Tools, openAITool{Type: "function", Function: openAIFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema}})
	}
	return out, nil
}

func convertMessage(m message) ([]openAIMessage, error) {
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return []openAIMessage{{Role: m.Role, Content: text}}, nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, fmt.Errorf("invalid %s message content", m.Role)
	}
	current := openAIMessage{Role: m.Role}
	var texts []string
	var result []openAIMessage
	for _, block := range blocks {
		switch block["type"] {
		case "text":
			if value, ok := block["text"].(string); ok {
				texts = append(texts, value)
			}
		case "tool_use":
			input, _ := json.Marshal(block["input"])
			current.ToolCalls = append(current.ToolCalls, openAIToolCall{ID: stringValue(block["id"]), Type: "function", Function: openAICallFunction{Name: stringValue(block["name"]), Arguments: string(input)}})
		case "tool_result":
			if len(texts) > 0 || len(current.ToolCalls) > 0 {
				current.Content = strings.Join(texts, "\n")
				result = append(result, current)
				texts = nil
				current = openAIMessage{Role: m.Role}
			}
			result = append(result, openAIMessage{Role: "tool", ToolCallID: stringValue(block["tool_use_id"]), Content: anyText(block["content"])})
		}
	}
	if len(texts) > 0 || len(current.ToolCalls) > 0 {
		current.Content = strings.Join(texts, "\n")
		result = append(result, current)
	}
	return result, nil
}

type openAIResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func translateResponse(in openAIResponse, fallbackModel string) map[string]any {
	content := []any{}
	stop := "end_turn"
	if len(in.Choices) > 0 {
		choice := in.Choices[0]
		if choice.Message.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": choice.Message.Content})
		}
		for _, call := range choice.Message.ToolCalls {
			var input any
			if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil {
				input = map[string]any{}
			}
			content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
		}
		if choice.FinishReason == "tool_calls" {
			stop = "tool_use"
		} else if choice.FinishReason == "length" {
			stop = "max_tokens"
		}
	}
	model := in.Model
	if model == "" {
		model = fallbackModel
	}
	return map[string]any{
		"id": in.ID, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": in.Usage.PromptTokens, "output_tokens": in.Usage.CompletionTokens},
	}
}

func (s *server) stream(w http.ResponseWriter, body io.Reader, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	emit := func(event string, data any) {
		encoded, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
		flusher.Flush()
	}
	emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_bridge", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}})

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	textStarted := false
	toolIndexes := map[int]int{}
	nextIndex := 0
	stopReason := "end_turn"
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int                `json:"index"`
						ID       string             `json:"id"`
						Function openAICallFunction `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			if !textStarted {
				emit("content_block_start", map[string]any{"type": "content_block_start", "index": nextIndex, "content_block": map[string]any{"type": "text", "text": ""}})
				textStarted = true
			}
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": nextIndex, "delta": map[string]any{"type": "text_delta", "text": choice.Delta.Content}})
		}
		for _, call := range choice.Delta.ToolCalls {
			idx, exists := toolIndexes[call.Index]
			if !exists {
				if textStarted {
					emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": nextIndex})
					textStarted = false
					nextIndex++
				}
				idx = nextIndex
				toolIndexes[call.Index] = idx
				nextIndex++
				emit("content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": map[string]any{}}})
			}
			if call.Function.Arguments != "" {
				emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "input_json_delta", "partial_json": call.Function.Arguments}})
			}
		}
		if choice.FinishReason == "tool_calls" {
			stopReason = "tool_use"
		} else if choice.FinishReason == "length" {
			stopReason = "max_tokens"
		}
	}
	if textStarted {
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": nextIndex})
	}
	for _, idx := range toolIndexes {
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
	}
	emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 0}})
	emit("message_stop", map[string]string{"type": "message_stop"})
}

func (s *server) models(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, s.baseURL+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	resp, err := s.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []map[string]any
	json.Unmarshal(raw, &blocks)
	var parts []string
	for _, block := range blocks {
		if block["type"] == "text" {
			parts = append(parts, stringValue(block["text"]))
		}
	}
	return strings.Join(parts, "\n")
}

func anyText(v any) string {
	if text, ok := v.(string); ok {
		return text
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func stringValue(v any) string { value, _ := v.(string); return value }
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		log.Fatalf("%s must be a positive duration, for example 60s", key)
	}
	return duration
}

func upstreamClient(firstByteTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = firstByteTimeout
	return &http.Client{Transport: transport}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": message}})
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
