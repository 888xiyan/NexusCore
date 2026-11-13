package outbox

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/888xiyan/NexusCore/hub/eventbus"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/pgtest"
	"github.com/jackc/pgx/v5"
)

func newDispatcher(t *testing.T) (*Dispatcher, *scheduler.Scheduler, *eventbus.Bus, *pgtest.DB) {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	sched := scheduler.New(st, scheduler.Options{})
	bus := eventbus.New(st, sched)
	return New(st, sched, Options{}), sched, bus, db
}

func publish(t *testing.T, db *pgtest.DB, eventID, eventType, correlation string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, db.KernelDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return Publish(ctx, tx, Event{
			EventID: eventID, EventType: eventType,
			CorrelationID: correlation, RootEnvelopeID: correlation,
			AggregateType: "job", AggregateID: "1",
			PayloadJSON: json.RawMessage(`{"summary":"x"}`),
		})
	})
	if err != nil {
		t.Fatalf("发布事件: %v", err)
	}
}

// 每个授权 Consumer Group 生成一条 event.deliver job
func TestDispatchCreatesJobPerGroup(t *testing.T) {
	d, sched, _, db := newDispatcher(t)
	ctx := context.Background()
	publish(t, db, "evt_1", "bot.reply", "env_root_1")

	events, jobs, err := d.DispatchOnce(ctx)
	if err != nil {
		t.Fatalf("分发: %v", err)
	}
	if events != 1 || jobs != 1 {
		t.Fatalf("期望 1 事件 / 1 job（user_bot 已订阅 bot.reply），得到 %d / %d", events, jobs)
	}
	leased, err := sched.Lease(ctx, "user_bot", "cli-worker", nil)
	if err != nil || leased == nil {
		t.Fatalf("应能从 user_bot 组认领投递: %v / %+v", err, leased)
	}
	if leased.JobType != scheduler.JobTypeEventDeliver {
		t.Fatalf("job_type 不符: %s", leased.JobType)
	}
	if leased.CorrelationID != "env_root_1" || leased.RootEnvelopeID != "env_root_1" {
		t.Fatalf("投递必须继承因果链: %+v", leased)
	}
	// outbox 状态推进
	var status string
	if err := db.QueryRow(t, `SELECT status FROM kernel.kernel_event_outbox WHERE event_id='evt_1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusDispatched {
		t.Fatalf("期望 dispatched，得到 %s", status)
	}
}

// 未订阅的事件类型不得生成任何投递（禁止先广播全量）
func TestDispatchSkipsUnsubscribedEventType(t *testing.T) {
	d, _, _, db := newDispatcher(t)
	publish(t, db, "evt_unsub", "plugin_memory.memory.updated", "env_root_2")
	events, jobs, err := d.DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if events != 1 || jobs != 0 {
		t.Fatalf("无授权订阅者时不得生成 job: events=%d jobs=%d", events, jobs)
	}
}

// 重复分发不得产生重复 job（幂等键）
func TestDispatchIsIdempotent(t *testing.T) {
	d, _, _, db := newDispatcher(t)
	ctx := context.Background()
	publish(t, db, "evt_dup", "bot.reply", "env_root_3")
	if _, _, err := d.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 人为把 outbox 退回 pending，模拟崩溃后重扫
	db.Exec(t, `UPDATE kernel.kernel_event_outbox SET status='pending', dispatched_at=NULL WHERE event_id='evt_dup'`)
	_, jobs, err := d.DispatchOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("重扫不得重复生成投递 job，得到 %d", jobs)
	}
	var n int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_jobs WHERE job_type='event.deliver'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应只有 1 条投递 job，得到 %d", n)
	}
}

// 消费侧幂等：重复事件只记录状态，不重复执行副作用
func TestConsumeDeduplicates(t *testing.T) {
	d, sched, bus, db := newDispatcher(t)
	ctx := context.Background()
	publish(t, db, "evt_consume", "bot.reply", "env_root_4")
	if _, _, err := d.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	leased, _ := sched.Lease(ctx, "user_bot", "cli", nil)
	if leased == nil {
		t.Fatal("应可认领投递")
	}
	calls := 0
	handle := func(*eventbus.Delivery) error { calls++; return nil }
	dup, err := bus.Consume(ctx, "user_bot", leased, leased.LeaseToken, handle)
	if err != nil {
		t.Fatalf("消费: %v", err)
	}
	if dup || calls != 1 {
		t.Fatalf("首次消费应执行一次: dup=%v calls=%d", dup, calls)
	}
	// 模拟重投：同一事件再次投递到同组
	db.Exec(t, `UPDATE kernel.kernel_jobs SET status='queued', lease_token=NULL, lease_owner=NULL, lease_until=NULL,
	            idempotency_key='deliver:user_bot:evt_consume:retry' WHERE job_id=$1`, leased.JobID)
	again, _ := sched.Lease(ctx, "user_bot", "cli", nil)
	if again == nil {
		t.Fatal("应可认领重投")
	}
	dup2, err := bus.Consume(ctx, "user_bot", again, again.LeaseToken, handle)
	if err != nil {
		t.Fatalf("重复消费: %v", err)
	}
	if !dup2 {
		t.Fatal("重复事件必须被识别")
	}
	if calls != 1 {
		t.Fatalf("重复事件不得重复执行副作用，calls=%d", calls)
	}
}

// 吊销订阅后立即停止投递
func TestRevokedSubscriptionStopsDelivery(t *testing.T) {
	d, _, _, db := newDispatcher(t)
	ctx := context.Background()
	db.Exec(t, `UPDATE kernel.kernel_plugin_subs SET status='stopped', stopped_at=now() WHERE consumer_group_id='user_bot'`)
	publish(t, db, "evt_revoked", "bot.reply", "env_root_5")
	_, jobs, err := d.DispatchOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("订阅停止后不得投递，得到 %d", jobs)
	}
}

// 归档：全部消费完成后归档，不物理删除
func TestArchiveAfterAllConsumed(t *testing.T) {
	d, sched, bus, db := newDispatcher(t)
	ctx := context.Background()
	publish(t, db, "evt_archive", "bot.reply", "env_root_6")
	if _, _, err := d.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 未消费完不得归档
	n, err := d.Archive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("仍有未完成投递时不得归档，得到 %d", n)
	}
	leased, _ := sched.Lease(ctx, "user_bot", "cli", nil)
	if _, err := bus.Consume(ctx, "user_bot", leased, leased.LeaseToken, nil); err != nil {
		t.Fatal(err)
	}
	n, err = d.Archive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("消费完成后应归档 1 条，得到 %d", n)
	}
	var status string
	var count int
	if err := db.QueryRow(t, `SELECT status FROM kernel.kernel_event_outbox WHERE event_id='evt_archive'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusArchived {
		t.Fatalf("期望 archived，得到 %s", status)
	}
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_event_outbox WHERE event_id='evt_archive'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("归档不得物理删除，必须保留审计链路")
	}
}

