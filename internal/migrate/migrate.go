// Package migrate 实现内核 schema 的顺序 SQL 迁移：成对 up/down 文件 + _migrations 记录表。
// 目标库为 PostgreSQL；调用方通过 pgx 的 database/sql 驱动打开连接。
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Item struct {
	Version  int
	Name     string
	UpPath   string
	DownPath string
}

var fileRe = regexp.MustCompile(`^(\d+)_(.+)\.(up|down)\.sql$`)

// List 解析目录中的成对迁移文件；要求 up/down 配对且版本号唯一递增。
func List(dir string, list func(dir string) ([]string, error)) ([]Item, error) {
	names, err := list(dir)
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录 %s: %w", dir, err)
	}
	m := map[int]*Item{}
	seen := map[string]string{}
	for _, name := range names {
		if strings.HasPrefix(name, ".") {
			continue
		}
		mo := fileRe.FindStringSubmatch(name)
		if mo == nil {
			return nil, fmt.Errorf("不合法的迁移文件名 %q（期望 NNNN_描述.up.sql / .down.sql）", name)
		}
		v, err := strconv.Atoi(mo[1])
		if err != nil {
			return nil, fmt.Errorf("迁移版本号解析失败 %q: %w", name, err)
		}
		key := fmt.Sprintf("%d|%s|%s", v, mo[2], mo[3])
		if prev, ok := seen[key]; ok {
			return nil, fmt.Errorf("迁移冲突：%d_%s 的 %s 文件同时被 %q 与 %q 使用", v, mo[2], mo[3], prev, name)
		}
		seen[key] = name
		it, ok := m[v]
		if !ok {
			it = &Item{Version: v, Name: mo[2]}
			m[v] = it
		}
		switch mo[3] {
		case "up":
			it.UpPath = name
		case "down":
			it.DownPath = name
		}
	}
	var out []Item
	for _, it := range m {
		if it.UpPath == "" || it.DownPath == "" {
			return nil, fmt.Errorf("迁移 %04d_%s 缺少 %s 文件", it.Version, it.Name, map[bool]string{true: "down", false: "up"}[it.UpPath != ""])
		}
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Probe 检查 DSN 可连通性并按当前 search_path 回显迁移记录数。
func Probe(ctx context.Context, db *sql.DB) (int, error) {
	if err := db.PingContext(ctx); err != nil {
		return 0, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return 0, fmt.Errorf("读取迁移状态: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM _migrations").Scan(&n); err != nil {
		return 0, fmt.Errorf("统计 _migrations: %w", err)
	}
	return n, nil
}

// EnsureTable 创建迁移记录表（不存在时）。
func EnsureTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS _migrations (
  version     INT          NOT NULL PRIMARY KEY,
  name        VARCHAR(128) NOT NULL,
  applied_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
)`)
	return err
}

// Up / Down 按 List 顺序或逆序执行到 to 指定版本（nil 表示全部）。
func Run(ctx context.Context, db *sql.DB, items []Item, up bool, read func(path string) ([]byte, error), to *int) error {
	if up {
		if err := EnsureTable(ctx, db); err != nil {
			return fmt.Errorf("创建 _migrations: %w", err)
		}
	}
	applied := map[int]bool{}

	rows, err := db.QueryContext(ctx, "SELECT version FROM _migrations")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				return err
			}
			applied[v] = true
		}
	} else if up {
		return fmt.Errorf("查询迁移记录: %w", err)
	}
	_ = rows.Err()

	exec := func(path string) error {
		body, err := read(path)
		if err != nil {
			return fmt.Errorf("读取 %s: %w", path, err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("执行 %s: %w", path, err)
		}
		return nil
	}

	if up {
		for _, it := range items {
			if applied[it.Version] || (to != nil && it.Version > *to) {
				continue
			}
			if err := exec(it.UpPath); err != nil {
				return err
			}
			_, err := db.ExecContext(ctx, "INSERT INTO _migrations (version, name) VALUES ($1, $2)", it.Version, it.Name)
			if err != nil {
				return fmt.Errorf("记录迁移 %04d: %w", it.Version, err)
			}
			fmt.Printf("up   %04d_%s\n", it.Version, it.Name)
		}
		return nil
	}

	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if !applied[it.Version] || (to != nil && it.Version <= *to) {
			continue
		}
		if err := exec(it.DownPath); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM _migrations WHERE version = $1", it.Version); err != nil {
			return fmt.Errorf("移除记录 %04d: %w", it.Version, err)
		}
		fmt.Printf("down %04d_%s\n", it.Version, it.Name)
	}
	return nil
}
