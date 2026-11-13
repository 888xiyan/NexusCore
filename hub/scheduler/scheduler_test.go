package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/pgtest"
)

func newScheduler(t *testing.T, opt Options) (*Scheduler, *pgtest.DB) {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	return New(st, opt), db
}

func req(queue, key string) EnqueueRequest {
	return EnqueueRequest{
		QueueName:      queue,
		JobType:        "bot.echo",
		BotID:          "echo_bot",
		CorrelationID:  "env_root_" + key,
		RootEnvelopeID: "env_root_" + key,
		EnvelopeID:     "env_root_" + key,
		IdempotencyKey: key,
		Traceparent:    "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
	}
}

func TestEnqueueAndLease(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	j, err := s.Enqueue(ctx, req("echo_bot", "k1"))
	if err != nil {
		t.Fatalf("入队: %v", err)
	}
	if j.Status != StatusQueued {
		t.Fatalf("期望 queued，得到 %s", j.Status)
	}
	got, err := s.Lease(ctx, "echo_bot", "worker-1", nil)
	if err != nil {
		t.Fatalf("认领: %v", err)
	}
	if got == nil || got.JobID != j.JobID {
		t.Fatalf("应认领到刚入队的任务: %+v", got)
	}
	if got.Status != StatusLeasing || got.LeaseToken == "" || got.LeaseUntil == nil {
		t.Fatalf("认领必须带租约令牌与到期时间: %+v", got)
	}
	if got.LeaseOwner != "worker-1" {
		t.Fatalf("lease_owner 不符: %s", got.LeaseOwner)
	}
	// 同一任务不得被第二个 worker 抢到
	again, err := s.Lease(ctx, "echo_bot", "worker-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if again != nil {
		t.Fatalf("已认领任务不得重复投递: %+v", again)
	}
}

func TestEnqueueRequiresCausalChain(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	r := req("echo_bot", "k-no-causal")
	r.CorrelationID = ""
	if _, err := s.Enqueue(context.Background(), r); !errors.Is(err, ErrBadTransition) {
		t.Fatalf("缺因果链必须被拒: %v", err)
	}
}

func TestIdempotencyKeyUnique(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	if _, err := s.Enqueue(ctx, req("echo_bot", "dup")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Enqueue(ctx, req("echo_bot", "dup"))
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("幂等键重复必须被拒: %v", err)
	}
}

func TestCompleteRequiresMatchingLeaseToken(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	j, _ := s.Enqueue(ctx, req("echo_bot", "k-ack"))
	leased, _ := s.Lease(ctx, "echo_bot", "worker-1", nil)

	// 错误令牌（模拟第二个 worker）必须被拒
	if err := s.Complete(ctx, j.JobID, "wrong-token", json.RawMessage(`{"ok":true}`)); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("错误租约令牌必须被拒: %v", err)
	}
	if err := s.Complete(ctx, leased.JobID, leased.LeaseToken, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("正确令牌应可完成: %v", err)
	}
	done, _ := s.Get(ctx, j.JobID)
	if done.Status != StatusDone {
		t.Fatalf("期望 done，得到 %s", done.Status)
	}
	// 完成后再次 ack 必须被拒（防重复回复）
	if err := s.Complete(ctx, j.JobID, leased.LeaseToken, nil); !errors.Is(err, ErrBadTransition) {
		t.Fatalf("完成后重复 ack 必须被拒: %v", err)
	}
}

func TestRenewLease(t *testing.T) {
	s, _ := newScheduler(t, Options{LeaseTTL: 2 * time.Second})
	ctx := context.Background()
	s.Enqueue(ctx, req("echo_bot", "k-renew"))
	leased, _ := s.Lease(ctx, "echo_bot", "worker-1", nil)
	before := *leased.LeaseUntil
	time.Sleep(1100 * time.Millisecond)
	if err := s.RenewLease(ctx, leased.JobID, leased.LeaseToken); err != nil {
		t.Fatalf("续租: %v", err)
	}
	after, _ := s.Get(ctx, leased.JobID)
	if !after.LeaseUntil.After(before) {
		t.Fatalf("续租必须延长租约: %v -> %v", before, *after.LeaseUntil)
	}
	if err := s.RenewLease(ctx, leased.JobID, "wrong"); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("错误令牌续租必须被拒: %v", err)
	}
}

