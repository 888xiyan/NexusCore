// Package envelope 实现 IngressEnvelope v1 通讯信封与因果链校验。
// 依据：架构总结 §6.8 / §6.10（D-19 后信封只承载通讯语义）、内核 MVP 详设 §3.4 / §5.3。
// 业务维度（project_id / conversation_id 等）不得进入本包，它们随载荷走。
package envelope

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

const (
	SchemaVersion  = 1
	MaxCausalDepth = 32
)

// 通讯语义取值域（架构总结 §6.10）
const (
	MsgRequest = "request"
	MsgReply   = "reply"
	MsgEvent   = "event"
	MsgControl = "control"
)

const (
	ChannelCLI       = "cli"
	ChannelIM        = "im"
	ChannelVSCode    = "vscode"
	ChannelBrowser   = "browser"
	ChannelTransport = "transport"
	ChannelHealth    = "health"
)

// Code 是固定错误码：入口适配器与内核共用同一份（架构总结 §6.10）。
type Code string

const (
	CodeSchemaVersion     Code = "SCHEMA_VERSION_UNSUPPORTED"
	CodeRequiredField     Code = "REQUIRED_FIELD_MISSING"
	CodeCausalRequired    Code = "CAUSAL_CONTEXT_REQUIRED"
	CodePathBroken        Code = "CAUSAL_PATH_BROKEN"
	CodeDepthExceeded     Code = "CAUSAL_DEPTH_EXCEEDED"
	CodeRootMismatch      Code = "ROOT_ENVELOPE_MISMATCH"
	CodeCausationMismatch Code = "CAUSATION_MISMATCH"
	CodeInvalidMsgType    Code = "INVALID_MSG_TYPE"
	CodeInvalidChannel    Code = "INVALID_CHANNEL"
	CodeInvalidBotID      Code = "INVALID_BOT_ID"
	CodeInvalidHash       Code = "INVALID_PAYLOAD_HASH"
	CodeDuplicate         Code = "DUPLICATE_IDEMPOTENCY_KEY"
)

type ValidationError struct {
	Code   Code
	Field  string
	Detail string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: 字段 %s %s", e.Code, e.Field, e.Detail)
}

func invalid(code Code, field, detail string) *ValidationError {
	return &ValidationError{Code: code, Field: field, Detail: detail}
}

// CausalNode 只存节点 ID / 类型 / actor / 时间，不存正文（§6.8 规则 6）。
type CausalNode struct {
	NodeID   string    `json:"node_id"`
	NodeType string    `json:"node_type"`
	Actor    string    `json:"actor"`
	At       time.Time `json:"at"`
}

type Envelope struct {
	EnvelopeID     string       `json:"envelope_id"`
	SchemaVersion  int          `json:"schema_version"`
	MsgType        string       `json:"msg_type"`
	FromBotID      string       `json:"from_bot_id"`
	Channel        string       `json:"channel"`
	ChannelRef     string       `json:"channel_ref,omitempty"`
	CorrelationID  string       `json:"correlation_id"`
	CausationID    string       `json:"causation_id,omitempty"`
	RootEnvelopeID string       `json:"root_envelope_id"`
	CausalPath     []CausalNode `json:"causal_path"`
	Traceparent    string       `json:"traceparent"`
	IdempotencyKey string       `json:"idempotency_key"`
	ReceivedAt     time.Time    `json:"received_at"`
	PayloadHash    string       `json:"payload_hash,omitempty"`
	PayloadRef     string       `json:"payload_ref,omitempty"`
	PolicyVersion  string       `json:"policy_version,omitempty"`
}

var (
	botIDRe   = regexp.MustCompile(`^[a-z0-9_]{5,64}$`)
	hashRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	traceRe   = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)
	msgTypes  = map[string]bool{MsgRequest: true, MsgReply: true, MsgEvent: true, MsgControl: true}
	channels  = map[string]bool{ChannelCLI: true, ChannelIM: true, ChannelVSCode: true, ChannelBrowser: true, ChannelTransport: true, ChannelHealth: true}
	nodeTypes = map[string]bool{"envelope": true, "job": true, "event": true, "stream": true}
)

