package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeAgentBindingKeepsCLI(t *testing.T) {
	raw := json.RawMessage(`["chat","agent-cli:codex","agent-id:old_probe"]`)
	next, err := mergeAgentBinding(raw, "local_probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(next, ",")
	if !strings.Contains(joined, "chat") || !strings.Contains(joined, "agent-cli:codex") || !strings.Contains(joined, "agent-id:local_probe") || strings.Contains(joined, "old_probe") {
		t.Fatalf("绑定结果不符: %v", next)
	}
	cleared, err := mergeAgentBinding(raw, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cleared, ","), "agent-id:") {
		t.Fatalf("清空绑定后仍有探针: %v", cleared)
	}
	id, cli := parseAgentBinding(json.RawMessage(`["agent-id:local_probe","agent-cli:codex"]`))
	if id != "local_probe" || cli != "codex" {
		t.Fatalf("解析绑定不符: %s %s", id, cli)
	}
}
