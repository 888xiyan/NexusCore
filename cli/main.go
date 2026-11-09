// Command cli 是用户(user_bot)的投递与管理代理：Step 0 骨架。
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
		fmt.Println("cli", version.Version)
		os.Exit(0)
	}
	fmt.Println("NexusCore cli", version.Version, "- 用法：cli <子命令> [参数]；子命令自 M1 起")
}
