// Command agent-daemon 是跨设备探针：Step 0 骨架，M5 前仅具备版本入口。
package main

import (
	"flag"
	"fmt"

	"github.com/888xiyan/NexusCore/internal/version"
)

func main() {
	flag.Parse()
	fmt.Println("agent-daemon", version.Version)
}