func TestPoisonPillBecomesDead(t *testing.T) {
	s, _ := newScheduler(t, Options{PoisonPill: 3, RetryBackoff: time.Millisecond})
	ctx := context.Background()
	j, _ := s.Enqueue(ctx, req("echo_bot", "k-poison"))
	for i := 0; i < 3; i++ {
		leased, err := s.Lease(ctx, "echo_bot", "worker-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if leased == nil {
			t.Fatalf("第 %d 轮应可认领", i)
		}
		if err := s.Fail(ctx, leased.JobID, leased.LeaseToken, "boom"); err != nil {
			t.Fatal(err)
		}
	}
	final, _ := s.Get(ctx, j.JobID)
	if final.Status != StatusDead {
		t.Fatalf("连续 3 次失败应进 dead，得到 %s（attempt=%d）", final.Status, final.AttemptCount)
	}
	if final.AttemptCount != 3 {
		t.Fatalf("attempt_count 应为 3，得到 %d", final.AttemptCount)
	}
}

// 严格分区：同分区不越位
func TestStrictPartitionNoOvertake(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	r1 := req("echo_bot", "p1")
	r1.PartitionKey = "conv_a"
	first, _ := s.Enqueue(ctx, r1)
	r2 := req("echo_bot", "p2")
	r2.PartitionKey = "conv_a"
	second, _ := s.Enqueue(ctx, r2)

	leased, _ := s.Lease(ctx, "echo_bot", "worker-1", nil)
	if leased == nil || leased.JobID != first.JobID {
		t.Fatalf("应先认领队头 %d，得到 %+v", first.JobID, leased)
	}
	// 队头未完成时，同分区后续任务不得被投递
	next, err := s.Lease(ctx, "echo_bot", "worker-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("同分区前序未完成不得越位，却认领到 %d", next.JobID)
	}
	// 队头完成后，后续可投递
	if err := s.Complete(ctx, first.JobID, leased.LeaseToken, nil); err != nil {
		t.Fatal(err)
	}
	next, _ = s.Lease(ctx, "echo_bot", "worker-2", nil)
	if next == nil || next.JobID != second.JobID {
		t.Fatalf("队头完成后应认领 %d，得到 %+v", second.JobID, next)
	}
}

// 不同分区可并行
func TestDifferentPartitionsParallel(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	r1 := req("echo_bot", "pa")
	r1.PartitionKey = "conv_a"
	r2 := req("echo_bot", "pb")
	r2.PartitionKey = "conv_b"
	s.Enqueue(ctx, r1)
	s.Enqueue(ctx, r2)
	l1, _ := s.Lease(ctx, "echo_bot", "worker-1", nil)
	l2, _ := s.Lease(ctx, "echo_bot", "worker-2", nil)
	if l1 == nil || l2 == nil || l1.JobID == l2.JobID {
		t.Fatalf("不同分区应可并行认领: %+v / %+v", l1, l2)
	}
}

// 宽松分区（无 partition_key）直接 SKIP LOCKED 并行
func TestLoosePartitionParallel(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	s.Enqueue(ctx, req("echo_bot", "l1"))
	s.Enqueue(ctx, req("echo_bot", "l2"))
	l1, _ := s.Lease(ctx, "echo_bot", "w1", nil)
	l2, _ := s.Lease(ctx, "echo_bot", "w2", nil)
	if l1 == nil || l2 == nil || l1.JobID == l2.JobID {
		t.Fatalf("宽松分区应可并行: %+v / %+v", l1, l2)
	}
}

