package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// CLIRequest 是 nexus.cli.v1 协议的执行请求负载
type CLIRequest struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Prompt     string            `json:"prompt"`
	WorkingDir string            `json:"working_dir"`
	TimeoutSec int               `json:"timeout_sec"`
	Env        map[string]string `json:"env"`
	JobID      string            `json:"job_id"`
}

// CLIResponse 是 nexus.cli.v1 协议的执行结果负载
type CLIResponse struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ElapsedMS int64  `json:"elapsed_ms"`
	JobID     string `json:"job_id"`
	AgentID   string `json:"agent_id"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

const (
	MaxOutputBytes = 1024 * 1024 // 1 MB 输出硬上限
	DefaultTimeout = 30 * time.Second
	MaxTimeout     = 120 * time.Second
)

var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

// LocalCLIDriver 负责受控拉起本地 CLI，执行超时封顶、进程组隔离与输出保护
type LocalCLIDriver struct {
	agentID string
}

func NewLocalCLIDriver(agentID string) *LocalCLIDriver {
	return &LocalCLIDriver{agentID: agentID}
}

// allowedCommands 定义允许执行的 Coding Agent 二进制白名单（包含测试夹具）
var allowedCommands = map[string]bool{
	"claude":           true,
	"codex":            true,
	"pi":               true,
	"agy":              true,
	"aider":            true,
	"gemini":           true,
	"nexus-mock-agent": true,
	"mock-agent":       true,
}

// ExecuteCLI 执行目标本地 CLI 命令
func (d *LocalCLIDriver) ExecuteCLI(ctx context.Context, req CLIRequest) CLIResponse {
	start := time.Now()
	resp := CLIResponse{
		JobID:   req.JobID,
		AgentID: d.agentID,
	}

	cmdName := strings.TrimSpace(req.Command)
	if cmdName == "" {
		resp.ExitCode = 1
		resp.Error = "缺少执行命令 (command 为空)"
		resp.ElapsedMS = time.Since(start).Milliseconds()
		return resp
	}

	baseName := filepath.Base(cmdName)
	if !allowedCommands[baseName] && !strings.HasPrefix(baseName, "nexus-mock-") {
		resp.ExitCode = 1
		resp.Error = fmt.Sprintf("拒绝执行非允许的 CLI 命令: %s (不在受控白名单内)", baseName)
		resp.ElapsedMS = time.Since(start).Milliseconds()
		return resp
	}

	binPath, err := exec.LookPath(cmdName)
	if err != nil {
		resp.ExitCode = 127
		resp.Error = fmt.Sprintf("找不到目标 CLI 可执行文件: %s (%v)", cmdName, err)
		resp.ElapsedMS = time.Since(start).Milliseconds()
		return resp
	}

	// 计算超时时间
	timeout := DefaultTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
		if timeout > MaxTimeout {
			timeout = MaxTimeout
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append([]string{}, req.Args...)
	if req.Prompt != "" && len(args) == 0 {
		switch baseName {
		case "claude":
			args = []string{"-p", req.Prompt, "--print"}
		case "codex":
			args = []string{"-p", req.Prompt}
		case "pi":
			args = []string{"-p", req.Prompt}
		default:
			args = []string{req.Prompt}
		}
	}

	cmd := exec.CommandContext(execCtx, binPath, args...)

	// 设置进程组隔离（Linux Setpgid）
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if req.WorkingDir != "" {
		if fi, err := os.Stat(req.WorkingDir); err == nil && fi.IsDir() {
			cmd.Dir = req.WorkingDir
		}
	}

	// 环境变量注入
	envMap := map[string]string{
		"TERM":     "dumb",
		"NO_COLOR": "1",
		"CI":       "true",
	}
	for k, v := range req.Env {
		envMap[k] = v
	}
	cmd.Env = os.Environ()
	for k, v := range envMap {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdoutBuf, max: MaxOutputBytes}
	cmd.Stderr = &limitedWriter{w: &stderrBuf, max: MaxOutputBytes}

	if err := cmd.Start(); err != nil {
		resp.ExitCode = 1
		resp.Error = fmt.Sprintf("启动 CLI 进程失败: %v", err)
		resp.ElapsedMS = time.Since(start).Milliseconds()
		return resp
	}

	// 进程退出等待通道
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var waitErr error
	select {
	case <-execCtx.Done():
		// 超时或被取消，向整个进程组发送 SIGTERM，超时强制 SIGKILL
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			pgid, pgErr := syscall.Getpgid(cmd.Process.Pid)
			if pgErr == nil {
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
				select {
				case <-time.After(1500 * time.Millisecond):
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
				case <-done:
				}
			}
		}
		resp.ExitCode = 124
		resp.Error = fmt.Sprintf("执行超时 (限制 %v 被触发)", timeout)
	case waitErr = <-done:
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				resp.ExitCode = exitErr.ExitCode()
			} else {
				resp.ExitCode = 1
				resp.Error = waitErr.Error()
			}
		} else {
			resp.ExitCode = 0
		}
	}

	resp.ElapsedMS = time.Since(start).Milliseconds()
	resp.Stdout = stripANSI(stdoutBuf.String())
	resp.Stderr = stripANSI(stderrBuf.String())

	if stdoutBuf.Len() >= MaxOutputBytes || stderrBuf.Len() >= MaxOutputBytes {
		resp.Truncated = true
	}

	return resp
}

type limitedWriter struct {
	w   io.Writer
	max int
	n   int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n >= l.max {
		return len(p), nil // 丢弃超过上限的部分
	}
	remaining := l.max - l.n
	if len(p) > remaining {
		wCount, err := l.w.Write(p[:remaining])
		l.n += wCount
		return len(p), err
	}
	wCount, err := l.w.Write(p)
	l.n += wCount
	return wCount, err
}

func handleCLIProtocol(driver *LocalCLIDriver, payload []byte) []byte {
	var req CLIRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		out, _ := json.Marshal(CLIResponse{
			ExitCode: 2,
			Error:    fmt.Sprintf("解析 nexus.cli.v1 负载失败: %v", err),
		})
		return out
	}
	resp := driver.ExecuteCLI(context.Background(), req)
	out, _ := json.Marshal(resp)
	return out
}
