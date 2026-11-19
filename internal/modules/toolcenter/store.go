package toolcenter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// catalogSnapshot 是工具插件自有存储的落盘形态。
// 分类、Skill 正文、MCP 登记、授权与轨迹都在这里，不进 kernel schema，也不进配置中心。
type catalogSnapshot struct {
	Tools     map[string]ToolDefinition          `json:"tools"`
	Grants    map[string]map[string]BotToolGrant `json:"grants"`
	Revisions []SkillRevision                    `json:"revisions"`
	Traces    []CallTrace                        `json:"traces"`
	Audits    []AuditNotice                      `json:"audits"`
}

func catalogPath(dir string) string {
	return filepath.Join(dir, "catalog.json")
}

func loadCatalog(path string) (catalogSnapshot, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return catalogSnapshot{}, nil
		}
		return catalogSnapshot{}, fmt.Errorf("读取工具目录 %s: %w", path, err)
	}
	var snap catalogSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return catalogSnapshot{}, fmt.Errorf("解析工具目录 %s: %w", path, err)
	}
	return snap, nil
}

func writeCatalog(path string, snap catalogSnapshot) error {
	if snap.Tools == nil {
		snap.Tools = map[string]ToolDefinition{}
	}
	if snap.Grants == nil {
		snap.Grants = map[string]map[string]BotToolGrant{}
	}
	if snap.Revisions == nil {
		snap.Revisions = []SkillRevision{}
	}
	if snap.Traces == nil {
		snap.Traces = []CallTrace{}
	}
	if snap.Audits == nil {
		snap.Audits = []AuditNotice{}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建工具目录 %s: %w", dir, err)
	}
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("编码工具目录: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "catalog-*.json")
	if err != nil {
		return fmt.Errorf("创建工具目录临时文件: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入工具目录: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置工具目录权限: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭工具目录临时文件: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换工具目录 %s: %w", path, err)
	}
	cleanup = false
	return nil
}

var (
	plainSecretPattern = regexp.MustCompile(`(?i)(api[_-]?key|password|token)\s*[:=]\s*\S+|secret\s*=\s*\S+|sk-[A-Za-z0-9]{8,}|Bearer\s+\S+`)
	secretRefPattern   = regexp.MustCompile(`^secret://[A-Za-z0-9_./:@-]{3,200}$`)
)

func rejectPlainSecret(parts ...string) error {
	for _, part := range parts {
		if plainSecretPattern.MatchString(part) {
			return ErrPlaintextSecret
		}
	}
	return nil
}

func validateSecretRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if !secretRefPattern.MatchString(ref) {
		return fmt.Errorf("%w: secret_ref 必须形如 secret://name", ErrPlaintextSecret)
	}
	if err := rejectPlainSecret(ref); err != nil {
		return err
	}
	return nil
}

func paramsNote(params map[string]interface{}) string {
	if len(params) == 0 {
		return ""
	}
	body, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	note := string(body)
	if len(note) > 120 {
		note = note[:120]
	}
	return note
}

func containsBot(list []string, botID string) bool {
	for _, id := range list {
		if id == botID {
			return true
		}
	}
	return false
}
