package config

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// 内核侧四角色：保留名，任何插件不得注册同名 bot。
const (
	UserBot     = "user_bot"
	CreateBot   = "create_bot"
	MonitorBot  = "monitor_bot"
	SecurityBot = "security_bot"
)

// ActorKind 区分调用主体类型，读取 ACL 与审批权限都据此判定。
const (
	KindKernelRole = "kernel_role"
	KindBot        = "bot"
	KindPlugin     = "plugin"
	KindAgent      = "agent"
)

type Actor struct {
	Kind string
	ID   string
}

func KernelRole(id string) Actor  { return Actor{Kind: KindKernelRole, ID: id} }
func BotActor(id string) Actor    { return Actor{Kind: KindBot, ID: id} }
func PluginActor(id string) Actor { return Actor{Kind: KindPlugin, ID: id} }
func AgentActor(id string) Actor  { return Actor{Kind: KindAgent, ID: id} }

var (
	ErrNotFound      = errors.New("配置文档不存在")
	ErrForbidden     = errors.New("配置访问被拒绝")
	ErrConflict      = errors.New("配置并发冲突")
	ErrInvalidSpec   = errors.New("配置内容非法")
	ErrBadTransition = errors.New("配置状态迁移非法")
)

// CausalContext 是激活事件必须继承的因果头（硬约束：所有消息携带完整因果链）。
type CausalContext struct {
	CorrelationID  string
	CausationID    string
	RootEnvelopeID string
}

type Document struct {
	DocID              string
	ScopeType          string
	ScopeID            string
	ConfigKey          string
	Version            int64
	Status             string
	Spec               json.RawMessage
	SchemaVersion      int
	Checksum           string
	BaseVersion        *int64
	CreatedBy          string
	ApprovedBy         *string
	ApprovedChecksum   *string
	SecurityDecisionID *int64
	CreatedAt          time.Time
	ApprovedAt         *time.Time
	ActivatedAt        *time.Time
	RetiredAt          *time.Time
}

type VersionSummary struct {
	Version   int64
	Status    string
	Checksum  string
	CreatedBy string
	CreatedAt time.Time
}

type Module struct {
	st *store.Store
}

func New(st *store.Store) *Module { return &Module{st: st} }

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func newID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("生成配置 ID 失败: %v", err))
	}
	return prefix + hex.EncodeToString(b)
}

func validScope(scopeType, scopeID string) error {
	switch scopeType {
	case ScopePlugin, ScopeBot, ScopeAgent:
		if scopeID == "" {
			return fmt.Errorf("%w: scope_id 不能为空", ErrInvalidSpec)
		}
	case ScopeGlobal:
		if scopeID != GlobalScopeID {
			return fmt.Errorf("%w: global 域 scope_id 必须为 %q", ErrInvalidSpec, GlobalScopeID)
		}
	default:
		return fmt.Errorf("%w: 非法 scope_type %q（只允许 plugin/bot/agent/global）", ErrInvalidSpec, scopeType)
	}
	return nil
}

// RequiresSecurityReview 判定可执行类变更（ConfigCenter §4.2）：
// plugin 涉及工具 / 权限，agent 涉及工具白名单。
func RequiresSecurityReview(scopeType string, spec any) bool {
	m, ok := spec.(map[string]any)
	if !ok {
		return false
	}
	switch scopeType {
	case ScopePlugin:
		for _, k := range []string{"tools", "requiring_tools", "permissions", "grants"} {
			if _, hit := m[k]; hit {
				return true
			}
		}
	case ScopeAgent:
		for _, k := range []string{"tools", "tool_whitelist", "allowed_tools"} {
			if _, hit := m[k]; hit {
				return true
			}
		}
	}
	return false
}

