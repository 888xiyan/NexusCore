// Command plugin_groupchat 是 GroupChatPlugin 独立群聊插件的 L2 运行时二进制。
// 职责：
//  1. 维护多 Bot 群聊房间与消息时序管理；
//  2. 严格执行“仅用户@唤醒”的路由安全铁律：Bot 回复中的 @ 仅作为展示属性，绝不触发下游唤醒；
//  3. 将用户 @ 触发的消息转换为 Ingress 信封投递给 Hub 总线；
//  4. 响应 OS 信号实现优雅停机与进程生命周期闭环。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/GroupChatPlugin/room"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/version"
)

type server struct {
	hubHTTP   string
	hubCred   string
	botID     string
	roomMgr   *room.Manager
	startTime time.Time
	mu        sync.RWMutex
}

func main() {
	var (
		port        = flag.Int("port", 7300, "GroupChat HTTP 监听端口")
		hubHTTP     = flag.String("hub-http", "127.0.0.1:7100", "Hub HTTP API 地址")
		hubCred     = flag.String("hub-cred", "", "访问 Hub 所用的凭据（可由环境变量 NEXUS_BOT_CREDENTIAL 提供）")
		botID       = flag.String("bot-id", "groupchat_bot", "宿主 owner bot 身份")
		showVersion = flag.Bool("version", false, "打印版本号")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("plugin_groupchat", version.Version)
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

	s := &server{
		hubHTTP:   targetHub,
		hubCred:   cred,
		botID:     *botID,
		roomMgr:   room.NewManager(),
		startTime: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/health", s.handleHealth)
	mux.HandleFunc("/v1/chat/rooms/", s.handleRooms)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", *port),
		Handler: mux,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		fmt.Fprintf(os.Stderr, "plugin_groupchat (PID %d, PGID %d) 已在 127.0.0.1:%d 启动\n", os.Getpid(), syscall.Getpgrp(), *port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "GroupChat HTTP 监听失败: %v\n", err)
			os.Exit(1)
		}
	}()

	sig := <-sigCh
	fmt.Fprintf(os.Stderr, "plugin_groupchat 收到信号 %v，正在优雅退出...\n", sig)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "HTTP 优雅退出异常: %v\n", err)
	}
	fmt.Fprintln(os.Stderr, "plugin_groupchat 已安全停机退出")
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"status":     "ok",
		"plugin_id":  "plugin_groupchat",
		"uptime_sec": int64(time.Since(s.startTime).Seconds()),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *server) handleRooms(w http.ResponseWriter, r *http.Request) {
	// 路由模式: /v1/chat/rooms/{room_id}/messages
	path := strings.TrimPrefix(r.URL.Path, "/v1/chat/rooms/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, `{"error":"room_id 缺失"}`, http.StatusBadRequest)
		return
	}
	roomID := parts[0]
	rm := s.roomMgr.GetOrCreateRoom(roomID, "")

	if len(parts) >= 2 && parts[1] == "messages" {
		switch r.Method {
		case http.MethodGet:
			msgs := rm.GetMessages(50)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"room_id":  roomID,
				"messages": msgs,
			})
			return
		case http.MethodPost:
			var req struct {
				SenderID string `json:"sender_id"`
				Role     string `json:"role"` // "user" | "bot"
				Content  string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"请求解析失败: %v"}`, err), http.StatusBadRequest)
				return
			}
			if req.Role == "" {
				req.Role = room.RoleUser
			}

			msg, routedBots, err := rm.Append(req.SenderID, req.Role, req.Content)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"追加消息失败: %v"}`, err), http.StatusBadRequest)
				return
			}

			// 只有用户 @ 产生的 routedBots 会投递到 Hub
			var dispatched []string
			if len(routedBots) > 0 {
				for _, targetBot := range routedBots {
					if err := s.dispatchToHub(r.Context(), roomID, targetBot, msg); err == nil {
						dispatched = append(dispatched, targetBot)
					} else {
						fmt.Fprintf(os.Stderr, "投递到 Hub (target=%s) 失败: %v\n", targetBot, err)
					}
				}
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":          true,
				"message":     msg,
				"routed_bots": routedBots,
				"dispatched":  dispatched,
			})
			return
		default:
			http.Error(w, `{"error":"Method Not Allowed"}`, http.StatusMethodNotAllowed)
			return
		}
	}

	http.Error(w, `{"error":"Not Found"}`, http.StatusNotFound)
}

func (s *server) dispatchToHub(ctx context.Context, roomID, targetBot string, msg *room.Message) error {
	if s.hubHTTP == "" {
		return nil
	}

	idempotencyKey := fmt.Sprintf("chat_%s_%s_%d", roomID, targetBot, msg.Timestamp.UnixNano())
	env := envelope.NewRoot(s.botID, envelope.ChannelIM, envelope.MsgRequest, idempotencyKey)
	env.ChannelRef = roomID
	env.CorrelationID = msg.ID

	payloadDoc := map[string]any{
		"room_id":   roomID,
		"sender_id": msg.SenderID,
		"content":   msg.Content,
		"text":      msg.Content, // 供简单 Agent 提取
	}
	payloadBytes, _ := json.Marshal(payloadDoc)

	ingressReq := map[string]any{
		"envelope":       env,
		"payload_base64": base64.StdEncoding.EncodeToString(payloadBytes),
		"bot_id":         targetBot,
		"queue_name":     targetBot,
		"job_type":       "chat.task",
	}
	reqData, _ := json.Marshal(ingressReq)

	url := fmt.Sprintf("http://%s/v1/ingress", s.hubHTTP)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqData))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.hubCred != "" {
		httpReq.Header.Set("X-Nexus-Credential", s.hubCred)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Hub ingress 响应状态码 %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
