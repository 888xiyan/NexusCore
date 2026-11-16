package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanner_ProbeAndCache(t *testing.T) {
	tmpDir := t.TempDir()
	mockBin := filepath.Join(tmpDir, "nexus-mock-agent")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
    echo "mock-agent v2.3.4"
    exit 0
fi
echo "OK"
`
	if err := os.WriteFile(mockBin, []byte(script), 0755); err != nil {
		t.Fatalf("写入 mock 脚本失败: %v", err)
	}

	sc := NewScanner(tmpDir)
	agents, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}

	var found bool
	for _, a := range agents {
		if a.BinName == "nexus-mock-agent" {
			found = true
			if a.Version != "2.3.4" {
				t.Errorf("版本号提取不符: %s (期望 2.3.4)", a.Version)
			}
			if a.Status != "online" {
				t.Errorf("状态不符: %s", a.Status)
			}
			break
		}
	}
	if !found {
		t.Fatalf("未在临时目录中搜寻到 nexus-mock-agent, 扫描结果: %+v", agents)
	}

	cached := sc.GetCachedAgents()
	if len(cached) != len(agents) {
		t.Fatalf("缓存大小不一致: 缓存 %d, 实际 %d", len(cached), len(agents))
	}
}
