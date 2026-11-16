// Command agent-daemon 是跨设备探针。M1 只提供 loopback 模式作为冒烟夹具：
// 反向连接 hub 传输端口，按插件协议执行 echo 工具。真实设备能力与 bash 白名单在 M5。
// 依据：内核 MVP 详设 §3.8、架构总结 §5.1。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/hub/transport"
	"github.com/888xiyan/NexusCore/internal/version"
)

func main() {
	var (
		hubTransport = flag.String("hub-transport", "127.0.0.1:7102", "hub 传输路由地址")
		agentID      = flag.String("agent-id", "loopback_agent", "Agent 身份")
		showVersion  = flag.Bool("version", false, "打印版本号")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("agent-daemon", version.Version)
		return
	}
	cred := os.Getenv("NEXUS_AGENT_CREDENTIAL")
	if cred == "" {
		fmt.Fprintln(os.Stderr, "缺少 NEXUS_AGENT_CREDENTIAL：凭证由 user_bot 经 hub 签发")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for ctx.Err() == nil {
		if err := serve(ctx, *hubTransport, *agentID, cred); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "agent 会话结束:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func serve(ctx context.Context, addr, agentID, cred string) error {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("反向连接 hub: %w", err)
	}
	defer conn.Close()

	if err := transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameAuth, Payload: []byte(cred)}); err != nil {
		return err
	}
	ack, err := transport.ReadFrame(conn)
	if err != nil {
		return fmt.Errorf("鉴权应答: %w", err)
	}
	if ack.FrameType != transport.FrameAuthAck {
		return fmt.Errorf("鉴权失败: %s", ack.Payload)
	}
	fmt.Fprintf(os.Stderr, "agent-daemon %s 已接入 hub 传输路由\n", agentID)

	var mu sync.Mutex
	streams := map[uint32]string{}

	// 心跳 15s；内核丢 2 次即判失联
	hbCtx, cancelHB := context.WithCancel(ctx)
	defer cancelHB()
	go func() {
		ticker := time.NewTicker(transport.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				_ = transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameHeartbeat})
				mu.Unlock()
			}
		}
	}()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	driver := NewLocalCLIDriver(agentID)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(transport.HeartbeatInterval * 3))
		f, err := transport.ReadFrame(conn)
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch f.FrameType {
		case transport.FrameHeartbeat:
			continue
		case transport.FrameOpen:
			mu.Lock()
			streams[f.StreamID] = f.ProtocolID
			mu.Unlock()
		case transport.FrameClose:
			mu.Lock()
			delete(streams, f.StreamID)
			mu.Unlock()
		case transport.FrameData:
			mu.Lock()
			protocol, ok := streams[f.StreamID]
			mu.Unlock()
			if !ok {
				continue // 未注册的 stream_id 不处理
			}
			result := execute(driver, protocol, f.Payload)
			mu.Lock()
			err := transport.WriteFrame(conn, transport.Frame{
				FrameType: transport.FrameData, StreamID: f.StreamID, ProtocolID: protocol, Payload: result,
			})
			mu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}

// execute 是插件协议侧的执行语义；内核不解析这些字节。
// 支持 M1 echo 工具与 M5 nexus.cli.v1 本地 CLI 驱动。
func execute(driver *LocalCLIDriver, protocol string, payload []byte) []byte {
	switch protocol {
	case "nexus.echo.v1":
		var req struct {
			Text  string `json:"text"`
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			out, _ := json.Marshal(map[string]any{"error": "bad_payload", "exit_code": 2})
			return out
		}
		out, _ := json.Marshal(map[string]any{
			"echo": req.Text, "exit_code": 0, "job_id": req.JobID, "agent": driver.agentID,
		})
		return out
	case "nexus.cli.v1":
		return handleCLIProtocol(driver, payload)
	default:
		out, _ := json.Marshal(map[string]any{"error": "unsupported_protocol", "protocol_id": protocol})
		return out
	}
}
