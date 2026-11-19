package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SetToolCenterUpstream 把工具读写转发到插件。Hub 不保存 Skill 正文、MCP 连接串或授权。
func (a *API) SetToolCenterUpstream(base string) {
	a.toolUpstream = strings.TrimRight(strings.TrimSpace(base), "/")
	if a.toolHTTP == nil {
		a.toolHTTP = &http.Client{Timeout: 10 * time.Second}
	}
}

func (a *API) relayTool(w http.ResponseWriter, r *http.Request) bool {
	if a.toolUpstream == "" {
		return false
	}
	destPath, ok := rewriteToolPath(r.URL.Path)
	if !ok {
		return false
	}
	target := a.toolUpstream + destPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	client := a.toolHTTP
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "转发工具请求失败: "+err.Error())
		return true
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("工具插件不可达: %v", err))
		return true
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return true
}

func rewriteToolPath(path string) (string, bool) {
	const prefix = "/v1/tools"
	if path != prefix && !strings.HasPrefix(path, prefix+"/") {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	switch {
	case rest == "/skills" || strings.HasPrefix(rest, "/skills/"):
		return "/api" + rest, true
	case rest == "/grants" || strings.HasPrefix(rest, "/grants/"):
		return "/api" + rest, true
	case rest == "/mcp" || strings.HasPrefix(rest, "/mcp/"):
		return "/api" + rest, true
	case strings.HasPrefix(rest, "/named-bots/"):
		toolID := strings.TrimPrefix(rest, "/named-bots/")
		return "/api/tools/" + toolID + "/named-bots", true
	case rest == "/traces" || strings.HasPrefix(rest, "/traces/"):
		return "/api" + rest, true
	default:
		return "/api/tools" + rest, true
	}
}
