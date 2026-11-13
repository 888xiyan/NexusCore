// Package mention 实现群聊中的 @ 语法提取器。
// 规则：
//  1. Bot ID 遵循 ^[a-z0-9_]{5,64}$；
//  2. 提取文本中的 @<bot_id>，保持出现顺序并去重；
//  3. 严格限制：只有人类用户的 @ 才能作为唤醒依据，Bot 回复中的 @ 仅作为展示属性。
package mention

import (
	"regexp"
	"strings"
)

// botMentionRegex 匹配 @ 后跟合法的 bot_id
// 例如: @ops_bot, @qa_bot, @echo_bot_1
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
