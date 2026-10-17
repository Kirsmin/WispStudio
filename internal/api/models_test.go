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
)

// doJSON 向服务发一个带本地 token 的 JSON 请求，返回响应记录器。
func (e *testEnv) doJSON(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Token", e.token)
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// findModel 在 GET /api/models 响应中按全称 id 找模型。
func findModel(t *testing.T, resp modelsResponse, fullID string) (modelDTO, bool) {
	t.Helper()
	for _, p := range resp.Providers {
		for _, m := range p.Models {
			if m.FullID == fullID {
				return m, true
			}
		}
	}
	return modelDTO{}, false
}

// TestAddModelAPI 验收 F4：POST /api/models 写回配置并热更新目录。
func TestAddModelAPI(t *testing.T) {
	env := newTestEnv(t, "http://127.0.0.1:9", "tok-add")

	rec := env.doJSON(t, http.MethodPost, "/api/models", map[string]string{
		"FullID": "fake/model-b",
		"Alias":  "模型乙",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/models = %d, 期望 201: %s", rec.Code, rec.Body.String())
	}

	rec = env.doJSON(t, http.MethodGet, "/api/models", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/models = %d: %s", rec.Code, rec.Body.String())
	}
	var resp modelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析模型目录失败: %v", err)
	}
	added, ok := findModel(t, resp, "fake/model-b")
	if !ok {
		t.Fatalf("新模型未出现在目录中: %+v", resp)
	}
	if added.Alias != "模型乙" || added.ProviderID != "fake" || added.ModelID != "model-b" {
		t.Fatalf("新模型条目不正确: %+v", added)
	}

	// 配置文件写回且保持既有内容
	text, err := os.ReadFile(env.configPath)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if !strings.Contains(string(text), "model-b") || !strings.Contains(string(text), "模型乙") {
		t.Fatalf("配置未写回新模型:\n%s", text)
	}
	if !strings.Contains(string(text), "model-a") {
		t.Fatalf("写回不应丢失原有模型:\n%s", text)
	}
}

// TestAddModelAPIRejectsInvalid 验收 F4：非法 FullID 与未知 provider 返回 400。
func TestAddModelAPIRejectsInvalid(t *testing.T) {
	env := newTestEnv(t, "http://127.0.0.1:9", "tok-add")
	cases := []struct {
		name   string
		fullID string
	}{
		{"缺斜杠", "model-b"},
		{"未知 provider", "nope/model-b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.doJSON(t, http.MethodPost, "/api/models", map[string]string{"FullID": tc.fullID})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST /api/models(%q) = %d, 期望 400: %s", tc.fullID, rec.Code, rec.Body.String())
			}
		})
	}
}