// Validate 按 §3.4 规则 1 的顺序校验：schema_version → 必填 → 因果链连续性 / 深度。
// 凭证校验与幂等去重由 ingress 模块承担（需要数据库与注册表）。
func (e *Envelope) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return invalid(CodeSchemaVersion, "schema_version", fmt.Sprintf("只支持 %d，收到 %d", SchemaVersion, e.SchemaVersion))
	}
	for _, f := range []struct{ name, val string }{
		{"envelope_id", e.EnvelopeID},
		{"msg_type", e.MsgType},
		{"from_bot_id", e.FromBotID},
		{"channel", e.Channel},
		{"correlation_id", e.CorrelationID},
		{"root_envelope_id", e.RootEnvelopeID},
		{"idempotency_key", e.IdempotencyKey},
		{"traceparent", e.Traceparent},
	} {
		if f.val == "" {
			return invalid(CodeRequiredField, f.name, "为必填字段")
		}
	}
	if e.ReceivedAt.IsZero() {
		return invalid(CodeRequiredField, "received_at", "为必填字段")
	}
	if !msgTypes[e.MsgType] {
		return invalid(CodeInvalidMsgType, "msg_type", fmt.Sprintf("非法取值 %q", e.MsgType))
	}
	if !channels[e.Channel] {
		return invalid(CodeInvalidChannel, "channel", fmt.Sprintf("非法取值 %q", e.Channel))
	}
	if !botIDRe.MatchString(e.FromBotID) {
		return invalid(CodeInvalidBotID, "from_bot_id", fmt.Sprintf("不满足 ^[a-z0-9_]{5,64}$：%q", e.FromBotID))
	}
	if !traceRe.MatchString(e.Traceparent) {
		return invalid(CodeCausalRequired, "traceparent", fmt.Sprintf("不是合法 W3C traceparent：%q", e.Traceparent))
	}
	if e.PayloadHash != "" && !hashRe.MatchString(e.PayloadHash) {
		return invalid(CodeInvalidHash, "payload_hash", "必须是 64 位小写十六进制 sha256")
	}
	if e.PayloadRef != "" && e.PayloadHash == "" {
		return invalid(CodeRequiredField, "payload_hash", "携带 payload_ref 时必须同时携带 payload_hash")
	}
	return e.validateCausal()
}

func (e *Envelope) validateCausal() error {
	if len(e.CausalPath) == 0 {
		return invalid(CodeCausalRequired, "causal_path", "缺失因果头")
	}
	if len(e.CausalPath) > MaxCausalDepth {
		return invalid(CodeDepthExceeded, "causal_path", fmt.Sprintf("深度 %d 超过上限 %d", len(e.CausalPath), MaxCausalDepth))
	}
	seen := make(map[string]bool, len(e.CausalPath))
	for i, n := range e.CausalPath {
		if n.NodeID == "" {
			return invalid(CodePathBroken, fmt.Sprintf("causal_path[%d].node_id", i), "为空")
		}
		if !nodeTypes[n.NodeType] {
			return invalid(CodePathBroken, fmt.Sprintf("causal_path[%d].node_type", i), fmt.Sprintf("非法取值 %q", n.NodeType))
		}
		if n.Actor == "" {
			return invalid(CodePathBroken, fmt.Sprintf("causal_path[%d].actor", i), "为空")
		}
		if seen[n.NodeID] {
			return invalid(CodePathBroken, fmt.Sprintf("causal_path[%d]", i), fmt.Sprintf("节点 %q 重复，因果链成环", n.NodeID))
		}
		seen[n.NodeID] = true
	}
	if e.CausalPath[0].NodeID != e.RootEnvelopeID {
		return invalid(CodeRootMismatch, "root_envelope_id",
			fmt.Sprintf("与 causal_path 首项 %q 不一致", e.CausalPath[0].NodeID))
	}
	// 根节点：causation_id 为空，路径只含自身一项（§6.10 规则 2）
	if e.CausationID == "" {
		if len(e.CausalPath) != 1 || e.CausalPath[0].NodeID != e.EnvelopeID {
			return invalid(CodePathBroken, "causal_path", "根节点的因果路径必须只含自身一项")
		}
		if e.RootEnvelopeID != e.EnvelopeID {
			return invalid(CodeRootMismatch, "root_envelope_id", "根节点必须等于 envelope_id")
		}
		return nil
	}
	last := e.CausalPath[len(e.CausalPath)-1]
	if last.NodeID != e.CausationID {
		return invalid(CodeCausationMismatch, "causation_id",
			fmt.Sprintf("与 causal_path 末项 %q 不一致", last.NodeID))
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("生成随机 ID 失败: %v", err))
	}
	return hex.EncodeToString(b)
}

