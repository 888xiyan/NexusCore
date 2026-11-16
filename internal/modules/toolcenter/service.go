package toolcenter

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// NetworkAuditCallback 网络外联审计通知回调函数
type NetworkAuditCallback func(botID string, toolID string, target string, timestamp time.Time)

// Service 工具中心核心无头服务接口
type Service interface {
	RegisterTool(tool ToolDefinition) error
	GetTool(toolID string) (ToolDefinition, error)
	ListTools(categoryFilter ToolCategory) []ToolDefinition
	
	// 可编辑 Skill 增删改查
	CreateSkill(skill ToolDefinition) error
	UpdateSkill(skillID string, code string, schema string) error
	DeleteSkill(skillID string) error

	// 授权矩阵管理
	GrantTool(grant BotToolGrant) error
	RevokeGrant(botID, toolID string) error
	ListGrants(botID string) []BotToolGrant
	CheckAccess(botID, toolID string, required PermissionLevel) error

	// 中台代批 RBAC 网关
	EvaluateDelegation(proposerBotID, actionType string, params map[string]interface{}) DelegationDecision

	// 受控沙盒执行与轨迹审计留痕
	ExecuteTool(botID, toolID string, params map[string]interface{}, sandboxRun func() (string, int, error)) (ToolExecutionResult, error)

	// 设置网络外联通知监听
	SetNetworkAuditCallback(cb NetworkAuditCallback)
}

// ToolCenterService 工具中心服务实现
type ToolCenterService struct {
	mu             sync.RWMutex
	tools          map[string]ToolDefinition
	grants         map[string]map[string]BotToolGrant // botID -> toolID -> BotToolGrant
	delegationGate *DelegationGate
	networkAuditCb NetworkAuditCallback
}

// NewToolCenterService 构造工具中心服务实例并装载内置系统工具
func NewToolCenterService() *ToolCenterService {
	s := &ToolCenterService{
		tools:          make(map[string]ToolDefinition),
		grants:         make(map[string]map[string]BotToolGrant),
		delegationGate: NewDelegationGate(),
	}

	// 注册默认内置不可编辑系统工具
	s.initBuiltinSystemTools()
	return s
}