// Propose 提交提案：提案不生效、不进入运行投影（ConfigCenter §4.1）。
func (m *Module) Propose(ctx context.Context, actor Actor, scopeType, scopeID, configKey string, specRaw json.RawMessage, baseVersion int64) (string, error) {
	if configKey == "" {
		configKey = DefaultConfigKey
	}
	if err := validScope(scopeType, scopeID); err != nil {
		return "", err
	}
	spec, err := DecodeSpec(specRaw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidSpec, err)
	}
	if err := ScanSecrets(spec); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidSpec, err)
	}
	checksum, err := Checksum(spec)
	if err != nil {
		return "", err
	}
	canonical, err := CanonicalJSON(spec)
	if err != nil {
		return "", err
	}
	review := RequiresSecurityReview(scopeType, spec)
	status := StatusProposed
	if review {
		status = StatusSecurityReview
	}

	var docID string
	err = m.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		activeVersion, err := activeVersionOf(ctx, tx, scopeType, scopeID, configKey)
		if err != nil {
			return err
		}
		if baseVersion != activeVersion {
			return fmt.Errorf("%w: base_version=%d 与当前 active 版本 %d 不一致，请重新提案", ErrConflict, baseVersion, activeVersion)
		}
		nextVersion, err := nextVersionOf(ctx, tx, scopeType, scopeID, configKey)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO kernel_config_documents
			  (scope_type, scope_id, config_key, version, status, spec, schema_version,
			   checksum, base_version, created_by)
			VALUES ($1,$2,$3,$4,$5,$6::jsonb,1,$7,$8,$9)
			RETURNING doc_id`,
			scopeType, scopeID, configKey, nextVersion, status, string(canonical),
			checksum, baseVersion, actor.ID).Scan(&docID); err != nil {
			return fmt.Errorf("写入配置提案: %w", err)
		}
		return writeAudit(ctx, tx, docID, "propose", actor.ID, "", status, checksum, "")
	})
	if err != nil {
		return "", err
	}
	return docID, nil
}

// Review 是 security_bot 的预审：只给 approved / rejected 意见，不能批准或激活。
func (m *Module) Review(ctx context.Context, actor Actor, proposalID, decision, reason string) error {
	if actor.ID != SecurityBot {
		return fmt.Errorf("%w: 只有 security_bot 能预审，收到 %q", ErrForbidden, actor.ID)
	}
	if decision != "approved" && decision != "rejected" {
		return fmt.Errorf("%w: 预审决定只能是 approved / rejected", ErrInvalidSpec)
	}
	return m.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		doc, err := lockDoc(ctx, tx, proposalID)
		if err != nil {
			return err
		}
		if doc.Status != StatusSecurityReview {
			return fmt.Errorf("%w: 文档状态 %s 不处于 security_review", ErrBadTransition, doc.Status)
		}
		var decisionID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO kernel_security_decisions
			  (subject_type, subject_id, action, decision, requested_by, decided_by, reason, checksum, correlation_id)
			VALUES ('config_proposal',$1,'review_config',$2,$3,'security_bot',$4,$5,$6)
			RETURNING decision_id`,
			proposalID, decision, doc.CreatedBy, reason, doc.Checksum, "").Scan(&decisionID); err != nil {
			return fmt.Errorf("写入安全预审决定: %w", err)
		}
		toStatus := StatusProposed
		if decision == "rejected" {
			toStatus = StatusRejected
		}
		if _, err := tx.Exec(ctx, `
			UPDATE kernel_config_documents SET status=$2, security_decision_id=$3 WHERE doc_id=$1`,
			proposalID, toStatus, decisionID); err != nil {
			return fmt.Errorf("更新提案状态: %w", err)
		}
		return writeAudit(ctx, tx, proposalID, "security_review", actor.ID, doc.Status, toStatus, doc.Checksum, reason)
	})
}

