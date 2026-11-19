package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/888xiyan/NexusCore/proto/gen/v1"
)

func TestCLIDispatchAllowed(t *testing.T) {
	if err := CLIDispatchAllowed(false, "loopback_agent"); err == nil {
		t.Fatal("未绑定应拒绝")
	}
	if err := CLIDispatchAllowed(true, ""); err == nil {
		t.Fatal("空探针应拒绝")
	}
	if err := CLIDispatchAllowed(true, "local_probe"); err != nil {
		t.Fatal(err)
	}
}

func TestUnboundBotDoesNotDialCLI(t *testing.T) {
	dialed := false
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			dialed = true
			_ = conn.Close()
		}
	}()

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "run cli"})
	}))
	defer httpSrv.Close()

	b := &bot{
		id:            "user_unbound_bot",
		httpBase:      httpSrv.URL,
		transportAddr: ln.Addr().String(),
		agentID:       "loopback_agent",
		agentCLI:      "codex",
		agentBound:    false,
	}
	err = b.handleTask(context.Background(), &pb.TaskAssignment{
		JobId:             "job_unbound",
		LeaseToken:        "lease",
		PayloadRef:        "ref",
		CausalContextJson: `{"job_type":"chat.task","correlation_id":"c","root_envelope_id":"r","envelope_id":"e"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "未绑定探针") {
		t.Fatalf("应拒绝派发: %v", err)
	}
	if dialed {
		t.Fatal("未绑定 Bot 仍然连接了传输路由")
	}
}
