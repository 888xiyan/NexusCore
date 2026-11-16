package creator

import (
	"strings"
	"testing"
)

func TestValidateBlueprint(t *testing.T) {
	tests := []struct {
		name      string
		bp        BotBlueprint
		wantValid bool
	}{
		{
			name: "合法蓝图",
			bp: BotBlueprint{
				BotID:         "user_code_reviewer",
				DisplayName:   "代码审查专家",
				Role:          "reviewer",
				SystemPrompt:  "你是一名严谨的代码审查员",
				Persona:       "严谨专业",
				InitialMemory: "# 代码审查专家单体记忆\n- 重点关注并发安全与错误处理\n",
				BoundAgentCLI: "claude",
			},
			wantValid: true,
		},
		{
			name: "拦截保留名 user_bot",
			bp: BotBlueprint{
				BotID:        "user_bot",
				DisplayName:  "用户代理",
				SystemPrompt: "测试",
			},
			wantValid: false,
		},
		{
			name: "拦截保留名 security_bot",
			bp: BotBlueprint{
				BotID:        "security_bot",
				DisplayName:  "安全审计",
				SystemPrompt: "测试",
			},
			wantValid: false,
		},
		{
			name: "拦截非法 bot_id",
			bp: BotBlueprint{
				BotID:        "bad-name!",
				DisplayName:  "非法命名",
				SystemPrompt: "测试",
			},
			wantValid: false,
		},
		{
			name: "拦截太短 bot_id",
			bp: BotBlueprint{
				BotID:        "bot",
				DisplayName:  "过短",
				SystemPrompt: "测试",
			},
			wantValid: false,
		},
		{
			name: "拦截超过 200 行记忆",
			bp: BotBlueprint{
				BotID:         "user_test_bot",
				DisplayName:   "超限测试",
				SystemPrompt:  "测试",
				InitialMemory: strings.Repeat("line\n", 201),
			},
			wantValid: false,
		},
		{
			name: "正好 200 行记忆允许通过",
			bp: BotBlueprint{
				BotID:         "user_test_bot",
				DisplayName:   "边界测试",
				SystemPrompt:  "测试",
				InitialMemory: strings.Repeat("line\n", 200),
			},
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ValidateBlueprint(tt.bp)
			if res.Valid != tt.wantValid {
				t.Fatalf("ValidateBlueprint() Valid = %v, want %v, violations: %v", res.Valid, tt.wantValid, res.Violations)
			}
		})
	}
}
