// Command plugin_tool_center 是 NexusCore 工具中心与 Skill 管理独立 L2 插件。
// 职责：
//  1. 系统不可编辑工具、可编辑 Skill 增删改查、外部 MCP 抽象统一治理；
//  2. 维系 Bot-Tool 授权矩阵，隔离未授权工具调用；
//  3. 协同内置沙盒记录工具轨迹，捕获网络外联审计告警；
//  4. 响应 OS 信号实现优雅停机。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/internal/modules/toolcenter"
	"github.com/888xiyan/NexusCore/internal/version"
)

type server struct {
	svc       *toolcenter.ToolCenterService
	startTime time.Time
}

func main() {
	var (
		port        = flag.Int("port", 7400, "ToolCenter HTTP 监听端口")
		showVersion = flag.Bool("version", false, "打印版本号")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("plugin_tool_center", version.Version)
		return
	}

	svc := toolcenter.NewToolCenterService()
	s := &server{
		svc:       svc,
		startTime: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/tools", s.handleToolsList)
	mux.HandleFunc("GET /api/tools/{tool_id}", s.handleToolGet)
	mux.HandleFunc("POST /api/skills", s.handleSkillCreate)
	mux.HandleFunc("PUT /api/skills/{tool_id}", s.handleSkillUpdate)
	mux.HandleFunc("DELETE /api/skills/{tool_id}", s.handleSkillDelete)
	mux.HandleFunc("GET /api/grants", s.handleGrantsList)
	mux.HandleFunc("POST /api/grants", s.handleGrantTool)
	mux.HandleFunc("POST /api/grants/revoke", s.handleRevokeGrant)
	mux.HandleFunc("POST /api/tools/{tool_id}/execute", s.handleToolExecute)

	addr := fmt.Sprintf(":%d", *port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: withCORS(mux),
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		fmt.Printf("[plugin_tool_center] 启动监听 %s ...\n", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "[plugin_tool_center] 服务异常退出: %v\n", err)
			os.Exit(1)
		}
	}()

	sig := <-sigCh
	fmt.Printf("[plugin_tool_center] 收到信号 %s，开始优雅停机 (15s 窗口)...\n", sig)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[plugin_tool_center] 停机错误: %v\n", err)
	}
	fmt.Println("[plugin_tool_center] 优雅停机完成")
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"uptime": time.Since(s.startTime).String(),
	})
}

func (s *server) handleToolsList(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	tools := s.svc.ListTools(toolcenter.ToolCategory(category))
	if tools == nil {
		tools = []toolcenter.ToolDefinition{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tools": tools,
		"count": len(tools),
	})
}

func (s *server) handleToolGet(w http.ResponseWriter, r *http.Request) {
	toolID := r.PathValue("tool_id")
	tool, err := s.svc.GetTool(toolID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tool)
}

func (s *server) handleSkillCreate(w http.ResponseWriter, r *http.Request) {
	var skill toolcenter.ToolDefinition
	if err := json.NewDecoder(r.Body).Decode(&skill); err != nil {
		writeErr(w, http.StatusBadRequest, "解析请求体失败: "+err.Error())
		return
	}
	if err := s.svc.CreateSkill(skill); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status":  "created",
		"tool_id": skill.ToolID,
	})
}

func (s *server) handleSkillUpdate(w http.ResponseWriter, r *http.Request) {
	toolID := r.PathValue("tool_id")
	var req struct {
		Code         string `json:"code"`
		ConfigSchema string `json:"config_schema"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "解析更新请求失败: "+err.Error())
		return
	}
	if err := s.svc.UpdateSkill(toolID, req.Code, req.ConfigSchema); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "tool_id": toolID})
}

func (s *server) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	toolID := r.PathValue("tool_id")
	if err := s.svc.DeleteSkill(toolID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "tool_id": toolID})
}

func (s *server) handleGrantsList(w http.ResponseWriter, r *http.Request) {
	botID := r.URL.Query().Get("bot_id")
	grants := s.svc.ListGrants(botID)
	if grants == nil {
		grants = []toolcenter.BotToolGrant{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"grants": grants,
		"count":  len(grants),
	})
}

func (s *server) handleGrantTool(w http.ResponseWriter, r *http.Request) {
	var grant toolcenter.BotToolGrant
	if err := json.NewDecoder(r.Body).Decode(&grant); err != nil {
		writeErr(w, http.StatusBadRequest, "解析授权配置失败: "+err.Error())
		return
	}
	if err := s.svc.GrantTool(grant); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "granted", "grant_id": grant.GrantID})
}

func (s *server) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BotID  string `json:"bot_id"`
		ToolID string `json:"tool_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "解析吊销请求失败: "+err.Error())
		return
	}
	if err := s.svc.RevokeGrant(req.BotID, req.ToolID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

func (s *server) handleToolExecute(w http.ResponseWriter, r *http.Request) {
	toolID := r.PathValue("tool_id")
	var req struct {
		BotID  string                 `json:"bot_id"`
		Params map[string]interface{} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "解析请求失败: "+err.Error())
		return
	}

	res, err := s.svc.ExecuteTool(req.BotID, toolID, req.Params, func() (string, int, error) {
		return fmt.Sprintf("执行工具 [%s] 成功，沙盒输出留痕", toolID), 0, nil
	})
	if err != nil {
		writeJSON(w, http.StatusForbidden, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