// initBuiltinSystemTools 初始化系统原生工具（不可在线编辑源码）
func (s *ToolCenterService) initBuiltinSystemTools() {
	now := time.Now()
	builtinTools := []ToolDefinition{
		{
			ToolID:             "sys.probe.cli",
			Name:               "探针受控 CLI 执行器",
			Description:        "在本地 agent-daemon 探针受控沙盒中运行白名单系统指令与开发工具",
			Category:           CategorySystemNative,
			IsEditable:         false,
			Runtime:            "native",
			ConfigSchema:       `{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}`,
			NetworkRequirement: NetworkNone,
			Tier:               Tier2DangerousHITL,
			Version:            "1.0.0",
			Author:             "NexusCore Kernel",
			CreatedAt:          now,
			UpdatedAt:          now,
		},
		{
			ToolID:             "sys.search.web",
			Name:               "受控网络搜索探针",
			Description:        "通过沙盒受限 443 端口发起网络检索，强制触发外联审计与中台通知",
			Category:           CategorySystemNative,
			IsEditable:         false,
			Runtime:            "native",
			ConfigSchema:       `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
			NetworkRequirement: NetworkEgressWhitelist,
			Tier:               Tier1SafeControlled,
			Version:            "1.0.0",
			Author:             "NexusCore Kernel",
			CreatedAt:          now,
			UpdatedAt:          now,
		},
		{
			ToolID:             "sys.git.status",
			Name:               "Git 状态查询驱动",
			Description:        "获取代码仓库只读变更状态与分支元数据",
			Category:           CategorySystemNative,
			IsEditable:         false,
			Runtime:            "native",
			ConfigSchema:       `{"type":"object"}`,
			NetworkRequirement: NetworkNone,
			Tier:               Tier1SafeControlled,
			Version:            "1.0.0",
			Author:             "NexusCore Kernel",
			CreatedAt:          now,
			UpdatedAt:          now,
		},
	}

	for _, tool := range builtinTools {
		_ = s.RegisterTool(tool)
	}
}

func (s *ToolCenterService) SetNetworkAuditCallback(cb NetworkAuditCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networkAuditCb = cb
}

func (s *ToolCenterService) RegisterTool(tool ToolDefinition) error {
	if tool.ToolID == "" {
		return ErrEmptyToolID
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tools[tool.ToolID]; exists {
		return ErrToolAlreadyExists
	}
	if tool.CreatedAt.IsZero() {
		tool.CreatedAt = time.Now()
	}
	tool.UpdatedAt = tool.CreatedAt
	s.tools[tool.ToolID] = tool
	return nil
}

func (s *ToolCenterService) GetTool(toolID string) (ToolDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tool, exists := s.tools[toolID]
	if !exists {
		return ToolDefinition{}, ErrToolNotFound
	}
	return tool, nil
}

func (s *ToolCenterService) ListTools(categoryFilter ToolCategory) []ToolDefinition {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []ToolDefinition
	for _, tool := range s.tools {
		if categoryFilter == "" || tool.Category == categoryFilter {
			result = append(result, tool)
		}
	}
	return result
}

// CreateSkill 创建可编辑技能（Skill）
func (s *ToolCenterService) CreateSkill(skill ToolDefinition) error {
	skill.Category = CategoryEditableSkill
	skill.IsEditable = true
	return s.RegisterTool(skill)
}

// UpdateSkill 更新可编辑技能的源码与参数契约
func (s *ToolCenterService) UpdateSkill(skillID string, code string, schema string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tool, exists := s.tools[skillID]
	if !exists {
		return ErrToolNotFound
	}
	if !tool.IsEditable {
		return ErrToolNotEditable
	}

	tool.Code = code
	if schema != "" {
		tool.ConfigSchema = schema
	}
	tool.UpdatedAt = time.Now()
	s.tools[skillID] = tool
	return nil
}

// DeleteSkill 删除可编辑技能
func (s *ToolCenterService) DeleteSkill(skillID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tool, exists := s.tools[skillID]
	if !exists {
		return ErrToolNotFound
	}
	if !tool.IsEditable {
		return ErrToolNotEditable
	}

	delete(s.tools, skillID)
	// 同步级联清理授权记录
	for botID := range s.grants {
		delete(s.grants[botID], skillID)
	}
	return nil
}

// GrantTool 向 Bot 授予指定工具的权限
func (s *ToolCenterService) GrantTool(grant BotToolGrant) error {
	if grant.BotID == "" || grant.ToolID == "" {
		return ErrInvalidGrant
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tools[grant.ToolID]; !exists {
		return ErrToolNotFound
	}

	if grant.GrantID == "" {
		grant.GrantID = fmt.Sprintf("grant_%s_%s", grant.BotID, grant.ToolID)
	}
	if grant.GrantedAt.IsZero() {
		grant.GrantedAt = time.Now()
	}
	if grant.Status == "" {
		grant.Status = "active"
	}

	if s.grants[grant.BotID] == nil {
		s.grants[grant.BotID] = make(map[string]BotToolGrant)
	}
	s.grants[grant.BotID][grant.ToolID] = grant
	return nil
}

// RevokeGrant 吊销 Bot 的工具权限
func (s *ToolCenterService) RevokeGrant(botID, toolID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if botGrants, exists := s.grants[botID]; exists {
		if grant, found := botGrants[toolID]; found {
			grant.Status = "revoked"
			botGrants[toolID] = grant
			return nil
		}
	}
	return ErrToolAccessDenied
}

// ListGrants 查询指定 Bot 获得的全部有效授权
func (s *ToolCenterService) ListGrants(botID string) []BotToolGrant {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []BotToolGrant
	if botGrants, exists := s.grants[botID]; exists {
		now := time.Now()
		for _, g := range botGrants {
			if g.Status == "active" {
				if g.ExpiresAt != nil && g.ExpiresAt.Before(now) {
					continue
				}
				result = append(result, g)
			}
		}
	}
	return result
}

// CheckAccess 校验指定 Bot 是否拥有执行指定工具的权限
func (s *ToolCenterService) CheckAccess(botID, toolID string, required PermissionLevel) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tool, exists := s.tools[toolID]
	if !exists {
		return ErrToolNotFound
	}

	// 系统核心 Bot（user_bot / security_bot）拥有全局特权
	if botID == "user_bot" || botID == "security_bot" {
		return nil
	}

	botGrants, exists := s.grants[botID]
	if !exists {
		return fmt.Errorf("%w: bot '%s' has no grants", ErrToolAccessDenied, botID)
	}

	grant, hasGrant := botGrants[toolID]
	if !hasGrant || grant.Status != "active" {
		return fmt.Errorf("%w: bot '%s' not granted tool '%s'", ErrToolAccessDenied, botID, toolID)
	}

	if grant.ExpiresAt != nil && grant.ExpiresAt.Before(time.Now()) {
		return fmt.Errorf("%w: grant for tool '%s' has expired", ErrToolAccessDenied, toolID)
	}

	// 权限层级比较: read_only < execute < admin
	if required == PermissionAdmin && grant.Permission != PermissionAdmin {
		return fmt.Errorf("%w: requires admin permission", ErrToolAccessDenied)
	}
	if required == PermissionExecute && grant.Permission == PermissionReadOnly {
		return fmt.Errorf("%w: read_only permission insufficient for execute", ErrToolAccessDenied)
	}

	_ = tool
	return nil
}

// EvaluateDelegation 中台 Bot 代批 RBAC 裁决
func (s *ToolCenterService) EvaluateDelegation(proposerBotID, actionType string, params map[string]interface{}) DelegationDecision {
	return s.delegationGate.Evaluate(proposerBotID, actionType, params)
}

// ExecuteTool 执行受控工具并捕获沙盒执行轨迹与网络外联告警
func (s *ToolCenterService) ExecuteTool(botID, toolID string, params map[string]interface{}, sandboxRun func() (string, int, error)) (ToolExecutionResult, error) {
	// 1. 检查 Bot-Tool 授权矩阵
	if err := s.CheckAccess(botID, toolID, PermissionExecute); err != nil {
		return ToolExecutionResult{
			ToolID:   toolID,
			BotID:    botID,
			ExitCode: 126,
			Output:   err.Error(),
		}, err
	}

	tool, err := s.GetTool(toolID)
	if err != nil {
		return ToolExecutionResult{}, err
	}

	// 2. 网络隔离与审计事件触发
	networkTriggered := false
	egressTarget := ""
	if tool.NetworkRequirement == NetworkEgressWhitelist || tool.NetworkRequirement == NetworkAny {
		networkTriggered = true
		if target, ok := params["query"].(string); ok {
			egressTarget = target
		} else if target, ok := params["target"].(string); ok {
			egressTarget = target
		} else {
			egressTarget = "external_network:443"
		}

		s.mu.RLock()
		cb := s.networkAuditCb
		s.mu.RUnlock()
		if cb != nil {
			cb(botID, toolID, egressTarget, time.Now())
		}
	}

	// 3. 沙盒执行
	start := time.Now()
	output, exitCode, runErr := sandboxRun()
	durationMs := time.Since(start).Milliseconds()

	if runErr != nil && exitCode == 0 {
		exitCode = 1
	}

	paramsBytes, _ := json.Marshal(params)
	_ = paramsBytes

	return ToolExecutionResult{
		ToolID:           toolID,
		BotID:            botID,
		ExitCode:         exitCode,
		Output:           output,
		DurationMs:       durationMs,
		NetworkTriggered: networkTriggered,
		EgressTarget:     egressTarget,
		AuditLogID:       fmt.Sprintf("audit_%s_%d", toolID, time.Now().UnixNano()),
	}, runErr
}
