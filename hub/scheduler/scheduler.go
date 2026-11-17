// Package scheduler 是 Job 调度器：D-04 全部语义的载体，PostgreSQL 持久队列。
// 依据：内核 MVP 详设 §3.5、架构总结 §6.5 / §6.6。
// 通知只做唤醒，正确性来自兜底轮询与崩溃恢复扫描；所有租约比较用数据库时钟 now()。
package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/jackc/pgx/v5"
)

// Job 状态机（详设 §3.5）。lease_expired 只是检测信号，不落库为状态。
const (
	StatusQueued       = "queued"
	StatusLeasing      = "leasing"
	StatusRunning      = "running"
	StatusRetryBackoff = "retry_backoff"
	StatusSuspended    = "suspended"
	StatusDone         = "done"
	StatusDead         = "dead"
	StatusCancelled    = "cancelled"
)

// 人工确认三选一（D-04 建议 6）。
const (
	ResumeContinue = "continue"
	ResumeReplay   = "replay"
	ResumeDiscard  = "discard"
)

// 挂起原因。
const (
	SuspendLeaseExpired  = "lease_expired"
	SuspendYieldConfirm  = "yield_confirm"
	SuspendReplayPending = "replay_pending"
	SuspendLeaseConflict = "lease_conflict"
)

const (
	DefaultLeaseTTL     = 60 * time.Second
	DefaultPoisonPill   = 5
	DefaultRetryBackoff = 5 * time.Second
	DefaultWakeupPoll   = 5 * time.Second
	JobTypeEventDeliver = "event.deliver"
	wakeupChannel       = "kernel_jobs_wakeup"
)

var (
	ErrNotFound        = errors.New("任务不存在")
	ErrLeaseMismatch   = errors.New("租约令牌不匹配")
	ErrLeaseExpired    = errors.New("租约已过期")
	ErrBadTransition   = errors.New("任务状态迁移非法")
	ErrDuplicateKey    = errors.New("幂等键重复")
	ErrConfirmRequired = errors.New("必须经人工确认")
)

type Job struct {
	JobID          int64
	QueueName      string
	JobType        string
	Status         string
	EnvelopeID     string
	BotID          string
	ProjectID      string
	PartitionKey   string
	CorrelationID  string
	CausationID    string
	RootEnvelopeID string
	CausalPath     json.RawMessage
	Traceparent    string
	IdempotencyKey string
	ParentJobID    *int64
	LeaseOwner     string
	LeaseToken     string
	LeaseUntil     *time.Time
	AttemptCount   int
	NextRetryAt    *time.Time
	Priority       int
	PayloadHash    string
	PayloadRef     string
	PayloadJSON    json.RawMessage
	ResultJSON     json.RawMessage
	ConfigVersion  *int64
	SuspendReason  string
	ResumeDecision string
	ResumeBy       string
	ResumedAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type EnqueueRequest struct {
	QueueName      string
	JobType        string
	BotID          string
	ProjectID      string
	PartitionKey   string
	EnvelopeID     string
	CorrelationID  string
	CausationID    string
	RootEnvelopeID string
	CausalPath     json.RawMessage
	Traceparent    string
	IdempotencyKey string
	ParentJobID    *int64
	Priority       int
	PayloadHash    string
	PayloadRef     string
	PayloadJSON    json.RawMessage
	ConfigVersion  *int64
}

type Intent struct {
	IntentID      int64
	JobID         int64
	Seq           int
	Tool          string
	RequestDigest string
	Status        string
	ExecutedAt    *time.Time
	DetailJSON    json.RawMessage
}

type Scheduler struct {
	st           *store.Store
	leaseTTL     time.Duration
	poisonPill   int
	retryBackoff time.Duration
}

type Options struct {
	LeaseTTL     time.Duration
	PoisonPill   int
	RetryBackoff time.Duration
}

func New(st *store.Store, opt Options) *Scheduler {
	if opt.LeaseTTL <= 0 {
		opt.LeaseTTL = DefaultLeaseTTL
	}
	if opt.PoisonPill <= 0 {
		opt.PoisonPill = DefaultPoisonPill
	}
	if opt.RetryBackoff <= 0 {
		opt.RetryBackoff = DefaultRetryBackoff
	}
	return &Scheduler{st: st, leaseTTL: opt.LeaseTTL, poisonPill: opt.PoisonPill, retryBackoff: opt.RetryBackoff}
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("生成租约令牌失败: %v", err))
	}
	return hex.EncodeToString(b)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// Enqueue 入队：同事务写 job 与 pg_notify 唤醒。因果头必须由调用方提供，内核不补造历史。
