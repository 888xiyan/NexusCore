package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/ingress"
	"github.com/888xiyan/NexusCore/hub/supervisor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

	// 错误 6: 不支持的 plugin_type
	badType := *valid
	badType.PluginType = "unknown_type"
	if err := ValidateManifest(&badType); err == nil {
		t.Errorf("不支持的 plugin_type 应该校验失败")
	}

	// 错误 7: bot_templates 为空
	emptyBots := *valid
	emptyBots.BotTemplates = nil
	if err := ValidateManifest(&emptyBots); err == nil {
		t.Errorf("空 bot_templates 应该校验失败")
	}

	// 错误 8: 重复的 bot_id
	dupBots := *valid
	dupBots.BotTemplates = []BotTemplate{
		{BotID: "bot_alpha", DisplayName: "Alpha", ActivationMode: "lazy"},
		{BotID: "bot_alpha", DisplayName: "Alpha2", ActivationMode: "lazy"},
	}
	if err := ValidateManifest(&dupBots); err == nil {
		t.Errorf("重复 bot_id 应该校验失败")
	}

	// 错误 9: 非法 bot_id 命名
	badBotID := *valid
	badBotID.BotTemplates = []BotTemplate{
		{BotID: "bad-bot!", DisplayName: "Bad", ActivationMode: "lazy"},
	}
	if err := ValidateManifest(&badBotID); err == nil {
		t.Errorf("非法字符 bot_id 应该校验失败")
	}

	// 错误 10: 非法 activation_mode
	badMode := *valid
	badMode.BotTemplates = []BotTemplate{
		{BotID: "bot_mode_test", DisplayName: "Mode", ActivationMode: "invalid_mode"},
	}
	if err := ValidateManifest(&badMode); err == nil {
		t.Errorf("非法 activation_mode 应该校验失败")
	}

	// 缺省 activation_mode 默认为 lazy
	defaultMode := *valid
	defaultMode.BotTemplates = []BotTemplate{
		{BotID: "bot_default_mode", DisplayName: "Mode", ActivationMode: ""},
	}
	if err := ValidateManifest(&defaultMode); err != nil {
		t.Errorf("缺省 activation_mode 校验报错: %v", err)
	}
	if defaultMode.BotTemplates[0].ActivationMode != "lazy" {
		t.Errorf("缺省 activation_mode 未置为 lazy")
	}

	// 错误 11: binary 未提供合法 binary_digest
	digestBytes := sha256.Sum256([]byte("mock_binary"))
	validDigest := hex.EncodeToString(digestBytes[:])

	binValid := *valid
	binValid.Binary = "/path/to/bin"
	binValid.BinaryDigest = validDigest
	if err := ValidateManifest(&binValid); err != nil {
		t.Errorf("合法 binary_digest 校验失败: %v", err)
	}

	binBadHex := binValid
	binBadHex.BinaryDigest = "not_hex"
	if err := ValidateManifest(&binBadHex); err == nil {
		t.Errorf("非 hex binary_digest 应该校验失败")
	}

	binBadLen := binValid
	binBadLen.BinaryDigest = validDigest[:10]
	if err := ValidateManifest(&binBadLen); err == nil {
		t.Errorf("长度不足 binary_digest 应该校验失败")
	}

	// 其他保留前缀
	for _, pfx := range []string{"agent.", "project.", "kernel.", "obsidian.", "pm."} {
		pfxMf := *valid
		pfxMf.Publishing = []string{pfx + "event"}
		if err := ValidateManifest(&pfxMf); err == nil {
			t.Errorf("保留前缀 %s 应该校验失败", pfx)
		}
	}
}

func TestRequireRole(t *testing.T) {
	botActor := config.BotActor(config.UserBot)
	if err := requireRole(botActor, config.UserBot); err != nil {
		t.Errorf("匹配的角色报错: %v", err)
	}

	if err := requireRole(botActor, config.CreateBot); err == nil {
		t.Errorf("不匹配的角色未报错")
	}

	pluginActor := config.PluginActor("plugin_pm")
	if err := requireRole(pluginActor, config.UserBot); err == nil {
		t.Errorf("非 Bot/KernelRole 主体未报错")
	}

	kernelActor := config.KernelRole("migration")
	if err := requireRole(kernelActor, "migration"); err != nil {
		t.Errorf("匹配的 KernelRole 报错: %v", err)
	}
}

