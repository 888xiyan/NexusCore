package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/ingress"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/hub/supervisor"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/osproc"
	"github.com/jackc/pgx/v5"
)

var (
	pluginIDPattern = regexp.MustCompile(`^plugin_[a-z0-9_]{3,24}$`)
	botIDPattern    = regexp.MustCompile(`^[a-z0-9_]{5,64}$`)
)

var (
	ErrInvalidManifest  = errors.New("插件清单非法")
	ErrPluginNotFound   = errors.New("插件不存在")
	ErrDecisionNotFound = errors.New("安全审查决定不存在")
	ErrConfirmRequired  = errors.New("强制终止需要 --confirm 确认")
)

type BotTemplate struct {
	BotID           string   `json:"bot_id"`
	Role            string   `json:"role,omitempty"`
	DisplayName     string   `json:"display_name"`
	ActivationMode  string   `json:"activation_mode"` // lazy / resident
	Capabilities    []string `json:"capabilities"`
	IdleTTLSeconds  int      `json:"idle_ttl_seconds,omitempty"`
	StartTimeoutSec int      `json:"start_timeout_seconds,omitempty"`
	PromptRef       string   `json:"prompt_ref,omitempty"`
	Model           string   `json:"model,omitempty"`
	MemoryProfile   string   `json:"memory_profile,omitempty"`
}

type Manifest struct {
	PluginID       string           `json:"plugin_id"`
	Name           string           `json:"name"`
	Version        string           `json:"version"`
	PluginType     string           `json:"plugin_type"`               // ingress, workspace, executor, knowledge
	PluginCategory string           `json:"plugin_category,omitempty"` // foundation, business, tool, theme, bot_pack；空值保持旧清单合法
	Description    string           `json:"description,omitempty"`
	Entry          string           `json:"entry,omitempty"`
	Binary         string           `json:"binary,omitempty"`
	BinaryDigest   string           `json:"binary_digest,omitempty"`
	Subscribing    []string         `json:"subscribing,omitempty"`
	Publishing     []string         `json:"publishing,omitempty"`
	ProvidingTools []string         `json:"providing_tools,omitempty"`
	RequiringTools []string         `json:"requiring_tools,omitempty"`
	BotTemplates   []BotTemplate    `json:"bot_templates"`
	ResourceLimits map[string]int   `json:"resource_limits,omitempty"`
	RequiredTables []string         `json:"required_tables,omitempty"`
	Deps           []string         `json:"deps,omitempty"`
	Dependencies   []DependencySpec `json:"dependencies,omitempty"`
	UI             *UIManifest      `json:"ui,omitempty"`
	SchemaVersion  int              `json:"schema_version"`
}

type UIManifest struct {
	Entry  string          `json:"entry,omitempty"`
	Menu   *UIMenuManifest `json:"menu,omitempty"`
	Routes []UIRouteItem   `json:"routes,omitempty"`
}

type UIMenuManifest struct {
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
	Path  string `json:"path"`
	Order int    `json:"order,omitempty"`
}

type UIRouteItem struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Entry string `json:"entry,omitempty"`
}

