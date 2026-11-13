package config

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/pgtest"
)

func newModule(t *testing.T) (*Module, *pgtest.DB) {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatalf("打开内核 store: %v", err)
	}
	t.Cleanup(st.Close)
	return New(st), db
}

func cc() CausalContext {
	return CausalContext{CorrelationID: "env_test_root", CausationID: "env_test_parent", RootEnvelopeID: "env_test_root"}
}

func mustPropose(t *testing.T, m *Module, actor Actor, scope, id string, spec string, base int64) string {
	t.Helper()
	docID, err := m.Propose(context.Background(), actor, scope, id, DefaultConfigKey, json.RawMessage(spec), base)
	if err != nil {
		t.Fatalf("提案失败: %v", err)
	}
	return docID
}

func mustActivate(t *testing.T, m *Module, db *pgtest.DB, docID string) {
	t.Helper()
	ctx := context.Background()
	scope, id, version, checksum := docMeta(t, db, docID)
	if _, err := m.Get(ctx, UserBotActor(), scope, id, DefaultConfigKey, version); err != nil {
		t.Fatalf("读取提案: %v", err)
	}
	if err := m.Approve(ctx, UserBotActor(), docID, checksum); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if err := m.Activate(ctx, UserBotActor(), docID, cc()); err != nil {
		t.Fatalf("激活失败: %v", err)
	}
}

func docMeta(t *testing.T, db *pgtest.DB, docID string) (scope, id string, version int64, checksum string) {
	t.Helper()
	row := db.QueryRow(t, `SELECT scope_type, scope_id, version, checksum FROM kernel.kernel_config_documents WHERE doc_id=$1`, docID)
	if err := row.Scan(&scope, &id, &version, &checksum); err != nil {
		t.Fatalf("读取文档元数据: %v", err)
	}
	return
}

func versionOf(t *testing.T, db *pgtest.DB, docID string) int64 {
	t.Helper()
	_, _, v, _ := docMeta(t, db, docID)
	return v
}

// §8.5：种子配置可读
func TestSeedConfigsReadable(t *testing.T) {
	m, _ := newModule(t)
	ctx := context.Background()
	bot, err := m.Get(ctx, UserBotActor(), ScopeBot, "echo_bot", DefaultConfigKey, 0)
	if err != nil {
		t.Fatalf("读取 echo_bot 种子配置: %v", err)
	}
	if bot.Version != 1 || bot.Status != StatusActive {
		t.Fatalf("期望 active v1，得到 %s v%d", bot.Status, bot.Version)
	}
	if bot.Checksum != "e71009676c0cefd144f872dde5f0474d9c39ee2d29faf9cd36262666cb8e5e9d" {
		t.Fatalf("种子 checksum 不符: %s", bot.Checksum)
	}
	glob, err := m.Get(ctx, UserBotActor(), ScopeGlobal, GlobalScopeID, DefaultConfigKey, 0)
	if err != nil {
		t.Fatalf("读取 global 种子配置: %v", err)
	}
	if glob.Version != 1 {
		t.Fatalf("global 期望 v1，得到 v%d", glob.Version)
	}
}

// §8.5：提案不生效
func TestProposalDoesNotTakeEffect(t *testing.T) {
	m, db := newModule(t)
	ctx := context.Background()
	docID := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"new-model","prompt_ref":"echo"}`, 1)

	active, err := m.Get(ctx, UserBotActor(), ScopeBot, "echo_bot", DefaultConfigKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	if active.Version != 1 || active.DocID == docID {
		t.Fatalf("提案不得生效：active 仍是 v1，实际 v%d", active.Version)
	}
	var manifestVersion int64
	if err := db.QueryRow(t, `SELECT config_version FROM kernel.bot_manifest WHERE bot_id='echo_bot'`).Scan(&manifestVersion); err != nil {
		t.Fatal(err)
	}
	if manifestVersion != 1 {
		t.Fatalf("提案不得进入运行投影，bot_manifest.config_version=%d", manifestVersion)
	}
}

// §8.5：无 user_bot 批准不能 active
func TestOnlyUserBotCanApprove(t *testing.T) {
	m, _ := newModule(t)
	ctx := context.Background()
	docID := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"x"}`, 1)
	for _, actor := range []Actor{KernelRole(CreateBot), KernelRole(SecurityBot), KernelRole(MonitorBot), BotActor("echo_bot")} {
		if err := m.Approve(ctx, actor, docID, "whatever"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s 批准必须被拒，得到 %v", actor.ID, err)
		}
	}
	// 未批准直接激活也必须被拒
	if err := m.Activate(ctx, UserBotActor(), docID, cc()); !errors.Is(err, ErrBadTransition) {
		t.Fatalf("未批准激活必须被拒，得到 %v", err)
	}
}