func (s *Scheduler) Enqueue(ctx context.Context, req EnqueueRequest) (*Job, error) {
	var job *Job
	err := s.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		job, err = s.EnqueueTx(ctx, tx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

func enqueueArgs(req EnqueueRequest) []any {
	return []any{
		req.QueueName, req.JobType, nullable(req.EnvelopeID), nullable(req.BotID),
		nullable(req.ProjectID), nullable(req.PartitionKey), req.CorrelationID,
		nullable(req.CausationID), req.RootEnvelopeID, nullableJSON(req.CausalPath),
		nullable(req.Traceparent), nullable(req.IdempotencyKey), req.ParentJobID,
		req.Priority, nullable(req.PayloadHash), nullable(req.PayloadRef),
		nullableJSON(req.PayloadJSON), req.ConfigVersion,
	}
}

// EnqueueTx 在调用方已有事务内入队，供 Outbox 等需要与业务状态同事务提交的场景使用。
func (s *Scheduler) EnqueueTx(ctx context.Context, tx pgx.Tx, req EnqueueRequest) (*Job, error) {
	if req.QueueName == "" || req.JobType == "" {
		return nil, fmt.Errorf("%w: queue_name 与 job_type 必填", ErrBadTransition)
	}
	if req.CorrelationID == "" || req.RootEnvelopeID == "" {
		return nil, fmt.Errorf("%w: 缺少完整因果链", ErrBadTransition)
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO kernel_jobs
		  (queue_name, job_type, status, envelope_id, bot_id, project_id, partition_key,
		   correlation_id, causation_id, root_envelope_id, causal_path_json, traceparent,
		   idempotency_key, parent_job_id, priority, payload_hash, payload_ref, payload_json, config_version)
		VALUES ($1,$2,'queued',$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,$13,$14,$15,$16,$17::jsonb,$18)
		RETURNING job_id`, enqueueArgs(req)...).Scan(&id)
	if err != nil {
		var pgErr interface{ SQLState() string }
		if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateKey, req.IdempotencyKey)
		}
		return nil, fmt.Errorf("入队失败: %w", err)
	}
	if err := store.NotifyTx(ctx, tx, wakeupChannel, req.QueueName); err != nil {
		return nil, err
	}
	return getJob(ctx, tx, id)
}

// EnqueueTxIfAbsent 与 EnqueueTx 相同，但幂等键冲突时返回 (nil, nil) 而非报错。
// 必须用 ON CONFLICT DO NOTHING：PostgreSQL 中语句一旦报错，整个事务即 aborted，
// 调用方无法在同事务内继续执行后续语句（Outbox 分发依赖这一点）。
func (s *Scheduler) EnqueueTxIfAbsent(ctx context.Context, tx pgx.Tx, req EnqueueRequest) (*Job, error) {
	if req.QueueName == "" || req.JobType == "" {
		return nil, fmt.Errorf("%w: queue_name 与 job_type 必填", ErrBadTransition)
	}
	if req.CorrelationID == "" || req.RootEnvelopeID == "" {
		return nil, fmt.Errorf("%w: 缺少完整因果链", ErrBadTransition)
	}
	if req.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: 幂等入队必须携带 idempotency_key", ErrBadTransition)
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO kernel_jobs
		  (queue_name, job_type, status, envelope_id, bot_id, project_id, partition_key,
		   correlation_id, causation_id, root_envelope_id, causal_path_json, traceparent,
		   idempotency_key, parent_job_id, priority, payload_hash, payload_ref, payload_json, config_version)
		VALUES ($1,$2,'queued',$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,$13,$14,$15,$16,$17::jsonb,$18)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING job_id`, enqueueArgs(req)...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // 已存在，跳过
	}
	if err != nil {
		return nil, fmt.Errorf("幂等入队失败: %w", err)
	}
	if err := store.NotifyTx(ctx, tx, wakeupChannel, req.QueueName); err != nil {
		return nil, err
	}
	return getJob(ctx, tx, id)
}

const jobColumns = `job_id, queue_name, job_type, status, COALESCE(envelope_id,''), COALESCE(bot_id,''),
	COALESCE(project_id,''), COALESCE(partition_key,''), correlation_id, COALESCE(causation_id,''),
	root_envelope_id, causal_path_json, COALESCE(traceparent,''), COALESCE(idempotency_key,''),
	parent_job_id, COALESCE(lease_owner,''), COALESCE(lease_token,''), lease_until, attempt_count,
	next_retry_at, priority, COALESCE(payload_hash,''), COALESCE(payload_ref,''), payload_json,
	result_json, config_version, COALESCE(suspend_reason,''), COALESCE(resume_decision,''),
	COALESCE(resume_by,''), resumed_at, created_at, updated_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var causalPath, payloadJSON, resultJSON []byte
	err := row.Scan(&j.JobID, &j.QueueName, &j.JobType, &j.Status, &j.EnvelopeID, &j.BotID,
		&j.ProjectID, &j.PartitionKey, &j.CorrelationID, &j.CausationID, &j.RootEnvelopeID,
		&causalPath, &j.Traceparent, &j.IdempotencyKey, &j.ParentJobID, &j.LeaseOwner,
		&j.LeaseToken, &j.LeaseUntil, &j.AttemptCount, &j.NextRetryAt, &j.Priority,
		&j.PayloadHash, &j.PayloadRef, &payloadJSON, &resultJSON, &j.ConfigVersion,
		&j.SuspendReason, &j.ResumeDecision, &j.ResumeBy, &j.ResumedAt, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取任务: %w", err)
	}
	j.CausalPath = json.RawMessage(causalPath)
	j.PayloadJSON = json.RawMessage(payloadJSON)
	j.ResultJSON = json.RawMessage(resultJSON)
	return &j, nil
}

func getJob(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int64) (*Job, error) {
	return scanJob(q.QueryRow(ctx, `SELECT `+jobColumns+` FROM kernel_jobs WHERE job_id=$1`, id))
}

func (s *Scheduler) Get(ctx context.Context, jobID int64) (*Job, error) {
	return getJob(ctx, s.st.Pool(), jobID)
}

// Lease 认领任务：单事务内 UPDATE ... FROM (SELECT ... FOR UPDATE SKIP LOCKED) RETURNING。
// 严格分区用 advisory lock 锁队头并检查同分区更早的非终态任务；宽松分区直接 SKIP LOCKED。
func (s *Scheduler) Lease(ctx context.Context, queueName, workerID string, configVersion *int64) (*Job, error) {
	return s.lease(ctx, queueName, workerID, configVersion, "", "")
}

// LeaseTask 只认领普通任务，避免任务流抢走 event.deliver 投递。
func (s *Scheduler) LeaseTask(ctx context.Context, queueName, workerID string, configVersion *int64) (*Job, error) {
	return s.lease(ctx, queueName, workerID, configVersion, " AND job_type <> $2", JobTypeEventDeliver)
}

// LeaseEvent 只认领持久事件投递，供 SubscribeEvents 消费。
func (s *Scheduler) LeaseEvent(ctx context.Context, queueName, workerID string) (*Job, error) {
	return s.lease(ctx, queueName, workerID, nil, " AND job_type = $2", JobTypeEventDeliver)
}

func (s *Scheduler) lease(ctx context.Context, queueName, workerID string, configVersion *int64, typeFilter, jobType string) (*Job, error) {
	var leased *Job
	err := s.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			SELECT job_id, COALESCE(partition_key,'') FROM kernel_jobs
			 WHERE queue_name=$1 AND status IN ('queued','retry_backoff')
			   AND (next_retry_at IS NULL OR next_retry_at <= now())` + typeFilter + `
			 ORDER BY priority DESC, job_id
			 LIMIT 20
			 FOR UPDATE SKIP LOCKED`
		var (
			rows pgx.Rows
			err  error
		)
		if typeFilter == "" {
			rows, err = tx.Query(ctx, query, queueName)
		} else {
			rows, err = tx.Query(ctx, query, queueName, jobType)
		}
		if err != nil {
			return fmt.Errorf("扫描可认领任务: %w", err)
		}
		type candidate struct {
			id        int64
			partition string
		}
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.partition); err != nil {
				rows.Close()
				return fmt.Errorf("读取候选任务: %w", err)
			}
			candidates = append(candidates, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("遍历候选任务: %w", err)
		}

		for _, c := range candidates {
			if c.partition != "" {
				var locked bool
				if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext($1 || ':' || $2))`,
					queueName, c.partition).Scan(&locked); err != nil {
					return fmt.Errorf("申请分区 advisory lock: %w", err)
				}
				if !locked {
					continue // 拿不到锁即本轮跳过该分区
				}
				var earlier int
				if err := tx.QueryRow(ctx, `
					SELECT count(*) FROM kernel_jobs
					 WHERE queue_name=$1 AND partition_key=$2 AND job_id < $3
					   AND status NOT IN ('done','dead','cancelled')`,
					queueName, c.partition, c.id).Scan(&earlier); err != nil {
					return fmt.Errorf("检查同分区前序任务: %w", err)
				}
				if earlier > 0 {
					continue // 严格分区：不越位
				}
			}
			token := randomToken()
			tag, err := tx.Exec(ctx, `
				UPDATE kernel_jobs
				   SET status='leasing', lease_owner=$2, lease_token=$3,
				       lease_until=now() + make_interval(secs => $4),
				       config_version=COALESCE($5, config_version), updated_at=now()
				 WHERE job_id=$1 AND status IN ('queued','retry_backoff')`,
				c.id, workerID, token, int(s.leaseTTL.Seconds()), configVersion)
			if err != nil {
				return fmt.Errorf("认领任务: %w", err)
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			leased, err = getJob(ctx, tx, c.id)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return leased, nil // nil 表示本轮无可认领任务
}

// RenewLease 续租：令牌必须一致，且租约尚未过期（时钟以数据库 now() 为准）。
func (s *Scheduler) RenewLease(ctx context.Context, jobID int64, leaseToken string) error {
	return s.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		var notExpired bool
		var token string
		err := tx.QueryRow(ctx, `
			SELECT status, (lease_until IS NOT NULL AND lease_until > now()), COALESCE(lease_token,'')
			  FROM kernel_jobs WHERE job_id=$1 FOR UPDATE`, jobID).
			Scan(&status, &notExpired, &token)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("读取租约: %w", err)
		}
		if token != leaseToken {
			return fmt.Errorf("%w: job %d", ErrLeaseMismatch, jobID)
		}
		if status != StatusLeasing && status != StatusRunning {
			return fmt.Errorf("%w: 状态 %s 不可续租", ErrBadTransition, status)
		}
		if !notExpired {
			return fmt.Errorf("%w: job %d", ErrLeaseExpired, jobID)
		}
		_, err = tx.Exec(ctx, `
			UPDATE kernel_jobs SET lease_until=now() + make_interval(secs => $2), updated_at=now()
			 WHERE job_id=$1`, jobID, int(s.leaseTTL.Seconds()))
		if err != nil {
			return fmt.Errorf("续租失败: %w", err)
		}
		return nil
	})
}

// MarkRunning 把 leasing 推进到 running（长任务开始执行）。
func (s *Scheduler) MarkRunning(ctx context.Context, jobID int64, leaseToken string) error {
	return s.withLease(ctx, jobID, leaseToken, func(ctx context.Context, tx pgx.Tx, j *Job) error {
		if j.Status != StatusLeasing && j.Status != StatusRunning {
			return fmt.Errorf("%w: 状态 %s 不可进入 running", ErrBadTransition, j.Status)
		}
		_, err := tx.Exec(ctx, `UPDATE kernel_jobs SET status='running', updated_at=now() WHERE job_id=$1`, jobID)
		return err
	})
}

// Complete 完成任务：必须校验 lease_token，防止双 worker 重复 ack。
func (s *Scheduler) Complete(ctx context.Context, jobID int64, leaseToken string, result json.RawMessage) error {
	return s.withLease(ctx, jobID, leaseToken, func(ctx context.Context, tx pgx.Tx, j *Job) error {
		if j.Status != StatusLeasing && j.Status != StatusRunning {
			return fmt.Errorf("%w: 状态 %s 不可完成", ErrBadTransition, j.Status)
		}
		_, err := tx.Exec(ctx, `
			UPDATE kernel_jobs SET status='done', result_json=$2::jsonb, lease_until=NULL, updated_at=now()
			 WHERE job_id=$1`, jobID, nullableJSON(result))
		if err != nil {
			return fmt.Errorf("完成任务: %w", err)
		}
		return nil
	})
}

// Fail 处理失败：attempt_count++ 进 retry_backoff；连续 N 次后直接 dead（毒丸防护）。
func (s *Scheduler) Fail(ctx context.Context, jobID int64, leaseToken, reason string) error {
	return s.withLease(ctx, jobID, leaseToken, func(ctx context.Context, tx pgx.Tx, j *Job) error {
		if j.Status != StatusLeasing && j.Status != StatusRunning {
			return fmt.Errorf("%w: 状态 %s 不可标记失败", ErrBadTransition, j.Status)
		}
		next := j.AttemptCount + 1
		if next >= s.poisonPill {
			_, err := tx.Exec(ctx, `
				UPDATE kernel_jobs SET status='dead', attempt_count=$2, lease_until=NULL,
				       suspend_reason=$3, updated_at=now() WHERE job_id=$1`,
				jobID, next, "poison_pill:"+reason)
			if err != nil {
				return fmt.Errorf("标记死信: %w", err)
			}
			return nil
		}
		_, err := tx.Exec(ctx, `
			UPDATE kernel_jobs SET status='retry_backoff', attempt_count=$2, lease_token=NULL, lease_owner=NULL,
			       lease_until=NULL, next_retry_at=now() + make_interval(secs => $3), updated_at=now()
			 WHERE job_id=$1`, jobID, next, int(s.retryBackoff.Seconds()))
		if err != nil {
			return fmt.Errorf("标记重试: %w", err)
		}
		return store.NotifyTx(ctx, tx, wakeupChannel, j.QueueName)
	})
}

// Suspend 持久化挂起（yield_confirm / 租约冲突 / replay 待确认），hub 重启不丢。
func (s *Scheduler) Suspend(ctx context.Context, jobID int64, reason string) error {
	if reason == "" {
		return fmt.Errorf("%w: 挂起必须带原因", ErrBadTransition)
	}
	return s.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE kernel_jobs SET status='suspended', suspend_reason=$2, lease_until=NULL, updated_at=now()
			 WHERE job_id=$1 AND status IN ('leasing','running','queued','retry_backoff','dead')`, jobID, reason)
		if err != nil {
			return fmt.Errorf("挂起任务: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: job %d 当前状态不可挂起", ErrBadTransition, jobID)
		}
		return nil
	})
}