// SecurityDecision 表示 kernel_security_decisions 中的记录
type SecurityDecision struct {
	DecisionID  int64     `json:"decision_id"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	Action      string    `json:"action"`
	Decision    string    `json:"decision"`
	RequestedBy string    `json:"requested_by"`
	DecidedBy   string    `json:"decided_by"`
	Reason      string    `json:"reason,omitempty"`
	Checksum    string    `json:"checksum,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ProcessReport 由 owner bot 上报其持有的插件进程 PID 与状态
type ProcessReport struct {
	Envelope       envelope.Envelope `json:"envelope"`
	OwnerPID       int               `json:"owner_pid"`
	OwnerStartTime int64             `json:"owner_start_time"`
	PluginID       string            `json:"plugin_id"`
	OwnerBotID     string            `json:"owner_bot_id"`
	PID            int               `json:"pid"`
	PGID           int               `json:"pgid"`
	PIDStartTime   int64             `json:"pid_start_time"`
	BinaryDigest   string            `json:"binary_digest"`
	ConfigVersion  int64             `json:"config_version"`
	State          string            `json:"state"`
	LastError      string            `json:"last_error,omitempty"`
}

// PluginProcessInfo 是 kernel_plugin_processes 的状态摘要
type PluginProcessInfo struct {
	ProcessID       string     `json:"process_id"`
	PluginID        string     `json:"plugin_id"`
	OwnerBotID      string     `json:"owner_bot_id"`
	OwnerInstanceID string     `json:"owner_instance_id"`
	PID             int        `json:"pid"`
	PGID            int        `json:"pgid"`
	PIDStartTime    int64      `json:"pid_start_time"`
	BinaryDigest    string     `json:"binary_digest"`
	ConfigVersion   int64      `json:"config_version"`
	State           string     `json:"state"`
	StopMode        *string    `json:"stop_mode,omitempty"`
	StartedAt       time.Time  `json:"started_at"`
	StoppedAt       *time.Time `json:"stopped_at,omitempty"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	LastError       *string    `json:"last_error,omitempty"`
}

type ControlRequest struct {
	Envelope   envelope.Envelope `json:"envelope"`
	Credential string            `json:"-"`
}

type Manager struct {
	st          *store.Store
	cfg         *config.Module
	sup         *supervisor.Supervisor
	reg         *registry.Registry
	ing         *ingress.Ingress
	lifecycleMu sync.Mutex

	// 内存模式与治理缓存 (支持零 PG 自适应测试与接口索引)
	providerIndex *InterfaceProviderIndex
	memPlugins    map[string]*Manifest
	memStatuses   map[string]string // plugin_id -> status ("installed", "enabled", "disabled", "uninstalled")
	memDecisions  []SecurityDecision
	memProcesses  map[string]PluginProcessInfo
	memAudits     []LifecycleRecord
	decisionSeq   int64
}

func (m *Manager) SetIngress(ing *ingress.Ingress) { m.ing = ing }

// ProviderIndex 返回内存中的接口提供者索引
func (m *Manager) ProviderIndex() *InterfaceProviderIndex {
	if m.providerIndex == nil {
		m.providerIndex = NewInterfaceProviderIndex()
	}
	return m.providerIndex
}

// BuildSystemDAG 收集当前已安装或已启用的插件并生成全局依赖 DAG。
// 数据库模式下从 kernel_plugins 读取清单，供卸载时的反向依赖判断使用。
func (m *Manager) BuildSystemDAG() (*DependencyDAG, error) {
	activePlugins := make(map[string]*Manifest)
	if m.st != nil && m.st.Pool() != nil {
		rows, err := m.st.Pool().Query(context.Background(), `
			SELECT manifest_json FROM kernel_plugins
			 WHERE status IN ('installed','enabled')`)
		if err != nil {
			return nil, fmt.Errorf("读取已安装清单: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return nil, err
			}
			var mf Manifest
			if err := json.Unmarshal(raw, &mf); err != nil || mf.PluginID == "" {
				continue
			}
			activePlugins[mf.PluginID] = &mf
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		m.lifecycleMu.Lock()
		for pID, mf := range m.memPlugins {
			if m.memStatuses[pID] == "enabled" || m.memStatuses[pID] == "installed" {
				activePlugins[pID] = mf
			}
		}
		m.lifecycleMu.Unlock()
	}
	provMap := make(map[string]string)
	if m.providerIndex != nil {
		provMap = m.providerIndex.GetProviderMap()
	}
	return BuildDAG(activePlugins, provMap)
}

func requireRole(actor config.Actor, ids ...string) error {
	if actor.Kind == config.KindBot || actor.Kind == config.KindKernelRole {
		for _, id := range ids {
			if actor.ID == id {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: 主体 %s(%s) 无权执行此操作", config.ErrForbidden, actor.ID, actor.Kind)
}

func manifestChecksum(raw []byte) (string, error) {
	v, err := config.DecodeSpec(raw)
	if err != nil {
		return "", err
	}
	return config.Checksum(v)
}

func NewManager(st *store.Store, cfg *config.Module, sup *supervisor.Supervisor, reg *registry.Registry) *Manager {
	return &Manager{
		st:            st,
		cfg:           cfg,
		sup:           sup,
		reg:           reg,
		providerIndex: NewInterfaceProviderIndex(),
		memPlugins:    make(map[string]*Manifest),
		memStatuses:   make(map[string]string),
		memProcesses:  make(map[string]PluginProcessInfo),
	}
}

// ValidateManifest 校验插件清单的命名与完整性。
func ValidateManifest(m *Manifest) error {
	if !pluginIDPattern.MatchString(m.PluginID) {
		return fmt.Errorf("%w: plugin_id %q 不符合规范 ^plugin_[a-z0-9_]{3,24}$", ErrInvalidManifest, m.PluginID)
	}
	switch m.PluginType {
	case "ingress", "workspace", "executor", "knowledge":
	default:
		return fmt.Errorf("%w: 不支持的 plugin_type %q", ErrInvalidManifest, m.PluginType)
	}
	if err := validateCategory(m); err != nil {
		return err
	}

	if m.Binary != "" {
		digest, err := hex.DecodeString(m.BinaryDigest)
		if err != nil || len(digest) != sha256.Size || strings.ToLower(m.BinaryDigest) != m.BinaryDigest {
			return fmt.Errorf("%w: binary 必须绑定有效的 SHA256 binary_digest", ErrInvalidManifest)
		}
	}
	seen := make(map[string]bool)
	for i, bt := range m.BotTemplates {
		if seen[bt.BotID] {
			return fmt.Errorf("%w: 重复的 bot_id %q", ErrInvalidManifest, bt.BotID)
		}
		seen[bt.BotID] = true
		if !botIDPattern.MatchString(bt.BotID) {
			return fmt.Errorf("%w: bot_id %q 不符合规范 ^[a-z0-9_]{5,64}$", ErrInvalidManifest, bt.BotID)
		}
		if reason := RejectedBotID(bt.BotID); reason != "" {
			return fmt.Errorf("%w: bot_id %q %s", ErrInvalidManifest, bt.BotID, reason)
		}
		// 插件侧声明的 bot 不得使用 user_ 前缀（user_ 前缀仅限用户手动建的 bot）
		if strings.HasPrefix(bt.BotID, "user_") {
			return fmt.Errorf("%w: 插件声明 bot %q 不得使用 user_ 前缀", ErrInvalidManifest, bt.BotID)
		}
		if bt.ActivationMode == "" {
			m.BotTemplates[i].ActivationMode = "lazy"
		} else if bt.ActivationMode != "lazy" && bt.ActivationMode != "resident" {
			return fmt.Errorf("%w: bot %q 的 activation_mode 必须是 lazy 或 resident", ErrInvalidManifest, bt.BotID)
		}
	}

	reservedPrefixes := []string{"bot.", "agent.", "project.", "kernel.", "obsidian.", "pm."}
	for _, ev := range m.Publishing {
		for _, pfx := range reservedPrefixes {
			if strings.HasPrefix(ev, pfx) {
				return fmt.Errorf("%w: 发布事件 %q 命中内核/保留前缀 %q", ErrInvalidManifest, ev, pfx)
			}
		}
		if !strings.HasPrefix(ev, m.PluginID+".") {
			return fmt.Errorf("%w: 发布事件 %q 必须以 %q 开头", ErrInvalidManifest, ev, m.PluginID+".")
		}
	}

	for _, dep := range m.Dependencies {
		if err := ValidateDependencySpec(&dep); err != nil {
			return fmt.Errorf("%w: 依赖声明校验失败: %v", ErrInvalidManifest, err)
		}
	}
	return nil
}

// Install 安装插件制品并提交 security 审查。成功与失败都追加生命周期留底。
func (m *Manager) Install(ctx context.Context, actor config.Actor, raw []byte) (*Manifest, int64, error) {
	pluginID := peekPluginID(raw)
	base := LifecycleRecord{PluginID: pluginID, Action: "install", Actor: actor.ID}
	attachDigest(&base, raw)
	if err := requireRole(actor, config.UserBot); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	var mf Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, fmt.Errorf("%w: JSON 解析失败: %v", ErrInvalidManifest, err))
	}
	base.PluginID = mf.PluginID
	if err := ValidateManifest(&mf); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	digest, err := manifestChecksum(raw)
	if err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	base.Digest = digest
	if err := checkBinaryDigest(&mf); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.st == nil {
		if err := m.checkCandidateDepth(ctx, &mf); err != nil {
			base.Result = "rejected"
			base.Reason = err.Error()
			m.appendLifecycle(base)
			return nil, 0, err
		}
		m.decisionSeq++
		decID := m.decisionSeq
		m.memPlugins[mf.PluginID] = &mf
		m.memStatuses[mf.PluginID] = "installed"
		m.memDecisions = append(m.memDecisions, SecurityDecision{
			DecisionID:  decID,
			SubjectType: "plugin",
			SubjectID:   mf.PluginID,
			Action:      "approve",
			Decision:    "pending",
			RequestedBy: actor.ID,
			DecidedBy:   "security_bot",
			Reason:      digest,
			Checksum:    digest,
		})
		base.Result = "ok"
		base.Reason = "已提交 security 审查"
		m.appendLifecycle(base)
		return &mf, decID, nil
	}
	if err := m.checkCandidateDepth(ctx, &mf); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	var decisionID int64
	err = m.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO kernel_plugins (plugin_id, plugin_type, display_name, namespace, manifest_digest, manifest_json, status)
			VALUES ($1,$2,$3,$4,$5,$6,'installed')
			ON CONFLICT (plugin_id) DO UPDATE SET
			 plugin_type=EXCLUDED.plugin_type, display_name=EXCLUDED.display_name,
			 namespace=EXCLUDED.namespace, manifest_digest=EXCLUDED.manifest_digest,
			 manifest_json=EXCLUDED.manifest_json, status='installed', revoked_at=NULL, updated_at=now()
			WHERE kernel_plugins.status='uninstalled'`,
			mf.PluginID, mf.PluginType, mf.Name, strings.TrimPrefix(mf.PluginID, "plugin_"), digest, raw)
		if err != nil {
			return fmt.Errorf("登记插件安装: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: 插件已安装，不能覆盖待审或运行中的清单", config.ErrConflict)
		}
		if _, err := tx.Exec(ctx, `UPDATE kernel_security_decisions SET decision='rejected', reason='superseded_install', updated_at=now() WHERE subject_type='plugin' AND subject_id=$1 AND decision='pending'`, mf.PluginID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO kernel_security_decisions (subject_type, subject_id, action, decision, requested_by, decided_by, checksum)
			VALUES ('plugin',$1,'approve','pending',$2,'security_bot',$3) RETURNING decision_id`, mf.PluginID, actor.ID, digest).Scan(&decisionID); err != nil {
			return fmt.Errorf("提交 security 审查: %w", err)
		}
		ok := base
		ok.Result = "ok"
		ok.Reason = "已提交 security 审查"
		return m.persistLifecycle(ctx, tx, ok)
	})
	if err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	return &mf, decisionID, nil
}

// SecurityList 列出待审查或指定状态的 security 决定。
func (m *Manager) SecurityList(ctx context.Context, actor config.Actor, status string) ([]SecurityDecision, error) {
	if err := requireRole(actor, config.UserBot, config.SecurityBot); err != nil {
		return nil, err
	}
	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		var out []SecurityDecision
		for _, d := range m.memDecisions {
			if status == "" || d.Decision == status {
				out = append(out, d)
			}
		}
		return out, nil
	}
	q := `SELECT decision_id, subject_type, subject_id, action, decision, requested_by, decided_by, COALESCE(reason, ''), COALESCE(checksum, ''), created_at, updated_at
	        FROM kernel_security_decisions`
	var args []any
	if status != "" {
		q += ` WHERE decision=$1`
		args = append(args, status)
	}
	q += ` ORDER BY decision_id DESC`

	rows, err := m.st.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 security_decisions: %w", err)
	}
	defer rows.Close()

	var out []SecurityDecision
	for rows.Next() {
		var d SecurityDecision
		if err := rows.Scan(&d.DecisionID, &d.SubjectType, &d.SubjectID, &d.Action, &d.Decision, &d.RequestedBy, &d.DecidedBy, &d.Reason, &d.Checksum, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SecurityApprove 批准安全决定并激活对应资源（如插件）。
func (m *Manager) SecurityApprove(ctx context.Context, actor config.Actor, decisionID int64, cc config.CausalContext) error {
	if err := requireRole(actor, config.UserBot, config.SecurityBot); err != nil {
		return err
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.st == nil {
		found := false
		for i, d := range m.memDecisions {
			if d.DecisionID == decisionID {
				if d.Decision != "pending" {
					return fmt.Errorf("%w: 决定已处理", config.ErrConflict)
				}
				m.memDecisions[i].Decision = "approved"
				mf := m.memPlugins[d.SubjectID]
				if mf != nil {
					if err := m.activatePlugin(ctx, nil, actor, decisionID, d.SubjectID, *mf, d.Reason, cc); err != nil {
						return err
					}
				}
				found = true
				break
			}
		}
		if !found {
			return ErrDecisionNotFound
		}
		return nil
	}
	return m.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var subjectType, subjectID, decision, requestedBy, checksum, action string
		if err := tx.QueryRow(ctx, `SELECT subject_type, subject_id, decision, requested_by, COALESCE(checksum,''), action FROM kernel_security_decisions WHERE decision_id=$1 FOR UPDATE`, decisionID).Scan(&subjectType, &subjectID, &decision, &requestedBy, &checksum, &action); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDecisionNotFound
			}
			return fmt.Errorf("读取安全决定: %w", err)
		}
		if subjectType != "plugin" || action != "approve" || requestedBy != config.UserBot {
			return fmt.Errorf("%w: 此入口仅处理 user_bot 发起的插件安装审查", config.ErrForbidden)
		}
		var raw []byte
		var digest, state string
		if err := tx.QueryRow(ctx, `SELECT manifest_json, manifest_digest, status FROM kernel_plugins WHERE plugin_id=$1 FOR UPDATE`, subjectID).Scan(&raw, &digest, &state); err != nil {
			return err
		}
		actualDigest, err := manifestChecksum(raw)
		if err != nil {
			return err
		}
		if checksum == "" || checksum != digest || actualDigest != digest {
			return fmt.Errorf("%w: 审批绑定的清单已变化", config.ErrConflict)
		}
		if decision == "approved" && state == "enabled" {
			return nil
		}
		if decision != "pending" || state != "installed" {
			return fmt.Errorf("%w: 插件或审批不处于待激活状态", config.ErrBadTransition)
		}
		var mf Manifest
		if err := json.Unmarshal(raw, &mf); err != nil {
			return err
		}
		if err := ValidateManifest(&mf); err != nil {
			return err
		}
		return m.activatePlugin(ctx, tx, actor, decisionID, subjectID, mf, digest, cc)
	})
}

func (m *Manager) activatePlugin(ctx context.Context, tx pgx.Tx, actor config.Actor, decisionID int64, pluginID string, mf Manifest, digest string, cc config.CausalContext) error {
	if err := ValidateManifest(&mf); err != nil {
		return err
	}
	var binPath, binDigest string
	if m.sup != nil {
		binPath, binDigest, _ = m.sup.RuntimeArtifact()
	}

	if tx != nil {
		if m.sup == nil {
			return errors.New("未配置 supervisor，无法取得可信运行时制品")
		}
		var err error
		binPath, binDigest, err = m.sup.RuntimeArtifact()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE kernel_security_decisions
			   SET decision='approved', decided_by=$2, updated_at=now()
			 WHERE decision_id=$1`, decisionID, actor.ID)
		if err != nil {
			return err
		}
	}

	// 登记发布的事件
	for _, ev := range mf.Publishing {
		if tx != nil {
			_, err := tx.Exec(ctx, `
				INSERT INTO kernel_plugin_event_registry (event_type, plugin_id, category, schema_json)
				VALUES ($1, $2, 'persistent', '{}'::jsonb)
				ON CONFLICT (event_type) DO NOTHING`, ev, pluginID)
			if err != nil {
				return fmt.Errorf("登记事件 %s: %w", ev, err)
			}
		}
	}

	// 登记订阅权限与工具权限
	for _, sub := range mf.Subscribing {
		if tx != nil {
			_, err := tx.Exec(ctx, `
				INSERT INTO kernel_plugin_grants (plugin_id, perm_type, perm_target, granted_by)
				VALUES ($1, 'subscribe', $2, $3)
				ON CONFLICT (plugin_id, perm_type, perm_target) DO NOTHING`, pluginID, sub, actor.ID)
			if err != nil {
				return fmt.Errorf("登记订阅授权: %w", err)
			}
		}
	}
	for _, tool := range mf.RequiringTools {
		if tx != nil {
			_, err := tx.Exec(ctx, `
				INSERT INTO kernel_plugin_grants (plugin_id, perm_type, perm_target, granted_by)
				VALUES ($1, 'tool', $2, $3)
				ON CONFLICT (plugin_id, perm_type, perm_target) DO NOTHING`, pluginID, tool, actor.ID)
			if err != nil {
				return fmt.Errorf("登记工具授权: %w", err)
			}
		}
	}

	// 写入 bot_manifest 与 bots
	for _, bt := range mf.BotTemplates {
		caps, _ := json.Marshal(bt.Capabilities)
		idleTTL := bt.IdleTTLSeconds
		if idleTTL <= 0 {
			idleTTL = 300
		}
		startTimeout := bt.StartTimeoutSec
		if startTimeout <= 0 {
			startTimeout = 30
		}

		var memProfile any
		if bt.MemoryProfile != "" {
			b, _ := json.Marshal(bt.MemoryProfile)
			memProfile = string(b)
		}

		minInstances := 0
		if bt.ActivationMode == "resident" {
			minInstances = 1
		}

		if tx != nil {
			_, err := tx.Exec(ctx, `
				INSERT INTO bot_manifest (
					bot_id, role, managed_by, parent_plugin, display_name,
					binary_path, binary_digest, manifest_digest, capabilities,
					memory_profile, activation_mode, idle_ttl_seconds, start_timeout_seconds,
					prompt_ref, model, config_version
				) VALUES (
					$1, 'plugin', $2, $2, $3,
					$4, $5, $6, $7,
					$8, $9, $10, $11,
					$12, $13, 1
				) ON CONFLICT (bot_id) DO UPDATE SET
					display_name=EXCLUDED.display_name, binary_path=EXCLUDED.binary_path,
					binary_digest=EXCLUDED.binary_digest, manifest_digest=EXCLUDED.manifest_digest,
					capabilities=EXCLUDED.capabilities, activation_mode=EXCLUDED.activation_mode,
					updated_at=now()`,
				bt.BotID, pluginID, bt.DisplayName,
				binPath, binDigest, digest, caps,
				memProfile, bt.ActivationMode, idleTTL, startTimeout,
				bt.PromptRef, bt.Model)
			if err != nil {
				return fmt.Errorf("写入 bot_manifest %s: %w", bt.BotID, err)
			}

			_, err = tx.Exec(ctx, `
				INSERT INTO bots (bot_id, desired_state, desired_by, observed_state, enabled, min_instances, config_version)
				VALUES ($1, 'start', 'create_bot', 'dormant', true, $2, 1)
				ON CONFLICT (bot_id) DO UPDATE SET
					desired_state='start', enabled=true, min_instances=EXCLUDED.min_instances, updated_at=now()`,
				bt.BotID, minInstances)
			if err != nil {
				return fmt.Errorf("写入 bots %s: %w", bt.BotID, err)
			}
		}
	}

	initSpec := `{"resource_limits":{"cpu_ms":100,"memory_mb":128}}`
	hSpec := sha256.Sum256([]byte(initSpec))
	csum := hex.EncodeToString(hSpec[:])

	if tx != nil {
		_, err := tx.Exec(ctx, `
			INSERT INTO kernel_config_documents (
				scope_type, scope_id, config_key, version, status, spec, schema_version,
				checksum, base_version, created_by, approved_by, approved_checksum,
				approved_at, activated_at
			) VALUES (
				'plugin', $1, 'default', 1, 'active', $2::jsonb, 1,
				$3, 0, 'user_bot', 'user_bot', $3,
				now(), now()
			) ON CONFLICT (scope_type, scope_id, config_key, version) DO NOTHING`,
			pluginID, initSpec, csum)
		if err != nil {
			return fmt.Errorf("写入 plugin 初始配置: %w", err)
		}

		_, err = tx.Exec(ctx, `
			UPDATE kernel_plugins
			   SET status='enabled', config_version=1, updated_at=now()
			 WHERE plugin_id=$1`, pluginID)
		if err != nil {
			return err
		}
	} else {
		m.memStatuses[pluginID] = "enabled"
	}

	// 触发 supervisor 调和
	if m.sup != nil && m.st != nil {
		go func() {
			_, _ = m.sup.Reconcile(context.Background())
		}()
	}
	return nil
}