// §8.5：checksum 变更后批准失效
func TestApproveChecksumMismatch(t *testing.T) {
	m, _ := newModule(t)
	ctx := context.Background()
	docID := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"x"}`, 1)
	err := m.Approve(ctx, UserBotActor(), docID, strings.Repeat("f", 64))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("checksum 不一致必须冲突，得到 %v", err)
	}
}

func TestApproveRequiresChecksum(t *testing.T) {
	m, _ := newModule(t)
	docID := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"x"}`, 1)
	if err := m.Approve(context.Background(), UserBotActor(), docID, ""); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("批准必须携带 checksum，得到 %v", err)
	}
}

// §8.5：可执行类缺 security_bot 决定不能批准
func TestExecutableRequiresSecurityReview(t *testing.T) {
	m, db := newModule(t)
	ctx := context.Background()
	db.Exec(t, `INSERT INTO kernel.kernel_plugins (plugin_id, plugin_type, display_name, namespace, manifest_digest, manifest_json)
	            VALUES ('plugin_demo','executor','Demo','demo',repeat('a',64),'{}'::jsonb)`)

	docID := mustPropose(t, m, UserBotActor(), ScopePlugin, "plugin_demo", `{"tools":["bash"]}`, 0)
	var status string
	if err := db.QueryRow(t, `SELECT status FROM kernel.kernel_config_documents WHERE doc_id=$1`, docID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusSecurityReview {
		t.Fatalf("可执行类提案应进入 security_review，得到 %s", status)
	}
	// 缺预审决定直接批准 → 状态不允许
	if err := m.Approve(ctx, UserBotActor(), docID, checksumOf(t, db, docID)); !errors.Is(err, ErrBadTransition) {
		t.Fatalf("缺预审不得批准，得到 %v", err)
	}
	// 非 security_bot 预审 → 拒绝
	if err := m.Review(ctx, UserBotActor(), docID, "approved", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非 security_bot 预审必须被拒，得到 %v", err)
	}
	if err := m.Review(ctx, KernelRole(SecurityBot), docID, "approved", "最小权限通过"); err != nil {
		t.Fatalf("security_bot 预审应成功: %v", err)
	}
	if err := m.Approve(ctx, UserBotActor(), docID, checksumOf(t, db, docID)); err != nil {
		t.Fatalf("预审通过后应可批准: %v", err)
	}
	if err := m.Activate(ctx, UserBotActor(), docID, cc()); err != nil {
		t.Fatalf("激活应成功: %v", err)
	}
}

func TestSecurityRejectedBlocksActivation(t *testing.T) {
	m, db := newModule(t)
	ctx := context.Background()
	db.Exec(t, `INSERT INTO kernel.kernel_plugins (plugin_id, plugin_type, display_name, namespace, manifest_digest, manifest_json)
	            VALUES ('plugin_demo','executor','Demo','demo',repeat('a',64),'{}'::jsonb)`)
	docID := mustPropose(t, m, UserBotActor(), ScopePlugin, "plugin_demo", `{"permissions":["*"]}`, 0)
	if err := m.Review(ctx, KernelRole(SecurityBot), docID, "rejected", "权限过宽"); err != nil {
		t.Fatal(err)
	}
	if err := m.Approve(ctx, UserBotActor(), docID, checksumOf(t, db, docID)); !errors.Is(err, ErrBadTransition) {
		t.Fatalf("预审 rejected 后不得批准，得到 %v", err)
	}
}

func checksumOf(t *testing.T, db *pgtest.DB, docID string) string {
	t.Helper()
	var c string
	if err := db.QueryRow(t, `SELECT checksum FROM kernel.kernel_config_documents WHERE doc_id=$1`, docID).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

// §8.5：激活后 kernel.config.changed 可订阅
func TestActivateEmitsConfigChangedEvent(t *testing.T) {
	m, db := newModule(t)
	docID := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"v2-model","prompt_ref":"echo"}`, 1)
	mustActivate(t, m, db, docID)

	var eventType, correlation, root, aggID string
	var payload []byte
	row := db.QueryRow(t, `SELECT event_type, correlation_id, root_envelope_id, aggregate_id, payload_json
	                         FROM kernel.kernel_event_outbox WHERE aggregate_id=$1`, docID)
	if err := row.Scan(&eventType, &correlation, &root, &aggID, &payload); err != nil {
		t.Fatalf("激活必须写 outbox 事件: %v", err)
	}
	if eventType != "kernel.config.changed" {
		t.Fatalf("事件类型不符: %s", eventType)
	}
	if correlation != cc().CorrelationID || root != cc().RootEnvelopeID {
		t.Fatalf("事件必须继承因果链: %s / %s", correlation, root)
	}
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	if p["scope_type"] != "bot" || p["scope_id"] != "echo_bot" {
		t.Fatalf("事件载荷不符: %v", p)
	}
	if p["checksum"] != checksumOf(t, db, docID) {
		t.Fatalf("事件必须携带 checksum: %v", p)
	}
	// user_bot 已订阅该事件类型
	var n int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_event_subscriptions
	                          WHERE consumer_group_id='user_bot' AND event_type='kernel.config.changed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("user_bot 应已订阅 kernel.config.changed，得到 %d", n)
	}
}

// 激活事务：旧 active 退役、投影更新、审计同事务
func TestActivateRetiresOldAndProjects(t *testing.T) {
	m, db := newModule(t)
	docID := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot",
		`{"activation_mode":"lazy","idle_ttl_seconds":120,"model":"v2","prompt_ref":"echo","start_timeout_seconds":45}`, 1)
	mustActivate(t, m, db, docID)

	var activeCount, retiredCount int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_config_documents
	                          WHERE scope_type='bot' AND scope_id='echo_bot' AND status='active'`).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 {
		t.Fatalf("同时只能有一个 active，得到 %d", activeCount)
	}
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_config_documents
	                          WHERE scope_type='bot' AND scope_id='echo_bot' AND status='retired'`).Scan(&retiredCount); err != nil {
		t.Fatal(err)
	}
	if retiredCount != 1 {
		t.Fatalf("旧 active 应退役，得到 %d", retiredCount)
	}
	var mode string
	var idle, timeout int
	var cv int64
	if err := db.QueryRow(t, `SELECT activation_mode, idle_ttl_seconds, start_timeout_seconds, config_version
	                          FROM kernel.bot_manifest WHERE bot_id='echo_bot'`).Scan(&mode, &idle, &timeout, &cv); err != nil {
		t.Fatal(err)
	}
	if idle != 120 || timeout != 45 || cv != 2 {
		t.Fatalf("投影未更新: mode=%s idle=%d timeout=%d cv=%d", mode, idle, timeout, cv)
	}
	var botsCV int64
	if err := db.QueryRow(t, `SELECT config_version FROM kernel.bots WHERE bot_id='echo_bot'`).Scan(&botsCV); err != nil {
		t.Fatal(err)
	}
	if botsCV != 2 {
		t.Fatalf("bots.config_version 应为 2，得到 %d", botsCV)
	}
	var audits int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_config_audit WHERE doc_id=$1 AND action='activate'`, docID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("激活必须写审计，得到 %d", audits)
	}
}