// Approve 只有 user_bot 能调用，且必须绑定 checksum（ConfigCenter §4.3）。
func (m *Module) Approve(ctx context.Context, actor Actor, proposalID, checksum string) error {
	if actor.ID != UserBot {
		return fmt.Errorf("%w: 只有 user_bot 能批准配置，收到 %q", ErrForbidden, actor.ID)
	}
	if checksum == "" {
		return fmt.Errorf("%w: 批准必须携带 checksum", ErrInvalidSpec)
	}
	return m.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		doc, err := lockDoc(ctx, tx, proposalID)
		if err != nil {
			return err
		}
		if doc.Status != StatusProposed {
			return fmt.Errorf("%w: 文档状态 %s 不可批准（可执行类须先过 security_review）", ErrBadTransition, doc.Status)
		}
		if doc.Checksum != checksum {
			return fmt.Errorf("%w: 批准 checksum 与文档不一致，批准失效", ErrConflict)
		}
		if err := requireSecurityApproved(ctx, tx, doc); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE kernel_config_documents
			   SET status='approved', approved_by='user_bot', approved_checksum=$2, approved_at=now()
			 WHERE doc_id=$1`, proposalID, checksum); err != nil {
			return fmt.Errorf("写入批准: %w", err)
		}
		return writeAudit(ctx, tx, proposalID, "approve", actor.ID, doc.Status, StatusApproved, checksum, "")
	})
}

// Reject 拒绝提案；user_bot 与 security_bot 均可。
func (m *Module) Reject(ctx context.Context, actor Actor, proposalID, reason string) error {
	if actor.ID != UserBot && actor.ID != SecurityBot {
		return fmt.Errorf("%w: 只有 user_bot / security_bot 能拒绝提案", ErrForbidden)
	}
	return m.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		doc, err := lockDoc(ctx, tx, proposalID)
		if err != nil {
			return err
		}
		switch doc.Status {
		case StatusProposed, StatusSecurityReview, StatusApproved:
		default:
			return fmt.Errorf("%w: 文档状态 %s 不可拒绝", ErrBadTransition, doc.Status)
		}
		if _, err := tx.Exec(ctx, `UPDATE kernel_config_documents SET status='rejected' WHERE doc_id=$1`, proposalID); err != nil {
			return fmt.Errorf("拒绝提案: %w", err)
		}
		return writeAudit(ctx, tx, proposalID, "reject", actor.ID, doc.Status, StatusRejected, doc.Checksum, reason)
	})
}

// Activate 在单事务内切 active + 更新投影 + 写 outbox + 写审计；失败整体回滚，不出现半生效。
func (m *Module) Activate(ctx context.Context, actor Actor, proposalID string, cc CausalContext) error {
	if cc.CorrelationID == "" || cc.RootEnvelopeID == "" {
		return fmt.Errorf("%w: 激活必须携带完整因果链", ErrInvalidSpec)
	}
	return m.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		doc, err := lockDoc(ctx, tx, proposalID)
		if err != nil {
			return err
		}
		if doc.Status != StatusApproved {
			return fmt.Errorf("%w: 只有 approved 文档可激活，当前 %s", ErrBadTransition, doc.Status)
		}
		if doc.ApprovedChecksum == nil || *doc.ApprovedChecksum != doc.Checksum {
			return fmt.Errorf("%w: approved_checksum 与文档 checksum 不一致（批准后内容被篡改）", ErrConflict)
		}
		if err := requireSecurityApproved(ctx, tx, doc); err != nil {
			return err
		}
		activeVersion, err := activeVersionOf(ctx, tx, doc.ScopeType, doc.ScopeID, doc.ConfigKey)
		if err != nil {
			return err
		}
		base := int64(0)
		if doc.BaseVersion != nil {
			base = *doc.BaseVersion
		}
		if base != activeVersion {
			return fmt.Errorf("%w: base_version=%d 与当前 active 版本 %d 不一致，请重新提案", ErrConflict, base, activeVersion)
		}
		spec, err := DecodeSpec(doc.Spec)
		if err != nil {
			return err
		}
		if err := requireSecretRefsExist(ctx, tx, spec); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE kernel_config_documents SET status='retired', retired_at=now()
			 WHERE scope_type=$1 AND scope_id=$2 AND config_key=$3 AND status='active'`,
			doc.ScopeType, doc.ScopeID, doc.ConfigKey); err != nil {
			return fmt.Errorf("退役旧 active 版本: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE kernel_config_documents SET status='active', activated_at=now() WHERE doc_id=$1`, proposalID); err != nil {
			return fmt.Errorf("激活新版本: %w", err)
		}
		if err := projectActivation(ctx, tx, doc, spec); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{
			"scope_type": doc.ScopeType, "scope_id": doc.ScopeID, "config_key": doc.ConfigKey,
			"version": doc.Version, "checksum": doc.Checksum,
		})
		if err != nil {
			return fmt.Errorf("序列化配置变更事件: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO kernel_event_outbox
			  (event_id, event_type, correlation_id, causation_id, root_envelope_id,
			   aggregate_type, aggregate_id, payload_json, status)
			VALUES ($1,'kernel.config.changed',$2,$3,$4,'config',$5,$6::jsonb,'pending')`,
			newID("evt_"), cc.CorrelationID, nullable(cc.CausationID), cc.RootEnvelopeID,
			proposalID, string(payload)); err != nil {
			return fmt.Errorf("写入配置变更事件: %w", err)
		}
		if err := writeAudit(ctx, tx, proposalID, "activate", actor.ID, doc.Status, StatusActive, doc.Checksum, ""); err != nil {
			return err
		}
		return store.NotifyTx(ctx, tx, "kernel_config_changed", doc.ScopeType+":"+doc.ScopeID+":"+doc.ConfigKey)
	})
}

// Get 按读取 ACL 返回文档；每次读取写审计。monitor_bot 只拿版本 / checksum / 状态，不拿 spec 正文。
func (m *Module) Get(ctx context.Context, actor Actor, scopeType, scopeID, configKey string, version int64) (*Document, error) {
	if configKey == "" {
		configKey = DefaultConfigKey
	}
	if err := validScope(scopeType, scopeID); err != nil {
		return nil, err
	}
	var doc *Document
	err := m.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if version > 0 {
			doc, err = docByVersion(ctx, tx, scopeType, scopeID, configKey, version)
		} else {
			doc, err = activeDoc(ctx, tx, scopeType, scopeID, configKey)
		}
		if err != nil {
			return err
		}
		if err := checkReadACL(actor, doc); err != nil {
			return err
		}
		if actor.Kind == KindKernelRole && actor.ID == MonitorBot {
			doc.Spec = nil // 只读版本 / checksum / 状态
		}
		return writeAudit(ctx, tx, doc.DocID, "read", actor.ID, doc.Status, doc.Status, doc.Checksum, "")
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func (m *Module) ListVersions(ctx context.Context, actor Actor, scopeType, scopeID, configKey string) ([]VersionSummary, error) {
	if configKey == "" {
		configKey = DefaultConfigKey
	}
	if err := validScope(scopeType, scopeID); err != nil {
		return nil, err
	}
	if !canListVersions(actor, scopeType, scopeID) {
		return nil, fmt.Errorf("%w: %s 无权列出 %s/%s 的版本", ErrForbidden, actor.ID, scopeType, scopeID)
	}
	rows, err := m.st.Pool().Query(ctx, `
		SELECT version, status, checksum, created_by, created_at
		  FROM kernel_config_documents
		 WHERE scope_type=$1 AND scope_id=$2 AND config_key=$3
		 ORDER BY version DESC`, scopeType, scopeID, configKey)
	if err != nil {
		return nil, fmt.Errorf("列出配置版本: %w", err)
	}
	defer rows.Close()
	var out []VersionSummary
	for rows.Next() {
		var v VersionSummary
		if err := rows.Scan(&v.Version, &v.Status, &v.Checksum, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("读取版本摘要: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListPending 供 user_bot / CLI 查看待审批提案。
func (m *Module) ListPending(ctx context.Context, actor Actor, scopeType string) ([]Document, error) {
	if actor.ID != UserBot && actor.ID != SecurityBot {
		return nil, fmt.Errorf("%w: 只有 user_bot / security_bot 能查看待审批提案", ErrForbidden)
	}
	q := `SELECT doc_id, scope_type, scope_id, config_key, version, status, spec, schema_version,
	             checksum, base_version, created_by, approved_by, approved_checksum, security_decision_id,
	             created_at, approved_at, activated_at, retired_at
	        FROM kernel_config_documents
	       WHERE status IN ('proposed','security_review','approved')`
	args := []any{}
	if scopeType != "" {
		q += ` AND scope_type=$1`
		args = append(args, scopeType)
	}
	q += ` ORDER BY created_at`
	rows, err := m.st.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("列出待审批提案: %w", err)
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// List 供查看当前 active 的配置文档列表（受读取 ACL 约束）。
func (m *Module) List(ctx context.Context, actor Actor, scopeType string) ([]Document, error) {
	q := `SELECT doc_id, scope_type, scope_id, config_key, version, status, spec, schema_version,
	             checksum, base_version, created_by, approved_by, approved_checksum, security_decision_id,
	             created_at, approved_at, activated_at, retired_at
	        FROM kernel_config_documents
	       WHERE status = 'active'`
	args := []any{}
	if scopeType != "" {
		q += ` AND scope_type=$1`
		args = append(args, scopeType)
	}
	q += ` ORDER BY scope_type, scope_id`
	rows, err := m.st.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("列出 active 配置: %w", err)
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		if err := checkReadACL(actor, d); err == nil {
			out = append(out, *d)
		}
	}
	return out, rows.Err()
}

// GetProposal 根据 doc_id 读取提案文档。
func (m *Module) GetProposal(ctx context.Context, actor Actor, proposalID string) (*Document, error) {
	if actor.ID != UserBot && actor.ID != SecurityBot {
		return nil, fmt.Errorf("%w: 只有 user_bot / security_bot 能查看提案详情", ErrForbidden)
	}
	row := m.st.Pool().QueryRow(ctx, `
		SELECT doc_id, scope_type, scope_id, config_key, version, status, spec, schema_version,
		       checksum, base_version, created_by, approved_by, approved_checksum, security_decision_id,
		       created_at, approved_at, activated_at, retired_at
		  FROM kernel_config_documents WHERE doc_id=$1`, proposalID)
	doc, err := scanDoc(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: proposal_id %s", ErrNotFound, proposalID)
	}
	return doc, err
}

// Diff 返回提案与 base 版本（或 active 版本）的差异对比。
func (m *Module) Diff(ctx context.Context, actor Actor, proposalID string) (map[string]any, error) {
	prop, err := m.GetProposal(ctx, actor, proposalID)
	if err != nil {
		return nil, err
	}
	var baseDoc *Document
	if prop.BaseVersion != nil && *prop.BaseVersion > 0 {
		baseDoc, _ = docByVersion(ctx, m.st.Pool(), prop.ScopeType, prop.ScopeID, prop.ConfigKey, *prop.BaseVersion)
	}
	if baseDoc == nil {
		baseDoc, _ = activeDoc(ctx, m.st.Pool(), prop.ScopeType, prop.ScopeID, prop.ConfigKey)
	}
	res := map[string]any{
		"proposal": prop,
		"checksum": prop.Checksum,
	}
	if baseDoc != nil {
		res["base"] = baseDoc
	}
	return res, nil
}

// AddSecret 在 kernel_secret_refs 登记新的凭据引用（只存引用定位，不存明文值）。
func (m *Module) AddSecret(ctx context.Context, actor Actor, provider, locator, description string) (string, error) {
	if actor.ID != UserBot {
		return "", fmt.Errorf("%w: 只有 user_bot 能登记 secret_ref", ErrForbidden)
	}
	if provider != "env" && provider != "file" && provider != "keyring" {
		return "", fmt.Errorf("provider 必须是 env / file / keyring，收到 %q", provider)
	}
	if locator == "" {
		return "", errors.New("locator 不能为空")
	}
	refID := newID("sec_")
	_, err := m.st.Pool().Exec(ctx, `
		INSERT INTO kernel_secret_refs (ref_id, provider, locator, description, created_by)
		VALUES ($1, $2, $3, $4, $5)`, refID, provider, locator, description, actor.ID)
	if err != nil {
		return "", fmt.Errorf("登记 secret_ref: %w", err)
	}
	return refID, nil
}

// Rollback 生成新提案（spec 取旧版本），仍走 review/approve/activate，保证审计线性。
func (m *Module) Rollback(ctx context.Context, actor Actor, scopeType, scopeID, configKey string, toVersion int64) (string, error) {
	if configKey == "" {
		configKey = DefaultConfigKey
	}
	old, err := m.Get(ctx, UserBotActor(), scopeType, scopeID, configKey, toVersion)
	if err != nil {
		return "", err
	}
	active, err := activeVersionOf(ctx, m.st.Pool(), scopeType, scopeID, configKey)
	if err != nil {
		return "", err
	}
	return m.Propose(ctx, actor, scopeType, scopeID, configKey, old.Spec, active)
}

// UserBotActor 是 CLI 作为 user_bot 投递代理时的主体。
func UserBotActor() Actor { return KernelRole(UserBot) }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func activeVersionOf(ctx context.Context, q querier, scopeType, scopeID, configKey string) (int64, error) {
	var v int64
	err := q.QueryRow(ctx, `
		SELECT version FROM kernel_config_documents
		 WHERE scope_type=$1 AND scope_id=$2 AND config_key=$3 AND status='active'`,
		scopeType, scopeID, configKey).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("读取 active 配置版本: %w", err)
	}
	return v, nil
}

func nextVersionOf(ctx context.Context, q querier, scopeType, scopeID, configKey string) (int64, error) {
	var v int64
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(MAX(version),0)+1 FROM kernel_config_documents
		 WHERE scope_type=$1 AND scope_id=$2 AND config_key=$3`,
		scopeType, scopeID, configKey).Scan(&v); err != nil {
		return 0, fmt.Errorf("计算下一配置版本: %w", err)
	}
	return v, nil
}

