package creator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/888xiyan/NexusCore/memory"
)

var botIDRegex = regexp.MustCompile(`^[a-z0-9_]{5,64}$`)

// 系统保留名黑名单
var reservedBots = map[string]bool{
	"user_bot":     true,
	"create_bot":   true,
	"monitor_bot":  true,
	"security_bot": true,
}

// BotBlueprint 定义新建或修改 Bot 的蓝图结构
type BotBlueprint struct {
	BotID          string `json:"bot_id"`
	DisplayName    string `json:"display_name"`
	Role           string `json:"role"`
	SystemPrompt   string `json:"system_prompt"`
	Persona        string `json:"persona"`
	InitialMemory  string `json:"initial_memory"`  // 必须严格 <= 200 行
	BoundAgentCLI  string `json:"bound_agent_cli"` // 绑定的目标 CLI (如 claude, codex, pi, nexus-mock-agent)
	BoundAgentID   string `json:"bound_agent_id"`  // 绑定的目标探针 ID (默认 loopback_agent)
	ActivationMode string `json:"activation_mode"` // lazy | resident (默认 lazy)
}

// ValidationResult 蓝图合规检查结果
type ValidationResult struct {
	Valid       bool     `json:"valid"`
	MemoryLines int      `json:"memory_lines"`
	Violations  []string `json:"violations,omitempty"`
}

// ValidateBlueprint 对 Bot 蓝图进行静态合规性校验（无副作用）
func ValidateBlueprint(bp BotBlueprint) ValidationResult {
	var violations []string

	if bp.BotID == "" {
		violations = append(violations, "bot_id 不能为空")
	} else if !botIDRegex.MatchString(bp.BotID) {
		violations = append(violations, fmt.Sprintf("bot_id %q 不符合命名规范 (^[a-z0-9_]{5,64}$)", bp.BotID))
	}

	if reservedBots[bp.BotID] {
		violations = append(violations, fmt.Sprintf("禁止使用内核保留名 %q 作为 bot_id", bp.BotID))
	} else {
		for _, pfx := range []string{"create_", "monitor_", "security_"} {
			if strings.HasPrefix(bp.BotID, pfx) {
				violations = append(violations, fmt.Sprintf("禁止使用内核保留前缀 %s", pfx))
				break
			}
		}
	}

	if strings.TrimSpace(bp.DisplayName) == "" {
		violations = append(violations, "display_name 不能为空")
	}

	if strings.TrimSpace(bp.SystemPrompt) == "" {
		violations = append(violations, "system_prompt 不能为空")
	}

	lines := memory.CountLines(bp.InitialMemory)
	if lines > memory.MaxMemoryLines {
		violations = append(violations, fmt.Sprintf("单体记忆 MEMORY.md 共有 %d 行，超过 %d 行硬约束限制", lines, memory.MaxMemoryLines))
	}

	if bp.ActivationMode != "" && bp.ActivationMode != "lazy" && bp.ActivationMode != "resident" {
		violations = append(violations, fmt.Sprintf("未知的 activation_mode %q，仅支持 lazy 或 resident", bp.ActivationMode))
	}

	return ValidationResult{
		Valid:       len(violations) == 0,
		MemoryLines: lines,
		Violations:  violations,
	}
}
