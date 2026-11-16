package groupchat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExtractMentions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"empty input", "", nil},
		{"whitespace only", "   \t\n  ", nil},
		{"no mentions", "Hello world this is a test", nil},
		{"single valid mention", "Hey @reviewer_bot please check this", []string{"reviewer_bot"}},
		{"multiple mentions", "Ping @qa_bot and @ops_bot and @reviewer_bot", []string{"qa_bot", "ops_bot", "reviewer_bot"}},
		{"duplicate mentions preserved order", "Hey @coder_bot check @ops_bot and then @coder_bot again", []string{"coder_bot", "ops_bot"}},
		{"invalid short bot_id", "Hey @bot! (too short, needs >= 5 chars)", nil},
		{"valid boundary 5 chars", "Hello @abcde how are you", []string{"abcde"}},
		{"special characters ignored", "Email test@example.com is not a bot", []string{"example"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractMentions(tc.input)
			if len(got) != len(tc.expected) {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
			for i := range got {
				if got[i] != tc.expected[i] {
					t.Errorf("at index %d: expected %s, got %s", i, tc.expected[i], got[i])
				}
			}
		})
	}
}

func TestAntiCascadeRoleFilter(t *testing.T) {
	mentions := []string{"coder_bot", "ops_bot"}

	// 1. RoleUser triggers bots
	userTriggered := FilterTriggeredBots(RoleUser, mentions)
	if len(userTriggered) != 2 || userTriggered[0] != "coder_bot" {
		t.Fatalf("RoleUser should trigger bots, got: %v", userTriggered)
	}

	// 2. RoleBot MUST NOT trigger bots
	botTriggered := FilterTriggeredBots(RoleBot, mentions)
	if botTriggered != nil {
		t.Fatalf("RoleBot must return nil TriggeredBots, got: %v", botTriggered)
	}

	// 3. RoleSystem MUST NOT trigger bots
	sysTriggered := FilterTriggeredBots(RoleSystem, mentions)
	if sysTriggered != nil {
		t.Fatalf("RoleSystem must return nil TriggeredBots, got: %v", sysTriggered)
	}

	// 4. Other custom role MUST NOT trigger bots
	otherTriggered := FilterTriggeredBots("guest", mentions)
	if otherTriggered != nil {
		t.Fatalf("Custom role must return nil TriggeredBots, got: %v", otherTriggered)
	}
}

func TestRoomLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	// 1. Create room validation
	_, err := svc.CreateRoom(ctx, CreateRoomRequest{Topic: "", OwnerID: "user_owner"})
	if err != ErrEmptyTopic {
		t.Fatalf("expected ErrEmptyTopic, got %v", err)
	}

	_, err = svc.CreateRoom(ctx, CreateRoomRequest{Topic: "Topic", OwnerID: ""})
	if err != ErrInvalidOwner {
		t.Fatalf("expected ErrInvalidOwner, got %v", err)
	}

	// 2. Create room with auto-generated ID
	r1, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:   "Project Sync",
		OwnerID: "plugin_pm",
		Members: []string{"user_pm", "reviewer_bot"},
		Metadata: map[string]string{
			"project_id": "prj_123",
		},
	})
	if err != nil {
		t.Fatalf("failed to create room: %v", err)
	}
	if !strings.HasPrefix(r1.RoomID, "room_") {
		t.Fatalf("expected roomID prefix room_, got %s", r1.RoomID)
	}
	if r1.Topic != "Project Sync" || r1.Metadata["project_id"] != "prj_123" {
		t.Fatalf("room fields mismatch: %+v", r1)
	}

	// 3. Create room with specified ID
	r2, err := svc.CreateRoom(ctx, CreateRoomRequest{
		RoomID:  "room_custom_001",
		Topic:   "Custom Room",
		OwnerID: "user_bot",
	})
	if err != nil {
		t.Fatalf("failed to create custom room: %v", err)
	}
	if r2.RoomID != "room_custom_001" {
		t.Fatalf("expected room_custom_001, got %s", r2.RoomID)
	}

	// 4. Duplicate room ID error
	_, err = svc.CreateRoom(ctx, CreateRoomRequest{
		RoomID:  "room_custom_001",
		Topic:   "Duplicate",
		OwnerID: "user_bot",
	})
	if err == nil {
		t.Fatalf("expected error on duplicate room ID")
	}

	// 5. GetRoom
	got, err := svc.GetRoom(ctx, "room_custom_001")
	if err != nil || got.RoomID != "room_custom_001" {
		t.Fatalf("GetRoom failed: %v", err)
	}

	_, err = svc.GetRoom(ctx, "non_existent_room")
	if err != ErrRoomNotFound {
		t.Fatalf("expected ErrRoomNotFound, got %v", err)
	}

	// 6. CloseRoom
	err = svc.CloseRoom(ctx, "room_custom_001")
	if err != nil {
		t.Fatalf("failed to close room: %v", err)
	}
	// Double close is idempotent
	err = svc.CloseRoom(ctx, "room_custom_001")
	if err != nil {
		t.Fatalf("double close failed: %v", err)
	}

	err = svc.CloseRoom(ctx, "non_existent_room")
	if err != ErrRoomNotFound {
		t.Fatalf("expected ErrRoomNotFound, got %v", err)
	}

	// Posting to closed room should fail
	_, err = svc.PostMessage(ctx, PostMessageRequest{
		RoomID:   "room_custom_001",
		SenderID: "user_bot",
		Role:     RoleUser,
		Content:  "Hello",
	})
	if err != ErrRoomClosed {
		t.Fatalf("expected ErrRoomClosed, got %v", err)
	}
}

