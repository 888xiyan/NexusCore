package main

import "fmt"

// CLIDispatchAllowed 要求清单里已经保存探针绑定。未绑定的 Bot 不能把任务派到 CLI 探针。
func CLIDispatchAllowed(bound bool, agentID string) error {
	if !bound || agentID == "" {
		return fmt.Errorf("未绑定探针的 Bot 不能派发 CLI")
	}
	return nil
}
