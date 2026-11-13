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
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/ingress"
	"github.com/888xiyan/NexusCore/hub/outbox"
	"github.com/888xiyan/NexusCore/hub/plugin"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/hub/supervisor"
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
	pm     *plugin.Manager
	sup    *supervisor.Supervisor
	// FixtureBinder 由 hub 注入，用于 M1 冒烟夹具绑定真实二进制（M2 起由插件 install 取代）
	FixtureBinder func(ctx context.Context) error
}

func (a *API) SetPluginManager(pm *plugin.Manager)     { a.pm = pm }
func (a *API) SetSupervisor(sup *supervisor.Supervisor) { a.sup = sup }

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

	// M2: 配置中心完整审批流 API
	mux.HandleFunc("GET /v1/config/list", a.handleConfigList)
	mux.HandleFunc("GET /v1/config/get", a.handleConfigGet)
	mux.HandleFunc("GET /v1/config/history", a.handleConfigHistory)
	mux.HandleFunc("POST /v1/config/propose", a.handleConfigPropose)
	mux.HandleFunc("GET /v1/config/diff/{proposal_id}", a.handleConfigDiff)
	mux.HandleFunc("POST /v1/config/review", a.handleConfigReview)
	mux.HandleFunc("POST /v1/config/approve", a.handleConfigApprove)
	mux.HandleFunc("POST /v1/config/reject", a.handleConfigReject)
	mux.HandleFunc("POST /v1/config/activate", a.handleConfigActivate)
	mux.HandleFunc("POST /v1/config/rollback", a.handleConfigRollback)
	mux.HandleFunc("GET /v1/config/pending", a.handleConfigPending)
	mux.HandleFunc("POST /v1/config/secret", a.handleConfigSecret)

	// M2: Bot 管理 API
	mux.HandleFunc("POST /v1/bots", a.handleBotCreate)
	mux.HandleFunc("GET /v1/bots", a.handleBotList)
	mux.HandleFunc("POST /v1/bots/{bot_id}/enable", a.handleBotEnable)
	mux.HandleFunc("POST /v1/create-bot/pull", a.handleCreateBotPull)

	// M2: 插件生命周期与两级进程 API
	mux.HandleFunc("POST /v1/plugins/install", a.handlePluginInstall)
	mux.HandleFunc("GET /v1/plugins", a.handlePluginList)
	mux.HandleFunc("GET /v1/plugins/security", a.handleSecurityList)
	mux.HandleFunc("POST /v1/plugins/security/approve", a.handleSecurityApprove)
	mux.HandleFunc("POST /v1/plugins/security/revoke", a.handleSecurityRevoke)
	mux.HandleFunc("POST /v1/plugins/upgrade", a.handlePluginUpgrade)
	mux.HandleFunc("POST /v1/plugins/{plugin_id}/disable", a.handlePluginDisable)
	mux.HandleFunc("POST /v1/plugins/{plugin_id}/uninstall", a.handlePluginUninstall)
	mux.HandleFunc("GET /v1/plugins/processes", a.handlePluginProcessList)
	mux.HandleFunc("POST /v1/plugins/{plugin_id}/process/stop", a.handlePluginProcessStop)
	mux.HandleFunc("POST /v1/plugins/{plugin_id}/process/start", a.handlePluginProcessStart)
	mux.HandleFunc("POST /v1/plugins/{plugin_id}/process/kill", a.handlePluginProcessKill)
	mux.HandleFunc("POST /v1/plugins/process/report", a.handlePluginProcessReport)

	// M2: DLQ 运营 API
	mux.HandleFunc("GET /v1/dlq", a.handleDLQList)
	mux.HandleFunc("POST /v1/dlq/{id}/archive", a.handleDLQArchive)

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

// ---------------------- M2: 配置中心 HTTP 接口 ----------------------

func (a *API) handleConfigList(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	scopeType := r.URL.Query().Get("scope")
	docs, err := a.cfg.List(r.Context(), actor, scopeType)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, docs)
}

