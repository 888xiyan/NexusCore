// Package agentmesh 实现无头的受控探针网格与命令调度核心模块。
// 职责：维护探针资产与 15s 心跳状态机、能力清单治理、受控执行调度、两阶段噪音蒸馏引擎。
package agentmesh

import (
	"errors"
)

// ProbeStatus 探针健康与连接状态
type ProbeStatus string

const (
	ProbeStatusOnline  ProbeStatus = "online"
	ProbeStatusBusy    ProbeStatus = "busy"
	ProbeStatusOffline ProbeStatus = "offline"
)

// DistillRule 蒸馏规则常量
const (
	DistillRuleCompilerErrorOnly = "compiler_error_only"
	DistillRuleJSONExtract       = "json_extract"
	DistillRuleCleanNoise        = "clean_noise"
)

var (
	ErrPeerUnavailable    = errors.New("探针不可用或处于离线状态")
	ErrProtocolDenied     = errors.New("未授权的协议调用")
	ErrExecutionTimeout   = errors.New("命令执行超时")
	ErrOutputLimitExceeded = errors.New("输出超过最大限制")
	ErrInvalidCommand     = errors.New("命令不在允许执行的白名单内")
	ErrDriverNotFound     = errors.New("未找到匹配的 CLI 驱动")
	ErrInvalidAgentID     = errors.New("探针 agent_id 不能为空")
)

// ProbeInfo 探针资产描述实体
type ProbeInfo struct {
	AgentID       string      `json:"agent_id"`
	DeviceName    string      `json:"device_name"`
	RemoteAddr    string      `json:"remote_addr"`
	Capabilities  []string    `json:"capabilities"` // 如 ["nexus.echo.v1", "nexus.cli.claude", "nexus.bash.v1"]
	Status        ProbeStatus `json:"status"`
	LastHeartbeat int64       `json:"last_heartbeat"` // Unix 秒级时间戳
}

// ProbeFilter 探针过滤条件
type ProbeFilter struct {
	Status     ProbeStatus `json:"status,omitempty"`
	Capability string      `json:"capability,omitempty"`
	AgentID    string      `json:"agent_id,omitempty"`
}

// ExecutionRequest 物理探针受控调用请求
type ExecutionRequest struct {
	AgentID     string `json:"agent_id"`
	ProtocolID  string `json:"protocol_id"`  // 对应插件协议，如 "nexus.cli.codex"
	Payload     []byte `json:"payload"`      // 协议特定载荷 (JSON 或命令行)
	TimeoutSec  int    `json:"timeout_sec"`  // 执行超时硬封顶 (默认 ≤ 30s，最大 ≤ 120s)
	DistillRule string `json:"distill_rule"` // 蒸馏规则标识 ("compiler_error_only", "json_extract", "clean_noise")
}

// ExecutionResult 执行结果（含蒸馏后的精炼产物）
type ExecutionResult struct {
	ExitCode        int    `json:"exit_code"`
	DistilledOutput string `json:"distilled_output"` // 精炼后的摘要文本，专供 Bot 决策
	RawStreamRef    string `json:"raw_stream_ref"`   // 完整日志存储索引 (不进 Bot 上下文，供人工回溯)
	DurationMs      int64  `json:"duration_ms"`
}

// StreamFrame 流式帧实体
type StreamFrame struct {
	StreamID  uint32 `json:"stream_id"`
	FrameType string `json:"frame_type"` // "stdout" | "stderr" | "status" | "error"
	Data      []byte `json:"data"`
}

// CLIDriverManifest CLI 驱动清单
type CLIDriverManifest struct {
	DriverName string   `json:"driver_name"`
	BinPath    string   `json:"bin_path"`
	Args       []string `json:"args"`
	MaxTimeout int      `json:"max_timeout"`
}
