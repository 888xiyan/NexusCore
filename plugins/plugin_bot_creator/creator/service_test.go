package creator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceCreateBot(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "creator_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	var botCreated bool
	var botRestarted bool

	mockHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/bots" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["bot_id"] == "user_code_reviewer" {
				botCreated = true
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/restart") {
			botRestarted = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "restarted"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockHub.Close()

	svc := NewService(mockHub.Listener.Addr().String(), "test_cred", tmpDir)

	bp := BotBlueprint{
		BotID:         "user_code_reviewer",
		DisplayName:   "代码审查专家",
		Role:          "reviewer",
		SystemPrompt:  "专注 Code Review",
		Persona:       "严谨",
		InitialMemory: "# 代码审查专家记忆\n- 检查并发与性能\n",
		BoundAgentCLI: "claude",
	}

	res, err := svc.CreateBot(context.Background(), bp)
	if err != nil {
		t.Fatalf("CreateBot 失败: %v", err)
	}

	if !botCreated {
		t.Errorf("期望 Hub 收到 Bot 创建请求，实际未触发")
	}
	if !botRestarted {
		t.Errorf("期望 Hub 收到 Bot 重启请求，实际未触发")
	}
	if res.BotID != "user_code_reviewer" {
		t.Errorf("期望 BotID user_code_reviewer，得到 %s", res.BotID)
	}

	// 校验单体记忆目录和 MEMORY.md 是否生成
	memFile := filepath.Join(tmpDir, "user_code_reviewer", "memory", "MEMORY.md")
	content, err := os.ReadFile(memFile)
	if err != nil {
		t.Fatalf("读取生成的 MEMORY.md 失败: %v", err)
	}
	if !strings.Contains(string(content), "检查并发与性能") {
		t.Errorf("MEMORY.md 内容不匹配: %s", string(content))
	}
}
