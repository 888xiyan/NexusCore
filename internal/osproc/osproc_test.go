package osproc

import (
	"os"
	"testing"
)

func TestReadSelfProcessInfo(t *testing.T) {
	pid := os.Getpid()
	info, err := ReadProcessInfo(pid)
	if err != nil {
		t.Fatalf("读取自身进程信息失败: %v", err)
	}
	if info.PID != pid {
		t.Errorf("PID = %d, want %d", info.PID, pid)
	}
	if info.StartTime <= 0 {
		t.Errorf("StartTime = %d, want > 0", info.StartTime)
	}
	if !IsProcessAlive(pid, info.StartTime) {
		t.Errorf("IsProcessAlive 返回 false，预期为 true")
	}
	if IsProcessAlive(pid, info.StartTime+999999) {
		t.Errorf("IsProcessAlive 带错误 StartTime 应该返回 false")
	}
}
