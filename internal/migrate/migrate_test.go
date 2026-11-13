package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lister(dir string) func(string) ([]string, error) {
	return func(d string) ([]string, error) {
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
}

func write(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("-- "+name), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListOrdersAndPairs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0002_second.up.sql")
	write(t, dir, "0001_first.down.sql")
	write(t, dir, "0001_first.up.sql")
	write(t, dir, "0002_second.down.sql")
	items, err := List(dir, lister(dir))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || items[0].Version != 1 || items[1].Version != 2 {
		t.Fatalf("排序错误: %+v", items)
	}
	if items[0].Name != "first" || items[1].DownPath == "" {
		t.Fatalf("字段错误: %+v", items[0])
	}
}

func TestListRejectsUnpaired(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_only.up.sql")
	_, err := List(dir, lister(dir))
	if err == nil {
		t.Fatal("缺少配对的 down/up 文件时应报错")
	}
	if !strings.Contains(err.Error(), "缺少") {
		t.Fatalf("错误信息应指明缺少文件: %v", err)
	}
}

func TestListRejectsBadName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "not-numbered.sql")
	_, err := List(dir, lister(dir))
	if err == nil {
		t.Fatal("不合法文件名应报错")
	}
}

func TestListRejectsConflictDifferentName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_first.up.sql")
	write(t, dir, "0001_first.down.sql")
	write(t, dir, "0001_conflict.up.sql")
	write(t, dir, "0001_conflict.down.sql")
	_, err := List(dir, lister(dir))
	if err == nil {
		t.Fatal("同一版本号存在不同描述时应报错")
	}
	if !strings.Contains(err.Error(), "迁移版本冲突") {
		t.Fatalf("错误信息应指明版本冲突: %v", err)
	}
}

func TestListRejectsDuplicateScript(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_first.up.sql")
	write(t, dir, "0001_first.down.sql")
	names := []string{"0001_first.up.sql", "0001_first.down.sql", "1_first.up.sql"}
	_, err := List(dir, func(string) ([]string, error) { return names, nil })
	if err == nil {
		t.Fatal("同一版本重复声明 up 脚本时应报错")
	}
	if !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("错误信息应指明冲突: %v", err)
	}
}
