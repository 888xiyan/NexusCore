package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/888xiyan/NexusCore/hub/httpapi"
	"github.com/888xiyan/NexusCore/hub/transport"
)

func newTestAPI(t *testing.T) (*httpapi.API, string) {
	t.Helper()
	tempDir := t.TempDir()
	api := httpapi.New(nil, nil, nil, nil, nil, nil, nil)
	api.SetMemoryDir(tempDir)
	return api, tempDir
}

func TestCORSPreflightAndHeaders(t *testing.T) {
	api, _ := newTestAPI(t)
	handler := api.Handler()

	// 1. OPTIONS preflight
	req := httptest.NewRequest(http.MethodOptions, "/v1/events/stream", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected preflight 204 No Content, got %d", rec.Code)
	}
	if origin := rec.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Fatalf("expected Access-Control-Allow-Origin '*', got %q", origin)
	}
	if methods := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(methods, "OPTIONS") || !strings.Contains(methods, "POST") {
		t.Fatalf("unexpected Access-Control-Allow-Methods: %s", methods)
	}
	if headers := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(headers, "X-Nexus-Credential") {
		t.Fatalf("unexpected Access-Control-Allow-Headers: %s", headers)
	}

	// 2. Regular GET with CORS headers
	req2 := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req2.Header.Set("Origin", "http://localhost:5173")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec2.Code)
	}
	if origin := rec2.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Fatalf("expected Access-Control-Allow-Origin '*', got %q", origin)
	}
}

func TestSSEStreamingAndBroadcasting(t *testing.T) {
	api, _ := newTestAPI(t)
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events/stream", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("expected Cache-Control no-cache, got %q", cc)
	}

	reader := bufio.NewReader(resp.Body)

	// Broadcast bot_status
	go func() {
		time.Sleep(50 * time.Millisecond)
		api.BroadcastBotStatus("ops_bot", "thinking")
		time.Sleep(50 * time.Millisecond)
		api.BroadcastChatMessage(httpapi.ChatMessage{
			ID:        "msg_101",
			RoomID:    "dev_room",
			SenderID:  "ops_bot",
			Role:      "bot",
			Content:   "任务已就绪",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		time.Sleep(50 * time.Millisecond)
		api.BroadcastAgentEvent(transport.FrameEvent{
			StreamID:   1,
			JobID:      "job_test",
			BotID:      "ops_bot",
			AgentID:    "loopback_agent",
			ProtocolID: "nexus.echo.v1",
			FrameType:  "open",
			Status:     "running",
			Payload:    `{"test":true}`,
			ElapsedMS:  5,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
		})
	}()

	readEvent := func() (eventType string, data string) {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE line: %v", err)
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			} else if line == "" && eventType != "" {
				return eventType, data
			}
		}
	}

	// 1. Verify bot_status
	ev1, data1 := readEvent()
	if ev1 != "bot_status" {
		t.Fatalf("expected event 'bot_status', got %q", ev1)
	}
	if !strings.Contains(data1, "ops_bot") || !strings.Contains(data1, "thinking") {
		t.Fatalf("unexpected bot_status data: %s", data1)
	}

	// 2. Verify chat_message
	ev2, data2 := readEvent()
	if ev2 != "chat_message" {
		t.Fatalf("expected event 'chat_message', got %q", ev2)
	}
	if !strings.Contains(data2, "任务已就绪") {
		t.Fatalf("unexpected chat_message data: %s", data2)
	}

	// 3. Verify agent_event
	ev3, data3 := readEvent()
	if ev3 != "agent_event" {
		t.Fatalf("expected event 'agent_event', got %q", ev3)
	}
	if !strings.Contains(data3, "nexus.echo.v1") || !strings.Contains(data3, "open") {
		t.Fatalf("unexpected agent_event data: %s", data3)
	}
}

