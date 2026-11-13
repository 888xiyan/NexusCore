// Command plugin_demo 是 M2 插件进程夹具：
// 模拟插件进程（L2），由 owner bot（demo_bot）拉起，响应 SIGTERM 优雅退出。
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/internal/version"
)

func main() {
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "打印版本号")
	flag.Parse()

	if showVersion {
		fmt.Println("plugin_demo", version.Version)
		os.Exit(0)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Fprintf(os.Stderr, "plugin_demo (PID %d, PGID %d) 已启动，运行中...\n", os.Getpid(), syscall.Getpgrp())

	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "plugin_demo 收到信号 %v，正在优雅退出...\n", sig)
		time.Sleep(100 * time.Millisecond) // 模拟 flush
		fmt.Fprintln(os.Stderr, "plugin_demo flush 完成，已退出")
		os.Exit(0)
	}
}
