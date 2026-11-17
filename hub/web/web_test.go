package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebHandler_FallbackAndStatic(t *testing.T) {
	h := Handler()

	// 1. 测试根路径 GET /
	reqRoot := httptest.NewRequest(http.MethodGet, "/", nil)
	rrRoot := httptest.NewRecorder()
	h.ServeHTTP(rrRoot, reqRoot)
	if rrRoot.Code != http.StatusOK {
		t.Fatalf("GET / 期望 200，实际: %d", rrRoot.Code)
	}
	body := rrRoot.Body.String()
	if !strings.Contains(body, "NexusCore") {
		t.Fatalf("GET / 期望包含 NexusCore，实际内容: %s", body)
	}

	// 2. 测试 SPA 路由 fallback GET /groupchat
	reqRoute := httptest.NewRequest(http.MethodGet, "/groupchat", nil)
	rrRoute := httptest.NewRecorder()
	h.ServeHTTP(rrRoute, reqRoute)
	if rrRoute.Code != http.StatusOK {
		t.Fatalf("GET /groupchat 期望 200 (fallback index.html)，实际: %d", rrRoute.Code)
	}
	if !strings.Contains(rrRoute.Body.String(), "NexusCore") {
		t.Fatalf("GET /groupchat 期望 fallback 到 index.html")
	}
}