// base_version 乐观并发
func TestStaleBaseVersionRejected(t *testing.T) {
	m, db := newModule(t)
	ctx := context.Background()
	first := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"a"}`, 1)
	mustActivate(t, m, db, first)
	// 现在 active=2，用 base=1 提案必须冲突
	_, err := m.Propose(ctx, UserBotActor(), ScopeBot, "echo_bot", DefaultConfigKey, json.RawMessage(`{"model":"b"}`), 1)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("过期 base_version 必须冲突，得到 %v", err)
	}
}

// §8.5：非授权主体读不到其他 scope 的配置
func TestReadACL(t *testing.T) {
	m, db := newModule(t)
	ctx := context.Background()
	pending := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"pending"}`, 1)

	cases := []struct {
		name    string
		actor   Actor
		scope   string
		id      string
		version int64
		allowed bool
	}{
		{"user_bot 读 pending", UserBotActor(), ScopeBot, "echo_bot", versionOf(t, db, pending), true},
		{"create_bot 读 pending 被拒", KernelRole(CreateBot), ScopeBot, "echo_bot", versionOf(t, db, pending), false},
		{"create_bot 读 active bot", KernelRole(CreateBot), ScopeBot, "echo_bot", 0, true},
		{"create_bot 读 global 被拒", KernelRole(CreateBot), ScopeGlobal, GlobalScopeID, 0, false},
		{"bot 读自己 active", BotActor("echo_bot"), ScopeBot, "echo_bot", 0, true},
		{"bot 读 global 公开子集", BotActor("echo_bot"), ScopeGlobal, GlobalScopeID, 0, true},
		{"bot 读他人配置被拒", BotActor("other_bot"), ScopeBot, "echo_bot", 0, false},
		{"bot 读 plugin 配置被拒", BotActor("echo_bot"), ScopePlugin, "plugin_demo", 0, false},
		{"agent 读 bot 配置被拒", AgentActor("agent_1"), ScopeBot, "echo_bot", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.Get(ctx, tc.actor, tc.scope, tc.id, DefaultConfigKey, tc.version)
			if tc.allowed && err != nil {
				t.Fatalf("应允许读取: %v", err)
			}
			if !tc.allowed && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
				t.Fatalf("应拒绝读取，得到 %v", err)
			}
		})
	}
	_ = db
}

