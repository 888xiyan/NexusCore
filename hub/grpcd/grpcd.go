// Package grpcd 实现 BotRuntime gRPC 控制面（内核 MVP 详设 §3.3 / §5.0）。
// 所有 RPC 必须携带因果上下文元数据，缺失返回 CAUSAL_CONTEXT_REQUIRED；
// 请求统一走 ingress 校验与日志。gRPC 只承载控制面，执行流走 transport。
package grpcd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/eventbus"
	"github.com/888xiyan/NexusCore/hub/ingress"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/envelope"
	pb "github.com/888xiyan/NexusCore/proto/gen/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Registrar 是 supervisor 暴露给控制面的回调（避免 grpcd 依赖 supervisor 具体类型）。
type Registrar interface {
	MarkRegistered(botID string, configVersion int64)
	MarkHeartbeat(botID string, configVersion int64)
}

type Server struct {
	pb.UnimplementedBotRuntimeServer
	st       *store.Store
	reg      *registry.Registry
	ing      *ingress.Ingress
	sched    *scheduler.Scheduler
	bus      *eventbus.Bus
	sup      Registrar
	leaseTTL time.Duration
	poll     time.Duration
}

func NewServer(st *store.Store, reg *registry.Registry, ing *ingress.Ingress, sched *scheduler.Scheduler, bus *eventbus.Bus, sup Registrar, leaseTTL, poll time.Duration) *Server {
	if leaseTTL <= 0 {
		leaseTTL = scheduler.DefaultLeaseTTL
	}
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	return &Server{st: st, reg: reg, ing: ing, sched: sched, bus: bus, sup: sup, leaseTTL: leaseTTL, poll: poll}
}

func (s *Server) RegisterService(gs *grpc.Server) {
	pb.RegisterBotRuntimeServer(gs, s)
}

