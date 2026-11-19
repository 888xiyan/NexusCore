package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/jackc/pgx/v5"
)

const lifecycleConfigKey = "lifecycle"

var (
	errCategoryOwnerRequired = fmt.Errorf("%w: bot_templates 不能为空（至少包含一个 owner bot）", ErrInvalidManifest)
	errCategoryNoBot         = fmt.Errorf("%w: theme / bot_pack 不注册 Bot", ErrInvalidManifest)
	errCategoryNoProcess     = fmt.Errorf("%w: theme / bot_pack 不进入进程监管", ErrInvalidManifest)
	errCategoryUnknown       = fmt.Errorf("%w: plugin_category 只允许 foundation、business、tool、theme、bot_pack", ErrInvalidManifest)
)

// LifecycleRecord 是一次安装、升级、禁用或卸载的留底。
// 落在 kernel_config_documents(config_key=lifecycle) 与 kernel_config_audit，不另建审计表。
type LifecycleRecord struct {
	PluginID string    `json:"plugin_id"`
	Action   string    `json:"action"`
	Actor    string    `json:"actor"`
	Digest   string    `json:"digest,omitempty"`
	Result   string    `json:"result"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"created_at"`
}

func (m *Manager) failLifecycle(ctx context.Context, rec LifecycleRecord, orig error) error {
	rec.Result = "rejected"
	if rec.Reason == "" && orig != nil {
		rec.Reason = orig.Error()
	}
	if err := m.recordLifecycle(ctx, rec); err != nil {
		return fmt.Errorf("%w（生命周期留底失败: %v）", orig, err)
	}
	return orig
}

func (m *Manager) recordLifecycle(ctx context.Context, rec LifecycleRecord) error {
	if err := normalizeLifecycle(&rec); err != nil {
		return err
	}
	if m.st == nil {
		m.lifecycleMu.Lock()
		m.appendLifecycle(rec)
		m.lifecycleMu.Unlock()
		return nil
	}
	return m.persistLifecycle(ctx, nil, rec)
}

func (m *Manager) appendLifecycle(rec LifecycleRecord) {
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	m.memAudits = append(m.memAudits, rec)
}

func (m *Manager) persistLifecycle(ctx context.Context, tx pgx.Tx, rec LifecycleRecord) error {
	if err := normalizeLifecycle(&rec); err != nil {
		return err
	}
	write := func(ctx context.Context, tx pgx.Tx) error {
		return insertLifecycle(ctx, tx, rec)
	}
	if tx != nil {
		return write(ctx, tx)
	}
	return m.st.WithKernelRuntimeTx(ctx, write)
}

func attachDigest(rec *LifecycleRecord, raw []byte) {
	if rec == nil || rec.Digest != "" || len(raw) == 0 {
		return
	}
	if sum, err := manifestChecksum(raw); err == nil {
		rec.Digest = sum
		return
	}
	sum := sha256.Sum256(raw)
	rec.Digest = hex.EncodeToString(sum[:])
}

func (m *Manager) knownPluginLocked(pluginID string) bool {
	if _, ok := m.memPlugins[pluginID]; ok {
		return true
	}
	_, ok := m.memStatuses[pluginID]
	return ok
}

func (m *Manager) pluginCategory(ctx context.Context, pluginID string) (string, error) {
	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		if mf := m.memPlugins[pluginID]; mf != nil {
			return mf.PluginCategory, nil
		}
		return "", nil
	}
	var raw []byte
	err := m.st.Pool().QueryRow(ctx, `
		SELECT manifest_json FROM kernel_plugins WHERE plugin_id=$1`, pluginID).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("读取插件分类: %w", err)
	}
	var mf Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return "", nil
	}
	return mf.PluginCategory, nil
}

func normalizeLifecycle(rec *LifecycleRecord) error {
	switch rec.Action {
	case "install", "upgrade", "disable", "uninstall":
	default:
		return fmt.Errorf("%w: 生命周期动作 %q 非法", ErrInvalidManifest, rec.Action)
	}
	if rec.PluginID == "" {
		rec.PluginID = "plugin_invalid"
	}
	if rec.Actor == "" {
		rec.Actor = config.UserBot
	}
	if rec.Result != "ok" && rec.Result != "rejected" {
		return fmt.Errorf("%w: 生命周期结果 %q 非法", ErrInvalidManifest, rec.Result)
	}
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	return nil
}

