package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/888xiyan/NexusCore/internal/modules/toolcenter"
)

func TestRewriteToolPath(t *testing.T) {
	cases := map[string]string{
		"/v1/tools":                           "/api/tools",
		"/v1/tools/skills":                    "/api/skills",
		"/v1/tools/skills/skill.a/revisions":  "/api/skills/skill.a/revisions",
		"/v1/tools/grants":                    "/api/grants",
		"/v1/tools/mcp":                       "/api/mcp",
		"/v1/tools/traces":                    "/api/traces",
		"/v1/tools/named-bots/sys.search.web": "/api/tools/sys.search.web/named-bots",
		"/v1/tools/sys.search.web/execute":    "/api/tools/sys.search.web/execute",
	}
	for in, want := range cases {
		got, ok := rewriteToolPath(in)
		if !ok || got != want {
			t.Fatalf("%s => %s ok=%v, want %s", in, got, ok, want)
		}
	}
	if _, ok := rewriteToolPath("/v1/delegation/evaluate"); ok {
		t.Fatal("代批路径不应转发到工具插件")
	}
}

func TestToolRelayDoesNotStoreSkillSource(t *testing.T) {
	const source = "PLAINTEXT_SKILL_BODY_m12"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/skills" || r.Method != http.MethodPost {
			t.Errorf("转发路径不符: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(source)) {
			t.Errorf("插件未收到正文")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"created","tool_id":"skill.m12.relay"}`))
	}))
	defer upstream.Close()

	api := New(nil, nil, nil, nil, nil, nil, nil)
	api.SetToolCenterUpstream(upstream.URL)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/tools/skills", "application/json", bytes.NewReader([]byte(`{"tool_id":"skill.m12.relay","name":"relay","code":"`+source+`"}`)))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("预期 201，实际 %d", resp.StatusCode)
	}
	if _, err := api.toolCenter.GetTool("skill.m12.relay"); !errors.Is(err, toolcenter.ErrToolNotFound) {
		t.Fatalf("Hub 不应保存工具源码: %v", err)
	}
}
