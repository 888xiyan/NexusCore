// Package config 是 hub 的配置中心控制面模块（不是第四原语、不是独立服务、不是总线主体）。
// 依据：ConfigCenter 详设 §3-§5、内核 MVP 详设 §3.10。
// 只存 plugin / bot / agent / global 四域期望状态；不存密钥值、业务数据、运行时观测状态、manifest 正文。
// MCP / Skill 不在本模块范围内。
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// ScopeType 是四个配置域；新增域属于契约变更，需用户确认。
const (
	ScopePlugin = "plugin"
	ScopeBot    = "bot"
	ScopeAgent  = "agent"
	ScopeGlobal = "global"
)

const (
	StatusDraft          = "draft"
	StatusProposed       = "proposed"
	StatusSecurityReview = "security_review"
	StatusApproved       = "approved"
	StatusActive         = "active"
	StatusRetired        = "retired"
	StatusRejected       = "rejected"
)

// GlobalScopeID 是 global 域固定的 scope_id。
const GlobalScopeID = "kernel"

// DefaultConfigKey：MVP 每个 scope 只用一份 default 文档。
const DefaultConfigKey = "default"

// DecodeSpec 是配置 spec 的唯一解码入口：用 UseNumber 保留数字字面量。
// 调用方不得用普通 json.Unmarshal 解码 spec，否则大整数会在计算 checksum 前丢失精度。
func DecodeSpec(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("解析配置 spec: %w", err)
	}
	return v, nil
}

// CanonicalJSON 生成规范化 JSON：对象键按字节序排序、紧凑无空白、不做 HTML 转义。
// checksum 必须基于此形式，任何字节变化都改变 checksum（ConfigCenter §3.4）。
func CanonicalJSON(spec any) ([]byte, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("序列化配置 spec: %w", err)
	}
	v, err := DecodeSpec(raw)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("规范化配置 spec: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Checksum 返回 canonical JSON 的 sha256（64 位小写十六进制）。
func Checksum(spec any) (string, error) {
	c, err := CanonicalJSON(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// secretFieldNames 是疑似密钥的明文字段名单（ConfigCenter §3.3 规则 1）。
var secretFieldNames = []string{
	"password", "passwd", "token", "secret", "api_key", "apikey",
	"private_key", "access_key", "credential", "client_secret",
}

// SecretRefKey 是配置内引用密钥的唯一合法形式：{"$secret_ref": "<ref_id>"}。
const SecretRefKey = "$secret_ref"

// ScanSecrets 拒绝 spec 中的明文密钥；只允许 secret_ref 引用。
// 命中即返回错误，调用方必须拒绝提案，不得降级为脱敏后接受。
func ScanSecrets(spec any) error {
	raw, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("序列化配置 spec: %w", err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("解析配置 spec: %w", err)
	}
	return walkSecrets(v, "")
}

func walkSecrets(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t[SecretRefKey]; ok {
			s, isStr := ref.(string)
			if !isStr || s == "" {
				return fmt.Errorf("配置字段 %s 的 %s 必须是非空字符串引用", displayPath(path), SecretRefKey)
			}
			if len(t) != 1 {
				return fmt.Errorf("配置字段 %s 的 secret_ref 对象不得携带其他字段", displayPath(path))
			}
			return nil
		}
		for k, val := range t {
			child := k
			if path != "" {
				child = path + "." + k
			}
			if isSecretFieldName(k) {
				if s, ok := val.(string); ok && s != "" {
					return fmt.Errorf("配置字段 %s 疑似明文密钥：只允许 {\"%s\":\"...\"} 引用", child, SecretRefKey)
				}
			}
			if err := walkSecrets(val, child); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range t {
			if err := walkSecrets(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func isSecretFieldName(k string) bool {
	lower := strings.ToLower(k)
	for _, name := range secretFieldNames {
		if lower == name || strings.HasSuffix(lower, "_"+name) {
			return true
		}
	}
	return false
}

func displayPath(p string) string {
	if p == "" {
		return "<root>"
	}
	return p
}

// CollectSecretRefs 收集 spec 中引用的全部 ref_id（激活时校验引用存在）。
func CollectSecretRefs(spec any) ([]string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("序列化配置 spec: %w", err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析配置 spec: %w", err)
	}
	var refs []string
	collectRefs(v, &refs)
	return refs, nil
}

func collectRefs(v any, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t[SecretRefKey].(string); ok && ref != "" {
			*out = append(*out, ref)
			return
		}
		for _, val := range t {
			collectRefs(val, out)
		}
	case []any:
		for _, item := range t {
			collectRefs(item, out)
		}
	}
}
