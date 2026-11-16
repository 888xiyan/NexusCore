package creator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/888xiyan/NexusCore/memory"
)

// Service 是 Bot 工厂核心服务
type Service struct {
	hubHTTP   string
	hubCred   string
	memMgr    *memory.Manager
	client    *http.Client
}

// NewService 创建 Bot 工厂服务实例
func NewService(hubHTTP, hubCred, memoryBaseDir string) *Service {
	targetHub := strings.TrimPrefix(hubHTTP, "http://")
	targetHub = strings.TrimPrefix(targetHub, "https://")
	return &Service{
		hubHTTP: targetHub,
		hubCred: hubCred,
		memMgr:  memory.NewManager(memoryBaseDir),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// CreateBotResult 创建结果
type CreateBotResult struct {
	BotID         string `json:"bot_id"`
	DisplayName   string `json:"display_name"`
	MemoryLines   int    `json:"memory_lines"`
	BoundAgentCLI string `json:"bound_agent_cli,omitempty"`
	Status        string `json:"status"`
}

// CreateBot 执行蓝图校验、四层单体记忆初始化、注册并重启 Bot 进程
func (s *Service) CreateBot(ctx context.Context, bp BotBlueprint) (*CreateBotResult, error) {
	val := ValidateBlueprint(bp)
	if !val.Valid {
		return nil, fmt.Errorf("蓝图合规校验失败: %s", strings.Join(val.Violations, "; "))
	}

	// 1. 初始化 Claude Code 四层单体记忆脚手架
	if err := s.memMgr.EnsureDirs(bp.BotID); err != nil {
		return nil, fmt.Errorf("初始化单体记忆目录失败: %w", err)
	}

	initialMem := bp.InitialMemory
	if strings.TrimSpace(initialMem) == "" {
		initialMem = fmt.Sprintf("# %s 核心单体记忆\n- 角色: %s\n- 人格: %s\n", bp.DisplayName, bp.Role, bp.Persona)
	}
	if err := s.memMgr.SaveInjectedContext(bp.BotID, initialMem); err != nil {
		return nil, fmt.Errorf("写入初始 MEMORY.md 失败: %w", err)
	}

	// 2. 组装 capabilities 注入 Agent 绑定标签
	caps := []string{"chat"}
	if bp.BoundAgentCLI != "" {
		caps = append(caps, "agent-cli:"+bp.BoundAgentCLI)
	}
	agentID := bp.BoundAgentID
	if agentID == "" {
		agentID = "loopback_agent"
	}
	caps = append(caps, "agent-id:"+agentID)

	actMode := bp.ActivationMode
	if actMode == "" {
		actMode = "lazy"
	}

	// 3. 向 Hub POST /v1/bots 注册 bot_manifest
	createPayload := map[string]any{
		"bot_id":          bp.BotID,
		"display_name":    bp.DisplayName,
		"role":            "plugin",
		"activation_mode": actMode,
		"capabilities":    caps,
		"memory_profile":  "personal_stack_v1",
		"prompt_ref":      bp.SystemPrompt,
		"model":           bp.Persona,
	}
	body, err := json.Marshal(createPayload)
	if err != nil {
		return nil, fmt.Errorf("编码创建请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+s.hubHTTP+"/v1/bots", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构建注册请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.hubCred != "" {
		req.Header.Set("X-Nexus-Credential", s.hubCred)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Hub 注册 Bot 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errResp, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Hub 注册 Bot 拒绝 (HTTP %d): %s", resp.StatusCode, string(errResp))
	}

	// 4. 调用 Supervisor 重启端点，毫秒级拉起/重启生效
	restartReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://%s/v1/bots/%s/restart", s.hubHTTP, bp.BotID), nil)
	if err == nil {
		if s.hubCred != "" {
			restartReq.Header.Set("X-Nexus-Credential", s.hubCred)
		}
		restartResp, rErr := s.client.Do(restartReq)
		if rErr == nil {
			_ = restartResp.Body.Close()
		}
	}

	return &CreateBotResult{
		BotID:         bp.BotID,
		DisplayName:   bp.DisplayName,
		MemoryLines:   memory.CountLines(initialMem),
		BoundAgentCLI: bp.BoundAgentCLI,
		Status:        "created_and_activated",
	}, nil
}
