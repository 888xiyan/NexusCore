package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/888xiyan/NexusCore/hub/config"
)

func TestDownloadRecordDoesNotInstall(t *testing.T) {
	mgr := NewManager(nil, nil, nil, nil)
	ctx := context.Background()
	actor := config.BotActor(config.UserBot)
	if err := mgr.RecordDownload(ctx, actor, "plugin_m14_mesh", "abc", "ok", "下载提案"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordDownload(ctx, actor, "plugin_m14_mesh", "bad", "rejected", "摘要不符"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.memStatuses["plugin_m14_mesh"]; ok {
		t.Fatal("下载留底不应把插件标成已安装")
	}
	recs, err := mgr.ListLifecycle(ctx, "plugin_m14_mesh")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Action != "download" || recs[0].Result != "ok" || recs[1].Result != "rejected" {
		t.Fatalf("下载留底不符: %+v", recs)
	}
	if err := mgr.RecordDownload(ctx, config.BotActor("marketplace_bot"), "plugin_m14_mesh", "abc", "ok", ""); err == nil {
		t.Fatal("非 user_bot 不应写下载留底")
	}
}

func TestPresentedChecksumAndDependencyDepth(t *testing.T) {
	mgr := NewManager(nil, nil, nil, nil)
	ctx := context.Background()
	actor := config.BotActor(config.UserBot)
	raw := func(id string, deps []string) []byte {
		depJSON := "[]"
		if len(deps) > 0 {
			depJSON = `["` + deps[0] + `"]`
		}
		return []byte(`{
			"plugin_id": "` + id + `",
			"name": "` + id + `",
			"version": "1.0.0",
			"plugin_type": "workspace",
			"plugin_category": "foundation",
			"deps": ` + depJSON + `,
			"bot_templates": [{"bot_id": "` + id[len("plugin_"):] + `_bot", "display_name": "b", "activation_mode": "lazy"}]
		}`)
	}
	_, dec, err := mgr.Install(ctx, actor, raw("plugin_m14_d1", nil))
	if err != nil {
		t.Fatal(err)
	}
	sum, err := mgr.DecisionChecksum(ctx, dec)
	if err != nil || len(sum) != 64 {
		t.Fatalf("checksum: %q %v", sum, err)
	}
	if err := mgr.VerifyPresentedChecksum(ctx, dec, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); !errors.Is(err, config.ErrConflict) {
		t.Fatalf("错误 checksum 应拒绝: %v", err)
	}
	if err := mgr.VerifyPresentedChecksum(ctx, dec, sum); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Install(ctx, actor, raw("plugin_m14_d2", []string{"plugin_m14_d1"})); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Install(ctx, actor, raw("plugin_m14_d3", []string{"plugin_m14_d2"})); err == nil || !errors.Is(err, ErrExceedsMaxDependencyDepth) {
		t.Fatalf("深度 3 应在安装入口拒绝: %v", err)
	}
}

func TestCascadeUninstallReverseOrder(t *testing.T) {
	mgr := NewManager(nil, nil, nil, nil)
	ctx := context.Background()
	actor := config.BotActor(config.UserBot)
	base := []byte(`{
		"plugin_id": "plugin_m14_base",
		"name": "base",
		"version": "1.0.0",
		"plugin_type": "workspace",
		"plugin_category": "foundation",
		"bot_templates": [{"bot_id": "m14_base_bot", "display_name": "base", "activation_mode": "lazy"}]
	}`)
	app := []byte(`{
		"plugin_id": "plugin_m14_app",
		"name": "app",
		"version": "1.0.0",
		"plugin_type": "workspace",
		"plugin_category": "business",
		"deps": ["plugin_m14_base"],
		"bot_templates": [{"bot_id": "m14_app_bot", "display_name": "app", "activation_mode": "lazy"}]
	}`)
	if _, _, err := mgr.Install(ctx, actor, base); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Install(ctx, actor, app); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Uninstall(ctx, actor, "plugin_m14_base", false, false); err == nil || !errors.Is(err, ErrReverseDependencyBlocked) {
		t.Fatalf("单点卸载应被拒绝: %v", err)
	}
	if err := mgr.CascadeUninstall(ctx, actor, "plugin_m14_base"); err != nil {
		t.Fatal(err)
	}
	recs, err := mgr.ListLifecycle(ctx, "plugin_m14_app")
	if err != nil {
		t.Fatal(err)
	}
	var sawUninstall bool
	for _, rec := range recs {
		if rec.Action == "uninstall" && rec.Result == "ok" {
			sawUninstall = true
		}
	}
	if !sawUninstall {
		t.Fatalf("级联应先卸掉依赖方: %+v", recs)
	}
	if mgr.memStatuses["plugin_m14_base"] != "uninstalled" || mgr.memStatuses["plugin_m14_app"] != "uninstalled" {
		t.Fatalf("级联后两者都应卸载: base=%s app=%s", mgr.memStatuses["plugin_m14_base"], mgr.memStatuses["plugin_m14_app"])
	}
}