// Release 将正在 leasing 的任务归还队列（清空租约，恢复 queued 状态）。
// 适用场景：worker 认领到不属于当前处理流的任务类型（例如事件流拉到普通任务），立即主动释放供对应 worker 认领。
func (s *Scheduler) Release(ctx context.Context, jobID int64, leaseToken string) error {
	return s.withLease(ctx, jobID, leaseToken, func(ctx context.Context, tx pgx.Tx, j *Job) error {
		if j.Status != StatusLeasing {
			return fmt.Errorf("%w: 状态 %s 不可释放回队列", ErrBadTransition, j.Status)
		}
		_, err := tx.Exec(ctx, `
			UPDATE kernel_jobs
			   SET status='queued', lease_token=NULL, lease_owner=NULL, lease_until=NULL, updated_at=now()
			 WHERE job_id=$1`, jobID)
		if err != nil {
			return fmt.Errorf("释放任务回队列: %w", err)
		}
		return store.NotifyTx(ctx, tx, wakeupChannel, j.QueueName)
	})
}

func (s *Scheduler) withLease(ctx context.Context, jobID int64, leaseToken string, fn func(context.Context, pgx.Tx, *Job) error) error {
	return s.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var dbNow time.Time
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
			return fmt.Errorf("读取数据库时钟: %w", err)
		}
		j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM kernel_jobs WHERE job_id=$1 FOR UPDATE`, jobID))
		if err != nil {
			return err
		}
		// 终态优先判定：重复 ack 的真实原因是任务已结束，而非租约过期
		switch j.Status {
		case StatusDone, StatusDead, StatusCancelled:
			return fmt.Errorf("%w: job %d 已处于终态 %s（防重复回复）", ErrBadTransition, jobID, j.Status)
		}
		if j.LeaseToken != leaseToken {
			return fmt.Errorf("%w: job %d（防双 worker）", ErrLeaseMismatch, jobID)
		}
		if j.LeaseUntil == nil || !j.LeaseUntil.After(dbNow) {
			return fmt.Errorf("%w: job %d", ErrLeaseExpired, jobID)
		}
		return fn(ctx, tx, j)
	})
}

// DeclareIntent 在执行非幂等副作用前落盘执行意图（D-04 建议 2）。
func (s *Scheduler) DeclareIntent(ctx context.Context, jobID int64, seq int, tool, requestDigest string, detail json.RawMessage) error {
	_, err := s.st.Pool().Exec(ctx, `
		INSERT INTO kernel_job_intents (job_id, seq, tool, request_digest, status, detail_json)
		VALUES ($1,$2,$3,$4,'declared',$5::jsonb)
		ON CONFLICT (job_id, seq) DO NOTHING`, jobID, seq, tool, requestDigest, nullableJSON(detail))
	if err != nil {
		return fmt.Errorf("落盘执行意图: %w", err)
	}
	return nil
}

// MarkIntentExecuted 标记意图已执行；重投递时据此判断副作用是否已发生。
func (s *Scheduler) MarkIntentExecuted(ctx context.Context, jobID int64, seq int, status string) error {
	switch status {
	case "executed", "failed", "skipped":
	default:
		return fmt.Errorf("%w: 非法意图状态 %q", ErrBadTransition, status)
	}
	tag, err := s.st.Pool().Exec(ctx, `
		UPDATE kernel_job_intents SET status=$3, executed_at=now() WHERE job_id=$1 AND seq=$2`, jobID, seq, status)
	if err != nil {
		return fmt.Errorf("更新执行意图: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: job %d seq %d 无执行意图", ErrNotFound, jobID, seq)
	}
	return nil
}

func (s *Scheduler) ListIntents(ctx context.Context, jobID int64) ([]Intent, error) {
	rows, err := s.st.Pool().Query(ctx, `
		SELECT intent_id, job_id, seq, tool, request_digest, status, executed_at, detail_json
		  FROM kernel_job_intents WHERE job_id=$1 ORDER BY seq`, jobID)
	if err != nil {
		return nil, fmt.Errorf("读取副作用清单: %w", err)
	}
	defer rows.Close()
	var out []Intent
	for rows.Next() {
		var it Intent
		var detail []byte
		if err := rows.Scan(&it.IntentID, &it.JobID, &it.Seq, &it.Tool, &it.RequestDigest, &it.Status, &it.ExecutedAt, &detail); err != nil {
			return nil, fmt.Errorf("读取意图: %w", err)
		}
		it.DetailJSON = json.RawMessage(detail)
		out = append(out, it)
	}
	return out, rows.Err()
}

// hasExecutedIntents 判断是否已发生非幂等副作用。
func (s *Scheduler) hasExecutedIntents(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, jobID int64) (bool, error) {
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM kernel_job_intents WHERE job_id=$1 AND status='executed'`, jobID).Scan(&n); err != nil {
		return false, fmt.Errorf("查询已执行意图: %w", err)
	}
	return n > 0, nil
}