// SecurityRevoke 驳回审查决定。
func (m *Manager) SecurityRevoke(ctx context.Context, actor config.Actor, decisionID int64, reason string) error {
	if err := requireRole(actor, config.UserBot, config.SecurityBot); err != nil {
		return err
	}
	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		found := false
		for i, d := range m.memDecisions {
			if d.DecisionID == decisionID {
				m.memDecisions[i].Decision = "rejected"
				m.memDecisions[i].Reason = reason
				found = true
				break
			}
		}
		if !found {
			return ErrDecisionNotFound
		}
		return nil
	}
	_, err := m.st.Pool().Exec(ctx, `
		UPDATE kernel_security_decisions
		   SET decision='rejected', decided_by=$2, reason=$3, updated_at=now()
		 WHERE decision_id=$1`, decisionID, actor.ID, reason)
	return err
}

// Disable 禁用插件（两阶段：先关插件进程，后停 Bot 进程）。
func (m *Manager) Disable(ctx context.Context, actor config.Actor, pluginID string) error {
	base := LifecycleRecord{PluginID: pluginID, Action: "disable", Actor: actor.ID}
	if err := requireRole(actor, config.UserBot); err != nil {
		return m.failLifecycle(ctx, base, err)
	}
	// 第一阶段：优雅关闭插件进程
	_ = m.StopProcess(ctx, actor, pluginID)

	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		if !m.knownPluginLocked(pluginID) {
			base.Result = "rejected"
			base.Reason = ErrPluginNotFound.Error()
			m.appendLifecycle(base)
			return fmt.Errorf("%w: %s", ErrPluginNotFound, pluginID)
		}
		m.memStatuses[pluginID] = "disabled"
		base.Result = "ok"
		m.appendLifecycle(base)
		return nil
	}

	// 第二阶段：下线该插件名下的所有 Bot
	_, err := m.st.Pool().Exec(ctx, `
		UPDATE bots
		   SET desired_state='stop', updated_at=now()
		 WHERE bot_id IN (SELECT bot_id FROM bot_manifest WHERE parent_plugin=$1)`, pluginID)
	if err != nil {
		return m.failLifecycle(ctx, base, fmt.Errorf("下线 Bot 进程: %w", err))
	}

	tag, err := m.st.Pool().Exec(ctx, `
		UPDATE kernel_plugins
		   SET status='disabled', updated_at=now()
		 WHERE plugin_id=$1`, pluginID)
	if err != nil {
		return m.failLifecycle(ctx, base, fmt.Errorf("禁用插件: %w", err))
	}
	if tag.RowsAffected() != 1 {
		return m.failLifecycle(ctx, base, fmt.Errorf("%w: %s", ErrPluginNotFound, pluginID))
	}
	base.Result = "ok"
	return m.recordLifecycle(ctx, base)
}

