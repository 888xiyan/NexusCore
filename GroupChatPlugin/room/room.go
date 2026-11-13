// Package room 实现多 Bot 群聊房间与消息时序管理，严格执行“仅用户@唤醒”的路由安全铁律。
package room

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/888xiyan/NexusCore/GroupChatPlugin/mention"
)

const (
	// DefaultMaxHistory 是房间保留的历史消息滑动窗口上限
	DefaultMaxHistory = 100

	RoleUser   = "user"
	RoleBot    = "bot"
	RoleSystem = "system"
)

var (
	ErrEmptyRoomID   = errors.New("room_id 不能为空")
	ErrEmptyContent  = errors.New("消息内容不能为空")
	ErrInvalidSender = errors.New("发送者身份不能为空")
)

// Message 表示群聊中的一条消息。
type Message struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	SenderID  string    `json:"sender_id"`
	Role      string    `json:"role"` // "user" | "bot" | "system"
	Content   string    `json:"content"`
	Mentions  []string  `json:"mentions,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Room 表示一个群聊房间。
type Room struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	mu         sync.RWMutex
	messages   []Message
	maxHistory int
}

// NewRoom 创建一个新房间。
func NewRoom(id, name string, maxHistory int) *Room {
	if maxHistory <= 0 {
		maxHistory = DefaultMaxHistory
	}
	return &Room{
		ID:         id,
		Name:       name,
		CreatedAt:  time.Now(),
		maxHistory: maxHistory,
	}
}

// Append 添加一条消息，返回新消息对象及**应该被唤醒的 Bot 列表**。
// 核心路由铁律：
//  1. 仅当 role == RoleUser 时，内容中提取到的 @bot_id 才会作为待唤醒目标返回；
//  2. 当 role == RoleBot 时，提取到的 @ 仅作为展示属性保存在 Mentions 中，返回的待唤醒列表恒为空！
//     彻底杜绝 Bot 间自发级联互 @ 唤醒与发散死循环。
func (r *Room) Append(senderID, role, content string) (*Message, []string, error) {
	if senderID == "" {
		return nil, nil, ErrInvalidSender
	}
	if content == "" {
		return nil, nil, ErrEmptyContent
	}

	allMentions := mention.ExtractMentions(content)

	msg := Message{
		ID:        fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		RoomID:    r.ID,
		SenderID:  senderID,
		Role:      role,
		Content:   content,
		Mentions:  allMentions,
		Timestamp: time.Now(),
	}

	r.mu.Lock()
	r.messages = append(r.messages, msg)
	if len(r.messages) > r.maxHistory {
		// 维持滑动窗口
		r.messages = r.messages[len(r.messages)-r.maxHistory:]
	}
	r.mu.Unlock()

	var routedBots []string
	if role == RoleUser {
		// 只有人类用户的 @ 才能唤醒 Bot
		routedBots = allMentions
	} else {
		// Bot 回复中的 @ 严禁唤醒下游 Bot
		routedBots = nil
	}

	return &msg, routedBots, nil
}

// GetMessages 获取房间内的消息列表（滑动窗口切片）。
func (r *Room) GetMessages(limit int) []Message {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 || limit > len(r.messages) {
		limit = len(r.messages)
	}
	start := len(r.messages) - limit
	out := make([]Message, limit)
	copy(out, r.messages[start:])
	return out
}

// Manager 统一管理所有群聊房间。
type Manager struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

// NewManager 创建房间管理器。
func NewManager() *Manager {
	return &Manager{
		rooms: make(map[string]*Room),
	}
}

// GetOrCreateRoom 获取或创建指定 ID 的房间。
func (m *Manager) GetOrCreateRoom(roomID, name string) *Room {
	m.mu.Lock()
	defer m.mu.Unlock()

	if r, exists := m.rooms[roomID]; exists {
		return r
	}
	if name == "" {
		name = "群聊房间 " + roomID
	}
	r := NewRoom(roomID, name, DefaultMaxHistory)
	m.rooms[roomID] = r
	return r
}

// GetRoom 获取指定房间。
func (m *Manager) GetRoom(roomID string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rooms[roomID]
	return r, ok
}
