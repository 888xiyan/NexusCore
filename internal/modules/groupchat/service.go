package groupchat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// CoreService 定义群聊无头基础服务接口。
type CoreService interface {
	// 房间生命周期管理
	CreateRoom(ctx context.Context, req CreateRoomRequest) (*Room, error)
	GetRoom(ctx context.Context, roomID string) (*Room, error)
	CloseRoom(ctx context.Context, roomID string) error

	// 消息投递与分发（核心路由逻辑）
	// 接收消息并严格实施“仅用户 @ 唤醒”原则，返回被实际触发调度入总线的 Bot 列表
	PostMessage(ctx context.Context, req PostMessageRequest) (*PostMessageResult, error)

	// 历史消息查询与上下文回溯
	GetHistory(ctx context.Context, roomID string, limit int, beforeID string) ([]*Message, error)

	// 实时消息订阅（推送给上层插件或前端桥接器）
	SubscribeEvents(ctx context.Context, roomID string) (<-chan *Message, error)
}

type roomInternal struct {
	room       Room
	window     []*Message // 滑动窗口，最大保留 DefaultMaxHistory 条
	history    []*Message // 房间全量历史记录，支持游标分页
	maxHistory int
}

type inMemoryGroupChatService struct {
	mu          sync.RWMutex
	rooms       map[string]*roomInternal
	idempotency map[string]*PostMessageResult // 键: roomID + ":" + clientToken

	subMu       sync.RWMutex
	subscribers map[string][]chan *Message // roomID -> subscribers
}

// NewService 创建基于内存的线程安全群聊核心服务实例。
func NewService() CoreService {
	return &inMemoryGroupChatService{
		rooms:       make(map[string]*roomInternal),
		idempotency: make(map[string]*PostMessageResult),
		subscribers: make(map[string][]chan *Message),
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateRoom 创建无头群聊虚拟房间。
func (s *inMemoryGroupChatService) CreateRoom(ctx context.Context, req CreateRoomRequest) (*Room, error) {
	if strings.TrimSpace(req.Topic) == "" {
		return nil, ErrEmptyTopic
	}
	if strings.TrimSpace(req.OwnerID) == "" {
		return nil, ErrInvalidOwner
	}

	roomID := strings.TrimSpace(req.RoomID)
	if roomID == "" {
		roomID = fmt.Sprintf("room_%d_%s", time.Now().UnixMilli(), randomHex(4))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.rooms[roomID]; exists {
		return nil, fmt.Errorf("房间 %s 已存在", roomID)
	}

	meta := make(map[string]string)
	for k, v := range req.Metadata {
		meta[k] = v
	}

	members := make([]string, len(req.Members))
	copy(members, req.Members)

	r := Room{
		RoomID:    roomID,
		Topic:     strings.TrimSpace(req.Topic),
		OwnerID:   strings.TrimSpace(req.OwnerID),
		Members:   members,
		Metadata:  meta,
		CreatedAt: time.Now().UnixMilli(),
		Closed:    false,
	}

	s.rooms[roomID] = &roomInternal{
		room:       r,
		window:     make([]*Message, 0, DefaultMaxHistory),
		history:    make([]*Message, 0),
		maxHistory: DefaultMaxHistory,
	}

	ret := r
	return &ret, nil
}

// GetRoom 获取指定房间元数据。
func (s *inMemoryGroupChatService) GetRoom(ctx context.Context, roomID string) (*Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, exists := s.rooms[roomID]
	if !exists {
		return nil, ErrRoomNotFound
	}
	ret := r.room
	return &ret, nil
}

// CloseRoom 关闭指定虚拟房间并归档上下文。
func (s *inMemoryGroupChatService) CloseRoom(ctx context.Context, roomID string) error {
	s.mu.Lock()
	r, exists := s.rooms[roomID]
	if !exists {
		s.mu.Unlock()
		return ErrRoomNotFound
	}
	if r.room.Closed {
		s.mu.Unlock()
		return nil
	}
	r.room.Closed = true
	s.mu.Unlock()

	// 优雅关闭该房间的所有订阅通道
	s.subMu.Lock()
	if subs, ok := s.subscribers[roomID]; ok {
		for _, ch := range subs {
			close(ch)
		}
		delete(s.subscribers, roomID)
	}
	s.subMu.Unlock()

	return nil
}

// PostMessage 接收投递消息，执行滑动窗口维护，严格限制“仅用户@唤醒目标Bot”。
func (s *inMemoryGroupChatService) PostMessage(ctx context.Context, req PostMessageRequest) (*PostMessageResult, error) {
	if strings.TrimSpace(req.SenderID) == "" {
		return nil, ErrInvalidSender
	}
	if strings.TrimSpace(req.Content) == "" {
		return nil, ErrEmptyContent
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = RoleUser
	}
	if role != RoleUser && role != RoleBot && role != RoleSystem {
		return nil, ErrInvalidRole
	}

	roomID := req.RoomID

	s.mu.Lock()
	defer s.mu.Unlock()

	r, exists := s.rooms[roomID]
	if !exists {
		return nil, ErrRoomNotFound
	}
	if r.room.Closed {
		return nil, ErrRoomClosed
	}

	// 幂等性校验
	clientToken := strings.TrimSpace(req.ClientToken)
	if clientToken != "" {
		idempotentKey := roomID + ":" + clientToken
		if cached, ok := s.idempotency[idempotentKey]; ok {
			hitResult := *cached
			hitResult.IdempotentHit = true
			return &hitResult, nil
		}
	}

	// 提取 @mentions
	allMentions := ExtractMentions(req.Content)

	// 严格防死循环：只有人类用户发出的消息才触发下游 Bot
	triggeredBots := FilterTriggeredBots(role, allMentions)

	now := time.Now().UnixMilli()
	msgID := fmt.Sprintf("msg_%d_%s", now, randomHex(4))
	msg := &Message{
		MessageID:   msgID,
		RoomID:      roomID,
		SenderID:    req.SenderID,
		Role:        role,
		Content:     req.Content,
		Mentions:    allMentions,
		Timestamp:   now,
		Correlation: fmt.Sprintf("corr_%s", msgID),
	}

	// 历史记录追加
	r.history = append(r.history, msg)

	// 滑动窗口维护（保持 len <= maxHistory）
	r.window = append(r.window, msg)
	if len(r.window) > r.maxHistory {
		r.window = r.window[len(r.window)-r.maxHistory:]
	}

	result := &PostMessageResult{
		Message:       msg,
		TriggeredBots: triggeredBots,
		IdempotentHit: false,
	}

	// 缓存幂等结果
	if clientToken != "" {
		idempotentKey := roomID + ":" + clientToken
		s.idempotency[idempotentKey] = result
	}

	// 广播给房间所有订阅者
	s.broadcast(roomID, msg)

	return result, nil
}

func (s *inMemoryGroupChatService) broadcast(roomID string, msg *Message) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()

	subs, ok := s.subscribers[roomID]
	if !ok || len(subs) == 0 {
		return
	}

	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			// 慢订阅者不阻塞主消息流
		}
	}
}