// Uninstall 卸载插件（两阶段进程下线，按 confirm/keep 清理元数据与类型化表）。
func (m *Manager) Uninstall(ctx context.Context, actor config.Actor, pluginID string, confirm, keep bool) error {
	base := LifecycleRecord{PluginID: pluginID, Action: "uninstall", Actor: actor.ID}
	if err := requireRole(actor, config.UserBot); err != nil {
		return m.failLifecycle(ctx, base, err)
	}
	// 依赖治理反向依赖安全校验
	dag, err := m.BuildSystemDAG()
	if err == nil && dag != nil {
		if _, exists := dag.Nodes[pluginID]; exists {
			if _, err := CheckUninstallReverseDependencies(dag, pluginID, confirm); err != nil {
				return m.failLifecycle(ctx, base, err)
			}
		}
	}

	// 无论 confirm 还是 keep，先走两阶段进程回收
	_ = m.StopProcess(ctx, actor, pluginID)

	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		if !m.knownPluginLocked(pluginID) {
			base.Result = "rejected"
			base.Reason = ErrPluginNotFound.Error()
			m.appendLifecycle(base)
			return fmt.Errorf("%w: %s", ErrPluginNotFound, pluginID)
		}
		m.memStatuses[pluginID] = "uninstalled"
		if confirm {
			delete(m.memPlugins, pluginID)
			delete(m.memProcesses, pluginID)
		}
		base.Result = "ok"
		m.appendLifecycle(base)
		return nil
	}

	_, _ = m.st.Pool().Exec(ctx, `
		UPDATE bots
		   SET desired_state='stop', updated_at=now()
		 WHERE bot_id IN (SELECT bot_id FROM bot_manifest WHERE parent_plugin=$1)`, pluginID)

	// 配置文档标记为 retired
	_, _ = m.st.Pool().Exec(ctx, `
		UPDATE kernel_config_documents
		   SET status='retired', retired_at=now()
		 WHERE scope_type='plugin' AND scope_id=$1 AND status='active'`, pluginID)

	if confirm {
		// 删除进程登记
		_, _ = m.st.Pool().Exec(ctx, `DELETE FROM kernel_plugin_processes WHERE plugin_id=$1`, pluginID)
		// 删除事件与授权
		_, _ = m.st.Pool().Exec(ctx, `DELETE FROM kernel_plugin_event_registry WHERE plugin_id=$1`, pluginID)
		_, _ = m.st.Pool().Exec(ctx, `DELETE FROM kernel_plugin_grants WHERE plugin_id=$1`, pluginID)
		// 删除 Bot manifest 与 bots
		_, _ = m.st.Pool().Exec(ctx, `DELETE FROM bot_instances WHERE bot_id IN (SELECT bot_id FROM bot_manifest WHERE parent_plugin=$1)`, pluginID)
		_, _ = m.st.Pool().Exec(ctx, `DELETE FROM bots WHERE bot_id IN (SELECT bot_id FROM bot_manifest WHERE parent_plugin=$1)`, pluginID)
		_, _ = m.st.Pool().Exec(ctx, `DELETE FROM bot_manifest WHERE parent_plugin=$1`, pluginID)
	}

	tag, err := m.st.Pool().Exec(ctx, `
		UPDATE kernel_plugins
		   SET status='uninstalled', updated_at=now()
		 WHERE plugin_id=$1`, pluginID)
	if err != nil {
		return m.failLifecycle(ctx, base, fmt.Errorf("卸载插件: %w", err))
	}
	if tag.RowsAffected() != 1 {
		return m.failLifecycle(ctx, base, fmt.Errorf("%w: %s", ErrPluginNotFound, pluginID))
	}
	base.Result = "ok"
	return m.recordLifecycle(ctx, base)
}

