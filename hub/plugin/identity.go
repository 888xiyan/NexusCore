package plugin

import (
	"fmt"
	"strings"
)

const (
	CategoryFoundation = "foundation"
	CategoryBusiness   = "business"
	CategoryTool       = "tool"
	CategoryTheme      = "theme"
	CategoryBotPack    = "bot_pack"

	IdentitySystem   = "system"
	IdentityUser     = "user"
	IdentityOwner    = "owner"
	IdentityBusiness = "business"
	IdentityPlugin   = "plugin"
)

func validateCategory(m *Manifest) error {
	switch m.PluginCategory {
	case "", CategoryFoundation, CategoryBusiness, CategoryTool:
		if len(m.BotTemplates) == 0 {
			return errCategoryOwnerRequired
		}
	case CategoryTheme, CategoryBotPack:
		if len(m.BotTemplates) != 0 {
			return fmt.Errorf("%w: plugin_category %q", errCategoryNoBot, m.PluginCategory)
		}
		if m.Binary != "" {
			return fmt.Errorf("%w: plugin_category %q", errCategoryNoProcess, m.PluginCategory)
		}
	default:
		return fmt.Errorf("%w: %q", errCategoryUnknown, m.PluginCategory)
	}
	return nil
}

// ClassifyBotIdentity 给出 Bot 列表上的身份。
// 保留名与内核角色是 system；user_ 前缀是用户 Bot；bot_templates[0] 是 owner；
// 业务插件清单里的 Bot 同时标 business。theme / bot_pack 没有模板，不会出现在这里。
func ClassifyBotIdentity(botID, role string, manifests []Manifest) (string, []string) {
	if isReservedBot(botID) || role == "user" || role == "create" || role == "monitor" || role == "security" {
		return IdentitySystem, []string{IdentitySystem}
	}
	if strings.HasPrefix(botID, "user_") {
		return IdentityUser, []string{IdentityUser}
	}
	owner, business, pluginBot := false, false, false
	for _, mf := range manifests {
		for i, bt := range mf.BotTemplates {
			if bt.BotID != botID {
				continue
			}
			if i == 0 {
				owner = true
			}
			if mf.PluginCategory == CategoryBusiness {
				business = true
				continue
			}
			if i > 0 {
				pluginBot = true
			}
		}
	}
	var all []string
	if owner {
		all = append(all, IdentityOwner)
	}
	if business {
		all = append(all, IdentityBusiness)
	}
	if pluginBot {
		all = append(all, IdentityPlugin)
	}
	if len(all) == 0 {
		return IdentityPlugin, []string{IdentityPlugin}
	}
	return all[0], all
}

func isReservedBot(botID string) bool {
	switch botID {
	case "user_bot", "create_bot", "monitor_bot", "security_bot":
		return true
	default:
		return false
	}
}
