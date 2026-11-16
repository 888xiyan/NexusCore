// Command plugin_agent_discovery 是本地 Coding Agent 搜寻插件的 L2 独立服务二进制。
// 职责：
//  1. 扫描宿主机 PATH 与全局包管理器目录，识别与探活 Coding Agent CLI；
//  2. 提取 CLI 版本、运行模式与能力，结构化缓存并提供查询/探测 API；
//  3. 响应 OS 信号实现优雅停机与进程生命周期闭环。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/internal/version"
	"github.com/888xiyan/NexusCore/plugins/plugin_agent_discovery/scanner"
)

type server struct {
	hubHTTP   string
	hubCred   string
	botID     string
	scanner   *scanner.Scanner
	startTime time.Time
}

func main() {
	var (
		port        = flag.Int("port", 7400, "AgentDiscovery HTTP 监听端口")
		hubHTTP     = flag.String("hub-http", "127.0.0.1:7100", "Hub HTTP API 地址")
		hubCred     = flag.String("hub-cred", "", "访问 Hub 所用的凭据（可由环境变量 NEXUS_BOT_CREDENTIAL 提供）")
		botID       = flag.String("bot-id", "discovery_bot", "宿主 owner bot 身份")
		extraDir    = flag.String("extra-dir", "", "额外优先扫描的目录（用于测试或定制路径）")
		showVersion = flag.Bool("version", false, "打印版本号")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("plugin_agent_discovery", version.Version)
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

	var extraDirs []string
	if *extraDir != "" {
		extraDirs = append(extraDirs, *extraDir)
	}

	sc := scanner.NewScanner(extraDirs...)
	s := &server{
		hubHTTP:   targetHub,
		hubCred:   cred,
		botID:     *botID,
		scanner:   sc,
		startTime: time.Now(),
	}

	// 启动时先行扫描一次
	_, _ = sc.Scan(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/discovery/health", s.handleHealth)
	mux.HandleFunc("/v1/discovery/agents", s.handleListAgents)
	mux.HandleFunc("/v1/discovery/scan", s.handleScan)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", *port),
		Handler: mux,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		fmt.Fprintf(os.Stderr, "plugin_agent_discovery (PID %d, PGID %d) 已在 127.0.0.1:%d 启动\n", os.Getpid(), syscall.Getpgrp(), *port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "AgentDiscovery HTTP 监听失败: %v\n", err)
			os.Exit(1)
		}
	}()

	sig := <-sigCh
	fmt.Fprintf(os.Stderr, "plugin_agent_discovery 收到信号 %v，正在优雅退出...\n", sig)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "HTTP 优雅关闭失败: %v\n", err)
	}
	fmt.Fprintln(os.Stderr, "plugin_agent_discovery 进程已退出")
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    "healthy",
		"service":   "plugin_agent_discovery",
		"uptime_ms": time.Since(s.startTime).Milliseconds(),
		"agents":    len(s.scanner.GetCachedAgents()),
	})
}

func (s *server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
		return
	}
	agents := s.scanner.GetCachedAgents()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count":  len(agents),
		"agents": agents,
	})
}

func (s *server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}
	agents, err := s.scanner.Scan(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("扫描失败: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"count":  len(agents),
		"agents": agents,
	})
}