// CascadeUninstall 按逆拓扑卸载：每个插件先停 L2，再停 L1。
func (m *Manager) CascadeUninstall(ctx context.Context, actor config.Actor, pluginID string) error {
	base := LifecycleRecord{PluginID: pluginID, Action: "uninstall", Actor: actor.ID}
	if err := requireRole(actor, config.UserBot); err != nil {
		return m.failLifecycle(ctx, base, err)
	}
	order, err := m.uninstallSequence(pluginID, true)
	if err != nil {
		return m.failLifecycle(ctx, base, err)
	}
	for _, id := range order {
		if err := m.Uninstall(ctx, actor, id, true, false); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) uninstallSequence(pluginID string, cascade bool) ([]string, error) {
	dag, err := m.BuildSystemDAG()
	if err != nil || dag == nil {
		if err != nil {
			return nil, err
		}
		return []string{pluginID}, nil
	}
	if _, ok := dag.Nodes[pluginID]; !ok {
		return []string{pluginID}, nil
	}
	return CheckUninstallReverseDependencies(dag, pluginID, cascade)
}

// DecisionChecksum 返回安全审查绑定的清单 checksum。
func (m *Manager) DecisionChecksum(ctx context.Context, decisionID int64) (string, error) {
	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		for _, d := range m.memDecisions {
			if d.DecisionID == decisionID {
				if d.Checksum != "" {
					return d.Checksum, nil
				}
				return d.Reason, nil
			}
		}
		return "", ErrDecisionNotFound
	}
	var sum string
	err := m.st.Pool().QueryRow(ctx, `SELECT COALESCE(checksum,'') FROM kernel_security_decisions WHERE decision_id=$1`, decisionID).Scan(&sum)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrDecisionNotFound
		}
		return "", fmt.Errorf("读取审查 checksum: %w", err)
	}
	return sum, nil
}