// 并发认领不得重复投递同一任务
func TestConcurrentLeaseNoDoubleDelivery(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	const n = 12
	for i := 0; i < n; i++ {
		if _, err := s.Enqueue(ctx, req("echo_bot", fmt.Sprintf("conc-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[int64]string{}
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(worker string) {
			defer wg.Done()
			for {
				j, err := s.Lease(ctx, "echo_bot", worker, nil)
				if err != nil || j == nil {
					return
				}
				mu.Lock()
				if prev, dup := seen[j.JobID]; dup {
					t.Errorf("任务 %d 被重复投递给 %s 与 %s", j.JobID, prev, worker)
				}
				seen[j.JobID] = worker
				mu.Unlock()
				if err := s.Complete(ctx, j.JobID, j.LeaseToken, nil); err != nil {
					t.Errorf("完成失败: %v", err)
				}
			}
		}(fmt.Sprintf("worker-%d", w))
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("应恰好投递 %d 个任务，实际 %d", n, len(seen))
	}
}

// 租约过期 + 已执行非幂等副作用 → suspended（不自动重放）
func TestExpiredLeaseWithExecutedIntentSuspends(t *testing.T) {
	s, db := newScheduler(t, Options{LeaseTTL: time.Second})
	ctx := context.Background()
	j, _ := s.Enqueue(ctx, req("echo_bot", "k-expire-intent"))
	leased, _ := s.Lease(ctx, "echo_bot", "worker-1", nil)
	if err := s.MarkRunning(ctx, leased.JobID, leased.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := s.DeclareIntent(ctx, j.JobID, 0, "sandbox_bash", "digest-1", json.RawMessage(`{"cmd":"git push"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkIntentExecuted(ctx, j.JobID, 0, "executed"); err != nil {
		t.Fatal(err)
	}
	// 让租约过期（用数据库时钟推进，避免依赖应用时钟）
	db.Exec(t, `UPDATE kernel.kernel_jobs SET lease_until = now() - interval '1 second' WHERE job_id=$1`, j.JobID)

	suspended, requeued, err := s.DetectExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("检测过期租约: %v", err)
	}
	if suspended != 1 || requeued != 0 {
		t.Fatalf("已执行副作用必须挂起而非自动重试: suspended=%d requeued=%d", suspended, requeued)
	}
	got, _ := s.Get(ctx, j.JobID)
	if got.Status != StatusSuspended || got.SuspendReason != SuspendLeaseExpired {
		t.Fatalf("期望 suspended/lease_expired，得到 %s/%s", got.Status, got.SuspendReason)
	}
	// 第二个 worker 不得抢占
	next, _ := s.Lease(ctx, "echo_bot", "worker-2", nil)
	if next != nil {
		t.Fatalf("挂起任务不得被抢占: %+v", next)
	}
}

// 租约过期 + 已声明幂等且无已执行副作用 → 自动重试
func TestExpiredLeaseIdempotentAutoRetry(t *testing.T) {
	s, db := newScheduler(t, Options{LeaseTTL: time.Second})
	ctx := context.Background()
	j, _ := s.Enqueue(ctx, req("echo_bot", "k-expire-idem"))
	leased, _ := s.Lease(ctx, "echo_bot", "worker-1", nil)
	db.Exec(t, `UPDATE kernel.kernel_jobs SET lease_until = now() - interval '1 second' WHERE job_id=$1`, leased.JobID)

	suspended, requeued, err := s.DetectExpiredLeases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if suspended != 0 || requeued != 1 {
		t.Fatalf("幂等任务应自动重试: suspended=%d requeued=%d", suspended, requeued)
	}
	got, _ := s.Get(ctx, j.JobID)
	if got.Status != StatusRetryBackoff {
		t.Fatalf("期望 retry_backoff，得到 %s", got.Status)
	}
}

// 人工确认三选一
func TestResumeConfirmedDecisions(t *testing.T) {
	for _, decision := range []string{ResumeContinue, ResumeReplay, ResumeDiscard} {
		t.Run(decision, func(t *testing.T) {
			s, _ := newScheduler(t, Options{})
			ctx := context.Background()
			j, _ := s.Enqueue(ctx, req("echo_bot", "k-resume-"+decision))
			if err := s.Suspend(ctx, j.JobID, SuspendYieldConfirm); err != nil {
				t.Fatal(err)
			}
			out, err := s.ResumeConfirmed(ctx, j.JobID, decision, "user_bot")
			if err != nil {
				t.Fatalf("人工确认: %v", err)
			}
			orig, _ := s.Get(ctx, j.JobID)
			if orig.ResumeDecision != decision || orig.ResumeBy != "user_bot" || orig.ResumedAt == nil {
				t.Fatalf("必须记录决定与确认人: %+v", orig)
			}
			switch decision {
			case ResumeContinue:
				if orig.Status != StatusQueued {
					t.Fatalf("continue 应回到 queued，得到 %s", orig.Status)
				}
			case ResumeDiscard:
				if orig.Status != StatusCancelled {
					t.Fatalf("discard 应为 cancelled，得到 %s", orig.Status)
				}
			case ResumeReplay:
				if orig.Status != StatusCancelled {
					t.Fatalf("replay 原任务应为 cancelled，得到 %s", orig.Status)
				}
				if out.JobID == j.JobID {
					t.Fatal("replay 必须生成新任务")
				}
				if out.IdempotencyKey == orig.IdempotencyKey {
					t.Fatal("replay 必须使用新的幂等键")
				}
				if out.CorrelationID != orig.CorrelationID || out.RootEnvelopeID != orig.RootEnvelopeID {
					t.Fatal("replay 必须继承因果链")
				}
				if out.ParentJobID == nil || *out.ParentJobID != j.JobID {
					t.Fatal("replay 必须记录 parent_job_id")
				}
				if out.Status != StatusQueued {
					t.Fatalf("replay 新任务应为 queued，得到 %s", out.Status)
				}
			}
		})
	}
}

func TestResumeRequiresDecisionAndUser(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	j, _ := s.Enqueue(ctx, req("echo_bot", "k-resume-bad"))
	s.Suspend(ctx, j.JobID, SuspendYieldConfirm)
	if _, err := s.ResumeConfirmed(ctx, j.JobID, "auto", "user_bot"); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("非法决定必须被拒: %v", err)
	}
	if _, err := s.ResumeConfirmed(ctx, j.JobID, ResumeContinue, ""); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("缺确认人必须被拒: %v", err)
	}
}

// DLQ replay 必经人工确认
func TestReplayRequiresConfirmation(t *testing.T) {
	s, _ := newScheduler(t, Options{PoisonPill: 1})
	ctx := context.Background()
	j, _ := s.Enqueue(ctx, req("echo_bot", "k-dlq"))
	leased, _ := s.Lease(ctx, "echo_bot", "w1", nil)
	if err := s.Fail(ctx, leased.JobID, leased.LeaseToken, "boom"); err != nil {
		t.Fatal(err)
	}
	dead, _ := s.Get(ctx, j.JobID)
	if dead.Status != StatusDead {
		t.Fatalf("期望 dead，得到 %s", dead.Status)
	}
	out, err := s.Replay(ctx, j.JobID, "user_bot")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if out.JobID == j.JobID || out.Status != StatusQueued {
		t.Fatalf("replay 应生成新的 queued 任务: %+v", out)
	}
	orig, _ := s.Get(ctx, j.JobID)
	if orig.ResumeDecision != ResumeReplay || orig.ResumeBy != "user_bot" {
		t.Fatalf("replay 必须留人工确认审计: %+v", orig)
	}
}

// resume-list 必须带副作用清单
func TestListSuspendedIncludesIntents(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	j, _ := s.Enqueue(ctx, req("echo_bot", "k-list"))
	s.DeclareIntent(ctx, j.JobID, 0, "sandbox_bash", "d1", json.RawMessage(`{"cmd":"git push"}`))
	s.MarkIntentExecuted(ctx, j.JobID, 0, "executed")
	s.DeclareIntent(ctx, j.JobID, 1, "file_write", "d2", nil)
	s.Suspend(ctx, j.JobID, SuspendLeaseExpired)

	views, err := s.ListSuspended(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("期望 1 条挂起，得到 %d", len(views))
	}
	if len(views[0].Intents) != 2 {
		t.Fatalf("副作用清单应含 2 条意图，得到 %d", len(views[0].Intents))
	}
	if views[0].Intents[0].Status != "executed" {
		t.Fatalf("意图状态不符: %s", views[0].Intents[0].Status)
	}
	if views[0].Job.CorrelationID == "" {
		t.Fatal("挂起视图必须带因果链")
	}
}

// hub 重启后挂起任务不丢（持久化而非连接状态）
func TestSuspendedSurvivesRestart(t *testing.T) {
	s, db := newScheduler(t, Options{})
	ctx := context.Background()
	j, _ := s.Enqueue(ctx, req("echo_bot", "k-restart"))
	s.Suspend(ctx, j.JobID, SuspendYieldConfirm)

	// 模拟 hub 重启：新建 Scheduler 实例（无任何内存状态）
	st2, err := store.Open(ctx, db.KernelDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	s2 := New(st2, Options{})
	got, err := s2.Get(ctx, j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusSuspended || got.SuspendReason != SuspendYieldConfirm {
		t.Fatalf("重启后挂起状态必须保留: %s / %s", got.Status, got.SuspendReason)
	}
	views, _ := s2.ListSuspended(ctx)
	if len(views) != 1 {
		t.Fatalf("重启后 resume-list 应仍可见，得到 %d", len(views))
	}
}

// 兜底轮询：无 NOTIFY 也能完成投递
func TestFallbackPollWithoutNotify(t *testing.T) {
	s, db := newScheduler(t, Options{})
	ctx := context.Background()
	// 入队后人为丢弃通知：直接清掉监听也不影响，这里验证纯扫描路径可认领
	j, _ := s.Enqueue(ctx, req("echo_bot", "k-poll"))
	_ = j
	// 模拟通知丢失：不做任何 LISTEN，仅靠 Lease 扫描
	leased, err := s.Lease(ctx, "echo_bot", "worker-poll", nil)
	if err != nil || leased == nil {
		t.Fatalf("无 NOTIFY 时兜底扫描必须仍能认领: %v / %+v", err, leased)
	}
	if err := s.Complete(ctx, leased.JobID, leased.LeaseToken, nil); err != nil {
		t.Fatal(err)
	}
	var done int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_jobs WHERE status='done'`).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if done != 1 {
		t.Fatalf("期望 1 条 done，得到 %d", done)
	}
}

// 延迟重试：next_retry_at 未到期不得投递
func TestRetryBackoffNotLeasedEarly(t *testing.T) {
	s, _ := newScheduler(t, Options{RetryBackoff: time.Hour})
	ctx := context.Background()
	s.Enqueue(ctx, req("echo_bot", "k-backoff"))
	leased, _ := s.Lease(ctx, "echo_bot", "w1", nil)
	if err := s.Fail(ctx, leased.JobID, leased.LeaseToken, "later"); err != nil {
		t.Fatal(err)
	}
	next, _ := s.Lease(ctx, "echo_bot", "w2", nil)
	if next != nil {
		t.Fatalf("退避未到期不得投递: %+v", next)
	}
}

func TestConfigVersionPinnedOnLease(t *testing.T) {
	s, _ := newScheduler(t, Options{})
	ctx := context.Background()
	s.Enqueue(ctx, req("echo_bot", "k-cv"))
	cv := int64(7)
	leased, _ := s.Lease(ctx, "echo_bot", "w1", &cv)
	if leased.ConfigVersion == nil || *leased.ConfigVersion != 7 {
		t.Fatalf("领取时必须继承实例配置版本: %+v", leased.ConfigVersion)
	}
}
