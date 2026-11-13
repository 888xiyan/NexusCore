package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/pgtest"
)

func newRegistry(t *testing.T) (*Registry, *pgtest.DB) {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	reg, err := New(st, []byte("0123456789abcdef0123456789abcdef"), DefaultTTL)
	if err != nil {
		t.Fatalf("建 registry: %v", err)
	}
	return reg, db
}

func registerPlugin(t *testing.T, reg *Registry, pluginID string) {
	t.Helper()
	if err := reg.RegisterPlugin(context.Background(), pluginID, "workspace", "测试插件", "", "digest", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("登记插件: %v", err)
	}
}

func TestIssueAndVerifyBotCredential(t *testing.T) {
	reg, _ := newRegistry(t)
	ctx := context.Background()
	cred, err := reg.IssueCredential(ctx, config.KindBot, "echo_bot")
	if err != nil {
		t.Fatal(err)
	}
	actor, _, err := reg.VerifyCredential(ctx, cred.Token)
	if err != nil {
		t.Fatal(err)
	}
	if actor.Kind != config.KindBot || actor.ID != "echo_bot" {
		t.Fatalf("主体不符: %+v", actor)
	}
	if time.Until(cred.ExpiresAt) <= 0 {
		t.Fatal("凭证应未过期")
	}
}

// 内核四角色凭证由 hub 自签，保留名不走插件注册校验
func TestKernelRoleCredentialIssuable(t *testing.T) {
	reg, _ := newRegistry(t)
	ctx := context.Background()
	for _, name := range []string{config.UserBot, config.CreateBot, config.MonitorBot, config.SecurityBot} {
		cred, err := reg.IssueCredential(ctx, config.KindBot, name)
		if err != nil {
			t.Fatalf("内核角色 %s 签发失败: %v", name, err)
		}
		if _, _, err := reg.VerifyCredential(ctx, cred.Token); err != nil {
			t.Fatalf("内核角色 %s 校验失败: %v", name, err)
		}
	}
}

func TestUnregisteredSubjectsRejected(t *testing.T) {
	reg, _ := newRegistry(t)
	ctx := context.Background()
	if _, err := reg.IssueCredential(ctx, config.KindBot, "ghost_bot"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("未注册 bot 应拒绝，实际 %v", err)
	}
	if _, err := reg.IssueCredential(ctx, config.KindPlugin, "plugin_ghost"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("未注册插件应拒绝，实际 %v", err)
	}
	if _, err := reg.IssueCredential(ctx, config.KindAgent, "ghost_agent"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("未注册 agent 应拒绝，实际 %v", err)
	}
	if _, err := reg.IssueCredential(ctx, "root", "echo_bot"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("非法主体类型应拒绝，实际 %v", err)
	}
	if _, err := reg.IssueCredential(ctx, config.KindBot, ""); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("空 subject 应拒绝，实际 %v", err)
	}
}

func TestDisabledBotCredentialRejected(t *testing.T) {
	reg, db := newRegistry(t)
	ctx := context.Background()
	cred, err := reg.IssueCredential(ctx, config.KindBot, "echo_bot")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(t, `UPDATE bots SET enabled=false WHERE bot_id='echo_bot'`)
	if _, _, err := reg.VerifyCredential(ctx, cred.Token); !errors.Is(err, ErrRevoked) {
		t.Fatalf("停用后旧令牌应立即失效，实际 %v", err)
	}
	if _, err := reg.IssueCredential(ctx, config.KindBot, "echo_bot"); !errors.Is(err, ErrRevoked) {
		t.Fatalf("停用后不应再签发，实际 %v", err)
	}
}

