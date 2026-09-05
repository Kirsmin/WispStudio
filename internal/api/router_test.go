package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"wisp/internal/config"
)

func TestChatStreamPersistsContentAndReasoning(t *testing.T) {
	var gotReasoningEffort string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-5-test","name":"GPT-5 Test","supported_parameters":["reasoning_effort"]}]}`)
		case "/v1/chat/completions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			gotReasoningEffort, _ = body["reasoning_effort"].(string)
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think \"},\"finish_reason\":null}]}\r\n\r\n")
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello \"},\"finish_reason\":null}]}\n\n")
			flusher.Flush()
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"completion_tokens_details\":{\"reasoning_tokens\":1},\"prompt_tokens_details\":{\"cached_tokens\":1}}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer providerServer.Close()

	dataDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{DataDir: dataDir},
		OpenAI:  config.OpenAIConfig{TimeoutSec: 5},
		Providers: []config.ProviderConfig{{
			Name: "mock", BaseURL: providerServer.URL + "/v1", APIKey: "test", Default: true,
		}},
	}
	router := NewRouter(cfg)
	server := httptest.NewServer(router)
	defer server.Close()

	createResp := mustRequest(t, http.MethodPost, server.URL+"/api/sessions", `{"title":"test"}`)
	defer createResp.Body.Close()
	var session struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}

	chatResp := mustRequest(t, http.MethodPost, server.URL+"/api/sessions/"+session.ID+"/chat", `{"message":"hi","model":"gpt-5-test","thinking":"medium"}`)
	defer chatResp.Body.Close()
	streamBody, err := io.ReadAll(chatResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	streamText := string(streamBody)
	for _, needle := range []string{"event: ack", "event: reasoning", "think ", "event: delta", "Hello ", "world", "event: done", `"persisted":true`} {
		if !strings.Contains(streamText, needle) {
			t.Fatalf("stream missing %q:\n%s", needle, streamText)
		}
	}
	if gotReasoningEffort != "medium" {
		t.Fatalf("reasoning_effort = %q, want medium", gotReasoningEffort)
	}

	messagesResp, err := http.Get(server.URL + "/api/sessions/" + session.ID + "/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer messagesResp.Body.Close()
	var messages []map[string]any
	if err := json.NewDecoder(messagesResp.Body).Decode(&messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	if messages[0]["content"] != "hi" {
		t.Fatalf("user content = %#v", messages[0]["content"])
	}
	if messages[1]["content"] != "Hello world" {
		t.Fatalf("assistant content = %#v", messages[1]["content"])
	}
	if messages[1]["reasoning"] != "think " {
		t.Fatalf("assistant reasoning = %#v", messages[1]["reasoning"])
	}

	if _, err := os.Stat(dataDir + "/Sessions/" + session.ID + ".jsonl"); err != nil {
		t.Fatalf("message persistence missing: %v", err)
	}
}

func TestStaticFrontendServed(t *testing.T) {
	cfg := &config.Config{Storage: config.StorageConfig{DataDir: t.TempDir()}, OpenAI: config.OpenAIConfig{TimeoutSec: 1}}
	server := httptest.NewServer(NewRouter(cfg))
	defer server.Close()
	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	for _, needle := range []string{"top-bar", "session-list", "开始一段对话", "thinkingSelect"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("frontend missing %q", needle)
		}
	}
}

func mustRequest(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("%s %s => %d: %s", method, url, resp.StatusCode, data)
	}
	return resp
}

func TestCancelPersistsReadableAbortedState(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-5-test","supported_parameters":["reasoning_effort"]}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
			flusher.Flush()
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer providerServer.Close()

	dataDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{DataDir: dataDir},
		OpenAI:  config.OpenAIConfig{TimeoutSec: 5},
		Providers: []config.ProviderConfig{{
			Name: "mock", BaseURL: providerServer.URL + "/v1", APIKey: "test", Default: true,
		}},
	}
	server := httptest.NewServer(NewRouter(cfg))
	defer server.Close()

	createResp := mustRequest(t, http.MethodPost, server.URL+"/api/sessions", `{"title":"cancel"}`)
	var session struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	createResp.Body.Close()

	streamDone := make(chan string, 1)
	go func() {
		resp := mustRequest(t, http.MethodPost, server.URL+"/api/sessions/"+session.ID+"/chat", `{"message":"hi","model":"gpt-5-test","thinking":"low"}`)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		streamDone <- string(body)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(server.URL + "/api/sessions/" + session.ID + "/chat/status")
		if err != nil {
			t.Fatal(err)
		}
		var status struct {
			Active bool `json:"active"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()
		if status.Active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("chat did not become active")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancelResp := mustRequest(t, http.MethodPost, server.URL+"/api/sessions/"+session.ID+"/chat/cancel", ``)
	cancelResp.Body.Close()
	select {
	case stream := <-streamDone:
		if !strings.Contains(stream, `"finish":"aborted"`) {
			t.Fatalf("stream did not finish aborted: %s", stream)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled stream did not finish")
	}

	resp, err := http.Get(server.URL + "/api/sessions/" + session.ID + "/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var messages []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages=%d", len(messages))
	}
	if messages[1]["finish"] != "aborted" {
		t.Fatalf("finish=%v", messages[1]["finish"])
	}
	if messages[1]["error"] != "生成已停止" {
		t.Fatalf("error=%v", messages[1]["error"])
	}
}
