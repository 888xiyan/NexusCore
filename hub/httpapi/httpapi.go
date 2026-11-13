// Package httpapi 是 hub 的网关侧 HTTP 入口：信封投递、载荷拉取、人工确认运营与 /metrics。
// 依据：内核 MVP 详设 §3.4（统一入口）、§5.2（replay 人工确认契约）、§7.1（可观测）。
// 说明：详设的 BotRuntime 七个 gRPC 方法不含信封投递 / 载荷拉取 / resume 运营 RPC，
// 新增 RPC 属契约变更需用户确认，故 M1 以网关 HTTP 入口承载，不动 proto 契约。
package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/ingress"
	"github.com/888xiyan/NexusCore/hub/outbox"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/hub/transport"
	"github.com/888xiyan/NexusCore/internal/envelope"
)

type API struct {
	st     *store.Store
	reg    *registry.Registry
	ing    *ingress.Ingress
	sched  *scheduler.Scheduler
	cfg    *config.Module
	disp   *outbox.Dispatcher
	router *transport.Router
	// FixtureBinder 由 hub 注入，用于 M1 冒烟夹具绑定真实二进制（M2 起由插件 install 取代）
	FixtureBinder func(ctx context.Context) error
}

func New(st *store.Store, reg *registry.Registry, ing *ingress.Ingress, sched *scheduler.Scheduler,
	cfg *config.Module, disp *outbox.Dispatcher, router *transport.Router) *API {
	return &API{st: st, reg: reg, ing: ing, sched: sched, cfg: cfg, disp: disp, router: router}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingress", a.handleIngress)
	mux.HandleFunc("GET /v1/payload/{ref}", a.handlePayload)
	mux.HandleFunc("GET /v1/jobs/suspended", a.handleSuspendedList)
	mux.HandleFunc("POST /v1/jobs/{id}/resume", a.handleResume)
	mux.HandleFunc("POST /v1/jobs/{id}/replay", a.handleReplay)
	mux.HandleFunc("POST /v1/dev/fixture", a.handleFixture)
	mux.HandleFunc("POST /v1/credentials", a.handleCredential)
	mux.HandleFunc("GET /metrics", a.handleMetrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// actorFromRequest 校验凭证并返回主体。
func (a *API) actorFromRequest(r *http.Request) (config.Actor, error) {
	cred := r.Header.Get("X-Nexus-Credential")
	if cred == "" {
		return config.Actor{}, errors.New("缺少 X-Nexus-Credential 头")
	}
	actor, _, err := a.reg.VerifyCredential(r.Context(), cred)
	return actor, err
}

func (a *API) requireUserBot(r *http.Request) (config.Actor, error) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		return config.Actor{}, err
	}
	if actor.ID != config.UserBot {
		return config.Actor{}, fmt.Errorf("只有 user_bot 能执行该操作，收到 %q", actor.ID)
	}
	return actor, nil
}