func (a *API) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	scopeType := r.URL.Query().Get("scope_type")
	scopeID := r.URL.Query().Get("scope_id")
	key := r.URL.Query().Get("key")
	verStr := r.URL.Query().Get("version")
	var ver int64
	if verStr != "" {
		ver, _ = strconv.ParseInt(verStr, 10, 64)
	}
	doc, err := a.cfg.Get(r.Context(), actor, scopeType, scopeID, key, ver)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, config.ErrNotFound) {
			code = http.StatusNotFound
		} else if errors.Is(err, config.ErrForbidden) {
			code = http.StatusForbidden
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (a *API) handleConfigHistory(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	scopeType := r.URL.Query().Get("scope_type")
	scopeID := r.URL.Query().Get("scope_id")
	key := r.URL.Query().Get("key")
	vers, err := a.cfg.ListVersions(r.Context(), actor, scopeType, scopeID, key)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vers)
}

type configProposeReq struct {
	ScopeType   string          `json:"scope_type"`
	ScopeID     string          `json:"scope_id"`
	ConfigKey   string          `json:"config_key"`
	Spec        json.RawMessage `json:"spec"`
	BaseVersion int64           `json:"base_version"`
}

func (a *API) handleConfigPropose(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req configProposeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if req.BaseVersion <= 0 {
		if act, err := a.cfg.Get(r.Context(), actor, req.ScopeType, req.ScopeID, req.ConfigKey, 0); err == nil && act != nil {
			req.BaseVersion = act.Version
		}
	}
	propID, err := a.cfg.Propose(r.Context(), actor, req.ScopeType, req.ScopeID, req.ConfigKey, req.Spec, req.BaseVersion)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, config.ErrConflict) {
			code = http.StatusConflict
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"proposal_id": propID})
}

