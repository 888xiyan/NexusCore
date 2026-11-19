package marketplace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/888xiyan/NexusCore/hub/plugin"
)

func TestSearchSentenceShowsRiskAndDownloadStaysProposal(t *testing.T) {
	dir := t.TempDir()
	writeCatalog(t, dir)
	svc, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	hits, err := svc.Search("帮我找一个能浏览网页的浏览器")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].PluginID != "plugin_browser" {
		t.Fatalf("一句话应只命中浏览器，得到 %#v", hits)
	}
	hit := hits[0]
	if hit.Installed {
		t.Fatal("目录项不应标成已安装")
	}
	if !hit.DigestMatch || hit.HasCycle || hit.Depth != 2 || hit.MaxDepth != 2 {
		t.Fatalf("浏览器检查结果不符: %+v", hit)
	}
	if len(hit.Dependencies) != 1 || hit.Dependencies[0] != "plugin_agentmesh" {
		t.Fatalf("依赖不符: %v", hit.Dependencies)
	}

	prop, err := svc.Download("plugin_browser", hit.Digest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if prop.Installed || prop.ProcessStarted || prop.Status != "proposed" || prop.ProposalID == "" {
		t.Fatalf("下载应只得到提案: %+v", prop)
	}
	if _, err := os.Stat(filepath.Join(dir, "installed.json")); !os.IsNotExist(err) {
		t.Fatal("下载不应写入已安装集合")
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatalf("重启后打开目录: %v", err)
	}
	saved, ok := again.LatestProposal("plugin_browser")
	if !ok || saved.ProposalID != prop.ProposalID || saved.Installed {
		t.Fatalf("重启后提案应仍在且未安装: %+v %v", saved, ok)
	}

	if _, err := svc.Download("plugin_browser", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("错误摘要应拒绝: %v", err)
	}
	if _, err := svc.Download("plugin_cycle_a", ""); err == nil || !errors.Is(err, plugin.ErrCircularDependency) {
		t.Fatalf("环路应拒绝: %v", err)
	}
	if _, err := svc.Download("plugin_deep_l3", ""); err == nil || !errors.Is(err, plugin.ErrExceedsMaxDependencyDepth) {
		t.Fatalf("深度 3 应拒绝: %v", err)
	}
	got, err := svc.RejectInstall("plugin_browser")
	if err == nil || !errors.Is(err, ErrInstallNotPermitted) || got.Installed || got.ProcessStarted {
		t.Fatalf("安装入口不能激活: %+v %v", got, err)
	}
}

func TestPlanUninstallBlocksSingleAndReturnsReverseOrder(t *testing.T) {
	dir := t.TempDir()
	writeCatalog(t, dir)
	installed := []any{
		manifest("plugin_agentmesh", nil),
		manifest("plugin_browser", []string{"plugin_agentmesh"}),
	}
	body, _ := json.Marshal(installed)
	if err := os.WriteFile(filepath.Join(dir, "installed.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlanUninstall("plugin_agentmesh", false); !errors.Is(err, ErrReverseDependency) {
		t.Fatalf("单点卸载应被拒绝: %v", err)
	}
	order, err := svc.PlanUninstall("plugin_agentmesh", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "plugin_browser" || order[1] != "plugin_agentmesh" {
		t.Fatalf("逆拓扑应为先业务后基础: %v", order)
	}
	if _, err := os.Stat(filepath.Join(dir, "installed.json")); err != nil {
		t.Fatal("计划卸载不应删除已安装集合")
	}
}

func writeCatalog(t *testing.T, dir string) {
	t.Helper()
	art := filepath.Join(dir, "artifacts")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{
		entryWithArtifact(t, dir, "plugin_agentmesh", "探针网格", "本地探针", "low", []string{"agent"}, nil),
		entryWithArtifact(t, dir, "plugin_browser", "浏览器", "提供网页浏览与页面快照", "medium", []string{"浏览器", "browser", "网页"}, []string{"plugin_agentmesh"}),
		entryWithArtifact(t, dir, "plugin_cycle_a", "环A", "环路甲", "high", []string{"cycle"}, []string{"plugin_cycle_b"}),
		entryWithArtifact(t, dir, "plugin_cycle_b", "环B", "环路乙", "high", []string{"cycle"}, []string{"plugin_cycle_a"}),
		entryWithArtifact(t, dir, "plugin_deep_l1", "一层", "基础", "low", []string{"deep"}, nil),
		entryWithArtifact(t, dir, "plugin_deep_l2", "二层", "中间", "low", []string{"deep"}, []string{"plugin_deep_l1"}),
		entryWithArtifact(t, dir, "plugin_deep_l3", "三层", "过深", "high", []string{"deep"}, []string{"plugin_deep_l2"}),
	}
	body, err := json.MarshalIndent(catalogFile{Plugins: entries}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func entryWithArtifact(t *testing.T, dir, id, name, desc, risk string, tags, deps []string) Entry {
	t.Helper()
	mf := manifest(id, deps)
	raw, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("artifacts", id+".bin")
	if err := os.WriteFile(filepath.Join(dir, rel), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return Entry{
		PluginID: id, Name: name, Description: desc, RiskLevel: risk, Tags: tags,
		Digest: sum, Artifact: rel, Manifest: raw,
	}
}

func manifest(id string, deps []string) map[string]any {
	return map[string]any{
		"plugin_id":       id,
		"name":            id,
		"version":         "1.0.0",
		"plugin_type":     "workspace",
		"plugin_category": "business",
		"deps":            deps,
		"bot_templates": []any{
			map[string]any{"bot_id": botID(id), "display_name": id, "activation_mode": "lazy"},
		},
		"schema_version": 1,
	}
}

func botID(pluginID string) string {
	return "b" + pluginID[len("plugin"):]
}

func fileSHA256(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