func TestPostMessageAndSlidingWindow(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	r, err := svc.CreateRoom(ctx, CreateRoomRequest{
		RoomID:  "room_test_window",
		Topic:   "Window Test",
		OwnerID: "user_bot",
	})
	if err != nil {
		t.Fatalf("create room failed: %v", err)
	}

	// 1. Input validations
	_, err = svc.PostMessage(ctx, PostMessageRequest{
		RoomID:   r.RoomID,
		SenderID: "",
		Role:     RoleUser,
		Content:  "test",
	})
	if err != ErrInvalidSender {
		t.Fatalf("expected ErrInvalidSender, got %v", err)
	}

	_, err = svc.PostMessage(ctx, PostMessageRequest{
		RoomID:   r.RoomID,
		SenderID: "user_bot",
		Role:     RoleUser,
		Content:  "   \n\t  ",
	})
	if err != ErrEmptyContent {
		t.Fatalf("expected ErrEmptyContent, got %v", err)
	}

	_, err = svc.PostMessage(ctx, PostMessageRequest{
		RoomID:   r.RoomID,
		SenderID: "user_bot",
		Role:     "invalid_role",
		Content:  "test",
	})
	if err != ErrInvalidRole {
		t.Fatalf("expected ErrInvalidRole, got %v", err)
	}

	// 2. Post as user with @mention -> triggers bot
	userRes, err := svc.PostMessage(ctx, PostMessageRequest{
		RoomID:      r.RoomID,
		SenderID:    "user_alice",
		Role:        RoleUser,
		Content:     "Hello @coder_bot and @reviewer_bot please review",
		ClientToken: "token_1001",
	})
	if err != nil {
		t.Fatalf("post message failed: %v", err)
	}
	if len(userRes.TriggeredBots) != 2 || userRes.TriggeredBots[0] != "coder_bot" {
		t.Fatalf("expected triggered bots [coder_bot, reviewer_bot], got %v", userRes.TriggeredBots)
	}
	if userRes.IdempotentHit {
		t.Fatalf("first post should not be idempotent hit")
	}

	// 3. Idempotency test: duplicate client_token
	dupRes, err := svc.PostMessage(ctx, PostMessageRequest{
		RoomID:      r.RoomID,
		SenderID:    "user_alice",
		Role:        RoleUser,
		Content:     "Hello @coder_bot and @reviewer_bot please review",
		ClientToken: "token_1001",
	})
	if err != nil {
		t.Fatalf("idempotent post failed: %v", err)
	}
	if !dupRes.IdempotentHit {
		t.Fatalf("expected IdempotentHit = true")
	}
	if dupRes.Message.MessageID != userRes.Message.MessageID {
		t.Fatalf("idempotent hit should return the same message ID")
	}

	// 4. Post as bot with @mention -> mentions extracted, but TriggeredBots is nil (anti-cascade)
	botRes, err := svc.PostMessage(ctx, PostMessageRequest{
		RoomID:   r.RoomID,
		SenderID: "coder_bot",
		Role:     RoleBot,
		Content:  "I finished review, pinging @ops_bot to deploy",
	})
	if err != nil {
		t.Fatalf("bot post failed: %v", err)
	}
	if len(botRes.Message.Mentions) != 1 || botRes.Message.Mentions[0] != "ops_bot" {
		t.Fatalf("expected Mentions to contain ops_bot, got %v", botRes.Message.Mentions)
	}
	if botRes.TriggeredBots != nil {
		t.Fatalf("anti-cascade violation: bot reply MUST NOT trigger bots, got %v", botRes.TriggeredBots)
	}

	// 5. Test sliding window with 110 messages
	for i := 0; i < 110; i++ {
		_, err := svc.PostMessage(ctx, PostMessageRequest{
			RoomID:   r.RoomID,
			SenderID: "user_alice",
			Role:     RoleUser,
			Content:  fmt.Sprintf("Message sequence %03d", i),
		})
		if err != nil {
			t.Fatalf("failed to post sequential message %d: %v", i, err)
		}
	}

	// Check history: total history should have 2 + 110 = 112 messages
	histAll, err := svc.GetHistory(ctx, r.RoomID, 0, "")
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	if len(histAll) != 112 {
		t.Fatalf("expected 112 messages in full history, got %d", len(histAll))
	}

	// Limit slicing
	histLimit, err := svc.GetHistory(ctx, r.RoomID, 10, "")
	if err != nil {
		t.Fatalf("GetHistory with limit failed: %v", err)
	}
	if len(histLimit) != 10 {
		t.Fatalf("expected 10 messages, got %d", len(histLimit))
	}
	if histLimit[9].Content != "Message sequence 109" {
		t.Fatalf("expected latest message 109, got %s", histLimit[9].Content)
	}

	// Slicing with beforeID
	targetID := histAll[50].MessageID
	histBefore, err := svc.GetHistory(ctx, r.RoomID, 5, targetID)
	if err != nil {
		t.Fatalf("GetHistory with beforeID failed: %v", err)
	}
	if len(histBefore) != 5 {
		t.Fatalf("expected 5 messages before target, got %d", len(histBefore))
	}
	if histBefore[4].MessageID != histAll[49].MessageID {
		t.Fatalf("expected message preceding targetID")
	}

	// Unknown beforeID returns empty
	histEmpty, err := svc.GetHistory(ctx, r.RoomID, 5, "unknown_msg_id")
	if err != nil {
		t.Fatalf("GetHistory unknown beforeID failed: %v", err)
	}
	if len(histEmpty) != 0 {
		t.Fatalf("expected 0 messages for unknown beforeID, got %d", len(histEmpty))
	}
}