// monitor_bot 只读版本 / checksum / 状态，不读 spec 正文
func TestMonitorBotCannotReadSpec(t *testing.T) {
	m, _ := newModule(t)
	doc, err := m.Get(context.Background(), KernelRole(MonitorBot), ScopeBot, "echo_bot", DefaultConfigKey, 0)
	if err != nil {
		t.Fatalf("monitor_bot 应可读摘要: %v", err)
	}
	if doc.Spec != nil {
		t.Fatalf("monitor_bot 不得拿到 spec 正文: %s", doc.Spec)
	}
	if doc.Checksum == "" || doc.Version == 0 {
		t.Fatal("monitor_bot 应拿到版本与 checksum")
	}
}

// §8.5：secret 值不出现在任何文档 / 审计中
func TestPlaintextSecretRejectedAndNoValueInAudit(t *testing.T) {
	m, db := newModule(t)
	ctx := context.Background()
	_, err := m.Propose(ctx, UserBotActor(), ScopeBot, "echo_bot", DefaultConfigKey,
		json.RawMessage(`{"model":"x","api_key":"sk-SUPER-SECRET"}`), 1)
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("明文密钥必须被拒，得到 %v", err)
	}
	var n int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_config_documents WHERE spec::text LIKE '%SUPER-SECRET%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("密钥值不得落库，命中 %d 行", n)
	}
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_config_audit WHERE reason LIKE '%SUPER-SECRET%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("密钥值不得进审计，命中 %d 行", n)
	}
}

