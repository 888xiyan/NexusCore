// Package store 是 hub 的内核运行存储：PostgreSQL kernel schema 的连接池、事务与 LISTEN 专用连接。
// 依据：内核 MVP 详设 §3.1。
// 边界：只读取 NEXUS_PG_KERNEL_DSN；不持有、不读取任何插件 DSN（memory / vault）。
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TxTimeout 是内核运行事务的上限（详设 §3.1）。
const TxTimeout = 5 * time.Second

// KernelDSNEnv 是 hub 唯一允许读取的 DSN 环境变量。
const KernelDSNEnv = "NEXUS_PG_KERNEL_DSN"

var channelRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// KernelDSNFromEnv 只读内核运行 DSN；缺失即报错，不回退到任何插件 DSN。
func KernelDSNFromEnv() (string, error) {
	dsn := os.Getenv(KernelDSNEnv)
	if dsn == "" {
		return "", fmt.Errorf("未设置 %s：hub 只使用内核运行角色 DSN", KernelDSNEnv)
	}
	return dsn, nil
}

type Store struct {
	pool *pgxpool.Pool
	dsn  string
}

// Open 建立 pgxpool 连接池。search_path 由角色的 ALTER ROLE 设定，DSN 不携带 schema。
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析内核 DSN: %w", err)
	}
	if cfg.MaxConns < 4 {
		cfg.MaxConns = 8
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("建立内核连接池: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("内核库探活失败: %w", err)
	}
	return &Store{pool: pool, dsn: dsn}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// WithKernelRuntimeTx 在 5s 超时内执行运行事务；fn 报错即整体回滚。
func (s *Store) WithKernelRuntimeTx(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	txCtx, cancel := context.WithTimeout(ctx, TxTimeout)
	defer cancel()
	tx, err := s.pool.Begin(txCtx)
	if err != nil {
		return fmt.Errorf("开启内核事务: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(txCtx))
			panic(p)
		}
	}()
	if err := fn(txCtx, tx); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(txCtx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return fmt.Errorf("%w（回滚亦失败: %v）", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(txCtx); err != nil {
		return fmt.Errorf("提交内核事务: %w", err)
	}
	return nil
}

// Now 返回数据库时钟：所有租约与重试比较必须用它，应用时钟只用于展示。
func (s *Store) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	if err := s.pool.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("读取数据库时钟: %w", err)
	}
	return now, nil
}

// NotifyTx 在事务内发通知；提交后才会送达，故与状态变更原子可见。
func NotifyTx(ctx context.Context, tx pgx.Tx, channel, payload string) error {
	if !channelRe.MatchString(channel) {
		return fmt.Errorf("非法通知频道名 %q", channel)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_notify($1, $2)", channel, payload); err != nil {
		return fmt.Errorf("发送通知 %s: %w", channel, err)
	}
	return nil
}

// ListenOptions 描述一路 LISTEN。通知只做唤醒，正确性来自兜底轮询与崩溃扫描。
type ListenOptions struct {
	Channel     string
	Handler     func(payload string)
	OnReconnect func()
	Backoff     time.Duration
}

// Listen 用独立专用连接监听（不经连接池，避免 PgBouncer transaction pooling 丢通知）。
// 断线自动重连，重连后执行一次 OnReconnect 全量扫描补偿。阻塞直到 ctx 结束。
func (s *Store) Listen(ctx context.Context, opt ListenOptions) error {
	if !channelRe.MatchString(opt.Channel) {
		return fmt.Errorf("非法监听频道名 %q", opt.Channel)
	}
	if opt.Handler == nil {
		return errors.New("Listen 必须提供 Handler")
	}
	backoff := opt.Backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	for ctx.Err() == nil {
		err := s.listenOnce(ctx, opt)
		if ctx.Err() != nil || err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (s *Store) listenOnce(ctx context.Context, opt ListenOptions) error {
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		return fmt.Errorf("建立 LISTEN 专用连接: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if _, err := conn.Exec(ctx, "LISTEN "+opt.Channel); err != nil {
		return fmt.Errorf("监听频道 %s: %w", opt.Channel, err)
	}
	if opt.OnReconnect != nil {
		opt.OnReconnect()
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("等待通知 %s: %w", opt.Channel, err)
		}
		opt.Handler(n.Payload)
	}
}