func TestBuildIngressEnvelope(t *testing.T) {
	msg := &Message{
		MessageID:   "msg_123456",
		RoomID:      "room_sync_01",
		SenderID:    "user_alice",
		Role:        RoleUser,
		Content:     "Hello @coder_bot",
		Mentions:    []string{"coder_bot"},
		Timestamp:   time.Now().UnixMilli(),
		Correlation: "corr_987654",
	}

	// 1. Success build
	env, err := BuildIngressEnvelope(msg, "coder_bot", "user_bot")
	if err != nil {
		t.Fatalf("BuildIngressEnvelope failed: %v", err)
	}
	if env.FromBotID != "user_bot" {
		t.Errorf("expected fromBotID user_bot, got %s", env.FromBotID)
	}
	if env.Channel != "im" {
		t.Errorf("expected channel im, got %s", env.Channel)
	}
	if env.MsgType != "request" {
		t.Errorf("expected msg_type request, got %s", env.MsgType)
	}
	if env.CorrelationID != "corr_987654" {
		t.Errorf("expected inherited correlation_id corr_987654, got %s", env.CorrelationID)
	}
	if !strings.Contains(env.IdempotencyKey, "coder_bot") {
		t.Errorf("expected idempotency_key containing targetBotID, got %s", env.IdempotencyKey)
	}

	// Validate envelope internally
	if err := env.Validate(); err != nil {
		t.Fatalf("generated envelope failed validation: %v", err)
	}

	// 2. Empty targetBotID error
	_, err = BuildIngressEnvelope(msg, "", "user_bot")
	if err == nil {
		t.Fatalf("expected error on empty targetBotID")
	}

	// 3. Nil msg error
	_, err = BuildIngressEnvelope(nil, "coder_bot", "user_bot")
	if err == nil {
		t.Fatalf("expected error on nil message")
	}

	// 4. Default fromBotID fallback
	msg2 := &Message{
		MessageID: "msg_222222",
		RoomID:    "room_02",
		SenderID:  "plain_alice",
		Content:   "hi",
	}
	env2, err := BuildIngressEnvelope(msg2, "reviewer_bot", "")
	if err != nil {
		t.Fatalf("BuildIngressEnvelope fallback failed: %v", err)
	}
	if env2.FromBotID != "user_bot" {
		t.Fatalf("expected fallback to user_bot, got %s", env2.FromBotID)
	}
}

func TestSubscribeEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewService()
	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		RoomID:  "room_sub_test",
		Topic:   "Subscription Room",
		OwnerID: "user_bot",
	})
	if err != nil {
		t.Fatalf("create room failed: %v", err)
	}

	ch1, err := svc.SubscribeEvents(ctx, room.RoomID)
	if err != nil {
		t.Fatalf("SubscribeEvents ch1 failed: %v", err)
	}
	ch2, err := svc.SubscribeEvents(ctx, room.RoomID)
	if err != nil {
		t.Fatalf("SubscribeEvents ch2 failed: %v", err)
	}

	// Subscribe non-existent room
	_, err = svc.SubscribeEvents(ctx, "non_existent")
	if err != ErrRoomNotFound {
		t.Fatalf("expected ErrRoomNotFound, got %v", err)
	}

	// Post message
	_, err = svc.PostMessage(ctx, PostMessageRequest{
		RoomID:   room.RoomID,
		SenderID: "user_bob",
		Role:     RoleUser,
		Content:  "Live broadcast message",
	})
	if err != nil {
		t.Fatalf("PostMessage failed: %v", err)
	}

	select {
	case m := <-ch1:
		if m.Content != "Live broadcast message" {
			t.Fatalf("ch1 received unexpected message: %s", m.Content)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for ch1")
	}

	select {
	case m := <-ch2:
		if m.Content != "Live broadcast message" {
			t.Fatalf("ch2 received unexpected message: %s", m.Content)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for ch2")
	}

	// Test close room closes channel
	err = svc.CloseRoom(ctx, room.RoomID)
	if err != nil {
		t.Fatalf("CloseRoom failed: %v", err)
	}

	select {
	case _, ok := <-ch1:
		if ok {
			t.Fatalf("expected ch1 to be closed")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for ch1 close")
	}

	// Subscribing to closed room fails
	_, err = svc.SubscribeEvents(ctx, room.RoomID)
	if err != ErrRoomClosed {
		t.Fatalf("expected ErrRoomClosed, got %v", err)
	}
}

func TestConcurrentPostAndHistory(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		RoomID:  "room_concurrent",
		Topic:   "Concurrency Test",
		OwnerID: "user_bot",
	})
	if err != nil {
		t.Fatalf("create room failed: %v", err)
	}

	var wg sync.WaitGroup
	workers := 10
	messagesPerWorker := 30

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < messagesPerWorker; j++ {
				_, err := svc.PostMessage(ctx, PostMessageRequest{
					RoomID:   room.RoomID,
					SenderID: fmt.Sprintf("user_worker_%d", workerID),
					Role:     RoleUser,
					Content:  fmt.Sprintf("Message from %d - %d @coder_bot", workerID, j),
				})
				if err != nil {
					t.Errorf("worker %d post %d failed: %v", workerID, j, err)
					return
				}
				_, _ = svc.GetHistory(ctx, room.RoomID, 10, "")
			}
		}(i)
	}

	wg.Wait()

	hist, err := svc.GetHistory(ctx, room.RoomID, 0, "")
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	expectedTotal := workers * messagesPerWorker
	if len(hist) != expectedTotal {
		t.Fatalf("expected %d messages in history, got %d", expectedTotal, len(hist))
	}
}

