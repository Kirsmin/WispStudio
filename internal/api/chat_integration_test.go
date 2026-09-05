package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wisp/internal/config"
	"wisp/internal/store"
)

func testConfig(dataDir, upstream string) *config.Config {
	return &config.Config{
		Storage: config.StorageConfig{DataDir: dataDir},
		OpenAI: config.OpenAIConfig{
			BaseURL: upstream, TimeoutSec: 2, MaxGenerationSec: 5,
		},
		Models: []config.ModelConfig{{ID: "test-model", Name: "test-model", Default: true, ThinkingLevels: []string{"off"}, ThinkingStyle: "none"}},
	}
}

func createTestSession(t *testing.T, serverURL string) string {
	t.Helper()
	resp, err := http.Post(serverURL+"/api/sessions", "application/json", strings.NewReader(`{"title":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var session store.Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.ID == "" {
		t.Fatal("empty session id")
	}
	return session.ID
}

func TestBrowserDisconnectDoesNotCancelGeneration(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		time.Sleep(80 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	router := NewRouter(testConfig(t.TempDir(), upstream.URL))
	server := httptest.NewServer(router)
	defer server.Close()
	sessionID := createTestSession(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/sessions/"+sessionID+"/chat", strings.NewReader(`{"message":"hello","model":"test-model","thinking":"off"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	// The first event is flushed before the upstream finishes. Closing here
	// emulates refresh/navigation/disconnect in the browser.
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		if line == "\n" {
			break
		}
	}
	_ = resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := router.messageStore.List(sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) == 2 {
			assistant := messages[1]
			if assistant.Reasoning != "think" || assistant.Content != "answer" || assistant.Finish != "stop" {
				t.Fatalf("unexpected persisted assistant: %#v", assistant)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("generation did not finish after browser disconnect")
}

func TestUpstreamHTTPErrorDoesNotPersistUserMessage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer upstream.Close()

	router := NewRouter(testConfig(t.TempDir(), upstream.URL))
	server := httptest.NewServer(router)
	defer server.Close()
	sessionID := createTestSession(t, server.URL)

	resp, err := http.Post(server.URL+"/api/sessions/"+sessionID+"/chat", "application/json", strings.NewReader(`{"message":"hello","model":"test-model","thinking":"off"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "rate limited") || !strings.Contains(string(body), `"persisted":false`) {
		t.Fatalf("unexpected SSE error body: %s", body)
	}
	messages, err := router.messageStore.List(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("upstream failure persisted messages: %#v", messages)
	}
}
