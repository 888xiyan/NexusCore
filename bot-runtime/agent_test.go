package main

import (
	"context"
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