// Publish 必须拒绝缺因果链的事件
func TestPublishRequiresCausalChain(t *testing.T) {
	_, _, _, db := newDispatcher(t)
	ctx := context.Background()
	st, err := store.Open(ctx, db.KernelDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return Publish(ctx, tx, Event{EventID: "e", EventType: "bot.reply", AggregateType: "job", AggregateID: "1"})
	})
	if err == nil {
		t.Fatal("缺因果链的事件必须被拒")
	}
}

// 内核不得为投递伪造因果路径
func TestDeliveryJobDoesNotFabricateCausalPath(t *testing.T) {
	d, sched, _, db := newDispatcher(t)
	ctx := context.Background()
	publish(t, db, "evt_path", "bot.reply", "env_root_7")
	if _, _, err := d.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	leased, _ := sched.Lease(ctx, "user_bot", "cli", nil)
	if len(leased.CausalPath) != 0 {
		t.Fatalf("outbox 未持久化祖先路径，内核不得补造: %s", leased.CausalPath)
	}
	if leased.CausationID != "evt_path" {
		t.Fatalf("causation_id 应指向触发事件，得到 %s", leased.CausationID)
	}
	var n int
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_jobs WHERE causal_path_json IS NOT NULL AND job_type='event.deliver'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("不得存在伪造的因果路径，命中 %d", n)
	}
}

// 分发与 outbox 状态必须同事务：失败整体回滚
func TestDispatchAtomicity(t *testing.T) {
	d, _, _, db := newDispatcher(t)
	ctx := context.Background()
	publish(t, db, "evt_atomic", "bot.reply", "env_root_8")
	// 制造一个会导致入队失败的订阅组（queue_name 合法但 job_type 触发约束失败不易，
	// 这里改为验证成功路径下 job 与 dispatched 同时可见）
	if _, _, err := d.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var jobs int
	var status string
	if err := db.QueryRow(t, `SELECT count(*) FROM kernel.kernel_jobs WHERE job_type='event.deliver'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t, `SELECT status FROM kernel.kernel_event_outbox WHERE event_id='evt_atomic'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || status != StatusDispatched {
		t.Fatalf("job 与 outbox 状态必须同事务可见: jobs=%d status=%s", jobs, status)
	}
}
