// Package migrate 实现双库顺序 SQL 迁移：成对 up/down 文件 + _migrations 记录表。
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

// Probe 检查 DSN 可连通性并回显当前迁移记录数。
func Probe(ctx context.Context, db *sql.DB) (int, error) {
	if err := db.PingContext(ctx); err != nil {
		return 0, err
	}
	var n int
	row := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = '_migrations'")
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("读取迁移状态: %w", err)
	}
	if n == 0 {
		return 0, nil
	}
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
  applied_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
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
			_, err := db.ExecContext(ctx, "INSERT INTO _migrations (version, name) VALUES (?, ?)", it.Version, it.Name)
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
		if _, err := db.ExecContext(ctx, "DELETE FROM _migrations WHERE version = ?", it.Version); err != nil {
			return fmt.Errorf("移除记录 %04d: %w", it.Version, err)
		}
		fmt.Printf("down %04d_%s\n", it.Version, it.Name)
	}
	return nil
}