func (a *API) handleConfigDiff(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	propID := r.PathValue("proposal_id")
	diff, err := a.cfg.Diff(r.Context(), actor, propID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

type configReviewReq struct {
	ProposalID string `json:"proposal_id"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
}

func (a *API) handleConfigReview(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req configReviewReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := a.cfg.Review(r.Context(), actor, req.ProposalID, req.Decision, req.Reason); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, config.ErrForbidden) {
			code = http.StatusForbidden
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reviewed"})
}

type configApproveReq struct {
	ProposalID string `json:"proposal_id"`
	Checksum   string `json:"checksum"`
}

func (a *API) handleConfigApprove(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	var req configApproveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := a.cfg.Approve(r.Context(), actor, req.ProposalID, req.Checksum); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, config.ErrConflict) {
			code = http.StatusConflict
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

type configRejectReq struct {
	ProposalID string `json:"proposal_id"`
	Reason     string `json:"reason"`
}

func (a *API) handleConfigReject(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req configRejectReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := a.cfg.Reject(r.Context(), actor, req.ProposalID, req.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

type configActivateReq struct {
	ProposalID     string `json:"proposal_id"`
	CorrelationID  string `json:"correlation_id"`
	CausationID    string `json:"causation_id"`
	RootEnvelopeID string `json:"root_envelope_id"`
}

func (a *API) handleConfigActivate(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req configActivateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	cc := config.CausalContext{
		CorrelationID:  req.CorrelationID,
		CausationID:    req.CausationID,
		RootEnvelopeID: req.RootEnvelopeID,
	}
	if cc.CorrelationID == "" {
		id := "act_" + fmt.Sprint(time.Now().UnixNano())
		cc.CorrelationID = id
		cc.CausationID = id
		cc.RootEnvelopeID = id
	}
	if cc.CausationID == "" {
		cc.CausationID = cc.CorrelationID
	}
	if cc.RootEnvelopeID == "" {
		cc.RootEnvelopeID = cc.CorrelationID
	}
	if err := a.cfg.Activate(r.Context(), actor, req.ProposalID, cc); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, config.ErrConflict) {
			code = http.StatusConflict
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "activated"})
}

type configRollbackReq struct {
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	ConfigKey string `json:"config_key"`
	ToVersion int64  `json:"to_version"`
}

func (a *API) handleConfigRollback(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req configRollbackReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	propID, err := a.cfg.Rollback(r.Context(), actor, req.ScopeType, req.ScopeID, req.ConfigKey, req.ToVersion)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"proposal_id": propID})
}

func (a *API) handleConfigPending(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	scopeType := r.URL.Query().Get("scope")
	docs, err := a.cfg.ListPending(r.Context(), actor, scopeType)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, docs)
}

type secretAddReq struct {
	Provider    string `json:"provider"`
	Locator     string `json:"locator"`
	Description string `json:"description"`
}

func (a *API) handleConfigSecret(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	var req secretAddReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	refID, err := a.cfg.AddSecret(r.Context(), actor, req.Provider, req.Locator, req.Description)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ref_id": refID})
}

// ---------------------- M2: Bot 管理 HTTP 接口 ----------------------

type botCreateReq struct {
	BotID          string   `json:"bot_id"`
	DisplayName    string   `json:"display_name"`
	ActivationMode string   `json:"activation_mode"`
	Capabilities   []string `json:"capabilities"`
	PromptRef      string   `json:"prompt_ref"`
	Model          string   `json:"model"`
	MemoryProfile  string   `json:"memory_profile"`
}

func (a *API) handleBotCreate(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	var req botCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.BotID == "" {
		writeErr(w, http.StatusBadRequest, "bot_id 不能为空")
		return
	}
	reserved := map[string]bool{
		config.UserBot: true, config.CreateBot: true, config.MonitorBot: true, config.SecurityBot: true,
	}
	if reserved[req.BotID] {
		writeErr(w, http.StatusBadRequest, "禁止创建内核保留名 Bot")
		return
	}

	mode := req.ActivationMode
	if mode == "" {
		mode = "lazy"
	}
	caps, _ := json.Marshal(req.Capabilities)

	// 规则 8: 用户建 user_xxx_bot 默认 memory_profile = personal_stack_v1
	memProfile := req.MemoryProfile
	if strings.HasPrefix(req.BotID, "user_") && memProfile == "" {
		memProfile = "personal_stack_v1"
	}

	var memJSON any
	if memProfile != "" {
		b, _ := json.Marshal(memProfile)
		memJSON = string(b)
	}

	var botBinaryPath, botDigest string
	_ = a.st.Pool().QueryRow(r.Context(), `SELECT binary_path, binary_digest FROM bot_manifest WHERE bot_id='user_bot'`).Scan(&botBinaryPath, &botDigest)
	if botBinaryPath == "" {
		botBinaryPath = "bin/bot-runtime"
	}
	if botDigest == "" {
		botDigest = strings.Repeat("0", 64)
	}

	_, err = a.st.Pool().Exec(r.Context(), `
		INSERT INTO bot_manifest (
			bot_id, role, managed_by, owner, display_name,
			binary_path, binary_digest, manifest_digest, capabilities,
			memory_profile, activation_mode, prompt_ref, model
		) VALUES (
			$1, 'plugin', 'user_bot', $2, $3,
			$9, $10, $10, $4,
			$5, $6, $7, $8
		) ON CONFLICT (bot_id) DO UPDATE SET
			display_name=EXCLUDED.display_name, activation_mode=EXCLUDED.activation_mode,
			binary_path=EXCLUDED.binary_path, binary_digest=EXCLUDED.binary_digest, manifest_digest=EXCLUDED.manifest_digest,
			memory_profile=EXCLUDED.memory_profile, capabilities=EXCLUDED.capabilities,
			updated_at=now()`,
		req.BotID, actor.ID, req.DisplayName, caps,
		memJSON, mode, req.PromptRef, req.Model, botBinaryPath, botDigest)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "写入 bot_manifest: "+err.Error())
		return
	}

	minInst := 0
	if mode == "resident" {
		minInst = 1
	}
	_, err = a.st.Pool().Exec(r.Context(), `
		INSERT INTO bots (bot_id, desired_state, desired_by, observed_state, enabled, min_instances)
		VALUES ($1, 'start', 'create_bot', 'dormant', true, $2)
		ON CONFLICT (bot_id) DO UPDATE SET
			desired_state='start', enabled=true, min_instances=EXCLUDED.min_instances, updated_at=now()`,
		req.BotID, minInst)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "写入 bots: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"bot_id":         req.BotID,
		"memory_profile": memProfile,
		"status":         "created",
	})
}

func (a *API) handleBotList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.st.Pool().Query(r.Context(), `
		SELECT b.bot_id, m.role, m.display_name, m.activation_mode, m.capabilities,
		       COALESCE(m.memory_profile#>>'{}', ''), b.config_version, b.desired_state, b.observed_state, b.enabled
		  FROM bots b
		  JOIN bot_manifest m ON m.bot_id = b.bot_id
		 ORDER BY b.bot_id`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	type botItem struct {
		BotID          string          `json:"bot_id"`
		Role           string          `json:"role"`
		DisplayName    string          `json:"display_name"`
		ActivationMode string          `json:"activation_mode"`
		Capabilities   json.RawMessage `json:"capabilities"`
		MemoryProfile  string          `json:"memory_profile,omitempty"`
		ConfigVersion  *int64          `json:"config_version,omitempty"`
		DesiredState   string          `json:"desired_state"`
		ObservedState  string          `json:"observed_state"`
		Enabled        bool            `json:"enabled"`
	}
	var out []botItem
	for rows.Next() {
		var it botItem
		if err := rows.Scan(
			&it.BotID, &it.Role, &it.DisplayName, &it.ActivationMode, &it.Capabilities,
			&it.MemoryProfile, &it.ConfigVersion, &it.DesiredState, &it.ObservedState, &it.Enabled,
		); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, out)
}

type botEnableReq struct {
	Mode    string `json:"mode"`
	Enabled bool   `json:"enabled"`
}

func (a *API) handleBotEnable(w http.ResponseWriter, r *http.Request) {
	if _, err := a.requireUserBot(r); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	botID := r.PathValue("bot_id")
	var req botEnableReq
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.Mode != "" {
		if req.Mode != "lazy" && req.Mode != "resident" {
			writeErr(w, http.StatusBadRequest, "mode 必须是 lazy 或 resident")
			return
		}
		_, _ = a.st.Pool().Exec(r.Context(), `
			UPDATE bot_manifest SET activation_mode=$2, updated_at=now() WHERE bot_id=$1`, botID, req.Mode)
		minInst := 0
		if req.Mode == "resident" {
			minInst = 1
		}
		_, _ = a.st.Pool().Exec(r.Context(), `
			UPDATE bots SET min_instances=$2, updated_at=now() WHERE bot_id=$1`, botID, minInst)
	}

	desired := "stop"
	if req.Enabled {
		desired = "start"
	}
	_, err := a.st.Pool().Exec(r.Context(), `
		UPDATE bots SET enabled=$2, desired_state=$3, updated_at=now() WHERE bot_id=$1`, botID, req.Enabled, desired)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bot_id": botID, "enabled": req.Enabled, "desired_state": desired})
}

func (a *API) handleCreateBotPull(w http.ResponseWriter, r *http.Request) {
	if _, err := a.requireUserBot(r); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if a.sup == nil {
		writeErr(w, http.StatusNotImplemented, "supervisor 未就绪")
		return
	}
	started, errs := a.sup.Reconcile(r.Context())
	errStrs := make([]string, 0, len(errs))
	for _, e := range errs {
		errStrs = append(errStrs, e.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"started": started,
		"errors":  errStrs,
	})
}

// ---------------------- M2: 插件生命周期与两级进程 HTTP 接口 ----------------------

func (a *API) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	mf, decisionID, err := a.pm.Install(r.Context(), actor, body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plugin_id":   mf.PluginID,
		"decision_id": decisionID,
		"status":      "installed",
	})
}

func (a *API) handlePluginList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.st.Pool().Query(r.Context(), `
		SELECT plugin_id, plugin_type, display_name, namespace, status, config_version, created_at
		  FROM kernel_plugins ORDER BY plugin_id`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	type pluginItem struct {
		PluginID      string    `json:"plugin_id"`
		PluginType    string    `json:"plugin_type"`
		DisplayName   string    `json:"display_name"`
		Namespace     string    `json:"namespace"`
		Status        string    `json:"status"`
		ConfigVersion *int64    `json:"config_version,omitempty"`
		CreatedAt     time.Time `json:"created_at"`
	}
	var out []pluginItem
	for rows.Next() {
		var it pluginItem
		if err := rows.Scan(&it.PluginID, &it.PluginType, &it.DisplayName, &it.Namespace, &it.Status, &it.ConfigVersion, &it.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleSecurityList(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	status := r.URL.Query().Get("status")
	list, err := a.pm.SecurityList(r.Context(), actor, status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type securityApproveReq struct {
	DecisionID int64 `json:"decision_id"`
}

func (a *API) handleSecurityApprove(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	var req securityApproveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := a.pm.SecurityApprove(r.Context(), actor, req.DecisionID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

type securityRevokeReq struct {
	DecisionID int64  `json:"decision_id"`
	Reason     string `json:"reason"`
}

func (a *API) handleSecurityRevoke(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	var req securityRevokeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := a.pm.SecurityRevoke(r.Context(), actor, req.DecisionID, req.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (a *API) handlePluginUpgrade(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	mf, decisionID, err := a.pm.Upgrade(r.Context(), actor, body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugin_id": mf.PluginID, "decision_id": decisionID})
}

func (a *API) handlePluginDisable(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	pluginID := r.PathValue("plugin_id")
	if err := a.pm.Disable(r.Context(), actor, pluginID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"plugin_id": pluginID, "status": "disabled"})
}

type pluginUninstallReq struct {
	Confirm bool `json:"confirm"`
	Keep    bool `json:"keep"`
}

func (a *API) handlePluginUninstall(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	pluginID := r.PathValue("plugin_id")
	var req pluginUninstallReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := a.pm.Uninstall(r.Context(), actor, pluginID, req.Confirm, req.Keep); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"plugin_id": pluginID, "status": "uninstalled"})
}

func (a *API) handlePluginProcessList(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	list, err := a.pm.ListProcesses(r.Context(), actor)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) handlePluginProcessStop(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	pluginID := r.PathValue("plugin_id")
	if err := a.pm.StopProcess(r.Context(), actor, pluginID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"plugin_id": pluginID, "status": "stopped"})
}

func (a *API) handlePluginProcessStart(w http.ResponseWriter, r *http.Request) {
	if _, err := a.requireUserBot(r); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	pluginID := r.PathValue("plugin_id")
	// 将状态置为 running / starting 等待 owner bot exec
	_, err := a.st.Pool().Exec(r.Context(), `
		UPDATE kernel_plugin_processes SET state='starting', last_seen_at=now() WHERE plugin_id=$1`, pluginID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"plugin_id": pluginID, "status": "starting"})
}

type pluginProcessKillReq struct {
	Confirm bool   `json:"confirm"`
	Reason  string `json:"reason"`
}

func (a *API) handlePluginProcessKill(w http.ResponseWriter, r *http.Request) {
	actor, err := a.requireUserBot(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	pluginID := r.PathValue("plugin_id")
	var req pluginProcessKillReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if !req.Confirm {
		writeErr(w, http.StatusBadRequest, "强制终止必须提供 confirm: true")
		return
	}
	if err := a.pm.KillProcess(r.Context(), actor, pluginID, req.Confirm, req.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"plugin_id": pluginID, "status": "killed"})
}

func (a *API) handlePluginProcessReport(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actorFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if a.pm == nil {
		writeErr(w, http.StatusNotImplemented, "plugin manager 未就绪")
		return
	}
	var rep plugin.ProcessReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := a.pm.ReportProcess(r.Context(), actor, rep); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reported"})
}

// ---------------------- M2: DLQ 运营 HTTP 接口 ----------------------

func (a *API) handleDLQList(w http.ResponseWriter, r *http.Request) {
	if _, err := a.requireUserBot(r); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	rows, err := a.st.Pool().Query(r.Context(), `
		SELECT job_id, queue_name, job_type, status, attempt_count, COALESCE(suspend_reason, ''), correlation_id, created_at
		  FROM kernel_jobs WHERE status='dead' ORDER BY job_id DESC`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	type dlqItem struct {
		JobID         int64     `json:"job_id"`
		QueueName     string    `json:"queue_name"`
		JobType       string    `json:"job_type"`
		Status        string    `json:"status"`
		AttemptCount  int       `json:"attempt_count"`
		LastError     string    `json:"last_error"`
		CorrelationID string    `json:"correlation_id"`
		CreatedAt     time.Time `json:"created_at"`
	}
	var out []dlqItem
	for rows.Next() {
		var it dlqItem
		if err := rows.Scan(&it.JobID, &it.QueueName, &it.JobType, &it.Status, &it.AttemptCount, &it.LastError, &it.CorrelationID, &it.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleDLQArchive(w http.ResponseWriter, r *http.Request) {
	if _, err := a.requireUserBot(r); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	idStr := r.PathValue("id")
	jobID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "job_id 必须是整数")
		return
	}
	res, err := a.st.Pool().Exec(r.Context(), `
		UPDATE kernel_jobs SET status='cancelled', updated_at=now() WHERE job_id=$1 AND status='dead'`, jobID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "未找到状态为 dead 的任务")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": jobID, "status": "archived"})
}