func TestCreateRoom_WhitespaceTopicAndOwner(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	_, err := svc.CreateRoom(ctx, CreateRoomRequest{Topic: "   \t\n  ", OwnerID: "user_owner"})
	if err != ErrEmptyTopic {
		t.Fatalf("expected ErrEmptyTopic for whitespace topic, got: %v", err)
	}

	_, err = svc.CreateRoom(ctx, CreateRoomRequest{Topic: "Valid Topic", OwnerID: "  \n\t "})
	if err != ErrInvalidOwner {
		t.Fatalf("expected ErrInvalidOwner for whitespace owner, got: %v", err)
	}
}

func TestCreateRoom_ExtremeLengthsAndMetadata(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	longTopic := strings.Repeat("A", 1000)
	var members []string
	for i := 0; i < 500; i++ {
		members = append(members, fmt.Sprintf("user_member_%03d", i))
	}
	metadata := make(map[string]string)
	for i := 0; i < 100; i++ {
		metadata[fmt.Sprintf("key_%d", i)] = fmt.Sprintf("val_%d", i)
	}

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:    longTopic,
		OwnerID:  "user_owner",
		Members:  members,
		Metadata: metadata,
	})
	if err != nil {
		t.Fatalf("failed to create room with extreme lengths: %v", err)
	}
	if room.Topic != longTopic || len(room.Members) != 500 || len(room.Metadata) != 100 {
		t.Fatalf("room properties not preserved: topicLen=%d, members=%d, meta=%d", len(room.Topic), len(room.Members), len(room.Metadata))
	}
}

func TestCreateRoom_NilMembersAndMetadata(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:    "Nil Test",
		OwnerID:  "user_owner",
		Members:  nil,
		Metadata: nil,
	})
	if err != nil {
		t.Fatalf("create room with nil members/metadata failed: %v", err)
	}
	if room.Members == nil {
		t.Fatalf("expected initialized Members slice, got nil")
	}
	if room.Metadata == nil {
		t.Fatalf("expected initialized Metadata map, got nil")
	}
}

func TestPostMessage_BoundaryMentions(t *testing.T) {
	// @bot (4 chars) - invalid
	m4 := ExtractMentions("Ping @bot now")
	if len(m4) != 0 {
		t.Fatalf("expected 0 mentions for 4 chars, got: %v", m4)
	}

	// @bot12 (5 chars) - valid boundary
	m5 := ExtractMentions("Ping @bot12 now")
	if len(m5) != 1 || m5[0] != "bot12" {
		t.Fatalf("expected bot12, got: %v", m5)
	}

	// @ + 64 chars - valid boundary
	bot64 := "b" + strings.Repeat("a", 63)
	m64 := ExtractMentions("Ping @" + bot64 + " now")
	if len(m64) != 1 || m64[0] != bot64 {
		t.Fatalf("expected 64-char bot, got: %v", m64)
	}

	// @ + 65 chars - invalid boundary
	bot65 := "b" + strings.Repeat("a", 64)
	m65 := ExtractMentions("Ping @" + bot65 + " now")
	if len(m65) != 0 {
		t.Fatalf("expected 0 mentions for 65-char bot, got: %v", m65)
	}
}

