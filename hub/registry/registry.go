// Package registry 是插件注册与 ACL：身份凭证签发 / 校验 / 吊销、授权表维护、订阅登记。
// 依据：内核 MVP 详设 §3.2、架构总结 §3.4。
// Manifest 只是声明，不构成授权；未批准的声明一律不生效。
package registry

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/jackc/pgx/v5"
)

// CredentialKeyEnv 是 hub 侧凭证签名密钥；与任何插件 DSN 无关。
const CredentialKeyEnv = "NEXUS_KERNEL_CREDENTIAL_KEY"

const DefaultTTL = 24 * time.Hour

var (
	ErrInvalidCredential = errors.New("凭证无效")
	ErrRevoked           = errors.New("凭证已吊销")
	ErrNotAuthorized     = errors.New("声明未获授权")
	ErrReservedName      = errors.New("保留名禁用")
)

// ReservedBotNames 是内核侧四角色，任何插件不得注册同名 bot。
var ReservedBotNames = map[string]bool{
	config.UserBot: true, config.CreateBot: true, config.MonitorBot: true, config.SecurityBot: true,
}

type Registry struct {
	st  *store.Store
	key []byte
	ttl time.Duration
}

// KeyFromEnv 读取签名密钥；缺失即报错，不使用内置默认密钥。
func KeyFromEnv() ([]byte, error) {
	v := os.Getenv(CredentialKeyEnv)
	if len(v) < 32 {
		return nil, fmt.Errorf("未设置 %s 或长度不足 32 字节：凭证签名密钥必须由 env 注入", CredentialKeyEnv)
	}
	return []byte(v), nil
}

func New(st *store.Store, key []byte, ttl time.Duration) (*Registry, error) {
	if len(key) < 32 {
		return nil, errors.New("凭证签名密钥至少 32 字节")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Registry{st: st, key: key, ttl: ttl}, nil
}

type claims struct {
	Subject string   `json:"sub"`
	Kind    string   `json:"kind"`
	JTI     string   `json:"jti"`
	Scopes  []string `json:"scopes,omitempty"`
	Issued  int64    `json:"iat"`
	Expires int64    `json:"exp"`
}

type Credential struct {
	Token     string
	Subject   string
	Kind      string
	ExpiresAt time.Time
}

func (r *Registry) sign(c claims) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("序列化凭证声明: %w", err)
	}
	mac := hmac.New(sha256.New, r.key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (r *Registry) parse(token string) (*claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: 令牌格式非法", ErrInvalidCredential)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: 载荷解码失败", ErrInvalidCredential)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: 签名解码失败", ErrInvalidCredential)
	}
	mac := hmac.New(sha256.New, r.key)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, fmt.Errorf("%w: 签名不匹配", ErrInvalidCredential)
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("%w: 声明解析失败", ErrInvalidCredential)
	}
	if time.Now().Unix() >= c.Expires {
		return nil, fmt.Errorf("%w: 令牌已过期", ErrInvalidCredential)
	}
	return &c, nil
}

func randomJTI() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("生成凭证 ID 失败: %v", err))
	}
	return hex.EncodeToString(b)
}

// IssueCredential 签发短期 HMAC token。插件主体必须已注册；Bot 主体必须已有 manifest。
func (r *Registry) IssueCredential(ctx context.Context, kind, subject string) (*Credential, error) {
	if subject == "" {
		return nil, fmt.Errorf("%w: subject 不能为空", ErrInvalidCredential)
	}
	switch kind {
	case config.KindPlugin:
		var status string
		var revoked *time.Time
		err := r.st.Pool().QueryRow(ctx, `SELECT status, revoked_at FROM kernel_plugins WHERE plugin_id=$1`, subject).
			Scan(&status, &revoked)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: 插件 %q 未注册", ErrNotAuthorized, subject)
		}
		if err != nil {
			return nil, fmt.Errorf("查询插件身份: %w", err)
		}
		if revoked != nil || status == "uninstalled" {
			return nil, fmt.Errorf("%w: 插件 %q 已吊销", ErrRevoked, subject)
		}
	case config.KindBot:
		if ReservedBotNames[subject] {
			// 内核四角色的凭证由 hub 自身签发，不经插件路径
			break
		}
		var enabled bool
		err := r.st.Pool().QueryRow(ctx, `
			SELECT b.enabled FROM bots b JOIN bot_manifest m ON m.bot_id=b.bot_id WHERE b.bot_id=$1`, subject).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: bot %q 未注册", ErrNotAuthorized, subject)
		}
		if err != nil {
			return nil, fmt.Errorf("查询 bot 身份: %w", err)
		}
		if !enabled {
			return nil, fmt.Errorf("%w: bot %q 已停用", ErrRevoked, subject)
		}
	case config.KindAgent:
		var status string
		err := r.st.Pool().QueryRow(ctx, `SELECT status FROM kernel_agents WHERE agent_id=$1`, subject).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: agent %q 未注册", ErrNotAuthorized, subject)
		}
		if err != nil {
			return nil, fmt.Errorf("查询 agent 身份: %w", err)
		}
		if status == "revoked" {
			return nil, fmt.Errorf("%w: agent %q 已吊销", ErrRevoked, subject)
		}
	default:
		return nil, fmt.Errorf("%w: 非法主体类型 %q", ErrInvalidCredential, kind)
	}

	now := time.Now()
	c := claims{Subject: subject, Kind: kind, JTI: randomJTI(), Issued: now.Unix(), Expires: now.Add(r.ttl).Unix()}
	token, err := r.sign(c)
	if err != nil {
		return nil, err
	}
	return &Credential{Token: token, Subject: subject, Kind: kind, ExpiresAt: time.Unix(c.Expires, 0).UTC()}, nil
}