func insertLifecycle(ctx context.Context, tx pgx.Tx, rec LifecycleRecord) error {
	spec, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("序列化生命周期留底: %w", err)
	}
	sum := sha256.Sum256(spec)
	checksum := hex.EncodeToString(sum[:])
	var docID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO kernel_config_documents
		  (scope_type, scope_id, config_key, version, status, spec, checksum, created_by)
		VALUES ('plugin', $1, $2,
		        COALESCE((SELECT MAX(version) FROM kernel_config_documents
		                   WHERE scope_type='plugin' AND scope_id=$1 AND config_key=$2), 0) + 1,
		        'rejected', $3::jsonb, $4, $5)
		RETURNING doc_id`,
		rec.PluginID, lifecycleConfigKey, string(spec), checksum, rec.Actor).Scan(&docID); err != nil {
		return fmt.Errorf("写入生命周期文档: %w", err)
	}
	toStatus := rec.Result
	if rec.Result == "ok" {
		switch rec.Action {
		case "install", "upgrade":
			toStatus = "installed"
		case "disable":
			toStatus = "disabled"
		case "uninstall":
			toStatus = "uninstalled"
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO kernel_config_audit
		  (doc_id, action, actor, from_status, to_status, checksum, reason)
		VALUES ($1,$2,$3,NULL,$4,$5,NULLIF($6,''))`,
		docID, rec.Action, rec.Actor, toStatus, checksum, rec.Reason); err != nil {
		return fmt.Errorf("写入生命周期审计: %w", err)
	}
	return nil
}

// ListLifecycle 按插件读出追加的安装、升级、禁用、卸载留底。
func (m *Manager) ListLifecycle(ctx context.Context, pluginID string) ([]LifecycleRecord, error) {
	if pluginID == "" {
		return nil, fmt.Errorf("%w: plugin_id 不能为空", ErrInvalidManifest)
	}
	if m.st == nil {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		var out []LifecycleRecord
		for _, rec := range m.memAudits {
			if rec.PluginID == pluginID {
				out = append(out, rec)
			}
		}
		return out, nil
	}
	rows, err := m.st.Pool().Query(ctx, `
		SELECT a.action, a.actor, COALESCE(a.reason, ''), a.created_at, d.spec
		  FROM kernel_config_audit a
		  JOIN kernel_config_documents d ON d.doc_id = a.doc_id
		 WHERE d.scope_type='plugin' AND d.scope_id=$1 AND d.config_key=$2
		   AND a.action IN ('install','upgrade','disable','uninstall')
		 ORDER BY a.created_at, a.audit_id`, pluginID, lifecycleConfigKey)
	if err != nil {
		return nil, fmt.Errorf("查询生命周期留底: %w", err)
	}
	defer rows.Close()
	var out []LifecycleRecord
	for rows.Next() {
		var rec LifecycleRecord
		var spec []byte
		if err := rows.Scan(&rec.Action, &rec.Actor, &rec.Reason, &rec.At, &spec); err != nil {
			return nil, err
		}
		var body LifecycleRecord
		if err := json.Unmarshal(spec, &body); err == nil {
			rec.PluginID = body.PluginID
			rec.Digest = body.Digest
			rec.Result = body.Result
			if rec.Reason == "" {
				rec.Reason = body.Reason
			}
		}
		if rec.PluginID == "" {
			rec.PluginID = pluginID
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func peekPluginID(raw []byte) string {
	var probe struct {
		PluginID string `json:"plugin_id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.PluginID == "" {
		return "plugin_invalid"
	}
	return probe.PluginID
}

func checkBinaryDigest(mf *Manifest) error {
	if mf == nil || mf.Binary == "" {
		return nil
	}
	f, err := os.Open(mf.Binary)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%w: 无法读取 binary: %v", ErrInvalidManifest, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("%w: 计算 binary 摘要失败: %v", ErrInvalidManifest, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != mf.BinaryDigest {
		return fmt.Errorf("%w: binary 摘要不符", ErrInvalidManifest)
	}
	return nil
}

// checkCandidateDepth 只在依赖图能够解析时检查深度。
// 提供者索引在安装之后才登记，未满足的接口依赖不在安装入口阻断。
func (m *Manager) checkCandidateDepth(ctx context.Context, mf *Manifest) error {
	if mf == nil || len(mf.Dependencies) == 0 {
		return nil
	}
	plugins := map[string]*Manifest{}
	if m.st == nil {
		for id, existing := range m.memPlugins {
			if m.memStatuses[id] == "installed" || m.memStatuses[id] == "enabled" {
				plugins[id] = existing
			}
		}
	} else if m.st.Pool() != nil {
		rows, err := m.st.Pool().Query(ctx, `
			SELECT manifest_json FROM kernel_plugins
			 WHERE status IN ('installed','enabled')`)
		if err != nil {
			return fmt.Errorf("读取已安装清单: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			var installed Manifest
			if err := json.Unmarshal(raw, &installed); err != nil {
				continue
			}
			if installed.PluginID != "" {
				plugins[installed.PluginID] = &installed
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	plugins[mf.PluginID] = mf
	prov := map[string]string{}
	if m.providerIndex != nil {
		prov = m.providerIndex.GetProviderMap()
	}
	dag, err := BuildDAG(plugins, prov)
	if err != nil {
		if errors.Is(err, ErrUnmetDependency) {
			return nil
		}
		return err
	}
	return CheckMaxDepth(dag, 2)
}
