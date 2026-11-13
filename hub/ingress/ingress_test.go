package ingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/pgtest"
)

const (
	testCredKey = "0123456789abcdef0123456789abcdef"
	testSnapKey = "abcdef0123456789abcdef0123456789"
)

func newIngress(t *testing.T) (*Ingress, *registry.Registry, *pgtest.DB) {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	reg, err := registry.New(st, []byte(testCredKey), time.Hour)
	if err != nil {
		t.Fatalf("创建 registry: %v", err)
	}
	ing, err := New(st, reg, scheduler.New(st, scheduler.Options{}), Options{SnapshotKey: []byte(testSnapKey), PayloadDir: t.TempDir()})
	if err != nil {
		t.Fatalf("创建 ingress: %v", err)
	}
	return ing, reg, db
}

func userCredential(t *testing.T, reg *registry.Registry) string {
	t.Helper()
	cred, err := reg.IssueCredential(context.Background(), config.KindBot, config.UserBot)
	if err != nil {
		t.Fatalf("签发 user_bot 凭证: %v", err)
	}
	return cred.Token
}

func rootEnv(key string) *envelope.Envelope {
	return envelope.NewRoot(config.UserBot, envelope.ChannelCLI, envelope.MsgRequest, key)
}

func logDecision(t *testing.T, db *pgtest.DB, envelopeID string) (decision, reason string) {
	t.Helper()
	row := db.QueryRow(t, `SELECT decision, COALESCE(reason,'') FROM kernel.kernel_ingress_log WHERE envelope_id=$1 ORDER BY log_id DESC LIMIT 1`, envelopeID)
	if err := row.Scan(&decision, &reason); err != nil {
		t.Fatalf("读取入口审计: %v", err)
	}
	return
}

