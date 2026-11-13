package envelope

import (
	"encoding/json"
	"fmt"
	"time"
)

// gRPC 因果上下文元数据键（架构总结 §5.4 规则 7 / §6.8）。
// 客户端与服务端共用同一份定义，避免字段漂移。
const (
	MetaCredential     = "x-nexus-credential"
	MetaEnvelopeID     = "x-nexus-envelope-id"
	MetaSchemaVersion  = "x-nexus-schema-version"
	MetaMsgType        = "x-nexus-msg-type"
	MetaFromBotID      = "x-nexus-from-bot-id"
	MetaChannel        = "x-nexus-channel"
	MetaCorrelationID  = "x-nexus-correlation-id"
	MetaCausationID    = "x-nexus-causation-id"
	MetaRootEnvelopeID = "x-nexus-root-envelope-id"
	MetaCausalPath     = "x-nexus-causal-path"
	MetaTraceparent    = "x-nexus-traceparent"
	MetaIdempotencyKey = "x-nexus-idempotency-key"
	MetaRequestID      = "x-nexus-request-id"
)

// Metadata 把信封映射为 gRPC 元数据键值。
func (e *Envelope) Metadata() map[string]string {
	pathJSON, err := json.Marshal(e.CausalPath)
	if err != nil {
		pathJSON = []byte("[]")
	}
	return map[string]string{
		MetaEnvelopeID:     e.EnvelopeID,
		MetaSchemaVersion:  fmt.Sprintf("%d", e.SchemaVersion),
		MetaMsgType:        e.MsgType,
		MetaFromBotID:      e.FromBotID,
		MetaChannel:        e.Channel,
		MetaCorrelationID:  e.CorrelationID,
		MetaCausationID:    e.CausationID,
		MetaRootEnvelopeID: e.RootEnvelopeID,
		MetaCausalPath:     string(pathJSON),
		MetaTraceparent:    e.Traceparent,
		MetaIdempotencyKey: e.IdempotencyKey,
	}
}

// FromMetadata 从 gRPC 元数据重建信封并校验。
// 缺失或不合法一律拒绝，内核不替调用方补造历史（§6.8 规则 5）。
func FromMetadata(md map[string]string) (*Envelope, error) {
	get := func(k string) string { return md[k] }
	if get(MetaEnvelopeID) == "" || get(MetaCorrelationID) == "" || get(MetaRootEnvelopeID) == "" ||
		get(MetaTraceparent) == "" || get(MetaFromBotID) == "" {
		return nil, invalid(CodeCausalRequired, "causal_context", "gRPC 元数据缺少完整因果头")
	}
	var path []CausalNode
	if raw := get(MetaCausalPath); raw != "" {
		if err := json.Unmarshal([]byte(raw), &path); err != nil {
			return nil, invalid(CodePathBroken, MetaCausalPath, "因果路径不是合法 JSON")
		}
	}
	sv := SchemaVersion
	if raw := get(MetaSchemaVersion); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &sv); err != nil {
			return nil, invalid(CodeSchemaVersion, MetaSchemaVersion, "不是整数")
		}
	}
	msgType := get(MetaMsgType)
	if msgType == "" {
		msgType = MsgControl
	}
	channel := get(MetaChannel)
	if channel == "" {
		channel = ChannelTransport
	}
	e := &Envelope{
		EnvelopeID:     get(MetaEnvelopeID),
		SchemaVersion:  sv,
		MsgType:        msgType,
		FromBotID:      get(MetaFromBotID),
		Channel:        channel,
		CorrelationID:  get(MetaCorrelationID),
		CausationID:    get(MetaCausationID),
		RootEnvelopeID: get(MetaRootEnvelopeID),
		CausalPath:     path,
		Traceparent:    get(MetaTraceparent),
		IdempotencyKey: get(MetaIdempotencyKey),
		ReceivedAt:     time.Now().UTC(),
	}
	if e.IdempotencyKey == "" {
		return nil, invalid(CodeRequiredField, MetaIdempotencyKey, "为必填字段")
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}