func TestPostMessage_DuplicateMentionDeduplication(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:   "Dedup Test",
		OwnerID: "user_owner",
	})
	if err != nil {
		t.Fatalf("failed to create room: %v", err)
	}

	var contentBuilder strings.Builder
	for i := 0; i < 50; i++ {
		contentBuilder.WriteString("@reviewer_bot @coder_bot ")
	}
	contentBuilder.WriteString("please review this PR")

	res, err := svc.PostMessage(ctx, PostMessageRequest{
		RoomID:   room.RoomID,
		SenderID: "user_human",
		Role:     RoleUser,
		Content:  contentBuilder.String(),
	})
	if err != nil {
		t.Fatalf("PostMessage failed: %v", err)
	}
	if len(res.TriggeredBots) != 2 || res.TriggeredBots[0] != "reviewer_bot" || res.TriggeredBots[1] != "coder_bot" {
		t.Fatalf("expected exactly [reviewer_bot, coder_bot], got: %v", res.TriggeredBots)
	}
}

func TestPostMessage_ConcurrentClientTokenRace(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:   "Idempotency Race Test",
		OwnerID: "user_owner",
	})
	if err != nil {
		t.Fatalf("failed to create room: %v", err)
	}

	var wg sync.WaitGroup
	const totalRoutines = 10
	results := make([]*PostMessageResult, totalRoutines)
	errors := make([]error, totalRoutines)

	clientToken := "unique_token_race_123"
	for i := 0; i < totalRoutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			res, err := svc.PostMessage(ctx, PostMessageRequest{
				RoomID:      room.RoomID,
				SenderID:    "user_racer",
				Role:        RoleUser,
				Content:     "Race message content",
				ClientToken: clientToken,
			})
			results[idx] = res
			errors[idx] = err
		}(i)
	}
	wg.Wait()

	for idx, err := range errors {
		if err != nil {
			t.Fatalf("routine %d encountered error: %v", idx, err)
		}
	}

	var idempotentHits int
	var messageID string
	for _, res := range results {
		if messageID == "" {
			messageID = res.Message.MessageID
		} else if res.Message.MessageID != messageID {
			t.Fatalf("message ID mismatch among idempotent calls: %s != %s", res.Message.MessageID, messageID)
		}
		if res.IdempotentHit {
			idempotentHits++
		}
	}

	if idempotentHits != totalRoutines-1 {
		t.Fatalf("expected exactly %d idempotent hits, got %d", totalRoutines-1, idempotentHits)
	}
}

func TestGetHistory_EmptyRoom(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:   "Empty History Test",
		OwnerID: "user_owner",
	})
	if err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	hist, err := svc.GetHistory(ctx, room.RoomID, 10, "")
	if err != nil {
		t.Fatalf("GetHistory on empty room failed: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(hist))
	}
}

func TestGetHistory_NonExistentRoom(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	_, err := svc.GetHistory(ctx, "non_existent_room_xyz", 10, "")
	if err != ErrRoomNotFound {
		t.Fatalf("expected ErrRoomNotFound, got: %v", err)
	}
}

func TestGetHistory_NegativeAndExcessLimit(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:   "Limit Bounds Test",
		OwnerID: "user_owner",
	})
	if err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	for i := 0; i < 10; i++ {
		_, err := svc.PostMessage(ctx, PostMessageRequest{
			RoomID:   room.RoomID,
			SenderID: "user_tester",
			Role:     RoleUser,
			Content:  fmt.Sprintf("Message %d", i),
		})
		if err != nil {
			t.Fatalf("PostMessage failed: %v", err)
		}
	}

	// Negative limit returns all history
	histNeg, err := svc.GetHistory(ctx, room.RoomID, -5, "")
	if err != nil {
		t.Fatalf("GetHistory negative limit failed: %v", err)
	}
	if len(histNeg) != 10 {
		t.Fatalf("expected 10 messages for negative limit, got %d", len(histNeg))
	}

	// Excess limit returns all available messages
	histExcess, err := svc.GetHistory(ctx, room.RoomID, 1000, "")
	if err != nil {
		t.Fatalf("GetHistory excess limit failed: %v", err)
	}
	if len(histExcess) != 10 {
		t.Fatalf("expected 10 messages for excess limit, got %d", len(histExcess))
	}
}

