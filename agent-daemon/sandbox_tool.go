package main

import (
	"context"
	"encoding/json"

	"github.com/888xiyan/NexusCore/internal/modules/toolcenter"
)

// toolEgressDial 可由测试替换。为空时走系统信任根的 DNS 与 HTTPS。
var toolEgressDial func(ctx context.Context, rawURL string) (string, error)

func handleSandboxTool(payload []byte) []byte {
	var req struct {
		ToolID     string                 `json:"tool_id"`
		Authorized bool                   `json:"authorized"`
		Params     map[string]interface{} `json:"params"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		out, _ := json.Marshal(map[string]any{"exit_code": 2, "error": "解析工具沙箱负载失败"})
		return out
	}
	output, code, networked, host, err := toolcenter.Isolate(context.Background(), toolcenter.Isolation{
		ToolID:           req.ToolID,
		SearchAuthorized: req.Authorized,
		Params:           req.Params,
		Dial:             toolEgressDial,
		Run: func() (string, int, error) {
			return toolcenter.Distill("沙箱内执行完成"), 0, nil
		},
	})
	resp := map[string]any{
		"exit_code":         code,
		"output":            toolcenter.Distill(output),
		"network_triggered": networked,
		"domain":            host,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	out, _ := json.Marshal(resp)
	return out
}
