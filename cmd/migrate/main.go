// Command migrate 是内核库迁移 CLI（up / down / probe），不依赖 golang-migrate 二进制。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/888xiyan/NexusCore/internal/migrate"
	_ "github.com/go-sql-driver/mysql"
)

func main() {
	var (
		cmdStr = flag.String("cmd", "probe", "up / down / probe")
		dsn    = flag.String("dsn", "", "目标库 DSN，如 user:pass@tcp(host:3306)/nexus_kernel")
		dir    = flag.String("dir", "", "迁移目录，如 migrations/kernel")
		to     = flag.String("to", "", "迁移到的版本（可选）")
	)
	flag.Parse()

	if *dsn == "" || *dir == "" {
		fmt.Fprintln(os.Stderr, "必须提供 -dsn 与 -dir；示例：migrate -cmd probe -dsn \"$MYSQL_KERNEL_DSN\" -dir migrations/kernel")
		os.Exit(2)
	}
	listDir := func(d string) ([]string, error) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out, nil
	}
	readFile := func(name string) ([]byte, error) { return os.ReadFile(*dir + "/" + name) }

	ctx := context.Background()
	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 DSN:", err)
		os.Exit(1)
	}
	defer db.Close()

	items, err := migrate.List(*dir, listDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var toPtr *int
	if *to != "" {
		v, convErr := strconv.Atoi(*to)
		if convErr != nil {
			fmt.Fprintln(os.Stderr, "-to 需为整数版本号:", convErr)
			os.Exit(2)
		}
		toPtr = &v
	}

	switch *cmdStr {
	case "probe":
		n, perr := migrate.Probe(ctx, db)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "探活失败:", perr)
			os.Exit(1)
		}
		fmt.Printf("probe ok: 已应用迁移 %d 项\n", n)
	case "up", "down":
		if rerr := migrate.Run(ctx, db, items, *cmdStr == "up", readFile, toPtr); rerr != nil {
			fmt.Fprintln(os.Stderr, rerr)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "-cmd 仅支持 up / down / probe")
		os.Exit(2)
	}
}