// DetectExpiredLeases 是崩溃恢复与租约冲突的检测入口。
// 已声明幂等（有 idempotency_key 且无已执行副作用）的任务自动重试；
// 其余一律 suspended 等人工确认，绝不自动重放非幂等副作用。
// 增加单批次上限（LIMIT 100）防单事务过大超时，单查询结合意图检查消除 N+1 查询。
func (s *Scheduler) DetectExpiredLeases(ctx context.Context) (suspended, requeued int, err error) {
	err = s.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT job_id, COALESCE(idempotency_key,''),
			       EXISTS(SELECT 1 FROM kernel_job_intents i WHERE i.job_id=kernel_jobs.job_id AND i.status='executed')
			  FROM kernel_jobs
			 WHERE status IN ('leasing','running') AND lease_until IS NOT NULL AND lease_until <= now()
			 ORDER BY job_id
			 LIMIT 100
			 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			return fmt.Errorf("扫描过期租约: %w", err)
		}
		defer rows.Close()

		type item struct {
			id          int64
			idempKey    string
			hasExecuted bool
		}
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.idempKey, &it.hasExecuted); err != nil {
				return fmt.Errorf("读取过期任务: %w", err)
			}
			items = append(items, it)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		for _, it := range items {
			if it.idempKey != "" && !it.hasExecuted {
				if _, err := tx.Exec(ctx, `
					UPDATE kernel_jobs SET status='retry_backoff', attempt_count=attempt_count+1,
					       lease_token=NULL, lease_owner=NULL, lease_until=NULL,
					       next_retry_at=now(), updated_at=now() WHERE job_id=$1`, it.id); err != nil {
					return fmt.Errorf("幂等任务自动重试: %w", err)
				}
				requeued++
				continue
			}
			if _, err := tx.Exec(ctx, `
				UPDATE kernel_jobs SET status='suspended', suspend_reason=$2, lease_until=NULL, updated_at=now()
				 WHERE job_id=$1`, it.id, SuspendLeaseExpired); err != nil {
				return fmt.Errorf("挂起租约冲突任务: %w", err)
			}
			suspended++
		}
		return nil
	})
	return suspended, requeued, err
}

