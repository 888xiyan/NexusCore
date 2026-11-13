// Package pgtest 提供内核 PostgreSQL 集成测试夹具：真库连接、迁移与运行表清理。
// 未设置 NEXUS_PG_KERNEL_DSN 时所有用例跳过（无库环境不阻塞 go test ./...）。
//
// 隔离策略：每个测试包使用独立 database（nexus_test_<包名>），其中各自建 kernel schema。
// go test ./... 会并行执行多个包，若共用一个库，各包的清理会互相抹掉在途数据。
// 用独立 database 而非独立 schema，是为了让测试中 `kernel.` 显式限定名天然成立。
// 清理用迁移角色执行：审计表对运行角色回收了 DELETE 权限，这正是被测边界。
package pgtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/888xiyan/NexusCore/internal/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	KernelDSNEnv  = "NEXUS_PG_KERNEL_DSN"
	MigrateDSNEnv = "NEXUS_PG_MIGRATE_DSN"

	SeedEchoBotConfigDocID = "00000000-0000-0000-0000-000000000001"
	SeedGlobalConfigDocID  = "00000000-0000-0000-0000-000000000002"
)

var nameSanitizer = regexp.MustCompile(`[^a-z0-9_]`)

type DB struct {
	KernelDSN  string
	MigrateDSN string
	Database   string
	Kernel     *pgxpool.Pool
	migrateDB  *sql.DB
}

// Open 打开本包专属 database 并确保迁移已应用；测试结束自动清理并关闭连接。
func Open(t *testing.T) *DB {
	t.Helper()
	kernelDSN := os.Getenv(KernelDSNEnv)
	if kernelDSN == "" {
		t.Skipf("未设置 %s，跳过 PostgreSQL 集成测试（可用 scripts/dev-pg.sh 起本地夹具）", KernelDSNEnv)
	}
	migrateDSN := os.Getenv(MigrateDSNEnv)
	if migrateDSN == "" {
		migrateDSN = kernelDSN
	}
	dbName := packageDatabase(t)
	ctx := context.Background()

	adminDB, err := sql.Open("pgx", migrateDSN)
	if err != nil {
		t.Fatalf("打开迁移 DSN: %v", err)
	}
	if err := ensureDatabase(ctx, adminDB, dbName); err != nil {
		adminDB.Close()
		t.Fatalf("准备测试 database %s: %v", dbName, err)
	}
	adminDB.Close()

	scopedMigrateDSN := withDatabase(migrateDSN, dbName)
	scopedKernelDSN := withDatabase(kernelDSN, dbName)

	mdb, err := sql.Open("pgx", scopedMigrateDSN)
	if err != nil {
		t.Fatalf("打开迁移 DSN: %v", err)
	}
	if err := ensureKernelSchema(ctx, mdb); err != nil {
		mdb.Close()
		t.Fatalf("准备 kernel schema: %v", err)
	}
	if err := applyMigrations(t, ctx, mdb); err != nil {
		mdb.Close()
		t.Fatalf("应用内核迁移: %v", err)
	}
	pool, err := pgxpool.New(ctx, scopedKernelDSN)
	if err != nil {
		mdb.Close()
		t.Fatalf("建立内核连接池: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		mdb.Close()
		t.Fatalf("内核库探活: %v", err)
	}
	d := &DB{KernelDSN: scopedKernelDSN, MigrateDSN: scopedMigrateDSN, Database: dbName, Kernel: pool, migrateDB: mdb}
	d.CleanRuntime(t)
	t.Cleanup(func() {
		d.CleanRuntime(t)
		pool.Close()
		mdb.Close()
	})
	return d
}

// packageDatabase 由包目录名派生，保证同包内共享、跨包隔离。
func packageDatabase(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取工作目录: %v", err)
	}
	name := strings.ToLower(filepath.Base(wd))
	name = nameSanitizer.ReplaceAllString(name, "_")
	if name == "" {
		name = "root"
	}
	return "nexus_test_" + name
}

func ensureDatabase(ctx context.Context, db *sql.DB, dbName string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s`, dbName))
	if err == nil {
		return nil
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) && pgErr.SQLState() == "42P04" {
		return nil // duplicate_database：已存在即复用
	}
	return fmt.Errorf("创建测试 database: %w", err)
}

func ensureKernelSchema(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE SCHEMA IF NOT EXISTS kernel AUTHORIZATION nexus_migrator`,
		`GRANT USAGE ON SCHEMA kernel TO nexus_kernel`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE nexus_migrator IN SCHEMA kernel
		   GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nexus_kernel`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE nexus_migrator IN SCHEMA kernel
		   GRANT USAGE, SELECT ON SEQUENCES TO nexus_kernel`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("执行 [%s]: %w", s, err)
		}
	}
	return nil
}