func newTraceparent(traceID string) string {
	return "00-" + traceID + "-" + randomHex(8) + "-01"
}

func traceIDOf(traceparent string) string {
	if len(traceparent) >= 36 {
		return traceparent[3:35]
	}
	return randomHex(16)
}

// NewRoot 生成根信封：入口开启新 trace，correlation_id 默认取 envelope_id。
func NewRoot(fromBotID, channel, msgType, idempotencyKey string) *Envelope {
	id := "env_" + randomHex(12)
	now := time.Now().UTC()
	return &Envelope{
		EnvelopeID:     id,
		SchemaVersion:  SchemaVersion,
		MsgType:        msgType,
		FromBotID:      fromBotID,
		Channel:        channel,
		CorrelationID:  id,
		RootEnvelopeID: id,
		CausalPath:     []CausalNode{{NodeID: id, NodeType: "envelope", Actor: fromBotID, At: now}},
		Traceparent:    newTraceparent(randomHex(16)),
		IdempotencyKey: idempotencyKey,
		ReceivedAt:     now,
	}
}

// Derive 派生子信封：继承 correlation_id 与 root_envelope_id，追加因果节点并生成子 span。
// causationID 指向直接父节点（上一个 job / 事件 / 信封）。
func Derive(parent *Envelope, fromBotID, channel, msgType, idempotencyKey, causationID string) (*Envelope, error) {
	if parent == nil {
		return nil, invalid(CodeCausalRequired, "parent", "派生信封必须有父信封，内核不补造历史")
	}
	if err := parent.Validate(); err != nil {
		return nil, err
	}
	if causationID == "" {
		return nil, invalid(CodeCausalRequired, "causation_id", "派生信封必须携带直接父节点")
	}
	id := "env_" + randomHex(12)
	now := time.Now().UTC()
	path := make([]CausalNode, 0, len(parent.CausalPath)+1)
	path = append(path, parent.CausalPath...)
	// causal_path 是 root → parent 的祖先列表：直接原因若不是末项，需补上该节点
	if path[len(path)-1].NodeID != causationID {
		nodeType := "envelope"
		actor := parent.FromBotID
		if causationID != parent.EnvelopeID {
			nodeType = "job"
		}
		path = append(path, CausalNode{NodeID: causationID, NodeType: nodeType, Actor: actor, At: now})
	}
	child := &Envelope{
		EnvelopeID:     id,
		SchemaVersion:  SchemaVersion,
		MsgType:        msgType,
		FromBotID:      fromBotID,
		Channel:        channel,
		CorrelationID:  parent.CorrelationID,
		CausationID:    causationID,
		RootEnvelopeID: parent.RootEnvelopeID,
		CausalPath:     path,
		Traceparent:    newTraceparent(traceIDOf(parent.Traceparent)),
		IdempotencyKey: idempotencyKey,
		ReceivedAt:     now,
		PolicyVersion:  parent.PolicyVersion,
	}
	if err := child.Validate(); err != nil {
		return nil, err
	}
	return child, nil
}