const docColumns = `doc_id, scope_type, scope_id, config_key, version, status, spec, schema_version,
	checksum, base_version, created_by, approved_by, approved_checksum, security_decision_id,
	created_at, approved_at, activated_at, retired_at`

func scanDoc(row pgx.Row) (*Document, error) {
	var d Document
	var spec []byte
	err := row.Scan(&d.DocID, &d.ScopeType, &d.ScopeID, &d.ConfigKey, &d.Version, &d.Status, &spec,
		&d.SchemaVersion, &d.Checksum, &d.BaseVersion, &d.CreatedBy, &d.ApprovedBy, &d.ApprovedChecksum,
		&d.SecurityDecisionID, &d.CreatedAt, &d.ApprovedAt, &d.ActivatedAt, &d.RetiredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置文档: %w", err)
	}
	d.Spec = json.RawMessage(spec)
	return &d, nil
}

func lockDoc(ctx context.Context, tx pgx.Tx, docID string) (*Document, error) {
	return scanDoc(tx.QueryRow(ctx, `SELECT `+docColumns+` FROM kernel_config_documents WHERE doc_id=$1 FOR UPDATE`, docID))
}

func activeDoc(ctx context.Context, q querier, scopeType, scopeID, configKey string) (*Document, error) {
	return scanDoc(q.QueryRow(ctx, `SELECT `+docColumns+` FROM kernel_config_documents
		WHERE scope_type=$1 AND scope_id=$2 AND config_key=$3 AND status='active'`, scopeType, scopeID, configKey))
}