type ingressRequest struct {
	Envelope   json.RawMessage `json:"envelope"`
	PayloadB64 string          `json:"payload_base64,omitempty"`
	// 路由提示由入口适配器携带（通讯元数据），不进信封、不解析业务载荷
	QueueName    string `json:"queue_name,omitempty"`
	BotID        string `json:"bot_id,omitempty"`
	JobType      string `json:"job_type,omitempty"`
	PartitionKey string `json:"partition_key,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
}

func (a *API) handleIngress(w http.ResponseWriter, r *http.Request) {
	cred := r.Header.Get("X-Nexus-Credential")
	var req ingressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	var env envelope.Envelope
	if err := json.Unmarshal(req.Envelope, &env); err != nil {
		writeErr(w, http.StatusBadRequest, "信封解析失败: "+err.Error())
		return
	}
	var payload []byte
	if req.PayloadB64 != "" {
		p, err := base64.StdEncoding.DecodeString(req.PayloadB64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "payload_base64 解码失败")
			return
		}
		payload = p
	}
	route := ingress.Route{
		QueueName: req.QueueName, BotID: req.BotID, JobType: req.JobType,
		PartitionKey: req.PartitionKey, ProjectID: req.ProjectID,
	}
	// 未显式给 queue_name 时按目标 Bot 寻址（lazy Bot 的队列名即 bot_id）
	if route.QueueName == "" && route.BotID != "" {
		route.QueueName = route.BotID
	}
	res, err := a.ing.Submit(r.Context(), &env, cred, payload, route)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, registry.ErrInvalidCredential) || errors.Is(err, registry.ErrRevoked) {
			code = http.StatusUnauthorized
		}
		writeJSON(w, code, map[string]any{
			"decision": res.Decision, "code": string(res.Code), "reason": res.Reason,
			"envelope_id": res.EnvelopeID, "log_id": res.LogID,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"decision": res.Decision, "envelope_id": res.EnvelopeID, "log_id": res.LogID,
		"payload_ref": res.PayloadRef, "snapshot_id": res.SnapshotID,
	})
}

func (a *API) handlePayload(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	ref := r.PathValue("ref")
	data, err := a.ing.FetchPayload(r.Context(), actor, ref)
	if err != nil {
		if errors.Is(err, ingress.ErrPayloadForbidden) {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

func (a *API) handleSuspendedList(w http.ResponseWriter, r *http.Request) {
	if _, err := a.requireUserBot(r); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	views, err := a.sched.ListSuspended(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(views))
	for _, v := range views {
		intents := make([]map[string]any, 0, len(v.Intents))
		for _, it := range v.Intents {
			intents = append(intents, map[string]any{
				"seq": it.Seq, "tool": it.Tool, "status": it.Status,
				"request_digest": it.RequestDigest, "executed_at": it.ExecutedAt,
			})
		}
		out = append(out, map[string]any{
			"job_id": v.Job.JobID, "queue_name": v.Job.QueueName, "job_type": v.Job.JobType,
			"status": v.Job.Status, "suspend_reason": v.Job.SuspendReason,
			"attempt_count": v.Job.AttemptCount, "correlation_id": v.Job.CorrelationID,
			"root_envelope_id": v.Job.RootEnvelopeID, "causation_id": v.Job.CausationID,
			"envelope_id": v.Job.EnvelopeID, "idempotency_key": v.Job.IdempotencyKey,
			"intents": intents,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type resumeRequest struct {
	Decision string `json:"decision"`
}

func (a *API) handleResume(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	jobID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "job_id 必须是整数")
		return
	}
	var req resumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	job, err := a.sched.ResumeConfirmed(r.Context(), jobID, req.Decision, actor.ID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id": job.JobID, "status": job.Status, "idempotency_key": job.IdempotencyKey,
		"resume_decision": job.ResumeDecision, "resume_by": job.ResumeBy,
	})
}

func (a *API) handleReplay(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	jobID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "job_id 必须是整数")
		return
	}
	job, err := a.sched.Replay(r.Context(), jobID, actor.ID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": job.JobID, "status": job.Status})
}

// handleFixture 是 M1 冒烟夹具绑定入口：把 echo_bot 的 binary_digest 绑定到真实二进制，
// 并登记 loopback agent 身份。M2 起由插件 install / security 审查流程取代。
func (a *API) handleFixture(w http.ResponseWriter, r *http.Request) {
	if _, err := a.requireUserBot(r); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if a.FixtureBinder == nil {
		writeErr(w, http.StatusNotImplemented, "未配置夹具绑定器")
		return
	}
	if err := a.FixtureBinder(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "fixture_bound"})
}

// handleCredential 由 user_bot 为已注册主体签发短期凭证（M1 供 CLI 与 loopback agent 引导）。
// M2 起插件 / Agent 凭证随 install 与注册流程签发。
func (a *API) handleCredential(w http.ResponseWriter, r *http.Request) {
	if _, err := a.requireUserBot(r); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	var req struct {
		Kind    string `json:"kind"`
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	cred, err := a.reg.IssueCredential(r.Context(), req.Kind, req.Subject)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": cred.Token, "subject": cred.Subject, "kind": cred.Kind, "expires_at": cred.ExpiresAt,
	})
}

// handleMetrics 暴露详设 §7.1 要求的指标。
func (a *API) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	depth, err := a.sched.QueueDepth(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	pending, _ := a.disp.PendingCount(ctx)
	var configPending, drift int
	_ = a.st.Pool().QueryRow(ctx, `SELECT count(*) FROM kernel_config_documents WHERE status IN ('proposed','security_review','approved')`).Scan(&configPending)
	_ = a.st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM bot_instances i
		  JOIN bots b ON b.bot_id = i.bot_id
		 WHERE i.stopped_at IS NULL AND b.config_version IS NOT NULL
		   AND i.config_version <> b.config_version`).Scan(&drift)
	var leaseExpired, outboxLag float64
	_ = a.st.Pool().QueryRow(ctx, `SELECT count(*) FROM kernel_jobs WHERE suspend_reason='lease_expired'`).Scan(&leaseExpired)
	_ = a.st.Pool().QueryRow(ctx, `
		SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (dispatched_at - created_at))),0)
		  FROM kernel_event_outbox WHERE dispatched_at IS NOT NULL`).Scan(&outboxLag)

	var b strings.Builder
	b.WriteString("# NexusCore kernel metrics (M1)\n")
	for _, st := range []string{"queued", "leasing", "running", "retry_backoff", "suspended", "done", "dead", "cancelled"} {
		fmt.Fprintf(&b, "kernel_jobs{status=%q} %d\n", st, depth[st])
	}
	total := 0
	for _, n := range depth {
		total += n
	}
	retryRate, dlqRate := 0.0, 0.0
	if total > 0 {
		retryRate = float64(depth["retry_backoff"]) / float64(total)
		dlqRate = float64(depth["dead"]) / float64(total)
	}
	fmt.Fprintf(&b, "kernel_retry_rate %f\n", retryRate)
	fmt.Fprintf(&b, "kernel_dlq_rate %f\n", dlqRate)
	fmt.Fprintf(&b, "kernel_lease_expired_total %f\n", leaseExpired)
	fmt.Fprintf(&b, "kernel_outbox_pending %d\n", pending)
	fmt.Fprintf(&b, "kernel_outbox_dispatch_seconds_avg %f\n", outboxLag)
	fmt.Fprintf(&b, "kernel_config_pending %d\n", configPending)
	fmt.Fprintf(&b, "kernel_config_version_drift %d\n", drift)
	if a.router != nil {
		fmt.Fprintf(&b, "kernel_transport_sessions %d\n", a.router.SessionCount())
		fmt.Fprintf(&b, "kernel_transport_streams %d\n", a.router.StreamCount())
	}
	var startLatency float64
	_ = a.st.Pool().QueryRow(ctx, `
		SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (last_heartbeat_at - started_at))),0)
		  FROM bot_instances WHERE last_heartbeat_at IS NOT NULL`).Scan(&startLatency)
	fmt.Fprintf(&b, "kernel_bot_start_seconds_avg %f\n", startLatency)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(b.String()))
}

// Server 启动 HTTP 服务，阻塞直到 ctx 结束。
func (a *API) Server(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
