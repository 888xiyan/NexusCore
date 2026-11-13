// Package eventbus 是事件总线的消费侧：持久事件投递的幂等去重与订阅代持。
// 依据：内核 MVP 详设 §3.7、架构总结 §6.7。
// MVP 只实现持久事件投递（经 outbox + event.deliver job）；内存尽力而为通道整体延后。
package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/jackc/pgx/v5"
)

var ErrNotSubscribed = errors.New("Consumer Group 未订阅或已停止")

type Bus struct {
	st    *store.Store
	sched *scheduler.Scheduler
}

func New(st *store.Store, sched *scheduler.Scheduler) *Bus {
	return &Bus{st: st, sched: sched}
}

// Delivery 是从 event.deliver job 载荷解出的投递单元。
type Delivery struct {
	EventID       string          `json:"event_id"`
	EventType     string          `json:"event_type"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	Summary       json.RawMessage `json:"summary"`
}

func ParseDelivery(job *scheduler.Job) (*Delivery, error) {
	if job.JobType != scheduler.JobTypeEventDeliver {
		return nil, fmt.Errorf("任务 %d 不是 event.deliver（实际 %s）", job.JobID, job.JobType)
	}
	var d Delivery
	if err := json.Unmarshal(job.PayloadJSON, &d); err != nil {
		return nil, fmt.Errorf("解析事件投递载荷: %w", err)
	}
	if d.EventID == "" {
		return nil, errors.New("事件投递载荷缺少 event_id")
	}
	return &d, nil
}

// Consume 处理一条投递：先查本组去重表，命中则只更新状态、不重复执行副作用。
// 返回 alreadyProcessed=true 表示这是重复投递（at-least-once 语义下的正常情况）。
func (b *Bus) Consume(ctx context.Context, consumerGroup string, job *scheduler.Job, leaseToken string, handle func(*Delivery) error) (alreadyProcessed bool, err error) {
	d, err := ParseDelivery(job)
	if err != nil {
		return false, err
	}
	err = b.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM kernel_event_dedup WHERE consumer_group_id=$1 AND event_id=$2`,
			consumerGroup, d.EventID).Scan(&n); err != nil {
			return fmt.Errorf("查询事件去重: %w", err)
		}
		if n > 0 {
			alreadyProcessed = true
			return nil
		}
		if handle != nil {
			if err := handle(d); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO kernel_event_dedup (consumer_group_id, event_id, processed_at)
			VALUES ($1,$2,now()) ON CONFLICT DO NOTHING`, consumerGroup, d.EventID); err != nil {
			return fmt.Errorf("登记事件去重: %w", err)
		}
		return nil
	})
	if err != nil {
		return alreadyProcessed, err
	}
	// 去重登记与任务完成分两步：即使此处崩溃，重投也会被去重表拦住，不会重复副作用
	if err := b.sched.Complete(ctx, job.JobID, leaseToken, nil); err != nil {
		return alreadyProcessed, err
	}
	return alreadyProcessed, nil
}

// IsProcessed 查询本组是否已处理过该事件（at-least-once 下的重复判定）。
func (b *Bus) IsProcessed(ctx context.Context, consumerGroup, eventID string) (bool, error) {
	var n int
	err := b.st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM kernel_event_dedup WHERE consumer_group_id=$1 AND event_id=$2`,
		consumerGroup, eventID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("查询事件去重: %w", err)
	}
	return n > 0, nil
}

// MarkProcessed 登记本组对该事件的处理事实（幂等去重）。
// 供流式订阅使用：网络发送与任务 ack 都必须在事务之外完成，发送成功后才调用本方法。
// 若登记后、ack 前崩溃，租约过期会重投，但去重表会拦住重复副作用。
func (b *Bus) MarkProcessed(ctx context.Context, consumerGroup, eventID string) error {
	_, err := b.st.Pool().Exec(ctx, `
		INSERT INTO kernel_event_dedup (consumer_group_id, event_id, processed_at)
		VALUES ($1,$2,now()) ON CONFLICT DO NOTHING`, consumerGroup, eventID)
	if err != nil {
		return fmt.Errorf("登记事件去重: %w", err)
	}
	return nil
}

// IsSubscribed 校验 Consumer Group 是否仍有效订阅该事件类型（吊销后立即停止投递）。
func (b *Bus) IsSubscribed(ctx context.Context, consumerGroup, eventType string) (bool, error) {
	var n int
	err := b.st.Pool().QueryRow(ctx, `
		SELECT count(*)
		  FROM kernel_event_subscriptions s
		  JOIN kernel_plugin_subs g ON g.consumer_group_id = s.consumer_group_id
		 WHERE s.consumer_group_id=$1 AND s.event_type=$2 AND g.status='active'`,
		consumerGroup, eventType).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("查询订阅状态: %w", err)
	}
	return n > 0, nil
}

// Ack 显式确认一条投递（消费者处理成功后调用）。
func (b *Bus) Ack(ctx context.Context, jobID int64, leaseToken string) error {
	return b.sched.Complete(ctx, jobID, leaseToken, nil)
}

// PendingFor 返回某 Consumer Group 待消费 / 待重投的投递数。
// 消费位点由 event.deliver job 状态隐式构成：acked 即已消费，leased/queued 即待消费。
func (b *Bus) PendingFor(ctx context.Context, consumerGroup string) (int, error) {
	var n int
	err := b.st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM kernel_jobs
		 WHERE queue_name=$1 AND job_type='event.deliver' AND status NOT IN ('done','dead','cancelled')`,
		consumerGroup).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计待消费投递: %w", err)
	}
	return n, nil
}

// LastProcessedAt 返回本组最近一次成功消费时间，供健康观测。
func (b *Bus) LastProcessedAt(ctx context.Context, consumerGroup string) (*time.Time, error) {
	var t *time.Time
	err := b.st.Pool().QueryRow(ctx, `
		SELECT max(processed_at) FROM kernel_event_dedup WHERE consumer_group_id=$1`, consumerGroup).Scan(&t)
	if err != nil {
		return nil, fmt.Errorf("读取消费位点时间: %w", err)
	}
	return t, nil
}
