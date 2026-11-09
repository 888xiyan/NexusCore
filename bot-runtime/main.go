// Command bot-runtime 是 Bot 独立二进制运行时：Step 0 骨架。
package main

import (
	"flag"
	"fmt"

	"github.com/888xiyan/NexusCore/internal/version"
)

func main() {
	flag.Parse()
	fmt.Println("bot-runtime", version.Version)
}
