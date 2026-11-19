package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/888xiyan/NexusCore/hub/transport"
	"github.com/888xiyan/NexusCore/memory"
	pb "github.com/888xiyan/NexusCore/proto/gen/v1"
	"google.golang.org/grpc"
)

type mockRuntimeServer struct {
	pb.UnimplementedBotRuntimeServer
	acked atomic.Int64
}

func (s *mockRuntimeServer) AckTask(ctx context.Context, req *pb.AckTaskRequest) (*pb.AckTaskResponse, error) {
	s.acked.Add(1)
	return &pb.AckTaskResponse{}, nil
}

func TestSimpleAgentExecutionAndMemory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bot_agent_test_*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	botID := "ops_bot"
	memMgr := memory.NewManager(tmpDir)
	if err := memMgr.EnsureDirs(botID); err != nil {
		t.Fatalf("EnsureDirs 失败: %v", err)
	}
	_ = memMgr.SaveInjectedContext(botID, "# ops_bot 核心记忆\n- 运行规则: 严禁直接执行 shell\n")

	// 1. Mock Hub HTTP Server
	var replyReceived atomic.Bool
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/payload/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"text": "@ops_bot 检查构建环境"})
			return
		}
		if r.URL.Path == "/v1/ingress" {
			replyReceived.Store(true)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer httpSrv.Close()

	// 2. Mock Hub Transport Server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen transport 失败: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Auth frame
				f, _ := transport.ReadFrame(c)
				_ = transport.WriteFrame(c, transport.Frame{FrameType: transport.FrameAuthAck})
				// Open frame
				f, _ = transport.ReadFrame(c)
				var streamID uint32 = 1
				_ = transport.WriteFrame(c, transport.Frame{FrameType: transport.FrameOpenAck, StreamID: streamID})
				// Data frame
				f, _ = transport.ReadFrame(c)
				respPayload, _ := json.Marshal(map[string]any{"stdout": "环境正常, Go 1.26 可用", "exit_code": 0})
				_ = transport.WriteFrame(c, transport.Frame{FrameType: transport.FrameData, StreamID: streamID, Payload: respPayload})
				// Close frame
				_, _ = transport.ReadFrame(c)
				_ = f
			}(conn)
		}
	}()

	// 3. Mock gRPC Runtime Server
	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen gRPC 失败: %v", err)
	}
	defer grpcLn.Close()

	grpcSrv := grpc.NewServer()
	mockServer := &mockRuntimeServer{}
	pb.RegisterBotRuntimeServer(grpcSrv, mockServer)
	go grpcSrv.Serve(grpcLn)
	defer grpcSrv.Stop()

	// 4. 创建 Client 连接
	conn, err := grpc.Dial(grpcLn.Addr().String(), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("连接 mock gRPC 失败: %v", err)
	}
	defer conn.Close()
	client := pb.NewBotRuntimeClient(conn)

	b := &bot{
		id:            botID,
		cred:          "test_cred",
		client:        client,
		httpBase:      httpSrv.URL,
		transportAddr: ln.Addr().String(),
		agentID:       "loopback_agent",
		memMgr:        memMgr,
		leases:        make(map[string]string),
	}
	injected, _ := memMgr.LoadInjectedContext(botID)
	b.injectedMemory = injected

	// 5. 模拟执行任务
	task := &pb.TaskAssignment{
		JobId:      "job_test_01",
		LeaseToken: "lease_01",
		PayloadRef: "ref_payload_01",
		CausalContextJson: `{
			"correlation_id": "corr_test_01",
			"root_envelope_id": "root_01",
			"envelope_id": "env_01",
			"job_type": "chat.task"
		}`,
	}

	ctx := context.Background()
	if err := b.handleTask(ctx, task); err != nil {
		t.Fatalf("handleTask 失败: %v", err)
	}

	// 6. 验证各环节
	if !replyReceived.Load() {
		t.Errorf("未收到回复 Ingress 投递")
	}
	if mockServer.acked.Load() != 1 {
		t.Errorf("未完成 AckTask")
	}

	// 7. 验证单体记忆文件已写入流水
	transFile := filepath.Join(tmpDir, botID, "memory", memory.DirTranscripts, "corr_test_01.jsonl")
	data, err := os.ReadFile(transFile)
	if err != nil {
		t.Fatalf("读取流水文件失败: %v", err)
	}
	if !strings.Contains(string(data), "环境正常, Go 1.26 可用") {
		t.Errorf("流水中未包含 Agent 执行结果: %s", string(data))
	}
}