func TestManifestChecksum(t *testing.T) {
	spec := []byte(`{"plugin_id": "plugin_demo", "version": "1.0.0"}`)
	checksum, err := manifestChecksum(spec)
	if err != nil {
		t.Fatalf("manifestChecksum 失败: %v", err)
	}
	if len(checksum) != 64 {
		t.Errorf("checksum 长度非 64: %s", checksum)
	}

	// 非法 JSON
	if _, err := manifestChecksum([]byte(`{invalid}`)); err == nil {
		t.Errorf("非法 JSON 计算 checksum 未报错")
	}
}

func TestManagerPrecheckAndControls(t *testing.T) {
	mgr := NewManager(nil, nil, nil, nil)
	mgr.SetIngress(&ingress.Ingress{})

	ctx := context.Background()

	// 1. Install 鉴权失败
	unauthorizedActor := config.BotActor("some_other_bot")
	if _, _, err := mgr.Install(ctx, unauthorizedActor, []byte(`{}`)); err == nil {
		t.Errorf("未授权用户调用 Install 未报错")
	}

	// 2. Install JSON 解析失败
	userActor := config.BotActor(config.UserBot)
	if _, _, err := mgr.Install(ctx, userActor, []byte(`invalid json`)); err == nil {
		t.Errorf("非法 JSON 调用 Install 未报错")
	}

	// 3. Install 清单校验失败
	if _, _, err := mgr.Install(ctx, userActor, []byte(`{"plugin_id": "bad"}`)); err == nil {
		t.Errorf("非法清单调用 Install 未报错")
	}

	// 4. Upgrade 鉴权失败与参数校验
	if _, _, err := mgr.Upgrade(ctx, unauthorizedActor, []byte(`{}`)); err == nil {
		t.Errorf("未授权用户调用 Upgrade 未报错")
	}
	if _, _, err := mgr.Upgrade(ctx, userActor, []byte(`invalid json`)); err == nil {
		t.Errorf("非法 JSON 调用 Upgrade 未报错")
	}
	if _, _, err := mgr.Upgrade(ctx, userActor, []byte(`{"plugin_id": "bad"}`)); err == nil {
		t.Errorf("非法清单调用 Upgrade 未报错")
	}

	// 5. SecurityList / Approve / Revoke / Disable / Uninstall 鉴权失败
	if _, err := mgr.SecurityList(ctx, unauthorizedActor, ""); err == nil {
		t.Errorf("未授权用户调用 SecurityList 未报错")
	}
	if err := mgr.SecurityApprove(ctx, unauthorizedActor, 1, config.CausalContext{}); err == nil {
		t.Errorf("未授权用户调用 SecurityApprove 未报错")
	}
	if err := mgr.SecurityRevoke(ctx, unauthorizedActor, 1, "test"); err == nil {
		t.Errorf("未授权用户调用 SecurityRevoke 未报错")
	}
	if err := mgr.Disable(ctx, unauthorizedActor, "plugin_demo"); err == nil {
		t.Errorf("未授权用户调用 Disable 未报错")
	}
	if err := mgr.Uninstall(ctx, unauthorizedActor, "plugin_demo", false, false); err == nil {
		t.Errorf("未授权用户调用 Uninstall 未报错")
	}

	// 6. KillProcess 无 confirm 报错
	if err := mgr.KillProcess(ctx, userActor, "plugin_demo", false, ""); !errors.Is(err, ErrConfirmRequired) {
		t.Errorf("KillProcess confirm=false 未返回 ErrConfirmRequired: %v", err)
	}

	// 7. ReportProcess 参数校验
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{PluginID: ""}); err == nil {
		t.Errorf("空 plugin_id 上报未报错")
	}
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{PluginID: "plugin_demo", OwnerBotID: ""}); err == nil {
		t.Errorf("空 owner_bot_id 上报未报错")
	}
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{PluginID: "plugin_demo", OwnerBotID: "demo_bot", PID: -1}); err == nil {
		t.Errorf("非法 PID 上报未报错")
	}
	// 僵尸 PID 存活校验失败
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{
		PluginID:     "plugin_demo",
		OwnerBotID:   "demo_bot",
		PID:          99999999,
		PIDStartTime: 1234567,
		State:        "running",
	}); err == nil {
		t.Errorf("失效进程上报未报错")
	}
}

