package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/888xiyan/NexusCore/hub/config"
)

func TestClassifyBotIdentity(t *testing.T) {
	manifests := []Manifest{
		{
			PluginID:       "plugin_groupchat",
			PluginCategory: CategoryFoundation,
			BotTemplates:   []BotTemplate{{BotID: "groupchat_bot"}},
		},
		{
			PluginID:       "plugin_browser",
			PluginCategory: CategoryBusiness,
			BotTemplates: []BotTemplate{
				{BotID: "browser_bot"},
				{BotID: "browser_nl_bot"},
			},
		},
	}
	cases := []struct {
		botID, role, primary string
		all                  []string
	}{
		{"user_bot", "user", IdentitySystem, []string{IdentitySystem}},
		{"create_bot", "create", IdentitySystem, []string{IdentitySystem}},
		{"user_writer_bot", "plugin", IdentityUser, []string{IdentityUser}},
		{"groupchat_bot", "plugin", IdentityOwner, []string{IdentityOwner}},
		{"browser_bot", "plugin", IdentityOwner, []string{IdentityOwner, IdentityBusiness}},
		{"browser_nl_bot", "plugin", IdentityBusiness, []string{IdentityBusiness}},
		{"echo_bot", "plugin", IdentityPlugin, []string{IdentityPlugin}},
	}
	for _, tc := range cases {
		primary, all := ClassifyBotIdentity(tc.botID, tc.role, manifests)
		if primary != tc.primary {
			t.Errorf("%s 主身份 %s，期望 %s", tc.botID, primary, tc.primary)
		}
		if len(all) != len(tc.all) {
			t.Errorf("%s 身份 %v，期望 %v", tc.botID, all, tc.all)
			continue
		}
		for i := range all {
			if all[i] != tc.all[i] {
				t.Errorf("%s 身份 %v，期望 %v", tc.botID, all, tc.all)
			}
		}
	}
}

func TestValidateManifestCategory(t *testing.T) {
	theme := &Manifest{PluginID: "plugin_theme_day", Name: "Day", Version: "1.0.0", PluginType: "workspace", PluginCategory: CategoryTheme}
	if err := ValidateManifest(theme); err != nil {
		t.Fatalf("theme 无 Bot 应合法: %v", err)
	}
	theme.BotTemplates = []BotTemplate{{BotID: "theme_bot", DisplayName: "Theme", ActivationMode: "lazy"}}
	if err := ValidateManifest(theme); err == nil {
		t.Fatal("theme 注册 Bot 应失败")
	}
	pack := &Manifest{
		PluginID: "plugin_pack_coder", Name: "Pack", Version: "1.0.0", PluginType: "workspace",
		PluginCategory: CategoryBotPack, Binary: "/tmp/pack", BinaryDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if err := ValidateManifest(pack); err == nil {
		t.Fatal("bot_pack 带 binary 应失败")
	}
	bad := &Manifest{
		PluginID: "plugin_bad_cat", Name: "Bad", Version: "1.0.0", PluginType: "workspace", PluginCategory: "business_pack",
		BotTemplates: []BotTemplate{{BotID: "bad_cat_bot", DisplayName: "Bad", ActivationMode: "lazy"}},
	}
	if err := ValidateManifest(bad); err == nil {
		t.Fatal("非法 plugin_category 应失败")
	}
}

func TestInstallLifecycleMemory(t *testing.T) {
	mgr := NewManager(nil, nil, nil, nil)
	ctx := context.Background()
	actor := config.BotActor(config.UserBot)

	bad := []byte(`{"plugin_id":"plugin_bad_cat","plugin_type":"workspace","plugin_category":"nope","bot_templates":[{"bot_id":"bad_cat_bot","display_name":"Bad","activation_mode":"lazy"}]}`)
	if _, _, err := mgr.Install(ctx, actor, bad); err == nil {
		t.Fatal("非法分类安装应失败")
	}
	recs, err := mgr.ListLifecycle(ctx, "plugin_bad_cat")
	if err != nil || len(recs) != 1 || recs[0].Action != "install" || recs[0].Result != "rejected" {
		t.Fatalf("非法分类留底不符: %+v err=%v", recs, err)
	}

	dir := t.TempDir()
	binPath := filepath.Join(dir, "plugin_digest_bot")
	if err := os.WriteFile(binPath, []byte("not-the-digest"), 0o755); err != nil {
		t.Fatal(err)
	}
	mismatch := []byte(`{
		"plugin_id":"plugin_digest_bot","name":"Digest","version":"1.0.0","plugin_type":"workspace","plugin_category":"tool",
		"binary":"` + binPath + `",
		"binary_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bot_templates":[{"bot_id":"digest_bot","display_name":"Digest","activation_mode":"lazy"}]
	}`)
	if _, _, err := mgr.Install(ctx, actor, mismatch); err == nil || !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("摘要不符应失败: %v", err)
	}
	recs, err = mgr.ListLifecycle(ctx, "plugin_digest_bot")
	if err != nil || len(recs) != 1 || recs[0].Result != "rejected" {
		t.Fatalf("摘要不符留底不符: %+v err=%v", recs, err)
	}

	theme := []byte(`{"plugin_id":"plugin_theme_day","name":"Day","version":"1.0.0","plugin_type":"workspace","plugin_category":"theme"}`)
	mf, _, err := mgr.Install(ctx, actor, theme)
	if err != nil {
		t.Fatalf("theme 安装失败: %v", err)
	}
	if len(mf.BotTemplates) != 0 {
		t.Fatal("theme 不应注册 Bot")
	}
	recs, err = mgr.ListLifecycle(ctx, "plugin_theme_day")
	if err != nil || len(recs) != 1 || recs[0].Result != "ok" {
		t.Fatalf("theme 成功留底不符: %+v err=%v", recs, err)
	}

	business := []byte(`{
		"plugin_id":"plugin_browser","name":"Browser","version":"1.0.0","plugin_type":"workspace","plugin_category":"business",
		"bot_templates":[
			{"bot_id":"browser_bot","display_name":"Browser Owner","activation_mode":"resident"},
			{"bot_id":"browser_nl_bot","display_name":"Browser","activation_mode":"lazy"}
		]
	}`)
	if _, _, err := mgr.Install(ctx, actor, business); err != nil {
		t.Fatalf("business 安装失败: %v", err)
	}
	primary, all := ClassifyBotIdentity("browser_bot", "plugin", []Manifest{*mgr.memPlugins["plugin_browser"]})
	if primary != IdentityOwner || len(all) != 2 || all[1] != IdentityBusiness {
		t.Fatalf("业务 owner 身份不符: %s %v", primary, all)
	}
	primary, all = ClassifyBotIdentity("user_writer_bot", "plugin", nil)
	if primary != IdentityUser {
		t.Fatalf("用户 Bot 身份不符: %s %v", primary, all)
	}
}

