// Command bot-runtime 是 Bot 独立二进制运行时（lazy 模式）。
// 依据：内核 MVP 详设 §3.9 / §6.1、架构总结 §5.5。
// M1 内置 echo 模式作为冒烟夹具：不持有任何本地长期状态，退出即丢弃全部内存态。
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/hub/supervisor"
	"github.com/888xiyan/NexusCore/hub/transport"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/version"
	pb "github.com/888xiyan/NexusCore/proto/gen/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func main() {
	var (
		botID         = flag.String("bot-id", "", "Bot 身份（必填）")
		hubGRPC       = flag.String("hub-grpc", "127.0.0.1:7101", "hub gRPC 控制面地址")
		hubHTTP       = flag.String("hub-http", "127.0.0.1:7100", "hub HTTP 入口地址")
		hubTransport  = flag.String("hub-transport", "127.0.0.1:7102", "hub 传输路由地址")
		idleTTL       = flag.Int("idle-ttl", 300, "无任务空闲多久后自退出（秒）")
		configVersion = flag.Int64("config-version", 0, "启动时加载的配置版本")
		agentID       = flag.String("agent-id", "loopback_agent", "echo 工具的目标 Agent")
		showVersion   = flag.Bool("version", false, "打印版本号")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("bot-runtime", version.Version)
		return
	}
	if *botID == "" {
		fmt.Fprintln(os.Stderr, "必须提供 -bot-id")
		os.Exit(2)
	}
	cred := os.Getenv(supervisor.BotCredentialEnv)
	if cred == "" {
		fmt.Fprintln(os.Stderr, "缺少 "+supervisor.BotCredentialEnv+"：凭证由 supervisor 注入")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conn, err := grpc.NewClient(*hubGRPC, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "连接 hub gRPC 失败:", err)
		os.Exit(1)
	}
	defer conn.Close()
	client := pb.NewBotRuntimeClient(conn)

	b := &bot{
		id: *botID, cred: cred, client: client, httpBase: "http://" + *hubHTTP,
		transportAddr: *hubTransport, agentID: *agentID, configVersion: *configVersion,
		idleTTL: time.Duration(*idleTTL) * time.Second,
	}
	if err := b.register(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Register 失败:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "bot-runtime %s ready (config_version=%d)\n", b.id, b.configVersion)

	go b.heartbeatLoop(ctx)
	if err := b.runTasks(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "任务流结束:", err)
	}
	_ = b.deregister(context.WithoutCancel(ctx))
}

type bot struct {
	id            string
	cred          string
	client        pb.BotRuntimeClient
	httpBase      string
	transportAddr string
	agentID       string
	configVersion int64
	idleTTL       time.Duration

	mu     sync.Mutex
	leases map[string]string // job_id -> lease_token
	last   time.Time
}

// callContext 为每次 RPC 生成新的 control 根信封（§6.10 规则 2）。
func (b *bot) callContext(ctx context.Context) context.Context {
	env := envelope.NewRoot(b.id, envelope.ChannelTransport, envelope.MsgControl, "rpc_"+randomID())
	md := env.Metadata()
	md[envelope.MetaCredential] = b.cred
	pairs := make([]string, 0, len(md)*2)
	for k, v := range md {
		pairs = append(pairs, k, v)
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}

func randomID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (b *bot) register(ctx context.Context) error {
	resp, err := b.client.Register(b.callContext(ctx), &pb.RegisterRequest{
		BotId: b.id, Capabilities: []string{"echo"}, ConfigVersion: b.configVersion,
	})
	if err != nil {
		return err
	}
	b.cred = resp.Token
	b.leases = map[string]string{}
	b.last = time.Now()
	return nil
}

func (b *bot) deregister(ctx context.Context) error {
	_, err := b.client.Deregister(b.callContext(ctx), &pb.DeregisterRequest{BotId: b.id})
	return err
}

func (b *bot) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.mu.Lock()
			tokens := make([]string, 0, len(b.leases))
			for _, t := range b.leases {
				tokens = append(tokens, t)
			}
			b.mu.Unlock()
			_, _ = b.client.Heartbeat(b.callContext(ctx), &pb.HeartbeatRequest{
				BotId: b.id, JobLeaseTokens: tokens, ConfigVersion: b.configVersion,
			})
		}
	}
}

func (b *bot) runTasks(ctx context.Context) error {
	stream, err := b.client.StreamTasks(b.callContext(ctx), &pb.StreamTasksRequest{ConsumerGroup: b.id})
	if err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		// idle_ttl：无任务超时即自退出（lazy 语义）
		b.mu.Lock()
		idle := time.Since(b.last)
		b.mu.Unlock()
		if idle > b.idleTTL {
			fmt.Fprintf(os.Stderr, "bot %s 空闲超过 %s，自退出\n", b.id, b.idleTTL)
			return nil
		}
		task, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		b.mu.Lock()
		b.last = time.Now()
		b.leases[task.JobId] = task.LeaseToken
		b.mu.Unlock()

		if err := b.handleTask(ctx, task); err != nil {
			fmt.Fprintln(os.Stderr, "处理任务失败:", err)
		}
		b.mu.Lock()
		delete(b.leases, task.JobId)
		b.last = time.Now()
		b.mu.Unlock()
	}
}

