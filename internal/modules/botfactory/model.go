// Package botfactory 实现无头的 Bot 蓝图定义、合规校验、配置提案编排与生命周期管理核心模块。
// 职责：命名合规与系统保留名拦截、单体记忆 ≤ 200 行校验、单 API Key 装配、配置中心提案生成、Supervisor 重启协同。
package botfactory

import (
	"encoding/json"
	"errors"
)

var (
	ErrBlueprintInvalid    = errors.New("Bot 蓝图校验未通过")
	ErrBotNotFound         = errors.New("Bot 不存在")
	ErrBotAlreadyExists    = errors.New("Bot 已存在")
	ErrReservedBotID       = errors.New("禁止使用系统保留名")
	ErrMemoryLimitExceeded = errors.New("单体记忆超过 200 行硬约束")
	ErrEmptyProposer       = errors.New("提案发起者不能为空")
)

// BotBlueprint Bot 蓝图配置结构
type BotBlueprint struct {
	BotID          string            `json:"bot_id"`
	DisplayName    string            `json:"display_name"`
	Role           string            `json:"role"`            // 角色定位 (如 "reviewer", "coder", "ops")
	SystemPrompt   string            `json:"system_prompt"`   // 核心系统指令
	Persona        string            `json:"persona"`         // 性格与语气特征
	InitialMemory  string            `json:"initial_memory"`  // 初始 MEMORY.md 内容 (严格限制 ≤ 200 行)
	AllowedAgents  []string          `json:"allowed_agents"`  // 允许调用的物理探针白名单
	Metadata       map[string]string `json:"metadata,omitempty"`
	BoundAgentCLI  string            `json:"bound_agent_cli,omitempty"` // 绑定的目标 CLI (claude, codex, pi 等)
	BoundAgentID   string            `json:"bound_agent_id,omitempty"`  // 绑定的目标探针 ID
	ActivationMode string            `json:"activation_mode,omitempty"` // lazy | resident
}

// ValidationResult 蓝图静态合规检查结果
type ValidationResult struct {
	Valid       bool     `json:"valid"`
	MemoryLines int      `json:"memory_lines"` // 行数统计
	Violations  []string `json:"violations,omitempty"`
}

// TemplateItem 预设角色模板项
type TemplateItem struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Blueprint   BotBlueprint `json:"blueprint"`
}

// Proposal 配置中心提案实体
type Proposal struct {
	ProposalID string          `json:"proposal_id"`
	ScopeType  string          `json:"scope_type"` // "bot"
	ScopeID    string          `json:"scope_id"`   // bot_id
	ConfigKey  string          `json:"config_key"` // "default"
	Spec       json.RawMessage `json:"spec"`
	Checksum   string          `json:"checksum"` // SHA-256 canonical 校验和
	Status     string          `json:"status"`   // "proposed"
	CreatedBy  string          `json:"created_by"`
	CreatedAt  int64           `json:"created_at"`
}
