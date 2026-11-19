package toolcenter

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	ListRevisions(toolID string) []SkillRevision

	// MCP 只登记连接引用与 secret_ref
	CreateMCP(tool ToolDefinition) error
	UpdateMCP(toolID, endpointRef, secretRef string) error

	// 强绑点名
	SetNamedBots(toolID string, botIDs []string) error

	// 授权矩阵管理
	GrantTool(grant BotToolGrant) error
	RevokeGrant(botID, toolID string) error
	ListGrants(botID string) []BotToolGrant
	CheckAccess(botID, toolID string, required PermissionLevel) error
	ListTraces(botID string) []CallTrace

	// 中台代批 RBAC 网关
	EvaluateDelegation(proposerBotID, actionType string, params map[string]interface{}) DelegationDecision

	// 受控沙盒执行与轨迹审计留痕
	ExecuteTool(botID, toolID string, params map[string]interface{}, sandboxRun func() (string, int, error)) (ToolExecutionResult, error)

	// 设置网络外联通知监听
	SetNetworkAuditCallback(cb NetworkAuditCallback)
	// 中台助手尚未落地时，额外接收者只是可配置的 bot_id。user_bot 始终接收。
	SetExtraAuditRecipients(botIDs []string)
	ListAudit(toBotID string) []AuditNotice
}

// ToolCenterService 工具中心服务实现
type ToolCenterService struct {
	mu             sync.RWMutex
	tools          map[string]ToolDefinition
	grants         map[string]map[string]BotToolGrant // botID -> toolID -> BotToolGrant
	revisions      []SkillRevision
	traces         []CallTrace
	audits         []AuditNotice
	extraAudit     []string
	egressDial     func(ctx context.Context, rawURL string) (string, error)
	storePath      string
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
	s.ensureBuiltins()
	return s
}

// OpenToolCenterService 打开插件自有目录。进程重启后分类、正文与授权仍在。
func OpenToolCenterService(dir string) (*ToolCenterService, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("工具持久化目录不能为空")
	}
	s := NewToolCenterService()
	s.storePath = catalogPath(dir)
	snap, err := loadCatalog(s.storePath)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if snap.Tools != nil {
		s.tools = snap.Tools
	}
	if snap.Grants != nil {
		s.grants = snap.Grants
	}
	if snap.Revisions != nil {
		s.revisions = snap.Revisions
	}
	if snap.Traces != nil {
		s.traces = snap.Traces
	}
	if snap.Audits != nil {
		s.audits = snap.Audits
	}
	s.mu.Unlock()
	s.ensureBuiltins()
	s.mu.Lock()
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s, nil
}

func builtinToolDefs(now time.Time) []ToolDefinition {
	return []ToolDefinition{
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
			Bound:              true,
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
}

// ensureBuiltins 补齐缺失的系统工具，并保持互联网搜索为强绑。已有点名清单不被覆盖。
func (s *ToolCenterService) ensureBuiltins() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tool := range builtinToolDefs(now) {
		existing, ok := s.tools[tool.ToolID]
		if !ok {
			s.tools[tool.ToolID] = tool
			continue
		}
		if tool.ToolID == "sys.search.web" {
			existing.Bound = true
			existing.IsEditable = false
			existing.Category = CategorySystemNative
			s.tools[tool.ToolID] = existing
		}
	}
}

func (s *ToolCenterService) persistLocked() error {
	if s.storePath == "" {
		return nil
	}
	snap := catalogSnapshot{
		Tools:     s.tools,
		Grants:    s.grants,
		Revisions: s.revisions,
		Traces:    s.traces,
		Audits:    s.audits,
	}
	if err := writeCatalog(s.storePath, snap); err != nil {
		return err
	}
	return nil
}

func (s *ToolCenterService) SetNetworkAuditCallback(cb NetworkAuditCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networkAuditCb = cb
}

// SetExtraAuditRecipients 追加搜索外联的接收者。user_bot 不在此列，投递时总会带上。
func (s *ToolCenterService) SetExtraAuditRecipients(botIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extraAudit = append([]string(nil), botIDs...)
}

// SetEgressDialer 替换搜索外联的 DNS/HTTPS 拨号。测试注入本地服务，生产路径走 DialHTTPS。
func (s *ToolCenterService) SetEgressDialer(dial func(ctx context.Context, rawURL string) (string, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.egressDial = dial
}

// SetSandboxCA 让搜索外联信任指定 PEM，供本地冒烟的自签证书使用。
func (s *ToolCenterService) SetSandboxCA(path string) error {
	dial, err := DialHTTPSWithCAFile(path)
	if err != nil {
		return err
	}
	s.SetEgressDialer(dial)
	return nil
}

func (s *ToolCenterService) currentDial() func(context.Context, string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.egressDial
}

// ListAudit 返回已投递的搜索外联审计。toBotID 为空时返回全部。
func (s *ToolCenterService) ListAudit(toBotID string) []AuditNotice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []AuditNotice
	for _, notice := range s.audits {
		if toBotID == "" || notice.ToBotID == toBotID {
			result = append(result, notice)
		}
	}
	return result
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
	return s.persistLocked()
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
	if !tool.IsEditable || tool.Category != CategoryEditableSkill {
		return ErrToolNotEditable
	}

	s.revisions = append(s.revisions, SkillRevision{
		RevisionID:   fmt.Sprintf("rev_%s_%d", skillID, len(s.revisions)+1),
		ToolID:       skillID,
		Code:         tool.Code,
		ConfigSchema: tool.ConfigSchema,
		Version:      tool.Version,
		RecordedAt:   time.Now(),
	})
	tool.Code = code
	if schema != "" {
		tool.ConfigSchema = schema
	}
	tool.UpdatedAt = time.Now()
	s.tools[skillID] = tool
	return s.persistLocked()
}

