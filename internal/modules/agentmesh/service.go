package agentmesh

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CoreService 探针网格无头基础服务接口
type CoreService interface {
	// 探针资产与拓扑查询
	ListProbes(ctx context.Context, filter ProbeFilter) ([]*ProbeInfo, error)
	GetProbe(ctx context.Context, agentID string) (*ProbeInfo, error)

	// 受控同步调度执行 (由 Bot 运行时或业务插件调用)
	Execute(ctx context.Context, req ExecutionRequest) (*ExecutionResult, error)

	// 受控流式调度 (支持实时捕获终端帧与中间进度)
	ExecuteStream(ctx context.Context, req ExecutionRequest) (<-chan StreamFrame, error)

	// CLI 驱动动态登记 (与 M5 本地 Agent 探测结果联动)
	RegisterCLIDriver(ctx context.Context, agentID string, driverManifest CLIDriverManifest) error
}

type agentMeshService struct {
	registry *MeshRegistry
	executor CommandExecutor
}

// Option 服务构建配置项
type Option func(*agentMeshService)

// WithExecutor 注入自定义命令执行器 (常用于 Mock 探针测试)
func WithExecutor(executor CommandExecutor) Option {
	return func(s *agentMeshService) {
		s.executor = executor
	}
}

// WithRegistry 注入已有注册表
func WithRegistry(registry *MeshRegistry) Option {
	return func(s *agentMeshService) {
		s.registry = registry
	}
}

// NewService 创建受控探针网格基础服务实例
func NewService(opts ...Option) CoreService {
	s := &agentMeshService{
		registry: NewMeshRegistry(),
		executor: &DefaultOSCommandExecutor{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Registry 暴露内部注册表供上层或测试直接管理探针资产
func (s *agentMeshService) Registry() *MeshRegistry {
	return s.registry
}

// ListProbes 检索当前探针资产与健康状态拓扑
func (s *agentMeshService) ListProbes(ctx context.Context, filter ProbeFilter) ([]*ProbeInfo, error) {
	probes := s.registry.ListProbes(filter)
	return probes, nil
}

// GetProbe 获取指定 Agent 探针的详细资产与拓扑信息
func (s *agentMeshService) GetProbe(ctx context.Context, agentID string) (*ProbeInfo, error) {
	p, err := s.registry.GetProbe(agentID)
	if err != nil {
		return nil, err
	}
	if p.Status == ProbeStatusOffline {
		return nil, ErrPeerUnavailable
	}
	return p, nil
}

// RegisterCLIDriver 动态登记外部 CLI 驱动
func (s *agentMeshService) RegisterCLIDriver(ctx context.Context, agentID string, driverManifest CLIDriverManifest) error {
	return s.registry.RegisterCLIDriver(agentID, driverManifest)
}

// Execute 受控同步调度物理探针执行命令，集成两阶段噪音拦截与结果蒸馏
func (s *agentMeshService) Execute(ctx context.Context, req ExecutionRequest) (*ExecutionResult, error) {
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		return nil, ErrInvalidAgentID
	}

	// 1. 探针在线状态校验
	probe, err := s.registry.GetProbe(agentID)
	if err != nil || probe.Status == ProbeStatusOffline {
		return nil, ErrPeerUnavailable
	}

	// 2. 协议与能力准入鉴权 (Default-Deny: 指定 ProtocolID 时探针必须显式声明并具备该能力)
	protocolID := strings.TrimSpace(req.ProtocolID)
	if protocolID != "" {
		matched := false
		for _, capItem := range probe.Capabilities {
			if capItem == protocolID {
				matched = true
				break
			}
		}
		if !matched {
			return nil, ErrProtocolDenied
		}
	}

	// 3. 解析命令与参数
	cmdName, args, err := ParseCommandPayload(req.Payload)
	if err != nil {
		return nil, fmt.Errorf("解析 payload 失败: %w", err)
	}

	// 4. 白名单校验
	if !IsCommandAllowed(cmdName) {
		return nil, ErrInvalidCommand
	}

	// 5. 超时时间钳位 (默认 30s，最大 120s)
	timeout := ClampTimeout(req.TimeoutSec)

	// 6. 执行命令
	start := time.Now()
	res, execErr := s.executor.Execute(ctx, cmdName, args, timeout)
	durationMs := time.Since(start).Milliseconds()

	stdout := ""
	stderr := ""
	exitCode := 0
	if res != nil {
		stdout = res.Stdout
		stderr = res.Stderr
		exitCode = res.ExitCode
		if res.DurationMs > 0 {
			durationMs = res.DurationMs
		}
	} else if execErr != nil {
		stderr = execErr.Error()
		exitCode = 1
	}

	// 7. 两阶段噪音蒸馏与 RawStreamRef 生成
	distilled, rawRef := DistillOutput(stdout, stderr, exitCode, durationMs, req.DistillRule)

	// 8. 存储全量原始输出以便追溯
	rawCombined := stdout
	if stderr != "" {
		rawCombined += "\n" + stderr
	}
	s.registry.StoreRawLog(rawRef, []byte(rawCombined))

	finalResult := &ExecutionResult{
		ExitCode:        exitCode,
		DistilledOutput: distilled,
		RawStreamRef:    rawRef,
		DurationMs:      durationMs,
	}

	if execErr != nil && exitCode == 0 {
		finalResult.ExitCode = 1
	}

	return finalResult, nil
}

// ExecuteStream 流式受控调度，支持终端逐帧回传与进度追踪
func (s *agentMeshService) ExecuteStream(ctx context.Context, req ExecutionRequest) (<-chan StreamFrame, error) {
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		return nil, ErrInvalidAgentID
	}

	probe, err := s.registry.GetProbe(agentID)
	if err != nil || probe.Status == ProbeStatusOffline {
		return nil, ErrPeerUnavailable
	}

	protocolID := strings.TrimSpace(req.ProtocolID)
	if protocolID != "" {
		matched := false
		for _, capItem := range probe.Capabilities {
			if capItem == protocolID {
				matched = true
				break
			}
		}
		if !matched {
			return nil, ErrProtocolDenied
		}
	}

	cmdName, args, err := ParseCommandPayload(req.Payload)
	if err != nil {
		return nil, fmt.Errorf("解析 payload 失败: %w", err)
	}

	if !IsCommandAllowed(cmdName) {
		return nil, ErrInvalidCommand
	}

	frameCh := make(chan StreamFrame, 16)
	timeout := ClampTimeout(req.TimeoutSec)

	go func() {
		defer close(frameCh)

		// 发送起始状态帧
		frameCh <- StreamFrame{
			StreamID:  1,
			FrameType: "status",
			Data:      []byte("started"),
		}

		res, execErr := s.executor.Execute(ctx, cmdName, args, timeout)
		if execErr != nil {
			frameCh <- StreamFrame{
				StreamID:  1,
				FrameType: "error",
				Data:      []byte(execErr.Error()),
			}
		}

		if res != nil {
			if len(res.Stdout) > 0 {
				frameCh <- StreamFrame{
					StreamID:  1,
					FrameType: "stdout",
					Data:      []byte(res.Stdout),
				}
			}
			if len(res.Stderr) > 0 {
				frameCh <- StreamFrame{
					StreamID:  1,
					FrameType: "stderr",
					Data:      []byte(res.Stderr),
				}
			}
			frameCh <- StreamFrame{
				StreamID:  1,
				FrameType: "status",
				Data:      []byte(fmt.Sprintf("exit_%d", res.ExitCode)),
			}
		}
	}()

	return frameCh, nil
}
