package plugin

import (
	"testing"
)

func TestValidateManifest(t *testing.T) {
	valid := &Manifest{
		PluginID:   "plugin_demo",
		Name:       "Demo Plugin",
		Version:    "1.0.0",
		PluginType: "workspace",
		BotTemplates: []BotTemplate{
			{
				BotID:          "demo_bot",
				Role:           "plugin",
				DisplayName:    "Demo Bot",
				ActivationMode: "lazy",
				Capabilities:   []string{"demo"},
			},
		},
		Publishing: []string{"plugin_demo.task.created"},
	}

	if err := ValidateManifest(valid); err != nil {
		t.Fatalf("合法清单校验失败: %v", err)
	}

	// 错误 1: plugin_id 格式不符
	badID := *valid
	badID.PluginID = "bad_id"
	if err := ValidateManifest(&badID); err == nil {
		t.Errorf("非法 plugin_id 应该校验失败")
	}

	// 错误 2: 命中内核保留名
	reservedBot := *valid
	reservedBot.BotTemplates = []BotTemplate{
		{BotID: "user_bot", Role: "plugin", DisplayName: "Hack"},
	}
	if err := ValidateManifest(&reservedBot); err == nil {
		t.Errorf("命中保留名 user_bot 应该校验失败")
	}

	// 错误 3: 插件侧 bot 带 user_ 前缀
	userPfxBot := *valid
	userPfxBot.BotTemplates = []BotTemplate{
		{BotID: "user_writer_bot", Role: "plugin", DisplayName: "User Writer"},
	}
	if err := ValidateManifest(&userPfxBot); err == nil {
		t.Errorf("插件声明 bot 带有 user_ 前缀应该校验失败")
	}

	// 错误 4: 事件前缀不以 plugin_id 开头
	badEvent := *valid
	badEvent.Publishing = []string{"other.event"}
	if err := ValidateManifest(&badEvent); err == nil {
		t.Errorf("事件未以 plugin_id 开头应该校验失败")
	}

	// 错误 5: 事件命中保留前缀
	reservedEvent := *valid
	reservedEvent.Publishing = []string{"bot.connected"}
	if err := ValidateManifest(&reservedEvent); err == nil {
		t.Errorf("事件以 bot. 开头应该校验失败")
	}
}
