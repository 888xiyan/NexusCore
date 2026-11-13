// Command run_dream_helper 协助触发 Auto Dream 离线整合四阶段流转
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/888xiyan/NexusCore/memory"
)

func main() {
	var (
		botID   = flag.String("bot-id", "", "Bot 身份")
		dataDir = flag.String("data-dir", "data/bots", "数据目录")
	)
	flag.Parse()

	if *botID == "" {
		fmt.Fprintln(os.Stderr, "缺少 -bot-id")
		os.Exit(1)
	}

	mgr := memory.NewManager(*dataDir)
	rep, err := mgr.RunDream(context.Background(), *botID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Auto Dream 执行失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Auto Dream 成功: %s (最终 %d 行)\n", rep.Summary, rep.MemoryLinesFinal)
}