// withDatabase 替换 DSN 中的 database 名，其余部分（角色 / 主机 / 参数）保持不变。
func withDatabase(dsn, dbName string) string {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		u.Path = "/" + dbName
		return u.String()
	}
	return dsn
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取工作目录: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("未找到仓库根（go.mod）")
		}
		dir = parent
	}
}

func applyMigrations(t *testing.T, ctx context.Context, mdb *sql.DB) error {
	t.Helper()
	root := repoRoot(t)
	dir := filepath.Join(root, "migrations", "kernel")
	items, err := migrate.List(dir, func(d string) ([]string, error) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	return migrate.Run(ctx, mdb, items, true, func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, name))
	}, nil)
}

// CleanRuntime 清空运行表但保留迁移种子（echo_bot / user_bot / 两份 active 配置 / user_bot 订阅组）。
func (d *DB) CleanRuntime(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seedDocs := []any{SeedEchoBotConfigDocID, SeedGlobalConfigDocID}
	stmts := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM kernel_job_intents`, nil},
		{`DELETE FROM kernel_jobs`, nil},
		{`DELETE FROM kernel_event_dedup`, nil},
		{`DELETE FROM kernel_event_outbox`, nil},
		{`DELETE FROM kernel_ingress_log`, nil},
		{`DELETE FROM kernel_context_snapshots`, nil},
		{`DELETE FROM kernel_payloads`, nil},
		{`DELETE FROM kernel_health_reports`, nil},
		{`DELETE FROM kernel_config_documents WHERE doc_id NOT IN ($1, $2)`, seedDocs},
		{`DELETE FROM kernel_secret_refs`, nil},
		{`DELETE FROM kernel_security_decisions`, nil},
		{`DELETE FROM kernel_config_audit WHERE doc_id NOT IN ($1, $2)`, seedDocs},
		{`DELETE FROM bot_instances`, nil},
		{`DELETE FROM kernel_event_subscriptions WHERE consumer_group_id <> 'user_bot'`, nil},
		{`DELETE FROM kernel_plugin_subs WHERE consumer_group_id <> 'user_bot'`, nil},
		// 种子订阅组复位：吊销类用例会把它置为 stopped，必须还原否则污染后续用例
		{`UPDATE kernel_plugin_subs SET status='active', stopped_at=NULL WHERE consumer_group_id='user_bot'`, nil},
		{`DELETE FROM kernel_plugin_grants`, nil},
		{`DELETE FROM kernel_plugin_event_registry`, nil},
		{`DELETE FROM kernel_plugins`, nil},
		{`DELETE FROM kernel_agents`, nil},
		{`DELETE FROM kernel_projects`, nil},
		{`DELETE FROM bots WHERE bot_id NOT IN ('echo_bot','user_bot')`, nil},
		{`DELETE FROM bot_manifest WHERE bot_id NOT IN ('echo_bot','user_bot')`, nil},
		{`UPDATE bots SET observed_state='dormant', desired_state='start', enabled=true, config_version=1
		  WHERE bot_id IN ('echo_bot','user_bot')`, nil},
		{`UPDATE bot_manifest SET binary_digest=repeat('0',64), manifest_digest=repeat('0',64) WHERE bot_id IN ('echo_bot','user_bot')`, nil},
		// 种子复位：前面的用例可能激活过新版本，使种子 v1 转为 retired。
		// 非种子文档已在上面删除，此处把种子恢复为唯一 active，并还原其投影。
		{`UPDATE kernel_config_documents SET status='active', retired_at=NULL WHERE doc_id IN ($1,$2)`, seedDocs},
		{`UPDATE bot_manifest SET activation_mode='lazy', idle_ttl_seconds=300, start_timeout_seconds=30,
		        prompt_ref='echo', model='stub', config_version=1, config_doc_id=$1
		  WHERE bot_id='echo_bot'`, []any{SeedEchoBotConfigDocID}},
	}
	for _, s := range stmts {
		if _, err := d.migrateDB.ExecContext(ctx, s.query, s.args...); err != nil {
			t.Fatalf("清理运行表失败 [%s]: %v", s.query, err)
		}
	}
}

// Exec 以迁移角色执行（用于 DDL 与越权边界夹具）。
func (d *DB) Exec(t *testing.T, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.migrateDB.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("迁移角色执行失败 [%s]: %v", query, err)
	}
}

// QueryRow 以迁移角色查询单行。
func (d *DB) QueryRow(t *testing.T, query string, args ...any) *sql.Row {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return d.migrateDB.QueryRowContext(ctx, query, args...)
}

func (d *DB) String() string { return fmt.Sprintf("pgtest(database=%s)", d.Database) }