func TestCLIAgentExecutionAndDistillation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bot_cli_agent_test_*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	botID := "code_bot"
	memMgr := memory.NewManager(tmpDir)
	_ = memMgr.EnsureDirs(botID)

	var replyPayload string
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/payload/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"text": "@code_bot 写一个斐波那契函数"})
			return
		}
		if r.URL.Path == "/v1/ingress" {
			var body struct {
				PayloadBase64 string `json:"payload_base64"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			rawBytes, _ := base64.StdEncoding.DecodeString(body.PayloadBase64)
			replyPayload = string(rawBytes)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer httpSrv.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen transport 失败: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				f, _ := transport.ReadFrame(c)
				_ = transport.WriteFrame(c, transport.Frame{FrameType: transport.FrameAuthAck})

				f, _ = transport.ReadFrame(c)
				var streamID uint32 = 2
				_ = transport.WriteFrame(c, transport.Frame{FrameType: transport.FrameOpenAck, StreamID: streamID})

				f, _ = transport.ReadFrame(c)
				// 模拟底层 CLI 输出大量日志、进度条与核心代码
				rawStdout := "Loading context from repo...\n[====================] 100%\nThinking (1.2s)...\n```go\nfunc Fib(n int) int {\n    if n <= 1 { return n }\n    return Fib(n-1) + Fib(n-2)\n}\n```"
				respPayload, _ := json.Marshal(map[string]any{
					"stdout":     rawStdout,
					"exit_code":  0,
					"elapsed_ms": 1250,
				})
				_ = transport.WriteFrame(c, transport.Frame{
					FrameType:  transport.FrameData,
					StreamID:   streamID,
					ProtocolID: f.ProtocolID,
					Payload:    respPayload,
				})
				_, _ = transport.ReadFrame(c)
			}(conn)
		}
	}()

	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen gRPC 失败: %v", err)
	}
	defer grpcLn.Close()

	grpcSrv := grpc.NewServer()
	mockServer := &mockRuntimeServer{}
	pb.RegisterBotRuntimeServer(grpcSrv, mockServer)
	go grpcSrv.Serve(grpcLn)
	defer grpcSrv.Stop()

	conn, err := grpc.Dial(grpcLn.Addr().String(), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("连接 mock gRPC 失败: %v", err)
	}
	defer conn.Close()
	client := pb.NewBotRuntimeClient(conn)

	b := &bot{
		id:            botID,
		cred:          "test_cred",
		client:        client,
		httpBase:      httpSrv.URL,
		transportAddr: ln.Addr().String(),
		agentID:       "local_probe",
		agentCLI:      "codex",
		agentBound:    true,
		memMgr:        memMgr,
		leases:        make(map[string]string),
	}

	task := &pb.TaskAssignment{
		JobId:      "job_cli_01",
		LeaseToken: "lease_cli_01",
		PayloadRef: "ref_payload_cli_01",
		CausalContextJson: `{
			"correlation_id": "corr_cli_01",
			"root_envelope_id": "root_cli_01",
			"envelope_id": "env_cli_01",
			"job_type": "chat.task"
		}`,
	}

	if err := b.handleTask(context.Background(), task); err != nil {
		t.Fatalf("handleTask 失败: %v", err)
	}

	if mockServer.acked.Load() != 1 {
		t.Errorf("未完成 AckTask")
	}

	// 验证蒸馏器生效：噪音被剔除，核心代码与卡片格式完整
	if strings.Contains(replyPayload, "Loading context") || strings.Contains(replyPayload, "[===") {
		t.Errorf("回复中泄漏了原始进度条噪音: %s", replyPayload)
	}
	if !strings.Contains(replyPayload, "func Fib(n int)") {
		t.Errorf("回复中未包含核心代码: %s", replyPayload)
	}
	if !strings.Contains(replyPayload, "执行器: `codex`") {
		t.Errorf("回复中未标注执行器: %s", replyPayload)
	}
}