func TestBotMemoryGetAndPutValidation(t *testing.T) {
	api, _ := newTestAPI(t)
	handler := api.Handler()

	// 1. GET initial memory (initial template created by EnsureDirs)
	req1 := httptest.NewRequest(http.MethodGet, "/v1/bots/ops_bot/memory", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}
	var getResp1 map[string]any
	if err := json.Unmarshal(rec1.Body.Bytes(), &getResp1); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if getResp1["bot_id"] != "ops_bot" || getResp1["current_lines"].(float64) <= 0 {
		t.Fatalf("unexpected getResp1: %+v", getResp1)
	}

	// 2. PUT memory <= 200 lines (e.g. 10 lines) -> HTTP 200
	var lines10 []string
	for i := 1; i <= 10; i++ {
		lines10 = append(lines10, fmt.Sprintf("- 记忆点 %d", i))
	}
	content10 := strings.Join(lines10, "\n")
	body10, _ := json.Marshal(map[string]string{"content": content10})

	req2 := httptest.NewRequest(http.MethodPut, "/v1/bots/ops_bot/memory", bytes.NewReader(body10))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var putResp2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &putResp2); err != nil {
		t.Fatalf("unmarshal putResp2: %v", err)
	}
	if putResp2["status"] != "saved" || putResp2["lines"].(float64) != 10 {
		t.Fatalf("unexpected putResp2: %+v", putResp2)
	}

	// Verify GET returns updated memory
	req3 := httptest.NewRequest(http.MethodGet, "/v1/bots/ops_bot/memory", nil)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	var getResp3 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &getResp3)
	if getResp3["current_lines"].(float64) != 10 || getResp3["content"] != content10 {
		t.Fatalf("unexpected getResp3: %+v", getResp3)
	}

	// 3. PUT memory > 200 lines (e.g. 205 lines) -> HTTP 400 rejection!
	var lines205 []string
	for i := 1; i <= 205; i++ {
		lines205 = append(lines205, fmt.Sprintf("- 溢出超限行 %d", i))
	}
	content205 := strings.Join(lines205, "\n")
	body205, _ := json.Marshal(map[string]string{"content": content205})

	req4 := httptest.NewRequest(http.MethodPut, "/v1/bots/ops_bot/memory", bytes.NewReader(body205))
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 Bad Request for >200 lines, got %d", rec4.Code)
	}
	if !strings.Contains(rec4.Body.String(), "单体记忆 MEMORY.md 超过 200 行硬约束限制") {
		t.Fatalf("expected limit error message, got: %s", rec4.Body.String())
	}

	// Verify original memory intact (rejection preserved old content)
	req5 := httptest.NewRequest(http.MethodGet, "/v1/bots/ops_bot/memory", nil)
	rec5 := httptest.NewRecorder()
	handler.ServeHTTP(rec5, req5)
	var getResp5 map[string]any
	_ = json.Unmarshal(rec5.Body.Bytes(), &getResp5)
	if getResp5["current_lines"].(float64) != 10 {
		t.Fatalf("expected memory to remain at 10 lines after rejection, got %v", getResp5["current_lines"])
	}
}

