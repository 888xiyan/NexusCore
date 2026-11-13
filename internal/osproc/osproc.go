package osproc

import (
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
	info, err := ReadProcessInfo(pid)
	if err != nil {
		return false
	}
	if wantStartTime > 0 && info.StartTime != wantStartTime {
		return false
	}
	return true
}

// KillProcessGroup 向指定进程组发送信号。
func KillProcessGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 1 {
		return fmt.Errorf("拒绝向非法进程组 %d 发送信号", pgid)
	}
	// 负数向进程组发送
	return syscall.Kill(-pgid, sig)
}