// VerifyCredential 校验签名与有效期，并回查数据库吊销状态（吊销立即生效且跨重启有效）。
func (r *Registry) VerifyCredential(ctx context.Context, token string) (config.Actor, []string, error) {
	c, err := r.parse(token)
	if err != nil {
		return config.Actor{}, nil, err
	}
	actor := config.Actor{Kind: c.Kind, ID: c.Subject}
	switch c.Kind {
	case config.KindPlugin:
		var revoked *time.Time
		var status string
		err := r.st.Pool().QueryRow(ctx, `SELECT status, revoked_at FROM kernel_plugins WHERE plugin_id=$1`, c.Subject).
			Scan(&status, &revoked)
		if errors.Is(err, pgx.ErrNoRows) {
			return config.Actor{}, nil, fmt.Errorf("%w: 插件不存在", ErrRevoked)
		}
		if err != nil {
			return config.Actor{}, nil, fmt.Errorf("校验插件吊销状态: %w", err)
		}
		if revoked != nil || status == "uninstalled" || status == "disabled" {
			return config.Actor{}, nil, fmt.Errorf("%w: 插件 %q", ErrRevoked, c.Subject)
		}
	case config.KindBot:
		if !ReservedBotNames[c.Subject] {
			var enabled bool
			err := r.st.Pool().QueryRow(ctx, `SELECT enabled FROM bots WHERE bot_id=$1`, c.Subject).Scan(&enabled)
			if errors.Is(err, pgx.ErrNoRows) {
				return config.Actor{}, nil, fmt.Errorf("%w: bot 不存在", ErrRevoked)
			}
			if err != nil {
				return config.Actor{}, nil, fmt.Errorf("校验 bot 吊销状态: %w", err)
			}
			if !enabled {
				return config.Actor{}, nil, fmt.Errorf("%w: bot %q", ErrRevoked, c.Subject)
			}
		}
	case config.KindAgent:
		var status string
		err := r.st.Pool().QueryRow(ctx, `SELECT status FROM kernel_agents WHERE agent_id=$1`, c.Subject).Scan(&status)
		if err != nil {
			return config.Actor{}, nil, fmt.Errorf("%w: agent 不可用", ErrRevoked)
		}
		if status == "revoked" {
			return config.Actor{}, nil, fmt.Errorf("%w: agent %q", ErrRevoked, c.Subject)
		}
	}
	return actor, c.Scopes, nil
}

// RenewCredential 续期：必须持有未吊销的有效旧令牌。
func (r *Registry) RenewCredential(ctx context.Context, token string) (*Credential, error) {
	actor, _, err := r.VerifyCredential(ctx, token)
	if err != nil {
		return nil, err
	}
	return r.IssueCredential(ctx, actor.Kind, actor.ID)
}

// Revoke 吊销插件：令牌立即失效 + 订阅停止（订阅由 dispatcher 按 status 过滤）。
func (r *Registry) Revoke(ctx context.Context, pluginID string) error {
	return r.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE kernel_plugins SET revoked_at=now(), status='disabled', updated_at=now() WHERE plugin_id=$1`, pluginID)
		if err != nil {
			return fmt.Errorf("吊销插件: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: 插件 %q 不存在", ErrNotAuthorized, pluginID)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE kernel_plugin_subs SET status='stopped', stopped_at=now() WHERE plugin_id=$1 AND status='active'`, pluginID); err != nil {
			return fmt.Errorf("停止插件订阅: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE kernel_plugin_grants SET revoked_at=now() WHERE plugin_id=$1 AND revoked_at IS NULL`, pluginID); err != nil {
			return fmt.Errorf("吊销插件授权: %w", err)
		}
		return nil
	})
}

type Permission struct {
	PermType    string          `json:"perm_type"`
	PermTarget  string          `json:"perm_target"`
	ScopeFilter json.RawMessage `json:"scope_filter,omitempty"`
}

