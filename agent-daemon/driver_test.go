package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalCLIDriver_AllowedCommands(t *testing.T) {
	driver := NewLocalCLIDriver("test_probe")

	// 1. 尝试执行禁止的命令
	resp := driver.ExecuteCLI(context.Background(), CLIRequest{
		Command: "rm",
		Args:    []string{"-rf", "/tmp"},
		JobID:   "job_forbidden",
	})
	if resp.ExitCode == 0 || resp.Error == "" {
		t.Fatalf("预期拒绝执行 rm，实际输出: %+v", resp)
	}

	// 2. 尝试执行空的命令
	resp = driver.ExecuteCLI(context.Background(), CLIRequest{
		Command: "",
		JobID:   "job_empty",
	})
	if resp.ExitCode == 0 || resp.Error == "" {
		t.Fatalf("预期拒绝空命令，实际输出: %+v", resp)
	}
}

func TestLocalCLIDriver_MockExecution(t *testing.T) {
	// 创建一个测试用的 mock CLI 脚本
	tmpDir := t.TempDir()
	mockBin := filepath.Join(tmpDir, "nexus-mock-agent")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
    echo "nexus-mock-agent 1.0.0"
    exit 0
fi
echo "\033[32m[Mock]\033[0m 处理提示词: $1"
echo "代码生成: func Hello() string { return \"world\" }"
`
	if err := os.WriteFile(mockBin, []byte(script), 0755); err != nil {
		t.Fatalf("写入 mock 脚本失败: %v", err)
	}

	// 将 tmpDir 加入 PATH
	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath)
	os.Setenv("PATH", tmpDir+":"+oldPath)

	driver := NewLocalCLIDriver("test_probe")

	// 1. 版本测试
	resp := driver.ExecuteCLI(context.Background(), CLIRequest{
		Command: "nexus-mock-agent",
		Args:    []string{"--version"},
		JobID:   "job_version",
	})
	if resp.ExitCode != 0 {
		t.Fatalf("执行 mock agent 失败: %s (%v)", resp.Error, resp.Stderr)
	}
	if resp.Stdout != "nexus-mock-agent 1.0.0\n" {
		t.Fatalf("输出不符合预期: %q", resp.Stdout)
	}

	// 2. 提示词执行与 ANSI 清洗测试
	payload, _ := json.Marshal(CLIRequest{
		Command:    "nexus-mock-agent",
		Prompt:     "生成 Hello 函数",
		TimeoutSec: 10,
		JobID:      "job_exec",
	})
	rawResp := handleCLIProtocol(driver, payload)
	var cliResp CLIResponse
	if err := json.Unmarshal(rawResp, &cliResp); err != nil {
		t.Fatalf("反序列化应答失败: %v", err)
	}
	if cliResp.ExitCode != 0 {
		t.Fatalf("执行失败: %s", cliResp.Error)
	}
	// 验证 ANSI 颜色被剥离
	if cliResp.Stdout != "[Mock] 处理提示词: 生成 Hello 函数\n代码生成: func Hello() string { return \"world\" }\n" {
		t.Fatalf("ANSI 剥离或执行输出不符: %q", cliResp.Stdout)
	}
	if cliResp.ElapsedMS <= 0 {
		t.Fatalf("ElapsedMS 必须大于 0: %d", cliResp.ElapsedMS)
	}
}

func TestLocalCLIDriver_TimeoutAndKill(t *testing.T) {
	tmpDir := t.TempDir()
	mockBin := filepath.Join(tmpDir, "nexus-mock-sleep")
	// 启动一个 sleep 脚本
	script := `#!/bin/sh
sleep 10
`
	if err := os.WriteFile(mockBin, []byte(script), 0755); err != nil {
		t.Fatalf("写入 sleep 脚本失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath)
	os.Setenv("PATH", tmpDir+":"+oldPath)

	driver := NewLocalCLIDriver("test_probe")

	start := time.Now()
	resp := driver.ExecuteCLI(context.Background(), CLIRequest{
		Command:    "nexus-mock-sleep",
		TimeoutSec: 1, // 1 秒超时
		JobID:      "job_timeout",
	})
	elapsed := time.Since(start)

	if resp.ExitCode != 124 {
		t.Fatalf("期望超时退出码 124，得到: %d, err: %s", resp.ExitCode, resp.Error)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("超时回收耗时过长: %v", elapsed)
	}
}