func TestSubmitAccepted(t *testing.T) {
	ing, reg, db := newIngress(t)
	env := rootEnv("idem-accept")
	payload := []byte(`{"text":"hello","token":"SECRET-TOKEN"}`)
	sum := sha256.Sum256(payload)
	env.PayloadHash = hex.EncodeToString(sum[:])

	res, err := ing.Submit(context.Background(), env, userCredential(t, reg), payload, Route{})
	if err != nil {
		t.Fatalf("合法信封应被接受: %v", err)
	}
	if res.Decision != DecisionAccepted {
		t.Fatalf("期望 accepted，得到 %s", res.Decision)
	}
	if res.PayloadRef == "" || res.SnapshotID == "" {
		t.Fatal("接受时必须落载荷引用与上下文快照")
	}
	decision, _ := logDecision(t, db, env.EnvelopeID)
	if decision != DecisionAccepted {
		t.Fatalf("审计决策不符: %s", decision)
	}
	// 载荷必须加密落盘，磁盘上不得出现明文
	var uri string
	if err := db.QueryRow(t, `SELECT storage_uri FROM kernel.kernel_payloads WHERE payload_id=$1`, res.PayloadRef).Scan(&uri); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(strings.TrimPrefix(uri, "file://"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hello") || strings.Contains(string(raw), "SECRET-TOKEN") {
		t.Fatal("载荷必须加密存储，磁盘不得出现明文")
	}
	// 快照内敏感字段必须脱敏
	var snapURI string
	if err := db.QueryRow(t, `SELECT storage_uri FROM kernel.kernel_context_snapshots WHERE snapshot_id=$1`, res.SnapshotID).Scan(&snapURI); err != nil {
		t.Fatal(err)
	}
	plain, err := ing.decryptFile(snapURI)
	if err != nil {
		t.Fatalf("解密快照: %v", err)
	}
	if strings.Contains(string(plain), "SECRET-TOKEN") {
		t.Fatalf("快照必须脱敏: %s", plain)
	}
	if !strings.Contains(string(plain), "<redacted:token>") {
		t.Fatalf("快照应含脱敏引用: %s", plain)
	}
}

// 失败注入 1：缺因果头
func TestRejectMissingCausalHeader(t *testing.T) {
	ing, reg, db := newIngress(t)
	env := rootEnv("idem-no-causal")
	env.CausalPath = nil
	res, err := ing.Submit(context.Background(), env, userCredential(t, reg), nil, Route{})
	if err == nil {
		t.Fatal("缺因果头必须被拒")
	}
	if res.Decision != DecisionRejected {
		t.Fatalf("期望 rejected，得到 %s", res.Decision)
	}
	if res.Code != envelope.CodeCausalRequired {
		t.Fatalf("错误码不符: %s", res.Code)
	}
	// 拒绝也必须留完整现场
	decision, reason := logDecision(t, db, env.EnvelopeID)
	if decision != DecisionRejected || reason == "" {
		t.Fatalf("拒绝必须落审计与原因: %s / %s", decision, reason)
	}
	var n int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_ingress_log WHERE envelope_id=$1 AND correlation_id IS NOT NULL`, env.EnvelopeID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("拒绝记录必须保留因果上下文")
	}
}

// 失败注入 2：路径断裂
func TestRejectBrokenCausalPath(t *testing.T) {
	ing, reg, db := newIngress(t)
	parent := rootEnv("idem-parent")
	child, err := envelope.Derive(parent, "echo_bot", envelope.ChannelCLI, envelope.MsgReply, "idem-child", parent.EnvelopeID)
	if err != nil {
		t.Fatal(err)
	}
	child.CausationID = "env_not_in_path"
	cred, err := reg.IssueCredential(context.Background(), config.KindBot, "echo_bot")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ing.Submit(context.Background(), child, cred.Token, nil, Route{})
	if err == nil || res.Decision != DecisionRejected {
		t.Fatalf("路径断裂必须被拒: %v / %s", err, res.Decision)
	}
	if res.Code != envelope.CodeCausationMismatch {
		t.Fatalf("错误码不符: %s", res.Code)
	}
	if d, _ := logDecision(t, db, child.EnvelopeID); d != DecisionRejected {
		t.Fatalf("审计决策不符: %s", d)
	}
}

// 失败注入 3：深度超过 32
func TestRejectCausalDepthExceeded(t *testing.T) {
	ing, reg, db := newIngress(t)
	env := rootEnv("idem-deep")
	env.CausalPath = make([]envelope.CausalNode, envelope.MaxCausalDepth+1)
	for i := range env.CausalPath {
		id := "env_deep_" + strings.Repeat("d", i+1)
		env.CausalPath[i] = envelope.CausalNode{NodeID: id, NodeType: "job", Actor: config.UserBot, At: time.Now().UTC()}
	}
	env.RootEnvelopeID = env.CausalPath[0].NodeID
	env.CausationID = env.CausalPath[len(env.CausalPath)-1].NodeID
	res, err := ing.Submit(context.Background(), env, userCredential(t, reg), nil, Route{})
	if err == nil || res.Decision != DecisionRejected {
		t.Fatalf("深度超限必须被拒: %v", err)
	}
	if res.Code != envelope.CodeDepthExceeded {
		t.Fatalf("错误码不符: %s", res.Code)
	}
	if d, _ := logDecision(t, db, env.EnvelopeID); d != DecisionRejected {
		t.Fatalf("审计决策不符: %s", d)
	}
}

// 失败注入 4：根不一致
func TestRejectRootMismatch(t *testing.T) {
	ing, reg, db := newIngress(t)
	env := rootEnv("idem-root-mismatch")
	env.RootEnvelopeID = "env_other_root"
	res, err := ing.Submit(context.Background(), env, userCredential(t, reg), nil, Route{})
	if err == nil || res.Decision != DecisionRejected {
		t.Fatalf("根不一致必须被拒: %v", err)
	}
	if res.Code != envelope.CodeRootMismatch {
		t.Fatalf("错误码不符: %s", res.Code)
	}
	if d, _ := logDecision(t, db, env.EnvelopeID); d != DecisionRejected {
		t.Fatalf("审计决策不符: %s", d)
	}
}

// 失败注入 5：重复幂等键
func TestDuplicateIdempotencyKey(t *testing.T) {
	ing, reg, db := newIngress(t)
	cred := userCredential(t, reg)
	first := rootEnv("idem-same")
	if _, err := ing.Submit(context.Background(), first, cred, []byte(`{"a":1}`), Route{}); err != nil {
		t.Fatal(err)
	}
	second := rootEnv("idem-same")
	res, err := ing.Submit(context.Background(), second, cred, []byte(`{"a":1}`), Route{})
	if err != nil {
		t.Fatalf("重复投递不应报错，应记 duplicate: %v", err)
	}
	if res.Decision != DecisionDuplicate || !res.IdempotencyHit {
		t.Fatalf("期望 duplicate，得到 %s", res.Decision)
	}
	// 重复也要记录完整上下文
	if d, _ := logDecision(t, db, second.EnvelopeID); d != DecisionDuplicate {
		t.Fatalf("重复必须落审计: %s", d)
	}
	var accepted, dup int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_ingress_log WHERE idempotency_key='idem-same' AND decision='accepted'`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 {
		t.Fatalf("同一幂等键只能有一条 accepted，得到 %d", accepted)
	}
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_ingress_log WHERE idempotency_key='idem-same' AND decision='duplicate'`).Scan(&dup); err != nil {
		t.Fatal(err)
	}
	if dup != 1 {
		t.Fatalf("重复投递必须留痕，得到 %d", dup)
	}
}

func TestRejectMissingCredential(t *testing.T) {
	ing, _, db := newIngress(t)
	env := rootEnv("idem-no-cred")
	res, err := ing.Submit(context.Background(), env, "", nil, Route{})
	if err == nil || res.Decision != DecisionRejected {
		t.Fatalf("缺凭证必须被拒: %v", err)
	}
	if d, _ := logDecision(t, db, env.EnvelopeID); d != DecisionRejected {
		t.Fatalf("审计决策不符: %s", d)
	}
}

func TestRejectCredentialSubjectMismatch(t *testing.T) {
	ing, reg, db := newIngress(t)
	env := rootEnv("idem-mismatch")
	// echo_bot 的凭证冒充 user_bot 发件
	cred, err := reg.IssueCredential(context.Background(), config.KindBot, "echo_bot")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ing.Submit(context.Background(), env, cred.Token, nil, Route{})
	if err == nil || res.Decision != DecisionRejected {
		t.Fatalf("凭证主体与 from_bot_id 不一致必须被拒: %v", err)
	}
	if d, _ := logDecision(t, db, env.EnvelopeID); d != DecisionRejected {
		t.Fatalf("审计决策不符: %s", d)
	}
}

func TestRevokedPluginCredentialRejected(t *testing.T) {
	ing, reg, db := newIngress(t)
	ctx := context.Background()
	if err := reg.RegisterPlugin(ctx, "plugin_demo", "executor", "Demo", "demo", strings.Repeat("a", 64), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	cred, err := reg.IssueCredential(ctx, config.KindPlugin, "plugin_demo")
	if err != nil {
		t.Fatal(err)
	}
	env := envelope.NewRoot("plugin_demo", envelope.ChannelTransport, envelope.MsgEvent, "idem-rev")
	if _, err := ing.Submit(ctx, env, cred.Token, nil, Route{}); err != nil {
		t.Fatalf("吊销前应可用: %v", err)
	}
	if err := reg.Revoke(ctx, "plugin_demo"); err != nil {
		t.Fatal(err)
	}
	env2 := envelope.NewRoot("plugin_demo", envelope.ChannelTransport, envelope.MsgEvent, "idem-rev2")
	res, err := ing.Submit(ctx, env2, cred.Token, nil, Route{})
	if err == nil || res.Decision != DecisionRejected {
		t.Fatalf("吊销后必须立即失效: %v", err)
	}
	if !errors.Is(err, registry.ErrRevoked) {
		t.Fatalf("期望 ErrRevoked，得到 %v", err)
	}
	if d, _ := logDecision(t, db, env2.EnvelopeID); d != DecisionRejected {
		t.Fatalf("审计决策不符: %s", d)
	}
}

// 载荷拉取授权：必须来自其收到的信封（投递关系）
func TestFetchPayloadAuthorization(t *testing.T) {
	ing, reg, db := newIngress(t)
	ctx := context.Background()
	env := rootEnv("idem-fetch")
	payload := []byte(`{"text":"business-content"}`)
	sum := sha256.Sum256(payload)
	env.PayloadHash = hex.EncodeToString(sum[:])
	res, err := ing.Submit(ctx, env, userCredential(t, reg), payload, Route{})
	if err != nil {
		t.Fatal(err)
	}

	// echo_bot 未收到任何引用该载荷的信封 → 拒绝
	if _, err := ing.FetchPayload(ctx, config.BotActor("echo_bot"), res.PayloadRef); !errors.Is(err, ErrPayloadForbidden) {
		t.Fatalf("未收到信封不得拉取载荷，得到 %v", err)
	}
	// 建立投递关系（任务分配给 echo_bot）后可拉取
	db.Exec(t, `INSERT INTO kernel.kernel_jobs (queue_name, job_type, bot_id, envelope_id, payload_ref, correlation_id, root_envelope_id)
	            VALUES ('echo_bot','bot.echo','echo_bot',$1,$2,$3,$1)`, env.EnvelopeID, res.PayloadRef, env.CorrelationID)
	got, err := ing.FetchPayload(ctx, config.BotActor("echo_bot"), res.PayloadRef)
	if err != nil {
		t.Fatalf("已投递应可拉取: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("拉取内容不符: %s", got)
	}
	// 其他 bot 仍无权
	if _, err := ing.FetchPayload(ctx, config.BotActor("other_bot"), res.PayloadRef); !errors.Is(err, ErrPayloadForbidden) {
		t.Fatalf("无关 bot 不得拉取，得到 %v", err)
	}
}

func TestPayloadHashMismatchRejected(t *testing.T) {
	ing, reg, _ := newIngress(t)
	env := rootEnv("idem-hash")
	env.PayloadHash = strings.Repeat("ab", 32)
	_, err := ing.Submit(context.Background(), env, userCredential(t, reg), []byte(`{"x":1}`), Route{})
	if err == nil {
		t.Fatal("payload_hash 与实际不符必须被拒")
	}
	var ve *envelope.ValidationError
	if !errors.As(err, &ve) || ve.Code != envelope.CodeInvalidHash {
		t.Fatalf("期望 INVALID_PAYLOAD_HASH，得到 %v", err)
	}
}

// 审计表对运行角色 append-only
func TestIngressLogIsAppendOnlyForRuntimeRole(t *testing.T) {
	ing, reg, db := newIngress(t)
	env := rootEnv("idem-append")
	if _, err := ing.Submit(context.Background(), env, userCredential(t, reg), nil, Route{}); err != nil {
		t.Fatal(err)
	}
	_, err := db.Kernel.Exec(context.Background(), `UPDATE kernel_ingress_log SET decision='rejected' WHERE envelope_id=$1`, env.EnvelopeID)
	if err == nil {
		t.Fatal("运行角色不得 UPDATE 入口审计")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("期望权限拒绝，得到 %v", err)
	}
	_, err = db.Kernel.Exec(context.Background(), `DELETE FROM kernel_ingress_log WHERE envelope_id=$1`, env.EnvelopeID)
	if err == nil {
		t.Fatal("运行角色不得 DELETE 入口审计")
	}
}