// VerifyPresentedChecksum 要求调用方出示与审查绑定一致的 checksum。
func (m *Manager) VerifyPresentedChecksum(ctx context.Context, decisionID int64, checksum string) error {
	if checksum == "" {
		return fmt.Errorf("%w: 批准必须提供 checksum", config.ErrForbidden)
	}
	got, err := m.DecisionChecksum(ctx, decisionID)
	if err != nil {
		return err
	}
	if got == "" || got != checksum {
		return fmt.Errorf("%w: checksum 与审查绑定不一致", config.ErrConflict)
	}
	return nil
}

// Upgrade 提交插件升级审查。
func (m *Manager) Upgrade(ctx context.Context, actor config.Actor, raw []byte) (*Manifest, int64, error) {
	pluginID := peekPluginID(raw)
	base := LifecycleRecord{PluginID: pluginID, Action: "upgrade", Actor: actor.ID}
	attachDigest(&base, raw)
	if err := requireRole(actor, config.UserBot); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	var mf Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, fmt.Errorf("%w: JSON 解析失败: %v", ErrInvalidManifest, err))
	}
	base.PluginID = mf.PluginID
	if err := ValidateManifest(&mf); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	if err := checkBinaryDigest(&mf); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	if err := m.checkCandidateDepth(ctx, &mf); err != nil {
		return nil, 0, m.failLifecycle(ctx, base, err)
	}
	h := sha256.Sum256(raw)
	digest := hex.EncodeToString(h[:])
	base.Digest = digest

	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		m.decisionSeq++
		decID := m.decisionSeq
		m.memDecisions = append(m.memDecisions, SecurityDecision{
			DecisionID:  decID,
			SubjectType: "hot_update",
			SubjectID:   mf.PluginID,
			Action:      "hot_update",
			Decision:    "pending",
			RequestedBy: actor.ID,
			DecidedBy:   "security_bot",
			Reason:      digest,
		})
		base.Result = "ok"
		base.Reason = "已提交升级审查"
		m.appendLifecycle(base)
		return &mf, decID, nil
	}

	var decisionID int64
	err := m.st.Pool().QueryRow(ctx, `
		INSERT INTO kernel_security_decisions (subject_type, subject_id, action, decision, requested_by, decided_by, checksum)
		VALUES ('hot_update', $1, 'hot_update', 'pending', $2, 'security_bot', $3)
		RETURNING decision_id`, mf.PluginID, actor.ID, digest).Scan(&decisionID)
	if err != nil {
		return nil, 0, m.failLifecycle(ctx, base, fmt.Errorf("提交升级审查: %w", err))
	}
	base.Result = "ok"
	base.Reason = "已提交升级审查"
	if err := m.recordLifecycle(ctx, base); err != nil {
		return nil, 0, err
	}
	return &mf, decisionID, nil
}