func TestChatRelayGetAndPost(t *testing.T) {
	api, _ := newTestAPI(t)
	handler := api.Handler()

	// 1. Initial GET on room
	req1 := httptest.NewRequest(http.MethodGet, "/v1/chat/messages?room_id=test_room", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}
	var getResp1 map[string]any
	_ = json.Unmarshal(rec1.Body.Bytes(), &getResp1)
	msgs1 := getResp1["messages"].([]any)
	if len(msgs1) != 0 {
		t.Fatalf("expected 0 messages initially, got %d", len(msgs1))
	}

	// 2. POST user message mentioning ops_bot and qa_bot
	userBody, _ := json.Marshal(map[string]string{
		"room_id":   "test_room",
		"sender_id": "tester",
		"role":      "user",
		"content":   "请问 @ops_bot 准备好了吗？@qa_bot 也在吗？",
	})
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/messages", bytes.NewReader(userBody))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var postResp2 httpapi.ChatMessage
	_ = json.Unmarshal(rec2.Body.Bytes(), &postResp2)
	if postResp2.RoomID != "test_room" || postResp2.Role != "user" {
		t.Fatalf("unexpected postResp2: %+v", postResp2)
	}
	if len(postResp2.Mentions) != 2 || postResp2.Mentions[0] != "ops_bot" || postResp2.Mentions[1] != "qa_bot" {
		t.Fatalf("expected mentions ['ops_bot', 'qa_bot'], got: %v", postResp2.Mentions)
	}

	// 3. POST bot reply containing mention (should be recorded, but not trigger loops)
	botBody, _ := json.Marshal(map[string]string{
		"room_id":   "test_room",
		"sender_id": "ops_bot",
		"role":      "bot",
		"content":   "[ops_bot] 已就绪 @tester！",
	})
	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/messages", bytes.NewReader(botBody))
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec3.Code)
	}

	// 4. GET messages should return both messages in order
	req4 := httptest.NewRequest(http.MethodGet, "/v1/chat/messages?room_id=test_room", nil)
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	var getResp4 map[string]any
	_ = json.Unmarshal(rec4.Body.Bytes(), &getResp4)
	msgs4 := getResp4["messages"].([]any)
	if len(msgs4) != 2 {
		t.Fatalf("expected 2 messages in room, got %d", len(msgs4))
	}
}

func TestBotRestartSupervisorUnconfigured(t *testing.T) {
	api, _ := newTestAPI(t)
	handler := api.Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/bots/ops_bot/restart", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when supervisor not configured, got %d", rec.Code)
	}
}

func TestTransportOnFrameIntegration(t *testing.T) {
	rt := transport.NewRouter(nil, nil)
	api := httpapi.New(nil, nil, nil, nil, nil, nil, rt)

	var receivedEvt transport.FrameEvent
	ch := make(chan transport.FrameEvent, 1)

	server := httptest.NewServer(api.Handler())
	defer server.Close()

	// Connect SSE subscriber to verify OnFrame leads to SSE agent_event broadcast
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events/stream", nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)

	// Simulate router OnFrame callback trigger
	testEvt := transport.FrameEvent{
		StreamID:   42,
		JobID:      "job_test_stream",
		BotID:      "ops_bot",
		AgentID:    "loopback_agent",
		ProtocolID: "nexus.echo.v1",
		FrameType:  "data_in",
		Status:     "running",
		Payload:    "echo command",
		ElapsedMS:  12,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		rt.OnFrame(testEvt)
	}()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v", err)
		}
		if strings.HasPrefix(line, "event: agent_event") {
			// next line is data: ...
			dataLine, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE data: %v", err)
			}
			payloadJSON := strings.TrimPrefix(strings.TrimSpace(dataLine), "data: ")
			if err := json.Unmarshal([]byte(payloadJSON), &receivedEvt); err != nil {
				t.Fatalf("unmarshal agent_event: %v", err)
			}
			ch <- receivedEvt
			break
		}
	}

	select {
	case evt := <-ch:
		if evt.StreamID != 42 || evt.FrameType != "data_in" || evt.BotID != "ops_bot" || evt.ElapsedMS != 12 {
			t.Fatalf("unexpected frame event received: %+v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent_event on SSE")
	}
}

func TestBotProfileGetUnconfigured(t *testing.T) {
	api, _ := newTestAPI(t)
	handler := api.Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/bots/ops_bot", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when db not connected, got %d", rec.Code)
	}
}

func TestInputValidationAndErrorPaths(t *testing.T) {
	api, _ := newTestAPI(t)
	handler := api.Handler()

	// 1. PUT memory malformed json
	req1 := httptest.NewRequest(http.MethodPut, "/v1/bots/ops_bot/memory", strings.NewReader(`{bad json`))
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed json, got %d", rec1.Code)
	}

	// 2. POST chat messages malformed json
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/messages", strings.NewReader(`{bad json`))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed json, got %d", rec2.Code)
	}

	// 3. POST chat messages empty content
	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/messages", strings.NewReader(`{"content":"   "}`))
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on empty content, got %d", rec3.Code)
	}
}
