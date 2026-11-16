package botfactory

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// CoreService Bot 工厂无头基础服务接口
type CoreService interface {
	// 静态语法与安全边界校验 (无副作用)
	ValidateBlueprint(ctx context.Context, bp BotBlueprint) (*ValidationResult, error)

	// 创建 Bot 提案并提交至内核配置中心
	ProposeCreateBot(ctx context.Context, bp BotBlueprint, proposerBotID string) (proposalID string, err error)

	// 修改已存在 Bot 的画像与单体记忆 (生成修改提案)
	ProposeUpdateBot(ctx context.Context, botID string, updates BotBlueprint, proposerBotID string) (proposalID string, err error)

	// 触发实例优雅重启 (需权限校验，联动 Supervisor)
	RestartInstance(ctx context.Context, botID string) error

	// 获取当前系统中活跃 Bot 的蓝图视图
	GetBlueprint(ctx context.Context, botID string) (*BotBlueprint, error)
}

// RestartCoordinator 重启协调器钩子函数定义
type RestartCoordinator func(ctx context.Context, botID string) error

type botFactoryService struct {
	mu          sync.RWMutex
	blueprints  map[string]*BotBlueprint
	proposals   map[string]*Proposal
	restartHook RestartCoordinator
}

// Option 构建选项
type Option func(*botFactoryService)

// WithRestartCoordinator 注入 Supervisor 重启协调器实现
func WithRestartCoordinator(coordinator RestartCoordinator) Option {
	return func(s *botFactoryService) {
		s.restartHook = coordinator
	}
}

// NewService 创建 Bot 工厂基础服务实例
func NewService(opts ...Option) CoreService {
	s := &botFactoryService{
		blueprints: make(map[string]*BotBlueprint),
		proposals:  make(map[string]*Proposal),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ValidateBlueprint 静态语法与安全边界校验 (无副作用)
func (s *botFactoryService) ValidateBlueprint(ctx context.Context, bp BotBlueprint) (*ValidationResult, error) {
	val := ValidateBlueprint(bp)
	return &val, nil
}

// ProposeCreateBot 校验蓝图并生成 Bot 创建配置提案
func (s *botFactoryService) ProposeCreateBot(ctx context.Context, bp BotBlueprint, proposerBotID string) (string, error) {
	// 1. 静态合规性校验
	val := ValidateBlueprint(bp)
	if !val.Valid {
		return "", fmt.Errorf("%w: %s", ErrBlueprintInvalid, strings.Join(val.Violations, "; "))
	}

	botID := bp.BotID

	s.mu.Lock()
	defer s.mu.Unlock()

	// 2. 重复性校验
	if _, exists := s.blueprints[botID]; exists {
		return "", ErrBotAlreadyExists
	}

	// 3. 生成配置提案
	proposal, err := CreateBotProposal(bp, proposerBotID)
	if err != nil {
		return "", err
	}

	// 4. 保存提案与蓝图快照
	s.proposals[proposal.ProposalID] = proposal
	bpCopy := bp
	s.blueprints[botID] = &bpCopy

	return proposal.ProposalID, nil
}

// ProposeUpdateBot 修改已存在 Bot 的画像与单体记忆
func (s *botFactoryService) ProposeUpdateBot(ctx context.Context, botID string, updates BotBlueprint, proposerBotID string) (string, error) {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return "", ErrBotNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. 检查 Bot 是否存在
	existing, exists := s.blueprints[botID]
	if !exists {
		return "", ErrBotNotFound
	}

	// 确保更新目标一致
	updates.BotID = botID
	if updates.DisplayName == "" {
		updates.DisplayName = existing.DisplayName
	}
	if updates.Role == "" {
		updates.Role = existing.Role
	}
	if updates.SystemPrompt == "" {
		updates.SystemPrompt = existing.SystemPrompt
	}

	// 2. 校验更新项
	val := ValidateBlueprint(updates)
	if !val.Valid {
		return "", fmt.Errorf("%w: %s", ErrBlueprintInvalid, strings.Join(val.Violations, "; "))
	}

	// 3. 编排提案
	proposal, err := CreateBotProposal(updates, proposerBotID)
	if err != nil {
		return "", err
	}

	s.proposals[proposal.ProposalID] = proposal
	updatesCopy := updates
	s.blueprints[botID] = &updatesCopy

	return proposal.ProposalID, nil
}

// RestartInstance 协同 Supervisor 执行 Bot 实例的启动或优雅热重载
func (s *botFactoryService) RestartInstance(ctx context.Context, botID string) error {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return ErrBotNotFound
	}

	s.mu.RLock()
	_, exists := s.blueprints[botID]
	hook := s.restartHook
	s.mu.RUnlock()

	if !exists {
		return ErrBotNotFound
	}

	if hook != nil {
		return hook(ctx, botID)
	}

	return nil
}

// GetBlueprint 读取当前活跃 Bot 的蓝图配置快照
func (s *botFactoryService) GetBlueprint(ctx context.Context, botID string) (*BotBlueprint, error) {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return nil, ErrBotNotFound
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	bp, exists := s.blueprints[botID]
	if !exists {
		return nil, ErrBotNotFound
	}

	bpCopy := *bp
	return &bpCopy, nil
}

// GetProposal 获取生成的配置提案
func (s *botFactoryService) GetProposal(proposalID string) (*Proposal, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.proposals[proposalID]
	if !ok {
		return nil, false
	}
	cp := *p
	return &cp, true
}

// ListBlueprints 列出系统中所有蓝图
func (s *botFactoryService) ListBlueprints() []*BotBlueprint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*BotBlueprint
	for _, bp := range s.blueprints {
		cp := *bp
		result = append(result, &cp)
	}
	return result
}
