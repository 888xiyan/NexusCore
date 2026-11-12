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
	fmt.Println("hub", version.Version, "- 骨架就绪，内核运行时实现见内核 MVP 详设（M1）；记忆与知识由插件 plugin_memory 承接（M3）")
}