// ApprovePermissions 是唯一的授权批准入口（MVP 为 CLI approve-plugin）。
func (r *Registry) ApprovePermissions(ctx context.Context, pluginID string, perms []Permission, byUser string) error {
	if byUser != config.UserBot {
		return fmt.Errorf("%w: 只有 user_bot 能批准授权，收到 %q", ErrNotAuthorized, byUser)
	}
	return r.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		for _, p := range perms {
			switch p.PermType {
			case "subscribe", "tool", "namespace", "transport":
			default:
				return fmt.Errorf("%w: 非法权限类型 %q", ErrNotAuthorized, p.PermType)
			}
			if p.PermTarget == "" {
				return fmt.Errorf("%w: perm_target 不能为空", ErrNotAuthorized)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO kernel_plugin_grants (plugin_id, perm_type, perm_target, scope_filter, granted_by)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (plugin_id, perm_type, perm_target)
				DO UPDATE SET scope_filter=EXCLUDED.scope_filter, granted_by=EXCLUDED.granted_by, granted_at=now(), revoked_at=NULL`,
				pluginID, p.PermType, p.PermTarget, nullableJSON(p.ScopeFilter), byUser); err != nil {
				return fmt.Errorf("写入授权: %w", err)
			}
		}
		return nil
	})
}

// HasGrant 检查某项声明是否已获批准；未批准一律拒绝。
func (r *Registry) HasGrant(ctx context.Context, pluginID, permType, permTarget string) (bool, error) {
	var n int
	err := r.st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM kernel_plugin_grants
		 WHERE plugin_id=$1 AND perm_type=$2 AND perm_target=$3 AND revoked_at IS NULL`,
		pluginID, permType, permTarget).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("查询授权: %w", err)
	}
	return n > 0, nil
}

// Subscribe 登记订阅并返回 Consumer Group；事件类型必须已获 subscribe 授权。
func (r *Registry) Subscribe(ctx context.Context, pluginID string, eventTypes []string, scope json.RawMessage, byUser string) (string, error) {
	if byUser != config.UserBot {
		return "", fmt.Errorf("%w: 只有 user_bot 能批准订阅", ErrNotAuthorized)
	}
	if len(eventTypes) == 0 {
		return "", fmt.Errorf("%w: 订阅事件类型不能为空", ErrNotAuthorized)
	}
	groupID := "cg_" + pluginID
	err := r.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		for _, et := range eventTypes {
			ok, err := r.HasGrant(ctx, pluginID, "subscribe", et)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: 插件 %q 未获订阅 %q 的授权", ErrNotAuthorized, pluginID, et)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO kernel_plugin_subs (consumer_group_id, plugin_id, scope_filter, status)
			VALUES ($1,$2,$3,'active')
			ON CONFLICT (consumer_group_id) DO UPDATE SET status='active', stopped_at=NULL, scope_filter=EXCLUDED.scope_filter`,
			groupID, pluginID, nullableJSON(scope)); err != nil {
			return fmt.Errorf("登记 Consumer Group: %w", err)
		}
		for _, et := range eventTypes {
			if _, err := tx.Exec(ctx, `
				INSERT INTO kernel_event_subscriptions (consumer_group_id, plugin_id, event_type, scope_filter, granted_by_user)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (consumer_group_id, event_type) DO UPDATE SET scope_filter=EXCLUDED.scope_filter, granted_by_user=EXCLUDED.granted_by_user`,
				groupID, pluginID, et, nullableJSON(scope), byUser); err != nil {
				return fmt.Errorf("登记订阅 %s: %w", et, err)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return groupID, nil
}

// RegisterPlugin 登记插件身份与 manifest 摘要；namespace 归属只在内核侧登记，不涉及任何知识数据。
func (r *Registry) RegisterPlugin(ctx context.Context, pluginID, pluginType, displayName, namespace string, manifestDigest string, manifest json.RawMessage) error {
	if ReservedBotNames[pluginID] {
		return fmt.Errorf("%w: %q", ErrReservedName, pluginID)
	}
	if namespace == "" {
		namespace = strings.TrimPrefix(pluginID, "plugin_")
	}
	_, err := r.st.Pool().Exec(ctx, `
		INSERT INTO kernel_plugins (plugin_id, plugin_type, display_name, namespace, manifest_digest, manifest_json)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb)
		ON CONFLICT (plugin_id) DO UPDATE SET manifest_digest=EXCLUDED.manifest_digest,
		     manifest_json=EXCLUDED.manifest_json, display_name=EXCLUDED.display_name, updated_at=now()`,
		pluginID, pluginType, displayName, namespace, manifestDigest, string(manifest))
	if err != nil {
		return fmt.Errorf("登记插件身份: %w", err)
	}
	return nil
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
