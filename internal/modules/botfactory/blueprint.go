package botfactory

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/888xiyan/NexusCore/memory"
)

var botIDRegex = regexp.MustCompile(`^[a-z0-9_]{5,64}$`)

// 系统保留名黑名单 (AGENTS.md 硬约束 10)
var reservedBots = map[string]bool{
	"user_bot":     true,
	"create_bot":   true,
	"monitor_bot":  true,
	"security_bot": true,
}

// ValidateBlueprint 对 Bot 蓝图进行静态合规性校验（无副作用）
func ValidateBlueprint(bp BotBlueprint) ValidationResult {
	var violations []string

	botID := strings.TrimSpace(bp.BotID)
	if botID == "" {
		violations = append(violations, "bot_id 不能为空")
	} else if !botIDRegex.MatchString(botID) {
		violations = append(violations, fmt.Sprintf("bot_id %q 不符合命名规范 (^[a-z0-9_]{5,64}$)", botID))
	}

	if reservedBots[botID] {
		violations = append(violations, fmt.Sprintf("禁止使用内核保留名 %q 作为 bot_id", botID))
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

	mode := strings.TrimSpace(bp.ActivationMode)
	if mode != "" && mode != "lazy" && mode != "resident" {
		violations = append(violations, fmt.Sprintf("未知的 activation_mode %q，仅支持 lazy 或 resident", bp.ActivationMode))
	}

	return ValidationResult{
		Valid:       len(violations) == 0,
		MemoryLines: lines,
		Violations:  violations,
	}
}
