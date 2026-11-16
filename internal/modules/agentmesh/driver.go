package agentmesh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	MaxOutputBytes = 1024 * 1024 // 1 MB 输出硬上限
	DefaultTimeout = 30 * time.Second
	MaxTimeout     = 120 * time.Second
)

// allowedCommands 定义允许执行的 Coding Agent 与受控命令白名单
var allowedCommands = map[string]bool{
	"claude":           true,
	"codex":            true,
	"pi":               true,
	"agy":              true,
	"aider":            true,
	"gemini":           true,
	"nexus-mock-agent": true,
	"mock-agent":       true,
	"echo":             true,
	"sh":               true,
	"bash":             true,
	"go":               true,
}

// IsCommandAllowed 校验命令是否在白名单中
func IsCommandAllowed(cmdName string) bool {
	base := strings.TrimSpace(cmdName)
	// 若包含路径，取最后一部分
	if idx := strings.LastIndex(base, "/"); idx != -1 {
		base = base[idx+1:]
	}
	return allowedCommands[base]
}

// ClampTimeout 将超时时间钳位在 1s ~ 120s 之间 (默认 30s)
func ClampTimeout(timeoutSec int) time.Duration {
	if timeoutSec <= 0 {
		return DefaultTimeout
	}
	d := time.Duration(timeoutSec) * time.Second
	if d > MaxTimeout {
		return MaxTimeout
	}
	return d
}

// CommandPayload 载荷结构
type CommandPayload struct {
	Cmd     string   `json:"cmd,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// ParseCommandPayload 解析载荷为命令名与参数
func ParseCommandPayload(payload []byte) (string, []string, error) {
	if len(payload) == 0 {
		return "", nil, fmt.Errorf("payload 不能为空")
	}

	var p CommandPayload
	if err := json.Unmarshal(payload, &p); err == nil {
		c := p.Cmd
		if c == "" {
			c = p.Command
		}
		if c != "" {
			parts := strings.Fields(c)
			if len(parts) > 0 {
				return parts[0], append(parts[1:], p.Args...), nil
			}
		}
	}

	// 尝试作为纯文本命令行解析
	rawStr := strings.TrimSpace(string(payload))
	parts := strings.Fields(rawStr)
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("未能解析有效命令")
	}
	return parts[0], parts[1:], nil
}

// CommandExecutionResult 底层执行响应
type CommandExecutionResult struct {
	Stdout     string
	Stderr     string
	ExitCode   int
	DurationMs int64
	Truncated  bool
}

// CommandExecutor 命令执行器抽象 (支持真实 exec 与 Mock 注入)
type CommandExecutor interface {
	Execute(ctx context.Context, cmdName string, args []string, timeout time.Duration) (*CommandExecutionResult, error)
}

// DefaultOSCommandExecutor 真实 OS 进程执行器 (带进程组隔离与输出限制)
type DefaultOSCommandExecutor struct{}

func (e *DefaultOSCommandExecutor) Execute(ctx context.Context, cmdName string, args []string, timeout time.Duration) (*CommandExecutionResult, error) {
	start := time.Now()
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, cmdName, args...)
	// 进程组隔离：设置独立的进程组 pgid
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdoutBuf, stderrBuf bytes.Buffer
	limitedOut := &limitedWriter{w: &stdoutBuf, remaining: MaxOutputBytes}
	limitedErr := &limitedWriter{w: &stderrBuf, remaining: MaxOutputBytes}

	cmd.Stdout = limitedOut
	cmd.Stderr = limitedErr

	err := cmd.Start()
	if err != nil {
		return &CommandExecutionResult{
			ExitCode:   1,
			Stderr:     fmt.Sprintf("启动命令失败: %v", err),
			DurationMs: time.Since(start).Milliseconds(),
		}, err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-timeoutCtx.Done():
		// 超时强杀整个进程组
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return &CommandExecutionResult{
			Stdout:     stdoutBuf.String(),
			Stderr:     stderrBuf.String() + "\n[ERROR] 执行超时硬截断",
			ExitCode:   124,
			DurationMs: time.Since(start).Milliseconds(),
			Truncated:  limitedOut.truncated || limitedErr.truncated,
		}, ErrExecutionTimeout

	case waitErr := <-done:
		exitCode := 0
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}

		return &CommandExecutionResult{
			Stdout:     stdoutBuf.String(),
			Stderr:     stderrBuf.String(),
			ExitCode:   exitCode,
			DurationMs: time.Since(start).Milliseconds(),
			Truncated:  limitedOut.truncated || limitedErr.truncated,
		}, nil
	}
}

type limitedWriter struct {
	w         io.Writer
	remaining int
	truncated bool
	mu        sync.Mutex
}

func (l *limitedWriter) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.remaining <= 0 {
		l.truncated = true
		return len(p), nil
	}

	if len(p) > l.remaining {
		n, err = l.w.Write(p[:l.remaining])
		l.remaining = 0
		l.truncated = true
		return len(p), err
	}

	n, err = l.w.Write(p)
	l.remaining -= n
	return n, err
}