// ReportProcess 由 owner bot 上报其启动的插件进程（包含 PID、PGID、pid_start_time）。
func (m *Manager) ReportProcess(ctx context.Context, actor config.Actor, rep ProcessReport) error {
	if rep.PluginID == "" || rep.OwnerBotID == "" || rep.PID <= 0 {
		return fmt.Errorf("非法进程上报参数: plugin_id=%s, owner=%s, pid=%d", rep.PluginID, rep.OwnerBotID, rep.PID)
	}
	cat, err := m.pluginCategory(ctx, rep.PluginID)
	if err != nil {
		return err
	}
	if cat == CategoryTheme || cat == CategoryBotPack {
		return fmt.Errorf("%w: %s", errCategoryNoProcess, rep.PluginID)
	}

	// 存活校验：防 PID 复用
	if rep.State == "running" && rep.PIDStartTime > 0 {
		if !osproc.IsProcessAlive(rep.PID, rep.PIDStartTime) {
			return fmt.Errorf("进程 %d 校验失败: 真实 starttime 与上报不符或已消失", rep.PID)
		}
	}

	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		var lastErr *string
		if rep.LastError != "" {
			lastErr = &rep.LastError
		}
		m.memProcesses[rep.PluginID] = PluginProcessInfo{
			ProcessID:     rep.PluginID + ":" + rep.OwnerBotID,
			PluginID:      rep.PluginID,
			OwnerBotID:    rep.OwnerBotID,
			PID:           rep.PID,
			PGID:          rep.PGID,
			PIDStartTime:  rep.PIDStartTime,
			BinaryDigest:  rep.BinaryDigest,
			ConfigVersion: rep.ConfigVersion,
			State:         rep.State,
			LastError:     lastErr,
		}
		return nil
	}

	processID := rep.PluginID + ":" + rep.OwnerBotID
	var ownerInstanceID string
	_ = m.st.Pool().QueryRow(ctx, `
		SELECT instance_id::text FROM bot_instances
		 WHERE bot_id=$1 AND stopped_at IS NULL
		 ORDER BY instance_id DESC LIMIT 1`, rep.OwnerBotID).Scan(&ownerInstanceID)
	if ownerInstanceID == "" {
		_ = m.st.Pool().QueryRow(ctx, `
			SELECT instance_id::text FROM bot_instances
			 WHERE bot_id=$1 ORDER BY instance_id DESC LIMIT 1`, rep.OwnerBotID).Scan(&ownerInstanceID)
	}
	if ownerInstanceID == "" {
		ownerInstanceID = fmt.Sprintf("%d", rep.OwnerPID)
	}

	_, err = m.st.Pool().Exec(ctx, `
		INSERT INTO kernel_plugin_processes (
			process_id, plugin_id, owner_bot_id, owner_instance_id,
			pid, pgid, pid_start_time, binary_digest, config_version,
			state, last_seen_at, last_error
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9,
			$10, now(), $11
		) ON CONFLICT (plugin_id) DO UPDATE SET
			pid=EXCLUDED.pid, pgid=EXCLUDED.pgid, pid_start_time=EXCLUDED.pid_start_time,
			binary_digest=EXCLUDED.binary_digest, config_version=EXCLUDED.config_version,
			state=EXCLUDED.state, last_seen_at=now(), last_error=EXCLUDED.last_error,
			updated_at=now()
		 WHERE kernel_plugin_processes.state NOT IN ('stopped', 'killed', 'failed')
		    OR EXCLUDED.pid_start_time <> kernel_plugin_processes.pid_start_time`,
		processID, rep.PluginID, rep.OwnerBotID, ownerInstanceID,
		rep.PID, rep.PGID, rep.PIDStartTime, rep.BinaryDigest, rep.ConfigVersion,
		rep.State, rep.LastError)
	return err
}

