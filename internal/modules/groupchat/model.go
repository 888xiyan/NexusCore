// Package groupchat 实现纯无头的多 Bot 群聊基础核心模块。
// 职责：维护虚拟房间、滑动消息窗口（100）、严格执行“仅用户@唤醒”防死循环、组装 Ingress 因果信封。
package groupchat

import (
	"errors"
)

const (
	RoleUser   = "user"
	RoleBot    = "bot"
	RoleSystem = "system"

	DefaultMaxHistory = 100
)

var (
	ErrEmptyContent  = errors.New("消息内容不能为空")
	ErrInvalidSender = errors.New("发送者身份不能为空")
	ErrInvalidRole   = errors.New("无效的角色类型")
	ErrRoomNotFound  = errors.New("房间不存在")
	ErrRoomClosed    = errors.New("房间已关闭")
	ErrEmptyTopic    = errors.New("房间主题不能为空")
	ErrInvalidOwner  = errors.New("房间创建者不能为空")
)

// Room 定义无头群聊房间元数据实体。
type Room struct {
	RoomID    string            `json:"room_id"`
	Topic     string            `json:"topic"`
	OwnerID   string            `json:"owner_id"`
	Members   []string          `json:"members"`
	Metadata  map[string]string `json:"metadata"`
	CreatedAt int64             `json:"created_at"`
	Closed    bool              `json:"closed"`
}

// Message 定义群聊消息实体。
type Message struct {
	MessageID   string   `json:"message_id"`
	RoomID      string   `json:"room_id"`
	SenderID    string   `json:"sender_id"`
	Role        string   `json:"role"` // "user" | "bot" | "system"
	Content     string   `json:"content"`
	Mentions    []string `json:"mentions"`
	Timestamp   int64    `json:"timestamp"`
	Correlation string   `json:"correlation_id"`
}

// CreateRoomRequest 创建房间请求参数。
type CreateRoomRequest struct {
	RoomID   string            `json:"room_id,omitempty"`
	Topic    string            `json:"topic"`
	OwnerID  string            `json:"owner_id"`
	Members  []string          `json:"members"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// PostMessageRequest 发送消息请求参数。
type PostMessageRequest struct {
	RoomID      string `json:"room_id"`
	SenderID    string `json:"sender_id"`
	Role        string `json:"role"` // "user" | "bot" | "system"
	Content     string `json:"content"`
	ClientToken string `json:"client_token,omitempty"` // 幂等键
}

// PostMessageResult 发送消息返回结果。
type PostMessageResult struct {
	Message       *Message `json:"message"`
	TriggeredBots []string `json:"triggered_bots"` // 实际触发总线调度的 Bot 列表 (若 Role != "user"，恒为 nil)
	IdempotentHit bool     `json:"idempotent_hit"`
}