func TestTamperedTokenRejected(t *testing.T) {
	reg, _ := newRegistry(t)
	ctx := context.Background()
	cred, err := reg.IssueCredential(ctx, config.KindBot, "echo_bot")
	if err != nil {
		t.Fatal(err)
	}
	bad := []byte(cred.Token)
	if bad[0] == 'A' {
		bad[0] = 'B'
	} else {
		bad[0] = 'A'
	}
	if _, _, err := reg.VerifyCredential(ctx, string(bad)); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("篡改令牌应拒绝，实际 %v", err)
	}
	if _, _, err := reg.VerifyCredential(ctx, "not-a-token"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("格式非法应拒绝，实际 %v", err)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	reg, _ := newRegistry(t)
	past := time.Now().Add(-time.Hour)
	token, err := reg.sign(claims{Subject: "echo_bot", Kind: config.KindBot, JTI: "jti_expired",
		Issued: past.Add(-time.Hour).Unix(), Expires: past.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.VerifyCredential(context.Background(), token); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("过期令牌应拒绝，实际 %v", err)
	}
}

// 另一把密钥签发的令牌不得通过校验
func TestForeignKeyRejected(t *testing.T) {
	reg, db := newRegistry(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other, err := New(st, []byte("ffffffffffffffffffffffffffffffff"), DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := other.IssueCredential(context.Background(), config.KindBot, "echo_bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.VerifyCredential(context.Background(), cred.Token); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("异密钥令牌应拒绝，实际 %v", err)
	}
}

func TestRenewCredential(t *testing.T) {
	reg, _ := newRegistry(t)
	ctx := context.Background()
	cred, err := reg.IssueCredential(ctx, config.KindBot, "echo_bot")
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := reg.RenewCredential(ctx, cred.Token)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Token == cred.Token {
		t.Fatal("续期应换发新令牌（新 jti）")
	}
	if _, _, err := reg.VerifyCredential(ctx, renewed.Token); err != nil {
		t.Fatalf("新令牌应可用: %v", err)
	}
}

func TestShortKeyRejected(t *testing.T) {
	db := pgtest.Open(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := New(st, []byte("too-short"), DefaultTTL); err == nil {
		t.Fatal("密钥短于 32 字节应拒绝")
	}
}

// 吊销立即生效：令牌失效 + 订阅停止 + 授权回收，且跨重启有效
func TestRevokePluginInvalidatesEverything(t *testing.T) {
	reg, db := newRegistry(t)
	ctx := context.Background()
	registerPlugin(t, reg, "plugin_test")
	if err := reg.ApprovePermissions(ctx, "plugin_test", []Permission{
		{PermType: "subscribe", PermTarget: "plugin_test.note.updated"},
	}, config.UserBot); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Subscribe(ctx, "plugin_test", []string{"plugin_test.note.updated"}, nil, config.UserBot); err != nil {
		t.Fatal(err)
	}
	cred, err := reg.IssueCredential(ctx, config.KindPlugin, "plugin_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Revoke(ctx, "plugin_test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.VerifyCredential(ctx, cred.Token); !errors.Is(err, ErrRevoked) {
		t.Fatalf("吊销后令牌应失效，实际 %v", err)
	}
	var stopped, revoked int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel_plugin_subs WHERE plugin_id='plugin_test' AND status='stopped'`).Scan(&stopped); err != nil {
		t.Fatal(err)
	}
	if stopped != 1 {
		t.Fatalf("订阅应停止，实际 %d", stopped)
	}
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel_plugin_grants WHERE plugin_id='plugin_test' AND revoked_at IS NOT NULL`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked != 1 {
		t.Fatalf("授权应回收，实际 %d", revoked)
	}
	// 跨重启：新建 Registry 实例仍拒绝
	st, err := store.Open(ctx, db.KernelDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reborn, err := New(st, []byte("0123456789abcdef0123456789abcdef"), DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reborn.VerifyCredential(ctx, cred.Token); !errors.Is(err, ErrRevoked) {
		t.Fatalf("重启后吊销仍应生效，实际 %v", err)
	}
}

func TestRevokeUnknownPluginRejected(t *testing.T) {
	reg, _ := newRegistry(t)
	if err := reg.Revoke(context.Background(), "plugin_ghost"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("吊销不存在插件应报错，实际 %v", err)
	}
}

// Manifest 只是声明：未批准的权限一律不生效
func TestPermissionsRequireUserBotApproval(t *testing.T) {
	reg, _ := newRegistry(t)
	ctx := context.Background()
	registerPlugin(t, reg, "plugin_test")
	perms := []Permission{{PermType: "tool", PermTarget: "plugin_test.write"}}

	if err := reg.ApprovePermissions(ctx, "plugin_test", perms, "create_bot"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("非 user_bot 批准应拒绝，实际 %v", err)
	}
	ok, err := reg.HasGrant(ctx, "plugin_test", "tool", "plugin_test.write")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("未批准的声明不得生效")
	}
	if err := reg.ApprovePermissions(ctx, "plugin_test", perms, config.UserBot); err != nil {
		t.Fatal(err)
	}
	if ok, err = reg.HasGrant(ctx, "plugin_test", "tool", "plugin_test.write"); err != nil || !ok {
		t.Fatalf("批准后应生效: ok=%v err=%v", ok, err)
	}
}

func TestUnknownPermTypeRejected(t *testing.T) {
	reg, _ := newRegistry(t)
	ctx := context.Background()
	registerPlugin(t, reg, "plugin_test")
	err := reg.ApprovePermissions(ctx, "plugin_test", []Permission{{PermType: "superuser", PermTarget: "*"}}, config.UserBot)
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("非法权限类型应拒绝，实际 %v", err)
	}
	err = reg.ApprovePermissions(ctx, "plugin_test", []Permission{{PermType: "tool", PermTarget: ""}}, config.UserBot)
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("空 perm_target 应拒绝，实际 %v", err)
	}
}

// 订阅必须先有 subscribe 授权，且只有 user_bot 能批准
func TestSubscribeRequiresGrantAndUserBot(t *testing.T) {
	reg, _ := newRegistry(t)
	ctx := context.Background()
	registerPlugin(t, reg, "plugin_test")

	if _, err := reg.Subscribe(ctx, "plugin_test", []string{"plugin_test.note.updated"}, nil, config.UserBot); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("无授权订阅应拒绝，实际 %v", err)
	}
	if err := reg.ApprovePermissions(ctx, "plugin_test", []Permission{
		{PermType: "subscribe", PermTarget: "plugin_test.note.updated"},
	}, config.UserBot); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Subscribe(ctx, "plugin_test", []string{"plugin_test.note.updated"}, nil, "create_bot"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("非 user_bot 批准订阅应拒绝，实际 %v", err)
	}
	if _, err := reg.Subscribe(ctx, "plugin_test", nil, nil, config.UserBot); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("空事件类型应拒绝，实际 %v", err)
	}
	group, err := reg.Subscribe(ctx, "plugin_test", []string{"plugin_test.note.updated"}, json.RawMessage(`{"project":"proj_nexus"}`), config.UserBot)
	if err != nil {
		t.Fatal(err)
	}
	if group != "cg_plugin_test" {
		t.Fatalf("Consumer Group 命名不符: %s", group)
	}
}

func TestRegisterPluginRejectsReservedName(t *testing.T) {
	reg, _ := newRegistry(t)
	err := reg.RegisterPlugin(context.Background(), config.UserBot, "workspace", "冒名", "", "digest", json.RawMessage(`{}`))
	if !errors.Is(err, ErrReservedName) {
		t.Fatalf("保留名应拒绝，实际 %v", err)
	}
}

func TestRegisterPluginDefaultsNamespace(t *testing.T) {
	reg, db := newRegistry(t)
	registerPlugin(t, reg, "plugin_test")
	var ns string
	if err := db.QueryRow(t, `SELECT namespace FROM kernel_plugins WHERE plugin_id='plugin_test'`).Scan(&ns); err != nil {
		t.Fatal(err)
	}
	if ns != "test" {
		t.Fatalf("namespace 默认应为 plugin_id 去前缀，实际 %q", ns)
	}
}