func docByVersion(ctx context.Context, q querier, scopeType, scopeID, configKey string, version int64) (*Document, error) {
	return scanDoc(q.QueryRow(ctx, `SELECT `+docColumns+` FROM kernel_config_documents
		WHERE scope_type=$1 AND scope_id=$2 AND config_key=$3 AND version=$4`, scopeType, scopeID, configKey, version))
}

func writeAudit(ctx context.Context, tx pgx.Tx, docID, action, actor, fromStatus, toStatus, checksum, reason string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO kernel_config_audit (doc_id, action, actor, from_status, to_status, checksum, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		docID, action, actor, nullable(fromStatus), nullable(toStatus), nullable(checksum), nullable(reason)); err != nil {
		return fmt.Errorf("写入配置审计: %w", err)
	}
	return nil
}

// requireSecurityApproved 强制可执行类必须有 security_bot 的 approved 决定。
func requireSecurityApproved(ctx context.Context, tx pgx.Tx, doc *Document) error {
	spec, err := DecodeSpec(doc.Spec)
	if err != nil {
		return err
	}
	if !RequiresSecurityReview(doc.ScopeType, spec) {
		return nil
	}
	if doc.SecurityDecisionID == nil {
		return fmt.Errorf("%w: 可执行类配置缺少 security_bot 预审决定", ErrForbidden)
	}
	var decision string
	if err := tx.QueryRow(ctx, `SELECT decision FROM kernel_security_decisions WHERE decision_id=$1`,
		*doc.SecurityDecisionID).Scan(&decision); err != nil {
		return fmt.Errorf("读取安全预审决定: %w", err)
	}
	if decision != "approved" {
		return fmt.Errorf("%w: 安全预审决定为 %s，不可批准/激活", ErrForbidden, decision)
	}
	return nil
}