func (b *bot) handleTask(ctx context.Context, task *pb.TaskAssignment) error {
	var causal struct {
		CorrelationID  string `json:"correlation_id"`
		RootEnvelopeID string `json:"root_envelope_id"`
		EnvelopeID     string `json:"envelope_id"`
		JobType        string `json:"job_type"`
	}
	if err := json.Unmarshal([]byte(task.CausalContextJson), &causal); err != nil {
		return fmt.Errorf("解析任务因果上下文: %w", err)
	}

	// 按 payload_ref 自行拉取业务载荷（内核不做上下文水合推送）
	text := ""
	if task.PayloadRef != "" {
		payload, err := b.fetchPayload(ctx, task.PayloadRef)
		if err != nil {
			return err
		}
		var doc struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(payload, &doc); err == nil {
			text = doc.Text
		} else {
			text = string(payload)
		}
	}

	// 经 transport 连 loopback agent 执行 echo 工具
	echoed, err := b.echoViaAgent(ctx, task.JobId, text)
	if err != nil {
		return err
	}

	// 以 reply 信封回投内核（from_bot_id=echo_bot），继承原始根，不新开 trace
	if err := b.submitReply(ctx, causal, echoed); err != nil {
		return err
	}
	_, err = b.client.AckTask(b.callContext(ctx), &pb.AckTaskRequest{
		JobId: task.JobId, LeaseToken: task.LeaseToken, ResultRef: echoed,
	})
	return err
}

func (b *bot) fetchPayload(ctx context.Context, ref string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.httpBase+"/v1/payload/"+ref, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Nexus-Credential", b.cred)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取载荷: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("拉取载荷被拒（%d）: %s", resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

// echoViaAgent 走内核传输路由：auth → open → data → 收响应 → close。
// payload 是插件协议字节，内核只校验 length / protocol_id / stream_id。
func (b *bot) echoViaAgent(ctx context.Context, jobID, text string) (string, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", b.transportAddr)
	if err != nil {
		return "", fmt.Errorf("连接传输路由: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	if err := transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameAuth, Payload: []byte(b.cred)}); err != nil {
		return "", err
	}
	if _, err := transport.ReadFrame(conn); err != nil {
		return "", fmt.Errorf("传输鉴权应答: %w", err)
	}
	openReq, err := json.Marshal(transport.OpenRequest{
		TargetAgentID: b.agentID, ProtocolID: "nexus.echo.v1", JobID: jobID,
	})
	if err != nil {
		return "", err
	}
	if err := transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameOpen, Payload: openReq}); err != nil {
		return "", err
	}
	ack, err := transport.ReadFrame(conn)
	if err != nil {
		return "", fmt.Errorf("开流应答: %w", err)
	}
	if ack.FrameType != transport.FrameOpenAck {
		return "", fmt.Errorf("期望 open_ack，得到 %s", ack.FrameType)
	}
	reqPayload, err := json.Marshal(map[string]any{"text": text, "job_id": jobID})
	if err != nil {
		return "", err
	}
	if err := transport.WriteFrame(conn, transport.Frame{
		FrameType: transport.FrameData, StreamID: ack.StreamID, ProtocolID: "nexus.echo.v1", Payload: reqPayload,
	}); err != nil {
		return "", err
	}
	resp, err := transport.ReadFrame(conn)
	if err != nil {
		return "", fmt.Errorf("读取 echo 结果: %w", err)
	}
	_ = transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameClose, StreamID: ack.StreamID})
	return string(resp.Payload), nil
}

func (b *bot) submitReply(ctx context.Context, causal struct {
	CorrelationID  string `json:"correlation_id"`
	RootEnvelopeID string `json:"root_envelope_id"`
	EnvelopeID     string `json:"envelope_id"`
	JobType        string `json:"job_type"`
}, result string) error {
	// 继承原始根：causal_path 为 root → 原信封，causation_id 指向原信封
	root := envelope.NewRoot(b.id, envelope.ChannelTransport, envelope.MsgReply, "reply_"+randomID())
	if causal.RootEnvelopeID != "" {
		root.CorrelationID = causal.CorrelationID
		root.RootEnvelopeID = causal.RootEnvelopeID
		root.CausationID = causal.EnvelopeID
		root.CausalPath = []envelope.CausalNode{
			{NodeID: causal.RootEnvelopeID, NodeType: "envelope", Actor: "user_bot", At: time.Now().UTC()},
		}
		if causal.EnvelopeID != causal.RootEnvelopeID {
			root.CausalPath = append(root.CausalPath, envelope.CausalNode{
				NodeID: causal.EnvelopeID, NodeType: "envelope", Actor: "user_bot", At: time.Now().UTC(),
			})
		}
		root.Traceparent = "00-" + root.Traceparent[3:35] + "-" + randomHex8() + "-01"
	}
	payload := []byte(result)
	body, err := json.Marshal(map[string]any{
		"envelope":       root,
		"payload_base64": base64.StdEncoding.EncodeToString(payload),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.httpBase+"/v1/ingress", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Nexus-Credential", b.cred)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("回投 reply 信封: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("reply 信封被拒（%d）: %s", resp.StatusCode, errBody)
	}
	return nil
}

func randomHex8() string {
	return fmt.Sprintf("%016x", time.Now().UnixNano())[:16]
}
