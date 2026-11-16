// Command plugin_bot_creator 是专职 Bot 蓝图定义、校验、记忆初始化与 Agent 绑定的独立 L2 插件。
// 职责：
//  1. 暴露蓝图静态合规校验接口（命名、保留名、MEMORY.md <= 200 行）；
//  2. 提供开箱即用的预设角色模板（Reviewer、Coder、Ops）；
//  3. 自动初始化 Claude Code 四层单体记忆脚手架；
//  4. 提交 Bot 注册并联动 Supervisor 毫秒级重启生效；
//  5. 响应 OS 信号实现优雅停机与进程生命周期闭环。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/internal/version"
	"github.com/888xiyan/NexusCore/plugins/plugin_bot_creator/creator"
)

type server struct {
	hubHTTP   string
	hubCred   string
	botID     string
	svc       *creator.Service
	startTime time.Time
}

func main() {
	var (
		port        = flag.Int("port", 7500, "BotCreator HTTP 监听端口")
		hubHTTP     = flag.String("hub-http", "127.0.0.1:7100", "Hub HTTP API 地址")
		hubCred     = flag.String("hub-cred", "", "访问 Hub 所用的凭据（可由环境变量 NEXUS_BOT_CREDENTIAL 提供）")
		botID       = flag.String("bot-id", "bot_creator_bot", "宿主 owner bot 身份")
		memoryDir   = flag.String("memory-dir", "", "单体记忆存储基准目录（默认为 data/bots）")
		showVersion = flag.Bool("version", false, "打印版本号")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("plugin_bot_creator", version.Version)
		return
	}

	cred := *hubCred
	if cred == "" {
		cred = os.Getenv("NEXUS_BOT_CREDENTIAL")
	}

	targetHub := *hubHTTP
	if envHub := os.Getenv("NEXUS_HUB_HTTP"); envHub != "" {
		targetHub = envHub
	}
	targetHub = strings.TrimPrefix(targetHub, "http://")
	targetHub = strings.TrimPrefix(targetHub, "https://")

	svc := creator.NewService(targetHub, cred, *memoryDir)
	s := &server{
		hubHTTP:   targetHub,
		hubCred:   cred,
		botID:     *botID,
		svc:       svc,
		startTime: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/creator/health", s.handleHealth)
	mux.HandleFunc("/v1/creator/templates", s.handleTemplates)
	mux.HandleFunc("/v1/creator/validate", s.handleValidate)
	mux.HandleFunc("/v1/creator/bots", s.handleCreateBot)
	mux.HandleFunc("/v1/creator/agents", s.handleListAgents)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", *port),
		Handler: withCORS(mux),
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		fmt.Fprintf(os.Stderr, "plugin_bot_creator (PID %d, PGID %d) 已在 127.0.0.1:%d 启动\n", os.Getpid(), syscall.Getpgrp(), *port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "BotCreator HTTP 监听失败: %v\n", err)
			os.Exit(1)
		}
	}()

	sig := <-sigCh
	fmt.Fprintf(os.Stderr, "plugin_bot_creator 收到信号 %v，正在优雅退出...\n", sig)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "BotCreator 优雅停机超时: %v\n", err)
	}
	fmt.Fprintln(os.Stderr, "plugin_bot_creator 已安全退出")
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "healthy",
		"plugin_id":  "plugin_bot_creator",
		"owner_bot":  s.botID,
		"uptime_sec": int64(time.Since(s.startTime).Seconds()),
	})
}

func (s *server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	templates := creator.BuiltinTemplates()
	writeJSON(w, http.StatusOK, templates)
}

func (s *server) handleValidate(w http.ResponseWriter, r *http.Request) {
	var bp creator.BotBlueprint
	if err := json.NewDecoder(r.Body).Decode(&bp); err != nil {
		writeErr(w, http.StatusBadRequest, "解析蓝图失败: "+err.Error())
		return
	}
	result := creator.ValidateBlueprint(bp)
	writeJSON(w, http.StatusOK, result)
}

func (s *server) handleCreateBot(w http.ResponseWriter, r *http.Request) {
	var bp creator.BotBlueprint
	if err := json.NewDecoder(r.Body).Decode(&bp); err != nil {
		writeErr(w, http.StatusBadRequest, "解析请求失败: "+err.Error())
		return
	}
	res, err := s.svc.CreateBot(r.Context(), bp)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://"+s.hubHTTP+"/v1/agents", nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.hubCred != "" {
		req.Header.Set("X-Nexus-Credential", s.hubCred)
	}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "获取 Agent 列表失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Nexus-Credential")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