func TestGetHistory_BeforeIDBoundaryIndices(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:   "BeforeID Bounds Test",
		OwnerID: "user_owner",
	})
	if err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	var msgIDs []string
	for i := 0; i < 5; i++ {
		res, err := svc.PostMessage(ctx, PostMessageRequest{
			RoomID:   room.RoomID,
			SenderID: "user_tester",
			Role:     RoleUser,
			Content:  fmt.Sprintf("Bound Msg %d", i),
		})
		if err != nil {
			t.Fatalf("PostMessage failed: %v", err)
		}
		msgIDs = append(msgIDs, res.Message.MessageID)
	}

	// beforeID = first message (index 0): no messages precede it
	histFirst, err := svc.GetHistory(ctx, room.RoomID, 5, msgIDs[0])
	if err != nil {
		t.Fatalf("GetHistory beforeID index 0 failed: %v", err)
	}
	if len(histFirst) != 0 {
		t.Fatalf("expected 0 messages before first message, got %d", len(histFirst))
	}

	// beforeID = last message (index 4): all 4 preceding messages
	histLast, err := svc.GetHistory(ctx, room.RoomID, 10, msgIDs[4])
	if err != nil {
		t.Fatalf("GetHistory beforeID index 4 failed: %v", err)
	}
	if len(histLast) != 4 {
		t.Fatalf("expected 4 messages before last message, got %d", len(histLast))
	}
}

func TestCloseRoom_ConcurrentCloses(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:   "Concurrent Close Test",
		OwnerID: "user_owner",
	})
	if err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	var wg sync.WaitGroup
	const total = 10
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.CloseRoom(ctx, room.RoomID)
		}()
	}
	wg.Wait()

	r, err := svc.GetRoom(ctx, room.RoomID)
	if err != nil || !r.Closed {
		t.Fatalf("room should be closed: %v, %+v", err, r)
	}
}

func TestCloseRoom_HistoryPreservedAfterClose(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:   "History Preserved Test",
		OwnerID: "user_owner",
	})
	if err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	_, err = svc.PostMessage(ctx, PostMessageRequest{
		RoomID:   room.RoomID,
		SenderID: "user_tester",
		Role:     RoleUser,
		Content:  "Preserved message content",
	})
	if err != nil {
		t.Fatalf("PostMessage failed: %v", err)
	}

	err = svc.CloseRoom(ctx, room.RoomID)
	if err != nil {
		t.Fatalf("CloseRoom failed: %v", err)
	}

	// Query history still succeeds
	hist, err := svc.GetHistory(ctx, room.RoomID, 10, "")
	if err != nil {
		t.Fatalf("GetHistory failed on closed room: %v", err)
	}
	if len(hist) != 1 || hist[0].Content != "Preserved message content" {
		t.Fatalf("history not preserved: %+v", hist)
	}

	// Further post is rejected
	_, err = svc.PostMessage(ctx, PostMessageRequest{
		RoomID:   room.RoomID,
		SenderID: "user_tester",
		Role:     RoleUser,
		Content:  "New post should fail",
	})
	if err != ErrRoomClosed {
		t.Fatalf("expected ErrRoomClosed, got: %v", err)
	}
}

