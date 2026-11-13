package osproc

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ProcessInfo 包含从 /proc/<pid>/stat 解析出的核心进程信息
type ProcessInfo struct {
	PID       int
	PPID      int
	PGRP      int
	StartTime int64
	State     byte
}

// ReadProcessInfo 读取指定 pid 的进程信息。
func ReadProcessInfo(pid int) (*ProcessInfo, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, err
	}
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx == -1 || idx+2 >= len(s) {
		return nil, fmt.Errorf("非法 /proc/%d/stat 格式", pid)
	}
	fields := strings.Fields(s[idx+2:])
	// field 3 (state) is index 0
	// field 4 (ppid)  is index 1
	// field 5 (pgrp)  is index 2
	// field 22 (starttime) is index 19 (22 - 3)
	if len(fields) < 20 {
		return nil, fmt.Errorf("/proc/%d/stat 字段不足 (%d < 20)", pid, len(fields))
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil, fmt.Errorf("解析 ppid 失败: %w", err)
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("解析 pgrp 失败: %w", err)
	}
	startTime, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("解析 starttime 失败: %w", err)
	}
	state := byte(0)
	if len(fields[0]) > 0 {
		state = fields[0][0]
	}

	return &ProcessInfo{
		PID:       pid,
		PPID:      ppid,
		PGRP:      pgrp,
		StartTime: startTime,
		State:     state,
	}, nil
}

// IsProcessAlive 检查指定 pid 是否存活且 starttime 双匹配（防 PID 复用）。
func IsProcessAlive(pid int, wantStartTime int64) bool {
	if pid <= 1 || wantStartTime <= 0 {
		return false
	}
	info, err := ReadProcessInfo(pid)
	if err != nil {
		return false
	}
	return info.StartTime == wantStartTime && info.State != 'Z' && info.State != 'X'
}

// SignalProcessGroup 校验原始组长身份后才向独立进程组发信号。
// 原进程消失或 PID 被复用时返回可由 errors.Is(err, os.ErrNotExist) 识别的错误。
// 允许尚未 Wait 的僵尸组长作为身份锚点以清理后代；组长已被回收则 fail closed。
// 调用方仍须校验业务归属及保留 PID 黑名单，不能用本函数替代授权。
func SignalProcessGroup(pid, pgid int, start int64, sig syscall.Signal) error {
	if pid <= 1 || pgid != pid || start <= 0 {
		return fmt.Errorf("拒绝向身份不完整的进程组发送信号: pid=%d pgid=%d start=%d", pid, pgid, start)
	}
	info, err := ReadProcessInfo(pid)
	if err != nil {
		return fmt.Errorf("校验进程组 %d 身份: %w", pgid, err)
	}
	if info.StartTime != start {
		return fmt.Errorf("原进程 %d 已消失（starttime 不符）: %w", pid, os.ErrNotExist)
	}
	if info.PGRP != pgid {
		return fmt.Errorf("拒绝向进程组发送信号: pid=%d 实际 pgid=%d，期望 %d", pid, info.PGRP, pgid)
	}
	if err := KillProcessGroup(pgid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("原进程组 %d 已消失: %w", pgid, os.ErrNotExist)
		}
		return fmt.Errorf("向进程组 %d 发送信号 %v: %w", pgid, sig, err)
	}
	return nil
}

// KillProcessGroup 是未校验身份的底层操作；业务调用方应使用 SignalProcessGroup。
func KillProcessGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 1 {
		return fmt.Errorf("拒绝向非法进程组 %d 发送信号", pgid)
	}
	// 负数向进程组发送
	return syscall.Kill(-pgid, sig)
}