// mdFromContext 取出 gRPC 元数据。
func mdFromContext(ctx context.Context) map[string]string {
	out := map[string]string{}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return out
	}
	for k, v := range md {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

// authenticate 校验因果元数据 + 凭证，并把本次 RPC 作为 control 信封过 ingress 落审计。
func (s *Server) authenticate(ctx context.Context) (config.Actor, *envelope.Envelope, error) {
	md := mdFromContext(ctx)
	env, err := envelope.FromMetadata(md)
	if err != nil {
		var ve *envelope.ValidationError
		if errors.As(err, &ve) && ve.Code == envelope.CodeCausalRequired {
			return config.Actor{}, nil, status.Error(codes.Unauthenticated, "CAUSAL_CONTEXT_REQUIRED: "+ve.Error())
		}
		return config.Actor{}, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	cred := md[envelope.MetaCredential]
	res, err := s.ing.Submit(ctx, env, cred, nil, ingress.Route{})
	if err != nil {
		if errors.Is(err, registry.ErrInvalidCredential) || errors.Is(err, registry.ErrRevoked) {
			return config.Actor{}, nil, status.Error(codes.Unauthenticated, err.Error())
		}
		return config.Actor{}, nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if res.Decision == ingress.DecisionRejected {
		return config.Actor{}, nil, status.Error(codes.Unauthenticated, res.Reason)
	}
	return res.Actor, env, nil
}

func (s *Server) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	actor, _, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if actor.Kind != config.KindBot || actor.ID != req.BotId {
		return nil, status.Error(codes.PermissionDenied, "凭证主体与 bot_id 不一致")
	}
	var wantDigest string
	var configVersion *int64
	err = s.st.Pool().QueryRow(ctx, `SELECT manifest_digest, config_version FROM bot_manifest WHERE bot_id=$1`, req.BotId).
		Scan(&wantDigest, &configVersion)
	if err != nil {
		return nil, status.Error(codes.NotFound, "bot 未注册: "+req.BotId)
	}
	if req.ManifestDigest != "" && req.ManifestDigest != wantDigest {
		return nil, status.Error(codes.PermissionDenied, "manifest_digest 与登记不一致")
	}
	cred, err := s.reg.IssueCredential(ctx, config.KindBot, req.BotId)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	cv := req.ConfigVersion
	if cv == 0 && configVersion != nil {
		cv = *configVersion
	}
	if s.sup != nil {
		s.sup.MarkRegistered(req.BotId, cv)
	}
	return &pb.RegisterResponse{Token: cred.Token, TtlSeconds: int64(time.Until(cred.ExpiresAt).Seconds())}, nil
}

func (s *Server) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	actor, _, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if actor.ID != req.BotId {
		return nil, status.Error(codes.PermissionDenied, "凭证主体与 bot_id 不一致")
	}
	// 兼作长任务租约续期：逐个校验令牌，失败的令牌不影响其他任务
	for _, token := range req.JobLeaseTokens {
		jobID, err := s.jobIDByLeaseToken(ctx, req.BotId, token)
		if err != nil {
			continue
		}
		if err := s.sched.RenewLease(ctx, jobID, token); err != nil {
			log.Printf("grpcd: renew lease failed for bot %s job %d: %v", req.BotId, jobID, err)
		}
	}
	if s.sup != nil {
		s.sup.MarkHeartbeat(req.BotId, req.ConfigVersion)
	}
	return &pb.HeartbeatResponse{}, nil
}

func (s *Server) jobIDByLeaseToken(ctx context.Context, botID, token string) (int64, error) {
	var id int64
	err := s.st.Pool().QueryRow(ctx, `
		SELECT job_id FROM kernel_jobs
		 WHERE lease_token=$1 AND bot_id=$2 AND status IN ('leasing','running')`, token, botID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("按租约令牌定位任务: %w", err)
	}
	return id, nil
}

func (s *Server) StreamTasks(req *pb.StreamTasksRequest, stream pb.BotRuntime_StreamTasksServer) error {
	ctx := stream.Context()
	actor, _, err := s.authenticate(ctx)
	if err != nil {
		return err
	}
	if actor.Kind != config.KindBot {
		return status.Error(codes.PermissionDenied, "只有 Bot 能拉取任务")
	}
	queue := req.ConsumerGroup
	if queue == "" {
		queue = actor.ID
	}
	var configVersion *int64
	_ = s.st.Pool().QueryRow(ctx, `SELECT config_version FROM bot_manifest WHERE bot_id=$1`, actor.ID).Scan(&configVersion)

	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		job, err := s.sched.Lease(ctx, queue, actor.ID, configVersion)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
			continue
		}
		causalJSON, err := json.Marshal(map[string]any{
			"job_id": job.JobID, "job_type": job.JobType, "queue_name": job.QueueName,
			"correlation_id": job.CorrelationID, "causation_id": job.CausationID,
			"root_envelope_id": job.RootEnvelopeID, "traceparent": job.Traceparent,
			"envelope_id": job.EnvelopeID, "config_version": job.ConfigVersion,
		})
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if err := stream.Send(&pb.TaskAssignment{
			JobId:             strconv.FormatInt(job.JobID, 10),
			LeaseToken:        job.LeaseToken,
			PayloadRef:        job.PayloadRef,
			CausalContextJson: string(causalJSON),
		}); err != nil {
			// 发送失败不 ack：租约到期后由 DetectExpiredLeases 处置
			return nil
		}
	}
}