// ListRevisions 返回某工具的历史版本，按记录顺序。
func (s *ToolCenterService) ListRevisions(toolID string) []SkillRevision {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []SkillRevision
	for _, rev := range s.revisions {
		if toolID == "" || rev.ToolID == toolID {
			result = append(result, rev)
		}
	}
	return result
}

// CreateMCP 登记外部 MCP。只保存 endpoint_ref 与 secret_ref，运行在探针侧。
func (s *ToolCenterService) CreateMCP(tool ToolDefinition) error {
	tool.ToolID = strings.TrimSpace(tool.ToolID)
	tool.EndpointRef = strings.TrimSpace(tool.EndpointRef)
	tool.SecretRef = strings.TrimSpace(tool.SecretRef)
	if tool.ToolID == "" {
		return ErrEmptyToolID
	}
	if tool.EndpointRef == "" {
		return fmt.Errorf("登记 MCP %s: 缺少 endpoint_ref", tool.ToolID)
	}
	if strings.TrimSpace(tool.Code) != "" {
		return fmt.Errorf("%w: MCP 不保存源码", ErrPlaintextSecret)
	}
	if err := rejectPlainSecret(tool.EndpointRef, tool.Description, tool.Name); err != nil {
		return err
	}
	if err := validateSecretRef(tool.SecretRef); err != nil {
		return err
	}
	tool.Category = CategoryExternalMCP
	tool.IsEditable = true
	tool.Code = ""
	if tool.Runtime == "" {
		tool.Runtime = "mcp_stdio"
	}
	return s.RegisterTool(tool)
}

// UpdateMCP 更新 MCP 登记并留下版本记录。
func (s *ToolCenterService) UpdateMCP(toolID, endpointRef, secretRef string) error {
	endpointRef = strings.TrimSpace(endpointRef)
	secretRef = strings.TrimSpace(secretRef)
	if endpointRef == "" {
		return fmt.Errorf("更新 MCP %s: 缺少 endpoint_ref", toolID)
	}
	if err := rejectPlainSecret(endpointRef); err != nil {
		return err
	}
	if err := validateSecretRef(secretRef); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tool, exists := s.tools[toolID]
	if !exists {
		return ErrToolNotFound
	}
	if tool.Category != CategoryExternalMCP {
		return fmt.Errorf("更新 MCP %s: 该工具不是 MCP 登记", toolID)
	}
	s.revisions = append(s.revisions, SkillRevision{
		RevisionID:  fmt.Sprintf("rev_%s_%d", toolID, len(s.revisions)+1),
		ToolID:      toolID,
		EndpointRef: tool.EndpointRef,
		SecretRef:   tool.SecretRef,
		Version:     tool.Version,
		RecordedAt:  time.Now(),
	})
	tool.EndpointRef = endpointRef
	tool.SecretRef = secretRef
	tool.UpdatedAt = time.Now()
	s.tools[toolID] = tool
	return s.persistLocked()
}