func requireSecretRefsExist(ctx context.Context, tx pgx.Tx, spec any) error {
	refs, err := CollectSecretRefs(spec)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		var deprecated *time.Time
		err := tx.QueryRow(ctx, `SELECT deprecated_at FROM kernel_secret_refs WHERE ref_id=$1`, ref).Scan(&deprecated)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: 配置引用的 secret_ref %q 不存在", ErrInvalidSpec, ref)
		}
		if err != nil {
			return fmt.Errorf("校验 secret_ref: %w", err)
		}
		if deprecated != nil {
			return fmt.Errorf("%w: secret_ref %q 已废弃，请先解除引用", ErrInvalidSpec, ref)
		}
	}
	return nil
}

// projectActivation 在激活事务内更新运行投影；投影只存 config_version 与必要字段。
func projectActivation(ctx context.Context, tx pgx.Tx, doc *Document, spec any) error {
	m, _ := spec.(map[string]any)
	switch doc.ScopeType {
	case ScopeBot:
		mode := strOf(m, "activation_mode", "lazy")
		idle := intOf(m, "idle_ttl_seconds", 300)
		timeout := intOf(m, "start_timeout_seconds", 30)
		tag, err := tx.Exec(ctx, `
			UPDATE bot_manifest
			   SET activation_mode=$2, idle_ttl_seconds=$3, start_timeout_seconds=$4,
			       prompt_ref=$5, model=$6, config_version=$7, config_doc_id=$8, updated_at=now()
			 WHERE bot_id=$1`,
			doc.ScopeID, mode, idle, timeout, nullable(strOf(m, "prompt_ref", "")),
			nullable(strOf(m, "model", "")), doc.Version, doc.DocID)
		if err != nil {
			return fmt.Errorf("投影 bot_manifest: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: bot %q 未注册，无法投影配置", ErrNotFound, doc.ScopeID)
		}
		if _, err := tx.Exec(ctx, `UPDATE bots SET config_version=$2, updated_at=now() WHERE bot_id=$1`,
			doc.ScopeID, doc.Version); err != nil {
			return fmt.Errorf("投影 bots: %w", err)
		}
	case ScopePlugin:
		if _, err := tx.Exec(ctx, `
			UPDATE kernel_plugins SET config_version=$2, config_doc_id=$3, updated_at=now() WHERE plugin_id=$1`,
			doc.ScopeID, doc.Version, doc.DocID); err != nil {
			return fmt.Errorf("投影 kernel_plugins: %w", err)
		}
	case ScopeAgent:
		if _, err := tx.Exec(ctx, `
			UPDATE kernel_agents SET config_version=$2, config_doc_id=$3, updated_at=now() WHERE agent_id=$1`,
			doc.ScopeID, doc.Version, doc.DocID); err != nil {
			return fmt.Errorf("投影 kernel_agents: %w", err)
		}
	case ScopeGlobal:
		// global 无运行投影，hub 各模块直接读 active 文档
	}
	return nil
}

