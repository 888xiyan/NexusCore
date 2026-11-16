package botfactory

import "strings"

const (
	// DefaultGlobalAPIKeyRef 全局单 API Key 凭证引用
	DefaultGlobalAPIKeyRef = "global.llm.api_key"

	// DefaultBoundAgentID 默认回环探针 ID
	DefaultBoundAgentID = "loopback_agent"
)

// AssembledBotConfig 装配完成的 Bot 运行时配置结构
type AssembledBotConfig struct {
	BotID          string            `json:"bot_id"`
	DisplayName    string            `json:"display_name"`
	Role           string            `json:"role"`
	SystemPrompt   string            `json:"system_prompt"`
	Persona        string            `json:"persona"`
	InitialMemory  string            `json:"initial_memory"`
	APIKeyRef      map[string]string `json:"api_key_ref"` // {"$secret_ref": "global.llm.api_key"}
	AllowedAgents  []string          `json:"allowed_agents"`
	BoundAgentCLI  string            `json:"bound_agent_cli,omitempty"`
	BoundAgentID   string            `json:"bound_agent_id,omitempty"`
	ActivationMode string            `json:"activation_mode"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// AssembleBotConfig 实施全局单一 API Key 注入与探针白名单绑定装配。
func AssembleBotConfig(bp BotBlueprint) AssembledBotConfig {
	mode := strings.TrimSpace(bp.ActivationMode)
	if mode == "" {
		mode = "lazy"
	}

	boundAgentID := strings.TrimSpace(bp.BoundAgentID)
	if boundAgentID == "" {
		boundAgentID = DefaultBoundAgentID
	}

	agents := make([]string, 0, len(bp.AllowedAgents)+1)
	hasBound := false
	for _, a := range bp.AllowedAgents {
		trimmed := strings.TrimSpace(a)
		if trimmed != "" {
			agents = append(agents, trimmed)
			if trimmed == boundAgentID {
				hasBound = true
			}
		}
	}
	if !hasBound && boundAgentID != "" {
		agents = append(agents, boundAgentID)
	}

	meta := make(map[string]string)
	for k, v := range bp.Metadata {
		meta[k] = v
	}

	return AssembledBotConfig{
		BotID:         bp.BotID,
		DisplayName:   bp.DisplayName,
		Role:          bp.Role,
		SystemPrompt:  bp.SystemPrompt,
		Persona:       bp.Persona,
		InitialMemory: bp.InitialMemory,
		APIKeyRef: map[string]string{
			"$secret_ref": DefaultGlobalAPIKeyRef,
		},
		AllowedAgents:  agents,
		BoundAgentCLI:  bp.BoundAgentCLI,
		BoundAgentID:   boundAgentID,
		ActivationMode: mode,
		Metadata:       meta,
	}
}
