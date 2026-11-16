package groupchat

import (
	"regexp"
	"strings"
)

var botMentionRegex = regexp.MustCompile(`@([a-z0-9_]{5,64})\b`)

// ExtractMentions 从文本中提取所有被 @ 的 bot_id 列表，保持首次出现的顺序并去重。
func ExtractMentions(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	matches := botMentionRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var result []string
	for _, m := range matches {
		if len(m) > 1 {
			botID := m[1]
			if !seen[botID] {
				seen[botID] = true
				result = append(result, botID)
			}
		}
	}
	return result
}

// FilterTriggeredBots 严格执行防死循环与 anti-cascade 过滤。
// 核心路由铁律：
// 1. 只有人类用户 (Role == RoleUser) 的 @ 才能作为唤醒目标返回；
// 2. 当 Role != RoleUser (如 RoleBot 或 RoleSystem) 时，触发列表恒返回 nil，
//    即便文本中包含大量 @ 标签，也彻底杜绝 Bot 间自发级联死循环。
func FilterTriggeredBots(role string, mentions []string) []string {
	if role == RoleUser {
		return mentions
	}
	return nil
}