func TestManagerLifecycleInMemory(t *testing.T) {
	mgr := NewManager(nil, nil, nil, nil)
	ctx := context.Background()
	userActor := config.BotActor(config.UserBot)

	rawGC := []byte(`{
		"plugin_id": "plugin_groupchat",
		"name": "GroupChat Plugin",
		"version": "1.0.0",
		"plugin_type": "workspace",
		"bot_templates": [
			{"bot_id": "groupchat_bot", "display_name": "GroupChat", "activation_mode": "resident"}
		],
		"publishing": ["plugin_groupchat.message.created"]
	}`)

	rawPM := []byte(`{
		"plugin_id": "plugin_project_mgr",
		"name": "PM Plugin",
		"version": "1.0.0",
		"plugin_type": "workspace",
		"bot_templates": [
			{"bot_id": "pm_bot", "display_name": "PM", "activation_mode": "resident"}
		],
		"dependencies": [
			{"interface": "nexus.groupchat.v1", "version_range": "^1.0.0", "optional": false}
		],
		"publishing": ["plugin_project_mgr.task.created"]
	}`)

	// 1. Install
	mfGC, decID1, err := mgr.Install(ctx, userActor, rawGC)
	if err != nil {
		t.Fatalf("Install GC 失败: %v", err)
	}
	if mfGC.PluginID != "plugin_groupchat" || decID1 != 1 {
		t.Errorf("Install 返回结果不符: %+v, decID=%d", mfGC, decID1)
	}

	mfPM, decID2, err := mgr.Install(ctx, userActor, rawPM)
	if err != nil {
		t.Fatalf("Install PM 失败: %v", err)
	}
	if mfPM.PluginID != "plugin_project_mgr" || decID2 != 2 {
		t.Errorf("Install PM 返回结果不符: %+v, decID=%d", mfPM, decID2)
	}

	// 2. SecurityList
	pending, err := mgr.SecurityList(ctx, userActor, "pending")
	if err != nil || len(pending) != 2 {
		t.Fatalf("SecurityList pending 失败: %v, count=%d", err, len(pending))
	}

	// 3. SecurityApprove
	if err := mgr.SecurityApprove(ctx, userActor, decID1, config.CausalContext{}); err != nil {
		t.Fatalf("SecurityApprove GC 失败: %v", err)
	}
	if err := mgr.SecurityApprove(ctx, userActor, decID2, config.CausalContext{}); err != nil {
		t.Fatalf("SecurityApprove PM 失败: %v", err)
	}
	// 重复审批冲突
	if err := mgr.SecurityApprove(ctx, userActor, decID1, config.CausalContext{}); err == nil {
		t.Errorf("重复审批已处理决定未报错")
	}
	// 审批不存在的决定
	if err := mgr.SecurityApprove(ctx, userActor, 9999, config.CausalContext{}); !errors.Is(err, ErrDecisionNotFound) {
		t.Errorf("审批不存在决定未返回 ErrDecisionNotFound: %v", err)
	}

	// 4. ProviderIndex 与 BuildSystemDAG
	mgr.ProviderIndex().RegisterDoc(&PluginInterfacesDoc{
		PluginID:      "plugin_groupchat",
		PluginVersion: "1.0.0",
		ProvidesInterfaces: []InterfaceDefinition{
			{
				InterfaceID: "nexus.groupchat.v1",
				Version:     "1.0.0",
				Protocol:    "http_rest",
				Endpoint:    "http://127.0.0.1:7300/v1/chat",
			},
		},
	})

	dag, err := mgr.BuildSystemDAG()
	if err != nil {
		t.Fatalf("BuildSystemDAG 失败: %v", err)
	}
	if len(dag.Nodes) != 2 {
		t.Errorf("DAG 节点数量不符: %d", len(dag.Nodes))
	}

	// 5. 进程上报与生命周期控制
	selfPID := os.Getpid()
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{
		PluginID:   "plugin_groupchat",
		OwnerBotID: "groupchat_bot",
		PID:        selfPID,
		State:      "running",
	}); err != nil {
		t.Fatalf("ReportProcess 失败: %v", err)
	}

	procs, err := mgr.ListProcesses(ctx, userActor)
	if err != nil || len(procs) != 1 {
		t.Fatalf("ListProcesses 失败: %v, count=%d", err, len(procs))
	}

	if err := mgr.StopProcess(ctx, userActor, "plugin_groupchat"); err != nil {
		t.Fatalf("StopProcess 失败: %v", err)
	}

	if err := mgr.KillProcess(ctx, userActor, "plugin_groupchat", true, "test_kill"); err != nil {
		t.Fatalf("KillProcess 失败: %v", err)
	}

	// 6. 反向依赖卸载拦截 (plugin_pm 依赖 plugin_groupchat)
	if err := mgr.Uninstall(ctx, userActor, "plugin_groupchat", false, false); err == nil {
		t.Fatalf("存在下游依赖时卸载基础模块未阻断")
	} else if !errors.Is(err, ErrReverseDependencyBlocked) {
		t.Errorf("卸载拦截未返回 ErrReverseDependencyBlocked: %v", err)
	}

	// 7. 级联卸载通过 (confirm = true)
	if err := mgr.Uninstall(ctx, userActor, "plugin_groupchat", true, false); err != nil {
		t.Fatalf("级联卸载失败: %v", err)
	}

	// 8. 升级提案与驳回
	rawUpgrade := []byte(`{
		"plugin_id": "plugin_project_mgr",
		"name": "PM Plugin",
		"version": "1.1.0",
		"plugin_type": "workspace",
		"bot_templates": [
			{"bot_id": "pm_bot", "display_name": "PM", "activation_mode": "resident"}
		],
		"publishing": ["plugin_project_mgr.task.created"]
	}`)
	_, upgradeDecID, err := mgr.Upgrade(ctx, userActor, rawUpgrade)
	if err != nil {
		t.Fatalf("Upgrade 失败: %v", err)
	}

	if err := mgr.SecurityRevoke(ctx, userActor, upgradeDecID, "test rejection"); err != nil {
		t.Fatalf("SecurityRevoke 失败: %v", err)
	}

	if err := mgr.SecurityRevoke(ctx, userActor, 9999, "bad"); !errors.Is(err, ErrDecisionNotFound) {
		t.Errorf("驳回不存在决定未返回 ErrDecisionNotFound: %v", err)
	}

	// 9. Disable
	if err := mgr.Disable(ctx, userActor, "plugin_project_mgr"); err != nil {
		t.Fatalf("Disable 失败: %v", err)
	}

	// 10. 覆盖 ProviderIndex 缺省创建
	bareMgr := &Manager{}
	if bareMgr.ProviderIndex() == nil {
		t.Errorf("ProviderIndex 缺省创建失败")
	}

	// 11. 进程上报携带 LastError 及缺省 KillProcess 原因
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{
		PluginID:   "plugin_project_mgr",
		OwnerBotID: "pm_bot",
		PID:        selfPID,
		State:      "failed",
		LastError:  "mock error",
	}); err != nil {
		t.Fatalf("ReportProcess 带错误失败: %v", err)
	}

	if err := mgr.KillProcess(ctx, userActor, "plugin_project_mgr", true, ""); err != nil {
		t.Fatalf("KillProcess 缺省原因失败: %v", err)
	}

	// 12. SecurityList 全量查询
	allDecs, err := mgr.SecurityList(ctx, userActor, "")
	if err != nil || len(allDecs) == 0 {
		t.Fatalf("全量 SecurityList 失败: %v", err)
	}

	// 13. Uninstall keep=true
	if err := mgr.Uninstall(ctx, userActor, "plugin_project_mgr", false, true); err != nil {
		t.Fatalf("Uninstall keep=true 失败: %v", err)
	}
}

