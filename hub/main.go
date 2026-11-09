// Command hub 是内核网关：Step 0 骨架，M1 前仅具备版本与配置入口。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/888xiyan/NexusCore/internal/version"
)

func main() {
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "打印版本号")
	flag.Parse()

	if showVersion {
		fmt.Println("hub", version.Version)
		os.Exit(0)
	}
	fmt.Println("hub", version.Version, "- 骨架就绪，服务器实现见内核 MVP 详设第 3 节（M1）")
}
