// Package outbox 是 Outbox Dispatcher：扫描 pending 事件，按授权订阅为每个 Consumer Group
// 生成一条 event.deliver job（复用 kernel_jobs）。依据：内核 MVP 详设 §3.6、架构总结 §6.7。
// 状态 pending -> dispatched -> archived；全部分发完成后归档，不物理删除。
// dispatcher 不直接读取任何插件库；插件事件经其 bot 投递委托回投 ingress 后进入本 Outbox。
package outbox

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

const (
	StatusPending    = "pending"
	StatusDispatched = "dispatched"
	StatusArchived   = "archived"

	DefaultBatchSize    = 100
	DefaultScanInterval = 250 * time.Millisecond
)

type Event struct {
	OutboxID       int64
	EventID        string
	EventType      string
	CorrelationID  string
	CausationID    string
	RootEnvelopeID string
	AggregateType  string
	AggregateID    string
	PayloadJSON    json.RawMessage
	Status         string
	CreatedAt      time.Time
	DispatchedAt   *time.Time
}

type Dispatcher struct {
	st       *store.Store
	sched    *scheduler.Scheduler
	batch    int
	interval time.Duration
}

type Options struct {
	BatchSize    int
	ScanInterval time.Duration
}

func New(st *store.Store, sched *scheduler.Scheduler, opt Options) *Dispatcher {
	if opt.BatchSize <= 0 {
		opt.BatchSize = DefaultBatchSize
	}
	if opt.ScanInterval <= 0 {
		opt.ScanInterval = DefaultScanInterval
	}
	return &Dispatcher{st: st, sched: sched, batch: opt.BatchSize, interval: opt.ScanInterval}
}

