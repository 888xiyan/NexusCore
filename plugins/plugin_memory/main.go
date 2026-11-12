// Command plugin_memory 是记忆插件的知识服务骨架（knowledge 类插件）：
// 图谱 / 金库 / 晋升 / 上下文装配全部属于本插件，内核不感知（D-22，M3 填充实现）。
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
		fmt.Println("plugin_memory", version.Version)
		os.Exit(0)
	}
	fmt.Println("plugin_memory", version.Version, "- 插件骨架就绪；记忆与知识实现见 doc/MemoryPlugin（M3 起）")
}
