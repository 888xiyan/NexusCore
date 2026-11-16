package toolcenter

import (
	"strings"
	"time"
)

// DelegationGate 中台代批 RBAC 网关
type DelegationGate struct{}

// NewDelegationGate 创建代批裁决网关实例
func NewDelegationGate() *DelegationGate {
	return &DelegationGate{}
}

// Evaluate 严格判定中台 Bot 的代批请求是否安全放行还是必须人工确认 (HITL)
// 遵循刚性安全防线：高危操作一票否决，未明确定义操作默认归入 Tier 2 拦截
func (g *DelegationGate) Evaluate(proposerBotID string, actionType string, params map[string]interface{}) DelegationDecision {
	action := strings.ToLower(strings.TrimSpace(actionType))
	now := time.Now()

	// 1. Tier 2 刚性拦截高危黑名单 (Mandatory HITL)
	if isTier2Action(action, params) {
		return DelegationDecision{
			ActionType:   actionType,
			Target:       extractTarget(params),
			Tier:         Tier2DangerousHITL,
			Allowed:      false,
			RequiresHITL: true,
			Reason:       "Tier 2 高危操作绝对禁止中台代批（包含插件增删改/进程Exec/凭证提取/穿透沙箱），必须人工确认 (Mandatory HITL)",
			AuditPayload: params,
			Timestamp:    now,
		}
	}

	// 2. Tier 1 受控代批白名单
	if isTier1Action(action) {
		return DelegationDecision{
			ActionType:   actionType,
			Target:       extractTarget(params),
			Tier:         Tier1SafeControlled,
			Allowed:      true,
			RequiresHITL: false,
			Reason:       "Tier 1 受控低危操作已由中台 Bot (HubBot) 代理审批通过",
			AuditPayload: params,
			Timestamp:    now,
		}
	}

	// 3. 默认安全防线：未明确识别的操作一律阻断并回退至人工审查 (Fail-Safe)
	return DelegationDecision{
		ActionType:   actionType,
		Target:       extractTarget(params),
		Tier:         Tier2DangerousHITL,
		Allowed:      false,
		RequiresHITL: true,
		Reason:       "未识别的操作类型，依据最小特权与安全默认防线强制转为人工确认",
		AuditPayload: params,
		Timestamp:    now,
	}
}

// isTier2Action 检查是否命中 Tier 2 高危操作
func isTier2Action(action string, params map[string]interface{}) bool {
	// 插件生命周期变更
	if strings.HasPrefix(action, "plugin.install") ||
		strings.HasPrefix(action, "plugin.uninstall") ||
		strings.HasPrefix(action, "plugin.upgrade") ||
		strings.HasPrefix(action, "plugin.disable") {
		return true
	}

	// 系统/进程物理执行与杀死
	if strings.HasPrefix(action, "process.exec") ||
		strings.HasPrefix(action, "process.kill") ||
		strings.HasPrefix(action, "os.exec") ||
		strings.HasPrefix(action, "agent.exec_raw_shell") ||
		strings.HasPrefix(action, "system.reboot") {
		return true
	}

	// 敏感凭证与密钥导出
	if strings.HasPrefix(action, "secret.") ||
		strings.HasPrefix(action, "vault.decrypt") ||
		strings.HasPrefix(action, "token.export") {
		return true
	}

	// 沙盒网络隔离穿透与改写
	if strings.HasPrefix(action, "network.bypass") ||
		strings.HasPrefix(action, "sandbox.penetrate") ||
		strings.HasPrefix(action, "firewall.allow_all") {
		return true
	}

	// 提示词注入启发式关键字识别
	if params != nil {
		for _, val := range params {
			if strVal, ok := val.(string); ok {
				lower := strings.ToLower(strVal)
				if strings.Contains(lower, "rm -rf") ||
					strings.Contains(lower, "drop table") ||
					strings.Contains(lower, "curl ") ||
					strings.Contains(lower, "chmod 777") ||
					strings.Contains(lower, "bypass_security") {
					return true
				}
			}
		}
	}

	return false
}

// isTier1Action 检查是否为合法的 Tier 1 低危操作
func isTier1Action(action string) bool {
	// UI 与主题偏好
	if action == "ui.theme.set" || action == "ui.theme.get" || action == "ui.lang.set" {
		return true
	}

	// 系统只读观测
	if action == "system.info.get" || action == "system.health.get" || action == "bot.status.list" {
		return true
	}

	// 群聊协同与消息中继
	if action == "chat.room.create" || action == "chat.bot.invite" || action == "chat.notify.send" {
		return true
	}

	// 工具中心只读操作
	if action == "tool.list" || action == "skill.list" || action == "tool.info.get" {
		return true
	}

	return false
}

func extractTarget(params map[string]interface{}) string {
	if params == nil {
		return "unknown"
	}
	if target, ok := params["target"].(string); ok && target != "" {
		return target
	}
	if pluginID, ok := params["plugin_id"].(string); ok && pluginID != "" {
		return pluginID
	}
	if toolID, ok := params["tool_id"].(string); ok && toolID != "" {
		return toolID
	}
	return "system"
}