// StopProcess 优雅关闭插件进程（user_bot -> owner bot -> 插件进程）。
func (m *Manager) StopProcess(ctx context.Context, actor config.Actor, pluginID string) error {
	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		if p, ok := m.memProcesses[pluginID]; ok {
			p.State = "stopped"
			m.memProcesses[pluginID] = p
		}
		return nil
	}
	var pid, pgid int
	var startTime int64
	var state string
	err := m.st.Pool().QueryRow(ctx, `
		SELECT pid, pgid, pid_start_time, state
		  FROM kernel_plugin_processes WHERE plugin_id=$1`, pluginID).Scan(&pid, &pgid, &startTime, &state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // 不存在即 no-op
		}
		return err
	}
	if state == "stopped" || state == "killed" || state == "failed" {
		return nil // 幂等
	}

	_, _ = m.st.Pool().Exec(ctx, `
		UPDATE kernel_plugin_processes
		   SET state='stopping', stop_mode='graceful', stop_requested_at=now()
		 WHERE plugin_id=$1`, pluginID)

	if osproc.IsProcessAlive(pid, startTime) {
		// 发送 SIGTERM 给插件进程组
		_ = osproc.KillProcessGroup(pgid, syscall.SIGTERM)
		// 等待退出
		for i := 0; i < 15; i++ {
			time.Sleep(100 * time.Millisecond)
			if !osproc.IsProcessAlive(pid, startTime) {
				break
			}
		}
		// 若仍存活则自行升级 SIGKILL (PluginMvp §6.4 规则 2)
		if osproc.IsProcessAlive(pid, startTime) {
			_ = osproc.KillProcessGroup(pgid, syscall.SIGKILL)
			_, _ = m.st.Pool().Exec(ctx, `
				UPDATE kernel_plugin_processes
				   SET state='killed', stop_mode='graceful', stopped_at=now(), last_error='stop_timeout'
				 WHERE plugin_id=$1`, pluginID)
			return nil
		}
	}

	_, err = m.st.Pool().Exec(ctx, `
		UPDATE kernel_plugin_processes
		   SET state='stopped', stop_mode='graceful', stopped_at=now()
		 WHERE plugin_id=$1`, pluginID)
	return err
}

// KillProcess 强制终止插件进程（要求 confirm）。
func (m *Manager) KillProcess(ctx context.Context, actor config.Actor, pluginID string, confirm bool, reason string) error {
	if !confirm {
		return ErrConfirmRequired
	}
	if m.sup == nil && m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		if p, ok := m.memProcesses[pluginID]; ok {
			p.State = "killed"
			m.memProcesses[pluginID] = p
		}
		return nil
	}
	if m.sup == nil {
		return errors.New("未配置 supervisor，无法执行 force kill")
	}
	if reason == "" {
		reason = "user_bot_force_kill"
	}
	return m.sup.KillPluginProcess(ctx, pluginID, reason, actor.ID)
}

// ListProcesses 列出所有登记的插件进程。
func (m *Manager) ListProcesses(ctx context.Context, actor config.Actor) ([]PluginProcessInfo, error) {
	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		var out []PluginProcessInfo
		for _, p := range m.memProcesses {
			out = append(out, p)
		}
		return out, nil
	}
	rows, err := m.st.Pool().Query(ctx, `
		SELECT process_id, plugin_id, owner_bot_id, owner_instance_id,
		       pid, pgid, pid_start_time, binary_digest, config_version,
		       state, stop_mode, started_at, stopped_at, last_seen_at, last_error
		  FROM kernel_plugin_processes
		 ORDER BY plugin_id`)
	if err != nil {
		return nil, fmt.Errorf("查询 kernel_plugin_processes: %w", err)
	}
	defer rows.Close()

	var out []PluginProcessInfo
	for rows.Next() {
		var p PluginProcessInfo
		if err := rows.Scan(
			&p.ProcessID, &p.PluginID, &p.OwnerBotID, &p.OwnerInstanceID,
			&p.PID, &p.PGID, &p.PIDStartTime, &p.BinaryDigest, &p.ConfigVersion,
			&p.State, &p.StopMode, &p.StartedAt, &p.StoppedAt, &p.LastSeenAt, &p.LastError,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