func strOf(m map[string]any, key, def string) string {
	if m == nil {
		return def
	}
	if s, ok := m[key].(string); ok && s != "" {
		return s
	}
	return def
}

func intOf(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	if n, ok := m[key].(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return def
}

// checkReadACL 实现 ConfigCenter §5.2 的读取范围。
func checkReadACL(actor Actor, doc *Document) error {
	if actor.ID == UserBot {
		return nil // user_bot 拥有全量读取权限，含 pending
	}
	if actor.Kind == KindKernelRole {
		switch actor.ID {
		case UserBot:
			return nil // 全部，含 pending
		case CreateBot:
			if doc.Status == StatusActive && (doc.ScopeType == ScopeBot || doc.ScopeType == ScopePlugin) {
				return nil
			}
		case MonitorBot:
			return nil // 只读版本 / checksum / 状态，spec 由 Get 剥离
		case SecurityBot:
			if doc.Status == StatusActive || doc.Status == StatusProposed || doc.Status == StatusSecurityReview {
				return nil
			}
		}
		return fmt.Errorf("%w: %s 无权读取 %s/%s 状态 %s", ErrForbidden, actor.ID, doc.ScopeType, doc.ScopeID, doc.Status)
	}
	switch actor.Kind {
	case KindBot:
		if doc.ScopeType == ScopeBot && doc.ScopeID == actor.ID && doc.Status == StatusActive {
			return nil
		}
	case KindPlugin:
		if doc.ScopeType == ScopePlugin && doc.ScopeID == actor.ID && doc.Status == StatusActive {
			return nil
		}
	case KindAgent:
		if doc.ScopeType == ScopeAgent && doc.ScopeID == actor.ID && doc.Status == StatusActive {
			return nil
		}
	}
	if doc.ScopeType == ScopeGlobal && doc.Status == StatusActive {
		return nil // global 公开子集
	}
	return fmt.Errorf("%w: %s(%s) 无权读取 %s/%s", ErrForbidden, actor.ID, actor.Kind, doc.ScopeType, doc.ScopeID)
}

func canListVersions(actor Actor, scopeType, scopeID string) bool {
	if actor.Kind == KindKernelRole {
		switch actor.ID {
		case UserBot, SecurityBot, MonitorBot:
			return true
		case CreateBot:
			return scopeType == ScopeBot || scopeType == ScopePlugin
		}
		return false
	}
	switch actor.Kind {
	case KindBot:
		return scopeType == ScopeBot && scopeID == actor.ID
	case KindPlugin:
		return scopeType == ScopePlugin && scopeID == actor.ID
	case KindAgent:
		return scopeType == ScopeAgent && scopeID == actor.ID
	}
	return false
}
