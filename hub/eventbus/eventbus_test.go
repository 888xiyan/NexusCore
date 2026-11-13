package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/888xiyan/NexusCore/hub/outbox"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/pgtest"
	"github.com/jackc/pgx/v5"
)

func newBus(t *testing.T) (*Bus, *scheduler.Scheduler, *pgtest.DB) {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	sched := scheduler.New(st, scheduler.Options{})
	return New(st, sched), sched, db
}

// deliverJob 走真实 outbox 路径生成一条投给 user_bot 的 event.deliver 任务并认领。
func deliverJob(t *testing.T, b *Bus, sched *scheduler.Scheduler, db *pgtest.DB, eventID string) *scheduler.Job {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, db.KernelDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return outbox.Publish(ctx, tx, outbox.Event{
			EventID: eventID, EventType: "bot.reply",
			CorrelationID: "env_" + eventID, RootEnvelopeID: "env_" + eventID,
			AggregateType: "job", AggregateID: "1",
			PayloadJSON: json.RawMessage(`{"payload_ref":"pl_test"}`),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	disp := outbox.New(st, sched, outbox.Options{})
	if _, _, err := disp.DispatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := sched.Lease(ctx, "user_bot", "consumer-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatal("未生成 event.deliver 任务")
	}
	return job
}

func TestParseDeliveryRejectsWrongJobType(t *testing.T) {
	_, err := ParseDelivery(&scheduler.Job{JobID: 1, JobType: "bot.request"})
	if err == nil {
		t.Fatal("非 event.deliver 任务应拒绝解析")
	}
}

func TestParseDeliveryRejectsMissingEventID(t *testing.T) {
	_, err := ParseDelivery(&scheduler.Job{JobID: 1, JobType: scheduler.JobTypeEventDeliver,
		PayloadJSON: json.RawMessage(`{"event_type":"bot.reply"}`)})
	if err == nil {
		t.Fatal("缺 event_id 应拒绝")
	}
}

func TestParseDeliveryKeepsSummaryOpaque(t *testing.T) {
	d, err := ParseDelivery(&scheduler.Job{JobID: 1, JobType: scheduler.JobTypeEventDeliver,
		PayloadJSON: json.RawMessage(`{"event_id":"evt_1","event_type":"bot.reply","summary":{"payload_ref":"pl_x"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if d.EventID != "evt_1" || d.EventType != "bot.reply" {
		t.Fatalf("投递单元解析不符: %+v", d)
	}
	var summary struct {
		PayloadRef string `json:"payload_ref"`
	}
	if err := json.Unmarshal(d.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.PayloadRef != "pl_x" {
		t.Fatalf("summary 应原样保留供消费者拉取，实际 %q", summary.PayloadRef)
	}
}

// 首次消费执行副作用并登记去重，任务转 done
func TestConsumeFirstTimeRunsHandlerAndCompletes(t *testing.T) {
	b, sched, db := newBus(t)
	ctx := context.Background()
	job := deliverJob(t, b, sched, db, "evt_first")

	calls := 0
	already, err := b.Consume(ctx, "user_bot", job, job.LeaseToken, func(d *Delivery) error {
		calls++
		if d.EventID != "evt_first" {
			t.Errorf("事件 ID 不符: %s", d.EventID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Fatal("首次消费不应判为重复")
	}
	if calls != 1 {
		t.Fatalf("副作用应执行 1 次，实际 %d", calls)
	}
	got, err := sched.Get(ctx, job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != scheduler.StatusDone {
		t.Fatalf("消费成功后任务应 done，实际 %s", got.Status)
	}
	if ok, err := b.IsProcessed(ctx, "user_bot", "evt_first"); err != nil || !ok {
		t.Fatalf("应登记去重: ok=%v err=%v", ok, err)
	}
}

// 重复投递：去重命中后只确认状态，绝不重跑副作用
func TestConsumeDuplicateSkipsHandler(t *testing.T) {
	b, sched, db := newBus(t)
	ctx := context.Background()
	job := deliverJob(t, b, sched, db, "evt_dup")
	// 模拟「送达后、ack 前崩溃」：去重已登记，任务仍在租约中
	if err := b.MarkProcessed(ctx, "user_bot", "evt_dup"); err != nil {
		t.Fatal(err)
	}

	calls := 0
	already, err := b.Consume(ctx, "user_bot", job, job.LeaseToken, func(*Delivery) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !already {
		t.Fatal("去重命中应判为重复投递")
	}
	if calls != 0 {
		t.Fatalf("重复投递不得重跑副作用，实际执行 %d 次", calls)
	}
	got, _ := sched.Get(ctx, job.JobID)
	if got.Status != scheduler.StatusDone {
		t.Fatalf("重复投递仍应确认任务，实际 %s", got.Status)
	}
}

// 副作用失败：不登记去重、不确认任务，留待重投
func TestConsumeHandlerErrorLeavesJobRedeliverable(t *testing.T) {
	b, sched, db := newBus(t)
	ctx := context.Background()
	job := deliverJob(t, b, sched, db, "evt_fail")

	boom := errors.New("下游处理失败")
	_, err := b.Consume(ctx, "user_bot", job, job.LeaseToken, func(*Delivery) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("应透传副作用错误，实际 %v", err)
	}
	if ok, _ := b.IsProcessed(ctx, "user_bot", "evt_fail"); ok {
		t.Fatal("副作用失败不得登记去重，否则事件永久丢失")
	}
	got, _ := sched.Get(ctx, job.JobID)
	if got.Status == scheduler.StatusDone {
		t.Fatal("副作用失败不得确认任务")
	}
}

func TestMarkProcessedIsIdempotent(t *testing.T) {
	b, _, _ := newBus(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := b.MarkProcessed(ctx, "user_bot", "evt_mark"); err != nil {
			t.Fatalf("第 %d 次登记应幂等: %v", i+1, err)
		}
	}
	if ok, err := b.IsProcessed(ctx, "user_bot", "evt_mark"); err != nil || !ok {
		t.Fatalf("应已登记: ok=%v err=%v", ok, err)
	}
	// 不同 Consumer Group 各自独立去重
	if ok, _ := b.IsProcessed(ctx, "cg_other", "evt_mark"); ok {
		t.Fatal("去重必须按 Consumer Group 隔离")
	}
}

// 吊销订阅后立即停止投递判定
func TestIsSubscribedRespectsGroupStatus(t *testing.T) {
	b, _, db := newBus(t)
	ctx := context.Background()
	if ok, err := b.IsSubscribed(ctx, "user_bot", "bot.reply"); err != nil || !ok {
		t.Fatalf("种子订阅应有效: ok=%v err=%v", ok, err)
	}
	db.Exec(t, `UPDATE kernel_plugin_subs SET status='stopped', stopped_at=now() WHERE consumer_group_id='user_bot'`)
	if ok, _ := b.IsSubscribed(ctx, "user_bot", "bot.reply"); ok {
		t.Fatal("订阅组停止后不得继续投递")
	}
	if ok, _ := b.IsSubscribed(ctx, "user_bot", "plugin_test.note.updated"); ok {
		t.Fatal("未订阅的事件类型不得投递")
	}
}

func TestPendingForAndLastProcessedAt(t *testing.T) {
	b, sched, db := newBus(t)
	ctx := context.Background()
	if ts, err := b.LastProcessedAt(ctx, "user_bot"); err != nil || ts != nil {
		t.Fatalf("未消费时应无位点: ts=%v err=%v", ts, err)
	}
	job := deliverJob(t, b, sched, db, "evt_pending")
	if n, err := b.PendingFor(ctx, "user_bot"); err != nil || n != 1 {
		t.Fatalf("应有 1 条待消费: n=%d err=%v", n, err)
	}
	if _, err := b.Consume(ctx, "user_bot", job, job.LeaseToken, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.PendingFor(ctx, "user_bot"); n != 0 {
		t.Fatalf("消费后待消费应清零，实际 %d", n)
	}
	ts, err := b.LastProcessedAt(ctx, "user_bot")
	if err != nil || ts == nil {
		t.Fatalf("消费后应有位点时间: ts=%v err=%v", ts, err)
	}
}

func TestAckRequiresMatchingLeaseToken(t *testing.T) {
	b, sched, db := newBus(t)
	ctx := context.Background()
	job := deliverJob(t, b, sched, db, "evt_ack")
	if err := b.Ack(ctx, job.JobID, "wrong-token"); err == nil {
		t.Fatal("错误 lease_token 不得确认任务（防双 worker）")
	}
	if err := b.Ack(ctx, job.JobID, job.LeaseToken); err != nil {
		t.Fatalf("正确 lease_token 应可确认: %v", err)
	}
}