func TestSubscribeEvents_ContextCancellationCleanup(t *testing.T) {
	svc := NewService()
	room, err := svc.CreateRoom(context.Background(), CreateRoomRequest{
		Topic:   "Cancel Sub Test",
		OwnerID: "user_owner",
	})
	if err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	subCtx, cancel := context.WithCancel(context.Background())
	ch, err := svc.SubscribeEvents(subCtx, room.RoomID)
	if err != nil {
		t.Fatalf("SubscribeEvents failed: %v", err)
	}

	// Post 1
	_, err = svc.PostMessage(context.Background(), PostMessageRequest{
		RoomID:   room.RoomID,
		SenderID: "user_tester",
		Role:     RoleUser,
		Content:  "Sub msg 1",
	})
	if err != nil {
		t.Fatalf("PostMessage 1 failed: %v", err)
	}

	select {
	case m := <-ch:
		if m.Content != "Sub msg 1" {
			t.Fatalf("unexpected message: %v", m)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for sub msg 1")
	}

	// Cancel subscriber context
	cancel()

	// Wait for goroutine cleanup
	time.Sleep(50 * time.Millisecond)

	// Post 2 should not block or panic even though subscriber canceled
	_, err = svc.PostMessage(context.Background(), PostMessageRequest{
		RoomID:   room.RoomID,
		SenderID: "user_tester",
		Role:     RoleUser,
		Content:  "Sub msg 2",
	})
	if err != nil {
		t.Fatalf("PostMessage 2 failed: %v", err)
	}
}

func TestSubscribeEvents_SlowSubscriberBufferOverflow(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	room, err := svc.CreateRoom(ctx, CreateRoomRequest{
		Topic:   "Buffer Overflow Test",
		OwnerID: "user_owner",
	})
	if err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}

	// Slow subscriber that never reads
	_, err = svc.SubscribeEvents(ctx, room.RoomID)
	if err != nil {
		t.Fatalf("SubscribeEvents failed: %v", err)
	}

	// Fast subscriber that reads
	fastCh, err := svc.SubscribeEvents(ctx, room.RoomID)
	if err != nil {
		t.Fatalf("SubscribeEvents fast failed: %v", err)
	}

	// Post 70 messages (> 64 buffer capacity)
	for i := 0; i < 70; i++ {
		_, err := svc.PostMessage(ctx, PostMessageRequest{
			RoomID:   room.RoomID,
			SenderID: "user_tester",
			Role:     RoleUser,
			Content:  fmt.Sprintf("Overflow msg %d", i),
		})
		if err != nil {
			t.Fatalf("PostMessage %d failed: %v", i, err)
		}
	}

	// Fast subscriber must still be able to read without blocking
	received := 0
	for i := 0; i < 64; i++ {
		select {
		case <-fastCh:
			received++
		default:
		}
	}
	if received == 0 {
		t.Fatalf("fast subscriber received 0 messages")
	}
}

func TestBuildEnvelope_EmptyCorrelationRoot(t *testing.T) {
	msg := &Message{
		MessageID:   "msg_no_corr",
		RoomID:      "room_corr",
		SenderID:    "user_tester",
		Role:        RoleUser,
		Content:     "Hello @coder_bot",
		Correlation: "",
	}

	env, err := BuildIngressEnvelope(msg, "coder_bot", "user_bot")
	if err != nil {
		t.Fatalf("BuildIngressEnvelope failed: %v", err)
	}
	if env.CorrelationID == "" {
		t.Fatalf("expected generated CorrelationID, got empty")
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("envelope validation failed: %v", err)
	}
}

func TestBuildEnvelope_TargetBotIDTrimmingAndValidation(t *testing.T) {
	msg := &Message{
		MessageID: "msg_trim",
		RoomID:    "room_trim",
		SenderID:  "user_alice",
		Content:   "test",
	}

	// Target bot ID with whitespace
	env, err := BuildIngressEnvelope(msg, "   reviewer_bot   ", "user_alice")
	if err != nil {
		t.Fatalf("BuildIngressEnvelope with trimmed target failed: %v", err)
	}
	if !strings.Contains(env.IdempotencyKey, "reviewer_bot") {
		t.Fatalf("expected trimmed targetBotID in idempotency key, got: %s", env.IdempotencyKey)
	}

	// Invalid fromBotID format (e.g. UPPERCASE) fails envelope.Validate()
	_, err = BuildIngressEnvelope(msg, "reviewer_bot", "INVALID-UPPER")
	if err == nil {
		t.Fatalf("expected error for invalid fromBotID format")
	}
}