// GetHistory 查询指定房间的历史消息，支持滑动窗口切片与游标分页。
func (s *inMemoryGroupChatService) GetHistory(ctx context.Context, roomID string, limit int, beforeID string) ([]*Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, exists := s.rooms[roomID]
	if !exists {
		return nil, ErrRoomNotFound
	}

	hist := r.history
	if len(hist) == 0 {
		return []*Message{}, nil
	}

	endIdx := len(hist)
	if beforeID != "" {
		found := false
		for i, m := range hist {
			if m.MessageID == beforeID {
				endIdx = i
				found = true
				break
			}
		}
		if !found {
			// beforeID 未匹配到，返回空
			return []*Message{}, nil
		}
	}

	if limit <= 0 || limit > endIdx {
		limit = endIdx
	}

	startIdx := endIdx - limit
	if startIdx < 0 {
		startIdx = 0
	}

	out := make([]*Message, endIdx-startIdx)
	copy(out, hist[startIdx:endIdx])
	return out, nil
}

// SubscribeEvents 订阅指定房间的实时消息流。
func (s *inMemoryGroupChatService) SubscribeEvents(ctx context.Context, roomID string) (<-chan *Message, error) {
	s.mu.RLock()
	r, exists := s.rooms[roomID]
	if !exists {
		s.mu.RUnlock()
		return nil, ErrRoomNotFound
	}
	if r.room.Closed {
		s.mu.RUnlock()
		return nil, ErrRoomClosed
	}
	s.mu.RUnlock()

	ch := make(chan *Message, 64)

	s.subMu.Lock()
	s.subscribers[roomID] = append(s.subscribers[roomID], ch)
	s.subMu.Unlock()

	// 监听 ctx 取消，自动清理订阅者
	go func() {
		<-ctx.Done()
		s.subMu.Lock()
		defer s.subMu.Unlock()
		subs := s.subscribers[roomID]
		for i, sub := range subs {
			if sub == ch {
				s.subscribers[roomID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}()

	return ch, nil
}