// Publish 在业务事务内写 outbox（与状态变更同事务，禁止先改状态后补事件）。
func Publish(ctx context.Context, tx pgx.Tx, ev Event) error {
	if ev.EventID == "" || ev.EventType == "" {
		return errors.New("outbox 事件必须有 event_id 与 event_type")
	}
	if ev.CorrelationID == "" || ev.RootEnvelopeID == "" {
		return errors.New("outbox 事件必须携带完整因果链")
	}
	payload := ev.PayloadJSON
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO kernel_event_outbox
		  (event_id, event_type, correlation_id, causation_id, root_envelope_id,
		   aggregate_type, aggregate_id, payload_json, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,'pending')`,
		ev.EventID, ev.EventType, ev.CorrelationID, nullable(ev.CausationID), ev.RootEnvelopeID,
		ev.AggregateType, ev.AggregateID, string(payload))
	if err != nil {
		return fmt.Errorf("写入 outbox: %w", err)
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// DispatchOnce 扫描一批 pending 事件并分发；返回分发的事件数与生成的 job 数。
// 订阅授权过滤在生成 job 之前完成：无权订阅者不会收到任何载荷。
func (d *Dispatcher) DispatchOnce(ctx context.Context) (events, jobs int, err error) {
	err = d.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT outbox_id, event_id, event_type, correlation_id, COALESCE(causation_id,''),
			       root_envelope_id, aggregate_type, aggregate_id, payload_json
			  FROM kernel_event_outbox
			 WHERE status='pending'
			 ORDER BY outbox_id
			 LIMIT $1
			 FOR UPDATE SKIP LOCKED`, d.batch)
		if err != nil {
			return fmt.Errorf("扫描 outbox: %w", err)
		}
		var pending []Event
		for rows.Next() {
			var ev Event
			var payload []byte
			if err := rows.Scan(&ev.OutboxID, &ev.EventID, &ev.EventType, &ev.CorrelationID,
				&ev.CausationID, &ev.RootEnvelopeID, &ev.AggregateType, &ev.AggregateID, &payload); err != nil {
				rows.Close()
				return fmt.Errorf("读取 outbox 事件: %w", err)
			}
			ev.PayloadJSON = json.RawMessage(payload)
			pending = append(pending, ev)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, ev := range pending {
			groups, err := authorizedGroups(ctx, tx, ev)
			if err != nil {
				return err
			}
			for _, g := range groups {
				// 消费侧幂等：已处理过的 (group, event) 不再重复生成 job
				var seen int
				if err := tx.QueryRow(ctx, `
					SELECT count(*) FROM kernel_event_dedup WHERE consumer_group_id=$1 AND event_id=$2`,
					g, ev.EventID).Scan(&seen); err != nil {
					return fmt.Errorf("查询事件去重: %w", err)
				}
				if seen > 0 {
					continue
				}
				payload, err := json.Marshal(map[string]any{
					"event_id": ev.EventID, "event_type": ev.EventType,
					"aggregate_type": ev.AggregateType, "aggregate_id": ev.AggregateID,
					"summary": ev.PayloadJSON,
				})
				if err != nil {
					return fmt.Errorf("序列化 event.deliver 载荷: %w", err)
				}
				// causal_path 不由内核补造：outbox 只承载 correlation/causation/root，
				// 祖先路径未随事件持久化，故留空而非伪造节点（硬约束 12）。
				job, err := d.sched.EnqueueTxIfAbsent(ctx, tx, scheduler.EnqueueRequest{
					QueueName:      g,
					JobType:        scheduler.JobTypeEventDeliver,
					CorrelationID:  ev.CorrelationID,
					CausationID:    ev.EventID,
					RootEnvelopeID: ev.RootEnvelopeID,
					IdempotencyKey: "deliver:" + g + ":" + ev.EventID,
					PayloadJSON:    payload,
				})
				if err != nil {
					return fmt.Errorf("生成 event.deliver job: %w", err)
				}
				if job != nil {
					jobs++ // 幂等键已存在时返回 nil，属跳过而非新建
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE kernel_event_outbox SET status='dispatched', dispatched_at=now() WHERE outbox_id=$1`,
				ev.OutboxID); err != nil {
				return fmt.Errorf("标记 outbox 已分发: %w", err)
			}
			events++
		}
		return nil
	})
	return events, jobs, err
}

// authorizedGroups 按订阅授权与 Consumer Group 状态过滤；吊销后立即停止投递。
func authorizedGroups(ctx context.Context, tx pgx.Tx, ev Event) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.consumer_group_id
		  FROM kernel_event_subscriptions s
		  JOIN kernel_plugin_subs g ON g.consumer_group_id = s.consumer_group_id
		 WHERE s.event_type=$1 AND g.status='active'`, ev.EventType)
	if err != nil {
		return nil, fmt.Errorf("查询授权订阅: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, fmt.Errorf("读取订阅组: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Archive 把已分发且全部消费完成的事件归档（不物理删除，保留审计链路）。
func (d *Dispatcher) Archive(ctx context.Context) (int, error) {
	tag, err := d.st.Pool().Exec(ctx, `
		UPDATE kernel_event_outbox o SET status='archived'
		 WHERE o.status='dispatched'
		   AND NOT EXISTS (
		     SELECT 1 FROM kernel_jobs j
		      WHERE j.job_type='event.deliver'
		        AND j.payload_json->>'event_id' = o.event_id
		        AND j.status NOT IN ('done','dead','cancelled'))`)
	if err != nil {
		return 0, fmt.Errorf("归档 outbox: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Run 是 dispatcher 主循环：NOTIFY 唤醒 + 定时兜底扫描。阻塞直到 ctx 结束。
func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	wake := make(chan struct{}, 1)
	go func() {
		_ = d.st.Listen(ctx, store.ListenOptions{
			Channel: "kernel_outbox_wakeup",
			Handler: func(string) {
				select {
				case wake <- struct{}{}:
				default:
				}
			},
			OnReconnect: func() {
				select {
				case wake <- struct{}{}:
				default:
				}
			},
		})
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-wake:
		}
		if _, _, err := d.DispatchOnce(ctx); err != nil && ctx.Err() == nil {
			return fmt.Errorf("outbox 分发: %w", err)
		}
	}
}

// PendingCount 供 /metrics 使用。
func (d *Dispatcher) PendingCount(ctx context.Context) (int, error) {
	var n int
	if err := d.st.Pool().QueryRow(ctx, `SELECT count(*) FROM kernel_event_outbox WHERE status='pending'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计 outbox 待分发: %w", err)
	}
	return n, nil
}