func TestSecretRefAcceptedAndValidated(t *testing.T) {
	m, db := newModule(t)
	ctx := context.Background()
	// 引用不存在 → 激活被拒
	docID := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"x","api_key":{"$secret_ref":"ref_missing"}}`, 1)
	if err := m.Approve(ctx, UserBotActor(), docID, checksumOf(t, db, docID)); err != nil {
		t.Fatal(err)
	}
	if err := m.Activate(ctx, UserBotActor(), docID, cc()); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("引用不存在的 secret_ref 必须被拒，得到 %v", err)
	}
	// 登记引用后可激活
	db.Exec(t, `INSERT INTO kernel.kernel_secret_refs (ref_id, provider, locator, created_by)
	            VALUES ('ref_missing','env','LLM_API_KEY','user_bot')`)
	if err := m.Activate(ctx, UserBotActor(), docID, cc()); err != nil {
		t.Fatalf("登记引用后应可激活: %v", err)
	}
	var spec string
	if err := db.QueryRow(t, `SELECT spec::text FROM kernel.kernel_config_documents WHERE doc_id=$1`, docID).Scan(&spec); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(spec, "LLM_API_KEY") {
		t.Fatalf("文档不得包含 locator 敏感部分: %s", spec)
	}
}

// Rollback 生成新提案，仍走审批，不直接改旧行为 active
func TestRollbackCreatesNewProposal(t *testing.T) {
	m, db := newModule(t)
	ctx := context.Background()
	v2 := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"v2"}`, 1)
	mustActivate(t, m, db, v2)

	newID, err := m.Rollback(ctx, UserBotActor(), ScopeBot, "echo_bot", DefaultConfigKey, 1)
	if err != nil {
		t.Fatalf("回滚应生成新提案: %v", err)
	}
	var status string
	var version int64
	if err := db.QueryRow(t, `SELECT status, version FROM kernel.kernel_config_documents WHERE doc_id=$1`, newID).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != StatusProposed {
		t.Fatalf("回滚必须是提案而非直接生效，得到 %s", status)
	}
	if version != 3 {
		t.Fatalf("回滚应生成新版本 3，得到 %d", version)
	}
	active, err := m.Get(ctx, UserBotActor(), ScopeBot, "echo_bot", DefaultConfigKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	if active.Version != 2 {
		t.Fatalf("回滚提案未批准前 active 仍是 v2，得到 v%d", active.Version)
	}
}

// 读取行为必须写审计
func TestReadIsAudited(t *testing.T) {
	m, db := newModule(t)
	if _, err := m.Get(context.Background(), BotActor("echo_bot"), ScopeBot, "echo_bot", DefaultConfigKey, 0); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_config_audit WHERE action='read' AND actor='echo_bot'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("读取配置必须写审计")
	}
}

// 激活必须携带因果链
func TestActivateRequiresCausalContext(t *testing.T) {
	m, db := newModule(t)
	ctx := context.Background()
	docID := mustPropose(t, m, UserBotActor(), ScopeBot, "echo_bot", `{"model":"x"}`, 1)
	if err := m.Approve(ctx, UserBotActor(), docID, checksumOf(t, db, docID)); err != nil {
		t.Fatal(err)
	}
	if err := m.Activate(ctx, UserBotActor(), docID, CausalContext{}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("缺因果链必须被拒，得到 %v", err)
	}
}

// 非法 scope 与 global scope_id 约束
func TestInvalidScopeRejected(t *testing.T) {
	m, _ := newModule(t)
	ctx := context.Background()
	if _, err := m.Propose(ctx, UserBotActor(), "mcp", "x", DefaultConfigKey, json.RawMessage(`{}`), 0); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("MCP 不是配置域，必须被拒: %v", err)
	}
	if _, err := m.Propose(ctx, UserBotActor(), "skill", "x", DefaultConfigKey, json.RawMessage(`{}`), 0); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Skill 不是配置域，必须被拒: %v", err)
	}
	if _, err := m.Propose(ctx, UserBotActor(), ScopeGlobal, "not_kernel", DefaultConfigKey, json.RawMessage(`{}`), 0); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("global scope_id 必须为 kernel: %v", err)
	}
}