// SuspendedView 是 CLI resume-list 的展示单元：元数据 + 因果链 + 副作用清单。
type SuspendedView struct {
	Job     *Job
	Intents []Intent
}

func (s *Scheduler) ListSuspended(ctx context.Context) ([]SuspendedView, error) {
	rows, err := s.st.Pool().Query(ctx, `SELECT `+jobColumns+` FROM kernel_jobs WHERE status='suspended' ORDER BY job_id`)
	if err != nil {
		return nil, fmt.Errorf("列出挂起任务: %w", err)
	}
	defer rows.Close()
	var out []SuspendedView
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		intents, err := s.ListIntents(ctx, j.JobID)
		if err != nil {
			return nil, err
		}
		out = append(out, SuspendedView{Job: j, Intents: intents})
	}
	return out, rows.Err()
}

// ResumeConfirmed 是人工确认幂等的唯一入口：continue / replay / discard 三选一。
// 决定与确认人写入 job 审计字段；replay 生成新 idempotency_key 重新入队。
func (s *Scheduler) ResumeConfirmed(ctx context.Context, jobID int64, decision, byUser string) (*Job, error) {
	switch decision {
	case ResumeContinue, ResumeReplay, ResumeDiscard:
	default:
		return nil, fmt.Errorf("%w: 决定只能是 continue / replay / discard", ErrConfirmRequired)
	}
	if byUser == "" {
		return nil, fmt.Errorf("%w: 必须记录确认人", ErrConfirmRequired)
	}
	var out *Job
	err := s.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM kernel_jobs WHERE job_id=$1 FOR UPDATE`, jobID))
		if err != nil {
			return err
		}
		if j.Status != StatusSuspended && j.Status != StatusDead {
			return fmt.Errorf("%w: 状态 %s 不需人工确认", ErrBadTransition, j.Status)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE kernel_jobs SET resume_decision=$2, resume_by=$3, resumed_at=now(), updated_at=now()
			 WHERE job_id=$1`, jobID, decision, byUser); err != nil {
			return fmt.Errorf("记录人工确认: %w", err)
		}
		switch decision {
		case ResumeContinue:
			// 确认已执行部分有效，从下一步继续：回到队列等待再次认领
			if _, err := tx.Exec(ctx, `
				UPDATE kernel_jobs SET status='queued', suspend_reason=NULL, attempt_count=0,
				       lease_token=NULL, lease_owner=NULL, lease_until=NULL, next_retry_at=NULL, updated_at=now()
				 WHERE job_id=$1`, jobID); err != nil {
				return fmt.Errorf("continue 迁移: %w", err)
			}
		case ResumeDiscard:
			if _, err := tx.Exec(ctx, `
				UPDATE kernel_jobs SET status='cancelled', suspend_reason=NULL, lease_until=NULL, updated_at=now()
				 WHERE job_id=$1`, jobID); err != nil {
				return fmt.Errorf("discard 迁移: %w", err)
			}
		case ResumeReplay:
			// 已执行部分可安全重放：新幂等键重新入队，原任务标记 cancelled 保留审计
			if _, err := tx.Exec(ctx, `
				UPDATE kernel_jobs SET status='cancelled', suspend_reason=NULL, lease_until=NULL, updated_at=now()
				 WHERE job_id=$1`, jobID); err != nil {
				return fmt.Errorf("replay 原任务迁移: %w", err)
			}
			newKey := "replay_" + randomToken()
			var newID int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO kernel_jobs
				  (queue_name, job_type, status, envelope_id, bot_id, project_id, partition_key,
				   correlation_id, causation_id, root_envelope_id, causal_path_json, traceparent,
				   idempotency_key, parent_job_id, priority, payload_hash, payload_ref, payload_json, config_version)
				VALUES ($1,$2,'queued',$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,$13,$14,$15,$16,$17::jsonb,$18)
				RETURNING job_id`,
				j.QueueName, j.JobType, nullable(j.EnvelopeID), nullable(j.BotID), nullable(j.ProjectID),
				nullable(j.PartitionKey), j.CorrelationID, fmt.Sprintf("%d", j.JobID), j.RootEnvelopeID,
				nullableJSON(j.CausalPath), nullable(j.Traceparent), newKey, j.JobID, j.Priority,
				nullable(j.PayloadHash), nullable(j.PayloadRef), nullableJSON(j.PayloadJSON), j.ConfigVersion).Scan(&newID); err != nil {
				return fmt.Errorf("replay 重新入队: %w", err)
			}
			if err := store.NotifyTx(ctx, tx, wakeupChannel, j.QueueName); err != nil {
				return err
			}
			out, err = getJob(ctx, tx, newID)
			return err
		}
		out, err = getJob(ctx, tx, jobID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Replay 是 DLQ 重放入口：必经人工确认，直接在单事务内行锁校验并生成新任务（消除 TOCTOU）。
func (s *Scheduler) Replay(ctx context.Context, jobID int64, byUser string) (*Job, error) {
	return s.ResumeConfirmed(ctx, jobID, ResumeReplay, byUser)
}

// DeadLetter 把任务标记为死信（DLQ 语义标记，不物理删除）。
func (s *Scheduler) DeadLetter(ctx context.Context, jobID int64, reason string) error {
	tag, err := s.st.Pool().Exec(ctx, `
		UPDATE kernel_jobs SET status='dead', suspend_reason=$2, lease_until=NULL, updated_at=now()
		 WHERE job_id=$1 AND status NOT IN ('done','cancelled')`, jobID, reason)
	if err != nil {
		return fmt.Errorf("标记死信: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: job %d 当前状态不可标记死信", ErrBadTransition, jobID)
	}
	return nil
}

// Cancel 取消尚未完成的任务。
func (s *Scheduler) Cancel(ctx context.Context, jobID int64) error {
	tag, err := s.st.Pool().Exec(ctx, `
		UPDATE kernel_jobs SET status='cancelled', lease_until=NULL, updated_at=now()
		 WHERE job_id=$1 AND status IN ('queued','retry_backoff','suspended')`, jobID)
	if err != nil {
		return fmt.Errorf("取消任务: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: job %d 当前状态不可取消", ErrBadTransition, jobID)
	}
	return nil
}

// QueueDepth 返回队列各状态计数，供 /metrics 使用。
func (s *Scheduler) QueueDepth(ctx context.Context) (map[string]int, error) {
	rows, err := s.st.Pool().Query(ctx, `SELECT status, count(*) FROM kernel_jobs GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("统计队列深度: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, fmt.Errorf("读取队列深度: %w", err)
		}
		out[st] = n
	}
	return out, rows.Err()
}