func (s *Server) AckTask(ctx context.Context, req *pb.AckTaskRequest) (*pb.AckTaskResponse, error) {
	actor, _, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	jobID, err := strconv.ParseInt(req.JobId, 10, 64)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "job_id 必须是整数")
	}
	result, err := json.Marshal(map[string]any{"result_ref": req.ResultRef, "acked_by": actor.ID})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.sched.Complete(ctx, jobID, req.LeaseToken, result); err != nil {
		switch {
		case errors.Is(err, scheduler.ErrLeaseMismatch):
			return nil, status.Error(codes.PermissionDenied, "lease_token 不匹配（防双 worker）")
		case errors.Is(err, scheduler.ErrLeaseExpired):
			return nil, status.Error(codes.FailedPrecondition, "租约已过期")
		case errors.Is(err, scheduler.ErrBadTransition):
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		case errors.Is(err, scheduler.ErrNotFound):
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.AckTaskResponse{}, nil
}

func (s *Server) SubscribeEvents(req *pb.SubscribeRequest, stream pb.BotRuntime_SubscribeEventsServer) error {
	ctx := stream.Context()
	actor, _, err := s.authenticate(ctx)
	if err != nil {
		return err
	}
	group := req.ConsumerGroup
	if group == "" {
		return status.Error(codes.InvalidArgument, "consumer_group 必填")
	}
	if err := s.authorizeGroup(ctx, actor, group); err != nil {
		return err
	}
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		job, err := s.sched.Lease(ctx, group, actor.ID, nil)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
			continue
		}
		if job.JobType != scheduler.JobTypeEventDeliver {
			// 非事件投递任务不属于本流，放回队列供对应任务流认领
			if err := s.sched.Release(ctx, job.JobID, job.LeaseToken); err != nil {
				log.Printf("grpcd: release non-event job %d failed: %v", job.JobID, err)
			}
			continue
		}
		var payload struct {
			EventID   string `json:"event_id"`
			EventType string `json:"event_type"`
			Summary   struct {
				PayloadRef string `json:"payload_ref"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(job.PayloadJSON, &payload); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		// 消费侧幂等：已处理过的事件只确认状态，不重复投递副作用
		processed, err := s.bus.IsProcessed(ctx, group, payload.EventID)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if processed {
			_ = s.sched.Complete(ctx, job.JobID, job.LeaseToken, nil)
			continue
		}
		if err := stream.Send(&pb.Event{
			EventId:       payload.EventID,
			EventType:     payload.EventType,
			PayloadRef:    payload.Summary.PayloadRef,
			CorrelationId: job.CorrelationID,
		}); err != nil {
			return nil // 未 ack，租约到期后重投
		}
		// 送达即脱离流上下文记账：订阅端收到后立刻断开是常态（CLI -wait），
		// 用 stream.Context() 会让去重与 ack 随取消一起失败，任务泄漏在 leasing。
		ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.TxTimeout)
		err = s.bus.MarkProcessed(ackCtx, group, payload.EventID)
		if err == nil {
			err = s.sched.Complete(ackCtx, job.JobID, job.LeaseToken, nil)
		}
		cancel()
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
}

// authorizeGroup 校验调用方是否有权消费该组：内核角色 user_bot 或组归属插件 / Bot。
func (s *Server) authorizeGroup(ctx context.Context, actor config.Actor, group string) error {
	if actor.Kind == config.KindKernelRole && actor.ID == config.UserBot {
		return nil
	}
	var pluginID, botID *string
	err := s.st.Pool().QueryRow(ctx, `SELECT plugin_id, bot_id FROM kernel_plugin_subs WHERE consumer_group_id=$1 AND status='active'`, group).
		Scan(&pluginID, &botID)
	if err != nil {
		return status.Error(codes.PermissionDenied, "Consumer Group 不存在或已停止")
	}
	if actor.Kind == config.KindPlugin && pluginID != nil && *pluginID == actor.ID {
		return nil
	}
	if actor.Kind == config.KindBot && botID != nil && *botID == actor.ID {
		return nil
	}
	return status.Error(codes.PermissionDenied, "无权消费该 Consumer Group")
}

func (s *Server) RenewCredential(ctx context.Context, req *pb.RenewRequest) (*pb.RenewResponse, error) {
	if _, _, err := s.authenticate(ctx); err != nil {
		return nil, err
	}
	cred, err := s.reg.RenewCredential(ctx, req.Token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	return &pb.RenewResponse{Token: cred.Token, TtlSeconds: int64(time.Until(cred.ExpiresAt).Seconds())}, nil
}

func (s *Server) Deregister(ctx context.Context, req *pb.DeregisterRequest) (*pb.DeregisterResponse, error) {
	actor, _, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if actor.ID != req.BotId {
		return nil, status.Error(codes.PermissionDenied, "凭证主体与 bot_id 不一致")
	}
	err = s.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE bot_instances SET observed_state='stopped', stopped_at=now()
			 WHERE bot_id=$1 AND stopped_at IS NULL`, req.BotId); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE bots SET observed_state='dormant', updated_at=now() WHERE bot_id=$1`, req.BotId)
		return err
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.DeregisterResponse{}, nil
}