type mockTx struct {
	failOnSQL string
}

func (m *mockTx) Begin(ctx context.Context) (pgx.Tx, error) { return m, nil }
func (m *mockTx) Commit(ctx context.Context) error          { return nil }
func (m *mockTx) Rollback(ctx context.Context) error        { return nil }
func (m *mockTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (m *mockTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { return nil }
func (m *mockTx) LargeObjects() pgx.LargeObjects                               { return pgx.LargeObjects{} }
func (m *mockTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (m *mockTx) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	if m.failOnSQL != "" && strings.Contains(sql, m.failOnSQL) {
		return pgconn.CommandTag{}, errors.New("mock exec failure")
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (m *mockTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, nil
}
func (m *mockTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row { return nil }
func (m *mockTx) Conn() *pgx.Conn                                               { return nil }

func TestActivatePluginWithMockTx(t *testing.T) {
	ctx := context.Background()
	userActor := config.BotActor(config.UserBot)

	// 1. 无 supervisor 报错
	mgrNoSup := NewManager(nil, nil, nil, nil)
	mf := Manifest{
		PluginID:   "plugin_activate_test",
		Name:       "Test",
		Version:    "1.0.0",
		PluginType: "workspace",
		BotTemplates: []BotTemplate{
			{BotID: "bot_resident", DisplayName: "Resident", ActivationMode: "resident", Capabilities: []string{"cap1"}},
			{BotID: "bot_lazy", DisplayName: "Lazy", ActivationMode: "lazy", Capabilities: []string{"cap2"}, MemoryProfile: "custom_profile"},
		},
		Publishing:     []string{"plugin_activate_test.event"},
		Subscribing:    []string{"chat.incoming"},
		RequiringTools: []string{"tool.run"},
	}
	if err := mgrNoSup.activatePlugin(ctx, &mockTx{}, userActor, 1, "plugin_activate_test", mf, "digest", config.CausalContext{}); err == nil {
		t.Errorf("无 supervisor 激活未报错")
	}

	// 2. 有 supervisor 但未配置可信制品报错
	sup := supervisor.New(nil, nil, nil, nil, supervisor.Options{})
	mgrWithSup := NewManager(nil, nil, sup, nil)
	if err := mgrWithSup.activatePlugin(ctx, &mockTx{}, userActor, 1, "plugin_activate_test", mf, "digest", config.CausalContext{}); err == nil {
		t.Errorf("未配置运行时制品激活未报错")
	}

	// 3. 配置虚拟可信制品并成功激活全流程
	tmpBin := filepath.Join(t.TempDir(), "fake_runtime")
	if err := os.WriteFile(tmpBin, []byte("#!/bin/sh\nexit 0"), 0o755); err != nil {
		t.Fatalf("写可信制品失败: %v", err)
	}
	if err := sup.ConfigureRuntime(tmpBin); err != nil {
		t.Fatalf("配置运行时制品失败: %v", err)
	}

	if err := mgrWithSup.activatePlugin(ctx, &mockTx{}, userActor, 1, "plugin_activate_test", mf, "digest", config.CausalContext{}); err != nil {
		t.Fatalf("全流程 mockTx 激活失败: %v", err)
	}

	// 4. 各步骤 SQL 失败分支覆盖
	failCases := []string{
		"UPDATE kernel_security_decisions",
		"INSERT INTO kernel_plugin_event_registry",
		"VALUES ($1, 'subscribe'",
		"VALUES ($1, 'tool'",
		"INSERT INTO bot_manifest",
		"INSERT INTO bots",
		"INSERT INTO kernel_config_documents",
		"UPDATE kernel_plugins",
	}
	for _, failSQL := range failCases {
		txFail := &mockTx{failOnSQL: failSQL}
		if err := mgrWithSup.activatePlugin(ctx, txFail, userActor, 1, "plugin_activate_test", mf, "digest", config.CausalContext{}); err == nil {
			t.Errorf("SQL %s 失败未被捕获", failSQL)
		}
	}
}

func TestManagerExtendedCoverage(t *testing.T) {
	ctx := context.Background()
	userActor := config.BotActor(config.UserBot)
	mgr := NewManager(nil, nil, nil, nil)

	// 1. KillProcess 缺少 confirm
	if err := mgr.KillProcess(ctx, userActor, "plugin_test", false, "reason"); !errors.Is(err, ErrConfirmRequired) {
		t.Errorf("KillProcess 缺少 confirm 未返回 ErrConfirmRequired: %v", err)
	}

	// 2. KillProcess 针对内存不存在的插件 (confirm=true)
	if err := mgr.KillProcess(ctx, userActor, "plugin_non_existent", true, "reason"); err != nil {
		t.Errorf("KillProcess 内存未知插件报错: %v", err)
	}

	// 3. StopProcess 针对内存不存在的插件
	if err := mgr.StopProcess(ctx, userActor, "plugin_non_existent"); err != nil {
		t.Errorf("StopProcess 内存未知插件报错: %v", err)
	}

	// 4. ReportProcess 参数校验边界
	// 4.1 空 PluginID
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{OwnerBotID: "bot1", PID: 1234}); err == nil {
		t.Errorf("ReportProcess 空 PluginID 未报错")
	}
	// 4.2 空 OwnerBotID
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{PluginID: "plugin_test", PID: 1234}); err == nil {
		t.Errorf("ReportProcess 空 OwnerBotID 未报错")
	}
	// 4.3 非法 PID <= 0
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{PluginID: "plugin_test", OwnerBotID: "bot1", PID: 0}); err == nil {
		t.Errorf("ReportProcess PID<=0 未报错")
	}
	// 4.4 伪造已死亡进程的 PIDStartTime
	if err := mgr.ReportProcess(ctx, userActor, ProcessReport{
		PluginID:     "plugin_test",
		OwnerBotID:   "bot1",
		PID:          99999999,
		PIDStartTime: 12345678,
		State:        "running",
	}); err == nil {
		t.Errorf("ReportProcess 伪造 PIDStartTime 未报错")
	}

	// 5. SecurityList 过滤测试
	// 预置两个决策
	mgr.memDecisions = []SecurityDecision{
		{DecisionID: 101, SubjectID: "p1", Decision: "pending"},
		{DecisionID: 102, SubjectID: "p2", Decision: "approved"},
		{DecisionID: 103, SubjectID: "p3", Decision: "rejected"},
	}
	pendingList, err := mgr.SecurityList(ctx, userActor, "pending")
	if err != nil || len(pendingList) != 1 || pendingList[0].DecisionID != 101 {
		t.Errorf("SecurityList 过滤 pending 失败: %+v", pendingList)
	}
	approvedList, err := mgr.SecurityList(ctx, userActor, "approved")
	if err != nil || len(approvedList) != 1 || approvedList[0].DecisionID != 102 {
		t.Errorf("SecurityList 过滤 approved 失败: %+v", approvedList)
	}

	// 6. SecurityApprove 重复审批与不存在审批
	if err := mgr.SecurityApprove(ctx, userActor, 102, config.CausalContext{}); err == nil {
		t.Errorf("已审批的决策再次审批未报错")
	}
	if err := mgr.SecurityApprove(ctx, userActor, 99999, config.CausalContext{}); !errors.Is(err, ErrDecisionNotFound) {
		t.Errorf("不存在的决策审批未返回 ErrDecisionNotFound: %v", err)
	}

	// 7. SecurityRevoke 不存在的决策
	if err := mgr.SecurityRevoke(ctx, userActor, 99999, "reason"); !errors.Is(err, ErrDecisionNotFound) {
		t.Errorf("不存在的决策撤销未返回 ErrDecisionNotFound: %v", err)
	}

	// 8. Disable 与 Uninstall 针对未知插件：失败也要留底
	if err := mgr.Disable(ctx, userActor, "plugin_unknown"); !errors.Is(err, ErrPluginNotFound) {
		t.Errorf("Disable 未知插件应返回 ErrPluginNotFound: %v", err)
	}
	if err := mgr.Uninstall(ctx, userActor, "plugin_unknown", false, false); !errors.Is(err, ErrPluginNotFound) {
		t.Errorf("Uninstall 未知插件应返回 ErrPluginNotFound: %v", err)
	}

	// 9. BuildSystemDAG 空 DAG
	dag, err := mgr.BuildSystemDAG()
	if err != nil || dag == nil || len(dag.Nodes) != 0 {
		t.Errorf("空状态 BuildSystemDAG 失败: %+v, err: %v", dag, err)
	}

	// 10. SecurityApprove 激活失败分支 (覆盖内存模式激活失败)
	mgrNoSup := NewManager(nil, nil, nil, nil)
	mgrNoSup.memPlugins["plugin_will_fail"] = &Manifest{
		PluginID:   "invalid_id_format",
		Name:       "Fail",
		Version:    "1.0.0",
		PluginType: "workspace",
	}
	mgrNoSup.memDecisions = []SecurityDecision{
		{DecisionID: 201, SubjectID: "plugin_will_fail", Decision: "pending", Reason: "digest"},
	}
	if err := mgrNoSup.SecurityApprove(ctx, userActor, 201, config.CausalContext{}); err == nil {
		t.Errorf("非法清单激活失败未被捕获")
	}
}
