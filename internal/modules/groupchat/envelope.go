package groupchat

import (
	"fmt"
	"strings"

	"github.com/888xiyan/NexusCore/internal/envelope"
)

// BuildIngressEnvelope 将被唤醒的消息封装为合法的 Ingress 信封。
// 遵循架构硬约束：
// 1. 信封只承载通讯语义（from_bot_id, channel=im, msg_type=request, correlation_id, causation_id, idempotency_key）；
// 2. 业务维度（如 room_id, content）不污染信封顶层；
// 3. 严格遵循 envelope.Validate 校验规则。
func BuildIngressEnvelope(msg *Message, targetBotID string, fromBotID string) (*envelope.Envelope, error) {
	if msg == nil {
		return nil, fmt.Errorf("message 实体不能为空")
	}
	targetBotID = strings.TrimSpace(targetBotID)
	if targetBotID == "" {
		return nil, fmt.Errorf("targetBotID 不能为空")
	}

	fromBotID = strings.TrimSpace(fromBotID)
	if fromBotID == "" {
		// 默认以当前发送者为准（若为合法 bot_id），否则降级为合法的 user_bot
		if strings.HasPrefix(msg.SenderID, "user_") || strings.HasSuffix(msg.SenderID, "_bot") {
			fromBotID = msg.SenderID
		} else {
			fromBotID = "user_bot"
		}
	}

	idempotencyKey := fmt.Sprintf("im_%s_%s_%s", msg.RoomID, msg.MessageID, targetBotID)
	env := envelope.NewRoot(fromBotID, envelope.ChannelIM, envelope.MsgRequest, idempotencyKey)

	// 若消息已自带因果追踪 ID，则继承该 CorrelationID
	if msg.Correlation != "" {
		env.CorrelationID = msg.Correlation
	}

	if err := env.Validate(); err != nil {
		return nil, fmt.Errorf("组装 Ingress 信封校验失败: %w", err)
	}

	return env, nil
}
