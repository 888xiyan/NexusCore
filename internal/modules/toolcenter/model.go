// Package toolcenter 实现独立工具中心无头核心模块。
// 职责：不可编辑系统工具、可编辑 Skill 与外部 MCP 服务的分级治理；
// Bot-Tool 授权矩阵（bot_tool_grants）多对多鉴权；
// 中台 Bot 分级代批 RBAC 网关（Tier 1 受控代批 vs Tier 2 刚性 HITL 阻断）；
// 沙盒网络隔离与外联审计通知。
package toolcenter

import (
	"errors"
	"time"
)

// 工具分类
type ToolCategory string

const (
	CategorySystemNative ToolCategory = "system_native" // 不可编辑系统工具 (底层驱动/探针 CLI)
	CategoryEditableSkill ToolCategory = "editable_skill" // 可编辑工具与技能 (自定义脚本/模板)
	CategoryExternalMCP   ToolCategory = "external_mcp"   // 外部 MCP 工具 (Stdio/SSE 隔离运行)
)

// 权限级别
type PermissionLevel string

const (
	PermissionReadOnly PermissionLevel = "read_only"
	PermissionExecute  PermissionLevel = "execute"
	PermissionAdmin    PermissionLevel = "admin"
)

// 代批授权安全分级
type TierLevel string

const (
	Tier1SafeControlled TierLevel = "tier1" // Tier 1: 允许受控代批 (只读/主题切换/低危通知/白名单调用)
	Tier2DangerousHITL  TierLevel = "tier2" // Tier 2: 严禁代批 (插件安装卸载/进程Exec/密钥/网络放行，强制 HITL)
)

// 网络隔离要求
type NetworkRequirement string

const (
	NetworkNone            NetworkRequirement = "none"             // 彻底离线 (默认)
	NetworkEgressWhitelist NetworkRequirement = "egress_whitelist" // 受限白名单外联 (需审计通知)
	NetworkAny             NetworkRequirement = "any"              // 任意网络 (极高危，需 Tier 2 审批)
)

var (
	ErrToolNotFound       = errors.New("指定工具未在工具中心注册")
	ErrToolAlreadyExists   = errors.New("同名工具已存在")
	ErrToolNotEditable    = errors.New("系统原生工具或不可编辑工具禁止修改代码")
	ErrToolAccessDenied   = errors.New("Bot 未被授予该工具的执行权限 (E_TOOL_ACCESS_DENIED)")
	ErrTier2HITLRequired  = errors.New("Tier 2 高危操作绝对禁止中台代批，必须人工确认 (Mandatory HITL)")
	ErrInvalidGrant       = errors.New("非法的授权配置")
	ErrEmptyToolID        = errors.New("工具标识符 (tool_id) 不能为空")
)

// ToolDefinition 工具元数据定义
type ToolDefinition struct {
	ToolID             string             `json:"tool_id"`             // 全局唯一标识 (如 sys.probe.cli, skill.python.calc, mcp.github)
	Name               string             `json:"name"`                // 显示名称
	Description        string             `json:"description"`         // 功能描述与用途
	Category           ToolCategory       `json:"category"`            // 工具类型
	IsEditable         bool               `json:"is_editable"`         // 是否允许在线编辑源码
	Runtime            string             `json:"runtime"`             // native | python | shell | mcp_stdio | mcp_sse
	Code               string             `json:"code,omitempty"`      // 源码或模板 (仅可编辑 Skill)
	ConfigSchema       string             `json:"config_schema"`       // JSON Schema 入参契约
	NetworkRequirement NetworkRequirement `json:"network_requirement"` // 网络隔离策略
	TargetAgentID      string             `json:"target_agent_id,omitempty"` // 绑定执行探针 ID
	RequiresApproval   bool               `json:"requires_approval"`   // 调用前是否需审批
	Tier               TierLevel          `json:"tier"`                // 风险分级
	Version            string             `json:"version"`             // SemVer 版本号
	Author             string             `json:"author,omitempty"`   // 创建者或维护者
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

// BotToolGrant Bot-Tool 授权矩阵项
type BotToolGrant struct {
	GrantID    string          `json:"grant_id"`
	BotID      string          `json:"bot_id"`
	ToolID     string          `json:"tool_id"`
	Permission PermissionLevel `json:"permission"`
	GrantedBy  string          `json:"granted_by"` // user_bot 或授权管理员
	GrantedAt  time.Time       `json:"granted_at"`
	ExpiresAt  *time.Time      `json:"expires_at,omitempty"`
	Status     string          `json:"status"` // active | revoked
}

// DelegationDecision 中台代批 RBAC 裁决结论
type DelegationDecision struct {
	ActionType     string                 `json:"action_type"`
	Target         string                 `json:"target"`
	Tier           TierLevel              `json:"tier"`
	Allowed        bool                   `json:"allowed"`
	RequiresHITL   bool                   `json:"requires_hitl"`
	Reason         string                 `json:"reason"`
	AuditPayload   map[string]interface{} `json:"audit_payload,omitempty"`
	Timestamp      time.Time              `json:"timestamp"`
}

// ToolExecutionResult 工具受控执行结果
type ToolExecutionResult struct {
	ToolID           string        `json:"tool_id"`
	BotID            string        `json:"bot_id"`
	ExitCode         int           `json:"exit_code"`
	Output           string        `json:"output"`
	DurationMs       int64         `json:"duration_ms"`
	NetworkTriggered bool          `json:"network_triggered"`
	EgressTarget     string        `json:"egress_target,omitempty"`
	AuditLogID       string        `json:"audit_log_id,omitempty"`
}