// SetNamedBots 设置强绑工具的点名清单。不改工具源码。
func (s *ToolCenterService) SetNamedBots(toolID string, botIDs []string) error {
	if toolID == "" {
		return ErrEmptyToolID
	}
	seen := make(map[string]struct{}, len(botIDs))
	named := make([]string, 0, len(botIDs))
	for _, id := range botIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		named = append(named, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tool, exists := s.tools[toolID]
	if !exists {
		return ErrToolNotFound
	}
	tool.NamedBots = named
	tool.UpdatedAt = time.Now()
	s.tools[toolID] = tool
	return s.persistLocked()
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
	return s.persistLocked()
}

// GrantTool 向 Bot 授予指定工具的权限
func (s *ToolCenterService) GrantTool(grant BotToolGrant) error {
	if grant.BotID == "" || grant.ToolID == "" {
		return ErrInvalidGrant
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tool, exists := s.tools[grant.ToolID]
	if !exists {
		return ErrToolNotFound
	}
	if tool.Bound && !containsBot(tool.NamedBots, grant.BotID) {
		return ErrToolNotNamed
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
	return s.persistLocked()
}

// RevokeGrant 吊销 Bot 的工具权限
func (s *ToolCenterService) RevokeGrant(botID, toolID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if botGrants, exists := s.grants[botID]; exists {
		if grant, found := botGrants[toolID]; found {
			grant.Status = "revoked"
			botGrants[toolID] = grant
			return s.persistLocked()
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

// ListTraces 返回插件自有轨迹。botID 为空时返回全部。
func (s *ToolCenterService) ListTraces(botID string) []CallTrace {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []CallTrace
	for _, trace := range s.traces {
		if botID == "" || trace.BotID == botID {
			result = append(result, trace)
		}
	}
	return result
}

func (s *ToolCenterService) recordTrace(botID, toolID, reason string, allowed bool, params map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces = append(s.traces, CallTrace{
		TraceID:    fmt.Sprintf("trace_%s_%d", toolID, time.Now().UnixNano()),
		BotID:      botID,
		ToolID:     toolID,
		Allowed:    allowed,
		Reason:     reason,
		ParamsNote: paramsNote(params),
		RecordedAt: time.Now(),
	})
	if len(s.traces) > 200 {
		s.traces = append([]CallTrace(nil), s.traces[len(s.traces)-200:]...)
	}
	_ = s.persistLocked()
}

func (s *ToolCenterService) recordExecution(botID, toolID, reason string, allowed bool, params map[string]interface{}, networked bool, exitCode int, durationMs int64, distilled, domain string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces = append(s.traces, CallTrace{
		TraceID:    fmt.Sprintf("trace_%s_%d", toolID, time.Now().UnixNano()),
		BotID:      botID,
		ToolID:     toolID,
		Allowed:    allowed,
		Reason:     reason,
		ParamsNote: paramsNote(params),
		Networked:  networked,
		ExitCode:   exitCode,
		DurationMs: durationMs,
		Distilled:  distilled,
		Domain:     domain,
		RecordedAt: time.Now(),
	})
	if len(s.traces) > 200 {
		s.traces = append([]CallTrace(nil), s.traces[len(s.traces)-200:]...)
	}
	_ = s.persistLocked()
}

func (s *ToolCenterService) deliverAudit(botID, toolID, domain string) {
	text := fmt.Sprintf("Bot %s 使用工具 %s 外联域名 %s", botID, toolID, domain)
	s.mu.Lock()
	recipients := []string{"user_bot"}
	seen := map[string]bool{"user_bot": true}
	for _, id := range s.extraAudit {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		recipients = append(recipients, id)
	}
	now := time.Now()
	for _, to := range recipients {
		s.audits = append(s.audits, AuditNotice{
			NoticeID:  fmt.Sprintf("audit_%s_%s_%d", toolID, to, time.Now().UnixNano()),
			ToBotID:   to,
			FromBotID: botID,
			ToolID:    toolID,
			Domain:    domain,
			Text:      text,
			At:        now,
		})
	}
	if len(s.audits) > 200 {
		s.audits = append([]AuditNotice(nil), s.audits[len(s.audits)-200:]...)
	}
	cb := s.networkAuditCb
	_ = s.persistLocked()
	s.mu.Unlock()
	if cb != nil {
		cb(botID, toolID, domain, now)
	}
}

// ExecuteTool 在授权通过后按沙箱策略执行。默认无出站；只有互联网搜索工具会走 DNS 与 HTTPS。
// 返回给调用方的 Output 是蒸馏结果，原始大输出不进入 Bot 上下文。
func (s *ToolCenterService) ExecuteTool(botID, toolID string, params map[string]interface{}, sandboxRun func() (string, int, error)) (ToolExecutionResult, error) {
	if err := s.CheckAccess(botID, toolID, PermissionExecute); err != nil {
		if errors.Is(err, ErrToolAccessDenied) {
			s.recordTrace(botID, toolID, err.Error(), false, params)
		}
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

	start := time.Now()
	output, exitCode, networked, host, runErr := Isolate(context.Background(), Isolation{
		ToolID:           tool.ToolID,
		SearchAuthorized: true,
		Params:           params,
		Dial:             s.currentDial(),
		Run:              sandboxRun,
	})
	durationMs := time.Since(start).Milliseconds()
	distilled := Distill(output)
	reason := "executed"
	allowed := runErr == nil
	if runErr != nil {
		reason = runErr.Error()
		if exitCode == 0 {
			exitCode = 1
		}
	}
	s.recordExecution(botID, toolID, reason, allowed, params, networked, exitCode, durationMs, distilled, host)
	auditID := ""
	if networked && runErr == nil {
		s.deliverAudit(botID, toolID, host)
		auditID = fmt.Sprintf("audit_%s_%d", toolID, time.Now().UnixNano())
	}

	return ToolExecutionResult{
		ToolID:           toolID,
		BotID:            botID,
		ExitCode:         exitCode,
		Output:           distilled,
		DurationMs:       durationMs,
		NetworkTriggered: networked,
		EgressTarget:     host,
		AuditLogID:       auditID,
	}, runErr
}