func TestInstallRejectsDepth(t *testing.T) {
	mgr := NewManager(nil, nil, nil, nil)
	ctx := context.Background()
	actor := config.BotActor(config.UserBot)
	install := func(raw string) error {
		_, _, err := mgr.Install(ctx, actor, []byte(raw))
		return err
	}
	if err := install(`{
		"plugin_id":"plugin_layer_one","name":"L1","version":"1.0.0","plugin_type":"workspace","plugin_category":"foundation",
		"bot_templates":[{"bot_id":"layer_one_bot","display_name":"L1","activation_mode":"lazy"}]
	}`); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ProviderIndex().RegisterDoc(ifaceDoc("plugin_layer_one", "nexus.layer.one")); err != nil {
		t.Fatal(err)
	}
	if err := install(`{
		"plugin_id":"plugin_layer_two","name":"L2","version":"1.0.0","plugin_type":"workspace","plugin_category":"foundation",
		"bot_templates":[{"bot_id":"layer_two_bot","display_name":"L2","activation_mode":"lazy"}],
		"dependencies":[{"interface":"nexus.layer.one","version_range":"^1.0.0"}]
	}`); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ProviderIndex().RegisterDoc(ifaceDoc("plugin_layer_two", "nexus.layer.two")); err != nil {
		t.Fatal(err)
	}
	err := install(`{
		"plugin_id":"plugin_layer_three","name":"L3","version":"1.0.0","plugin_type":"workspace","plugin_category":"business",
		"bot_templates":[{"bot_id":"layer_three_bot","display_name":"L3","activation_mode":"lazy"}],
		"dependencies":[{"interface":"nexus.layer.two","version_range":"^1.0.0"}]
	}`)
	if err == nil || !errors.Is(err, ErrExceedsMaxDependencyDepth) {
		t.Fatalf("深度超限应失败: %v", err)
	}
	recs, listErr := mgr.ListLifecycle(ctx, "plugin_layer_three")
	if listErr != nil || len(recs) != 1 || recs[0].Result != "rejected" {
		t.Fatalf("深度超限留底不符: %+v err=%v", recs, listErr)
	}
	if _, ok := mgr.memPlugins["plugin_layer_three"]; ok {
		t.Fatal("深度超限不应留下已安装清单")
	}
}

func ifaceDoc(pluginID, interfaceID string) *PluginInterfacesDoc {
	return &PluginInterfacesDoc{
		PluginID:      pluginID,
		PluginVersion: "1.0.0",
		ProvidesInterfaces: []InterfaceDefinition{{
			InterfaceID:  interfaceID,
			Version:      "1.0.0",
			Protocol:     "http_rest",
			Endpoint:     "http://127.0.0.1:9",
			HealthCheck:  "/health",
			Capabilities: []string{"probe"},
			Stability:    "stable",
		}},
	}
}
