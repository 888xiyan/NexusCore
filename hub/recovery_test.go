// 本文件覆盖内核 MVP 详设 §6.8「hub 崩溃恢复」与 §8.4 的崩溃恢复验收：
// 重启后 outbox pending 续扫、LISTEN 重连全量扫描补偿、租约过期挂起持久化、
// ingress 日志与入队同事务（失败即整体回滚，不留半截现场）。
package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/ingress"
	"github.com/888xiyan/NexusCore/hub/outbox"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/pgtest"
	"github.com/jackc/pgx/v5"
)

const (
	recoveryCredKey = "0123456789abcdef0123456789abcdef"
	recoverySnapKey = "abcdef0123456789abcdef0123456789"
)

func openStore(t *testing.T, db *pgtest.DB) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func enqueueReq(queue, key string) scheduler.EnqueueRequest {
	return scheduler.EnqueueRequest{
		QueueName: queue, JobType: "bot.request", BotID: "echo_bot",
		CorrelationID: "env_root_" + key, RootEnvelopeID: "env_root_" + key,
		EnvelopeID: "env_root_" + key, IdempotencyKey: key,
		Traceparent: "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
	}
}

// 崩溃时 outbox 已落库但未分发：重启后新实例必须续扫，事件不丢
func TestRestartResumesPendingOutbox(t *testing.T) {
	db := pgtest.Open(t)
	ctx := context.Background()
	st1 := openStore(t, db)

	err := st1.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return outbox.Publish(ctx, tx, outbox.Event{
			EventID: "evt_crash_pending", EventType: "bot.reply",
			CorrelationID: "env_crash", RootEnvelopeID: "env_crash",
			AggregateType: "job", AggregateID: "1",
			PayloadJSON: json.RawMessage(`{"summary":"x"}`),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃：分发前直接关掉旧实例（内存状态全丢）
	st1.Close()

	st2 := openStore(t, db)
	disp2 := outbox.New(st2, scheduler.New(st2, scheduler.Options{}), outbox.Options{})
	pending, err := disp2.PendingCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("重启后应仍有 1 条 pending 事件，实际 %d", pending)
	}
	events, jobs, err := disp2.DispatchOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if events != 1 || jobs != 1 {
		t.Fatalf("重启后应续扫出 1 事件 / 1 投递任务，实际 events=%d jobs=%d", events, jobs)
	}
	if pending, err = disp2.PendingCount(ctx); err != nil || pending != 0 {
		t.Fatalf("续扫后 pending 应清零: pending=%d err=%v", pending, err)
	}
}

// LISTEN 建立（含重连）后必须触发一次全量扫描补偿，不能只等通知
func TestListenFiresFullScanOnConnect(t *testing.T) {
	db := pgtest.Open(t)
	st := openStore(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var scans atomic.Int32
	got := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		done <- st.Listen(ctx, store.ListenOptions{
			Channel:     "kernel_jobs_wakeup",
			Handler:     func(p string) { got <- p },
			OnReconnect: func() { scans.Add(1) },
			Backoff:     50 * time.Millisecond,
		})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for scans.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if scans.Load() == 0 {
		t.Fatal("建立监听后未触发全量扫描补偿")
	}

	if _, err := st.Pool().Exec(ctx, `SELECT pg_notify($1,$2)`, "kernel_jobs_wakeup", "echo_bot"); err != nil {
		t.Fatalf("发送通知: %v", err)
	}
	select {
	case p := <-got:
		if p != "echo_bot" {
			t.Fatalf("通知载荷不符: %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到 LISTEN 通知")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ctx 结束后 Listen 应干净返回，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Listen 未随 ctx 结束退出")
	}
}

func TestListenRejectsInvalidChannel(t *testing.T) {
	db := pgtest.Open(t)
	st := openStore(t, db)
	for _, ch := range []string{"bad-channel", "BadChannel", "1abc", "a;DROP TABLE x"} {
		err := st.Listen(context.Background(), store.ListenOptions{Channel: ch, Handler: func(string) {}})
		if err == nil {
			t.Fatalf("非法频道名 %q 应拒绝", ch)
		}
	}
	if err := st.Listen(context.Background(), store.ListenOptions{Channel: "kernel_jobs_wakeup"}); err == nil {
		t.Fatal("缺少 Handler 应拒绝")
	}
}

// 崩溃前已执行副作用的任务：重启后租约过期必须挂起等人工确认，绝不自动重放
func TestExpiredLeaseWithSideEffectSuspendsAcrossRestart(t *testing.T) {
	db := pgtest.Open(t)
	ctx := context.Background()
	st1 := openStore(t, db)
	s1 := scheduler.New(st1, scheduler.Options{LeaseTTL: time.Second})
	j, err := s1.Enqueue(ctx, enqueueReq("echo_bot", "k-crash-side-effect"))
	if err != nil {
		t.Fatal(err)
	}
	leased, err := s1.Lease(ctx, "echo_bot", "worker-before-crash", nil)
	if err != nil || leased == nil {
		t.Fatalf("认领任务: %v", err)
	}
	if err := s1.MarkRunning(ctx, leased.JobID, leased.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := s1.DeclareIntent(ctx, j.JobID, 0, "sandbox_bash", "digest-1", json.RawMessage(`{"cmd":"git push"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s1.MarkIntentExecuted(ctx, j.JobID, 0, "executed"); err != nil {
		t.Fatal(err)
	}
	db.Exec(t, `UPDATE kernel.kernel_jobs SET lease_until = now() - interval '1 second' WHERE job_id=$1`, j.JobID)
	st1.Close() // 崩溃

	st2 := openStore(t, db)
	s2 := scheduler.New(st2, scheduler.Options{})
	suspended, requeued, err := s2.DetectExpiredLeases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if suspended != 1 || requeued != 0 {
		t.Fatalf("已执行副作用必须挂起而非自动重试: suspended=%d requeued=%d", suspended, requeued)
	}
	got, err := s2.Get(ctx, j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != scheduler.StatusSuspended || got.SuspendReason != scheduler.SuspendLeaseExpired {
		t.Fatalf("期望 suspended/lease_expired，实际 %s/%s", got.Status, got.SuspendReason)
	}
	views, err := s2.ListSuspended(ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("重启后 resume-list 应可见该任务: n=%d err=%v", len(views), err)
	}
	if next, _ := s2.Lease(ctx, "echo_bot", "worker-after-crash", nil); next != nil {
		t.Fatalf("挂起任务不得被新 worker 抢占: %+v", next)
	}
}

// 幂等且无已执行副作用的任务：重启后自动重试，不需人工介入
func TestExpiredLeaseIdempotentRetriesAcrossRestart(t *testing.T) {
	db := pgtest.Open(t)
	ctx := context.Background()
	st1 := openStore(t, db)
	s1 := scheduler.New(st1, scheduler.Options{LeaseTTL: time.Second})
	j, err := s1.Enqueue(ctx, enqueueReq("echo_bot", "k-crash-idempotent"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Lease(ctx, "echo_bot", "worker-before-crash", nil); err != nil {
		t.Fatal(err)
	}
	db.Exec(t, `UPDATE kernel.kernel_jobs SET lease_until = now() - interval '1 second' WHERE job_id=$1`, j.JobID)
	st1.Close()

	st2 := openStore(t, db)
	s2 := scheduler.New(st2, scheduler.Options{})
	suspended, requeued, err := s2.DetectExpiredLeases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if suspended != 0 || requeued != 1 {
		t.Fatalf("幂等任务应自动重试: suspended=%d requeued=%d", suspended, requeued)
	}
	got, _ := s2.Get(ctx, j.JobID)
	if got.Status != scheduler.StatusRetryBackoff {
		t.Fatalf("期望 retry_backoff，实际 %s", got.Status)
	}
}

// 失败注入：入队失败时入口日志必须一起回滚（同事务，不留半截现场）
func TestIngressLogRollsBackWhenEnqueueFails(t *testing.T) {
	db := pgtest.Open(t)
	ctx := context.Background()
	st := openStore(t, db)
	reg, err := registry.New(st, []byte(recoveryCredKey), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ing, err := ingress.New(st, reg, scheduler.New(st, scheduler.Options{}), ingress.Options{
		SnapshotKey: []byte(recoverySnapKey), PayloadDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := reg.IssueCredential(ctx, config.KindBot, config.UserBot)
	if err != nil {
		t.Fatal(err)
	}

	db.Exec(t, `CREATE FUNCTION kernel.inject_job_failure() RETURNS trigger LANGUAGE plpgsql AS $$
	            BEGIN
	              IF NEW.job_type = 'inject.fail' THEN
	                RAISE EXCEPTION '注入失败：入队阶段崩溃';
	              END IF;
	              RETURN NEW;
	            END $$`)
	db.Exec(t, `CREATE TRIGGER kernel_jobs_inject_failure BEFORE INSERT ON kernel.kernel_jobs
	            FOR EACH ROW EXECUTE FUNCTION kernel.inject_job_failure()`)
	t.Cleanup(func() {
		db.Exec(t, `DROP TRIGGER IF EXISTS kernel_jobs_inject_failure ON kernel.kernel_jobs`)
		db.Exec(t, `DROP FUNCTION IF EXISTS kernel.inject_job_failure()`)
	})

	env := envelope.NewRoot(config.UserBot, envelope.ChannelCLI, envelope.MsgRequest, "idem-inject-rollback")
	_, err = ing.Submit(ctx, env, cred.Token, []byte(`{"text":"x"}`), ingress.Route{
		QueueName: "echo_bot", BotID: "echo_bot", JobType: "inject.fail",
	})
	if err == nil {
		t.Fatal("注入失败后 Submit 应报错")
	}
	var logs, jobs int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_ingress_log WHERE envelope_id=$1`, env.EnvelopeID).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_jobs WHERE envelope_id=$1`, env.EnvelopeID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if logs != 0 || jobs != 0 {
		t.Fatalf("入队失败必须整体回滚，实际 logs=%d jobs=%d", logs, jobs)
	}

	// 去掉注入后同一链路可正常落库：日志与任务成对出现
	db.Exec(t, `DROP TRIGGER kernel_jobs_inject_failure ON kernel.kernel_jobs`)
	env2 := envelope.NewRoot(config.UserBot, envelope.ChannelCLI, envelope.MsgRequest, "idem-inject-ok")
	if _, err := ing.Submit(ctx, env2, cred.Token, []byte(`{"text":"x"}`), ingress.Route{
		QueueName: "echo_bot", BotID: "echo_bot", JobType: "bot.request",
	}); err != nil {
		t.Fatalf("正常链路应成功: %v", err)
	}
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_ingress_log WHERE envelope_id=$1`, env2.EnvelopeID).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_jobs WHERE envelope_id=$1`, env2.EnvelopeID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if logs != 1 || jobs != 1 {
		t.Fatalf("接受链路应日志与任务各 1 行，实际 logs=%d jobs=%d", logs, jobs)
	}
}

// 被拒绝的信封：留完整审计现场，但绝不产生任务
func TestRejectedEnvelopeLogsButEnqueuesNothing(t *testing.T) {
	db := pgtest.Open(t)
	ctx := context.Background()
	st := openStore(t, db)
	reg, err := registry.New(st, []byte(recoveryCredKey), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ing, err := ingress.New(st, reg, scheduler.New(st, scheduler.Options{}), ingress.Options{
		SnapshotKey: []byte(recoverySnapKey), PayloadDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := reg.IssueCredential(ctx, config.KindBot, config.UserBot)
	if err != nil {
		t.Fatal(err)
	}

	env := envelope.NewRoot(config.UserBot, envelope.ChannelCLI, envelope.MsgRequest, "idem-rejected")
	env.CausalPath = nil // 缺因果链
	res, err := ing.Submit(ctx, env, cred.Token, []byte(`{}`), ingress.Route{QueueName: "echo_bot", BotID: "echo_bot"})
	if err == nil {
		t.Fatal("缺因果链必须被拒")
	}
	if res.Decision != ingress.DecisionRejected {
		t.Fatalf("缺因果链应拒绝，实际 %s", res.Decision)
	}
	var logs, jobs int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_ingress_log WHERE envelope_id=$1 AND decision='rejected'`, env.EnvelopeID).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_jobs WHERE correlation_id=$1`, env.CorrelationID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if logs != 1 {
		t.Fatalf("拒绝必须留审计，实际 %d 行", logs)
	}
	if jobs != 0 {
		t.Fatalf("拒绝不得入队，实际 %d 行", jobs)
	}
}
