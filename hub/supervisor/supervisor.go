// Package supervisor 是 Bot 进程的唯一执行器：lazy exec、制品校验、监督与回收。
// 依据：内核 MVP 详设 §3.9、架构总结 §5.5、D-20 决策执行分离。
// desired_state 的修改者只有 create_bot；supervisor 只读 desired、只写 observed。
// M1 只处理 Bot 进程（L1）；插件进程（L2）的强制终止与孤儿回收自 M2 起。
package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/osproc"
	"github.com/jackc/pgx/v5"
)

const (
	DefaultReconcileInterval = 2 * time.Second
	DefaultStartTimeout      = 30 * time.Second
	DefaultMaxStartFailures  = 3
	maxBackoff               = 60 * time.Second
	// BotCredentialEnv 是传给子进程的凭证通道（不走 argv，避免出现在进程列表）
	BotCredentialEnv = "NEXUS_BOT_CREDENTIAL"
)

var (
	ErrDigestMismatch = errors.New("制品 binary_digest 与 manifest 不一致")
	ErrCircuitBroken  = errors.New("连续启动失败已熔断")
	ErrNotLazy        = errors.New("不支持的 activation_mode（仅支持 lazy 或 resident）")
)

type process struct {
	botID         string
	instanceID    int64
	cmd           *exec.Cmd
	pid           int
	configVersion int64
	startedAt     time.Time
	registered    chan struct{}
	once          sync.Once
	exited        chan struct{}
}

type Supervisor struct {
	st    *store.Store
	reg   *registry.Registry
	sched *scheduler.Scheduler
	cfg   *config.Module

	grpcAddr      string
	httpAddr      string
	transportAddr string
	reconcile     time.Duration
	startTimeout  time.Duration
	maxFailures   int

	mu       sync.Mutex
	procs    map[string]*process
	failures map[string]int
	backoff  map[string]time.Time

	// OnHealth 上报健康证据（channel=health 由 hub 接到 ingress）
	OnHealth func(sourceID, targetType, targetID, failureClass, severity, detail string)
}

type Options struct {
	GRPCAddr          string
	HTTPAddr          string
	TransportAddr     string
	ReconcileInterval time.Duration
	StartTimeout      time.Duration
	MaxStartFailures  int
}

func New(st *store.Store, reg *registry.Registry, sched *scheduler.Scheduler, cfg *config.Module, opt Options) *Supervisor {
	if opt.ReconcileInterval <= 0 {
		opt.ReconcileInterval = DefaultReconcileInterval
	}
	if opt.StartTimeout <= 0 {
		opt.StartTimeout = DefaultStartTimeout
	}
	if opt.MaxStartFailures <= 0 {
		opt.MaxStartFailures = DefaultMaxStartFailures
	}
	httpAddr := opt.HTTPAddr
	if httpAddr == "" {
		httpAddr = "127.0.0.1:7100"
	}
	return &Supervisor{
		st: st, reg: reg, sched: sched, cfg: cfg,
		grpcAddr: opt.GRPCAddr, httpAddr: httpAddr, transportAddr: opt.TransportAddr,
		reconcile: opt.ReconcileInterval, startTimeout: opt.StartTimeout, maxFailures: opt.MaxStartFailures,
		procs: map[string]*process{}, failures: map[string]int{}, backoff: map[string]time.Time{},
	}
}

type manifest struct {
	botID          string
	binaryPath     string
	binaryDigest   string
	activationMode string
	idleTTL        int
	startTimeout   int
	configVersion  int64
	pluginID       string
	pluginBinary   string
	pluginDigest   string
}

func (s *Supervisor) loadManifest(ctx context.Context, botID string) (*manifest, error) {
	var m manifest
	var cv *int64
	var parentPlugin *string
	err := s.st.Pool().QueryRow(ctx, `
		SELECT m.bot_id, m.binary_path, m.binary_digest, m.activation_mode,
		       m.idle_ttl_seconds, m.start_timeout_seconds, m.config_version, m.parent_plugin
		  FROM bot_manifest m JOIN bots b ON b.bot_id = m.bot_id
		 WHERE m.bot_id=$1 AND b.enabled = true AND b.desired_state='start'`, botID).
		Scan(&m.botID, &m.binaryPath, &m.binaryDigest, &m.activationMode, &m.idleTTL, &m.startTimeout, &cv, &parentPlugin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("bot %q 未注册或未处于 desired_state=start", botID)
	}
	if err != nil {
		return nil, fmt.Errorf("读取 bot manifest: %w", err)
	}
	if cv != nil {
		m.configVersion = *cv
	}

	if parentPlugin != nil && *parentPlugin != "" {
		var rawMf []byte
		if err := s.st.Pool().QueryRow(ctx, `SELECT manifest_json FROM kernel_plugins WHERE plugin_id=$1`, *parentPlugin).Scan(&rawMf); err == nil {
			var pMf struct {
				Binary       string `json:"binary"`
				BinaryDigest string `json:"binary_digest"`
				BotTemplates []struct {
					BotID string `json:"bot_id"`
				} `json:"bot_templates"`
			}
			if err := json.Unmarshal(rawMf, &pMf); err == nil && len(pMf.BotTemplates) > 0 {
				// bot_templates[0] 即 owner bot
				if pMf.BotTemplates[0].BotID == botID && pMf.Binary != "" {
					m.pluginID = *parentPlugin
					m.pluginBinary = pMf.Binary
					m.pluginDigest = pMf.BinaryDigest
				}
			}
		}
	}

	return &m, nil
}

// verifyDigest 制品校验：sha256 必须与 manifest 一致，不匹配拒绝执行。
func verifyDigest(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开制品 %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("计算制品摘要: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("%w: 期望 %s，实际 %s", ErrDigestMismatch, want, got)
	}
	return nil
}

// Start 拉起一个 lazy Bot：校验制品 → 签发凭证 → exec → 登记实例 → 等 Register。
func (s *Supervisor) Start(ctx context.Context, botID string) error {
	s.mu.Lock()
	if _, running := s.procs[botID]; running {
		s.mu.Unlock()
		return nil
	}
	if until, ok := s.backoff[botID]; ok && time.Now().Before(until) {
		s.mu.Unlock()
		return fmt.Errorf("%w: bot %s 退避至 %s", ErrCircuitBroken, botID, until.Format(time.RFC3339))
	}
	s.mu.Unlock()

	m, err := s.loadManifest(ctx, botID)
	if err != nil {
		return err
	}
	if m.activationMode != "lazy" && m.activationMode != "resident" {
		return fmt.Errorf("%w: %s", ErrNotLazy, m.activationMode)
	}
	if err := verifyDigest(m.binaryPath, m.binaryDigest); err != nil {
		s.recordFailure(botID, "binary_digest_mismatch")
		return err
	}
	cred, err := s.reg.IssueCredential(ctx, config.KindBot, botID)
	if err != nil {
		return fmt.Errorf("签发 bot 凭证: %w", err)
	}

	idleTTL := m.idleTTL
	if m.activationMode == "resident" {
		idleTTL = 0
	}

	args := []string{
		"-bot-id", botID,
		"-hub-grpc", s.grpcAddr,
		"-hub-http", s.httpAddr,
		"-hub-transport", s.transportAddr,
		"-idle-ttl", fmt.Sprintf("%d", idleTTL),
		"-config-version", fmt.Sprintf("%d", m.configVersion),
	}
	if m.pluginID != "" && m.pluginBinary != "" {
		args = append(args,
			"-plugin-id", m.pluginID,
			"-plugin-binary", m.pluginBinary,
			"-plugin-digest", m.pluginDigest,
		)
	}

	cmd := exec.Command(m.binaryPath, args...)
	cmd.Env = append(os.Environ(), BotCredentialEnv+"="+cred.Token)
	// 独立进程组，便于回收整棵子树
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		s.recordFailure(botID, "exec_failed")
		return fmt.Errorf("exec bot-runtime: %w", err)
	}

	p := &process{
		botID: botID, cmd: cmd, pid: cmd.Process.Pid, configVersion: m.configVersion,
		startedAt: time.Now(), registered: make(chan struct{}), exited: make(chan struct{}),
	}
	instanceID, err := s.insertInstance(ctx, botID, p)
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	p.instanceID = instanceID

	s.mu.Lock()
	s.procs[botID] = p
	s.mu.Unlock()

	go s.watch(botID, p)

	startTimeout := s.startTimeout
	if m.startTimeout > 0 {
		startTimeout = time.Duration(m.startTimeout) * time.Second
	}
	select {
	case <-p.registered:
		s.mu.Lock()
		s.failures[botID] = 0
		delete(s.backoff, botID)
		s.mu.Unlock()
		if err := s.setObserved(ctx, botID, "ready"); err != nil {
			return err
		}
		return nil
	case <-p.exited:
		s.recordFailure(botID, "exited_before_register")
		return fmt.Errorf("bot %s 在 Register 前退出", botID)
	case <-time.After(startTimeout):
		// 启动超时：终止进程，任务留在队列由后续 reconcile 重试
		s.stopProcess(botID, p, "start_timeout")
		s.recordFailure(botID, "start_timeout")
		return fmt.Errorf("bot %s 启动超时（%s）", botID, startTimeout)
	case <-ctx.Done():
		s.stopProcess(botID, p, "context_cancelled")
		return ctx.Err()
	}
}

func (s *Supervisor) insertInstance(ctx context.Context, botID string, p *process) (int64, error) {
	var id int64
	err := s.st.Pool().QueryRow(ctx, `
		INSERT INTO bot_instances (bot_id, observed_state, pid, pgid, binary_digest, config_version, started_at)
		SELECT $1, 'starting', $2, $3, m.binary_digest, $4, now()
		  FROM bot_manifest m WHERE m.bot_id=$1
		RETURNING instance_id`, botID, p.pid, p.pid, p.configVersion).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("登记 bot 实例: %w", err)
	}
	if err := s.setObserved(ctx, botID, "starting"); err != nil {
		return 0, err
	}
	return id, nil
}

// MarkRegistered 由 grpcd 在收到 Register 时调用，解除启动等待。
func (s *Supervisor) MarkRegistered(botID string, configVersion int64) {
	s.mu.Lock()
	p, ok := s.procs[botID]
	s.mu.Unlock()
	if !ok {
		return
	}
	p.configVersion = configVersion
	p.once.Do(func() { close(p.registered) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.st.Pool().Exec(ctx, `
		UPDATE bot_instances SET observed_state='ready', config_version=$2, last_heartbeat_at=now()
		 WHERE instance_id=$1`, p.instanceID, configVersion)
}

// MarkHeartbeat 由 grpcd 在收到 Heartbeat 时调用。
func (s *Supervisor) MarkHeartbeat(botID string, configVersion int64) {
	s.mu.Lock()
	p, ok := s.procs[botID]
	s.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.st.Pool().Exec(ctx, `
		UPDATE bot_instances SET last_heartbeat_at=now(), config_version=$2 WHERE instance_id=$1`,
		p.instanceID, configVersion)
}

func (s *Supervisor) watch(botID string, p *process) {
	err := p.cmd.Wait()
	close(p.exited)
	s.mu.Lock()
	delete(s.procs, botID)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state := "stopped"
	if err != nil {
		state = "crashed"
	}
	_, _ = s.st.Pool().Exec(ctx, `
		UPDATE bot_instances SET observed_state=$2, stopped_at=now(), last_error=$3 WHERE instance_id=$1`,
		p.instanceID, state, errString(err))
	_ = s.setObserved(ctx, botID, "dormant")

	// 级联回收孤儿插件进程（PluginMvp §6.6）
	s.cascadeKillPluginProcess(ctx, botID)

	if err != nil {
		s.recordFailure(botID, "crashed")
		s.report("supervisor", "bot", botID, "bot_crash", "warning", err.Error())
	}
}

// cascadeKillPluginProcess 级联回收 owner bot 名下的孤儿插件进程（含孙进程）。
func (s *Supervisor) cascadeKillPluginProcess(ctx context.Context, ownerBotID string) {
	rows, err := s.st.Pool().Query(ctx, `
		SELECT plugin_id, pid, pgid, pid_start_time
		  FROM kernel_plugin_processes
		 WHERE owner_bot_id=$1 AND state IN ('pending','starting','running','stopping')`, ownerBotID)
	if err != nil {
		return
	}
	defer rows.Close()
	type procTarget struct {
		pluginID  string
		pid       int
		pgid      int
		startTime int64
	}
	var targets []procTarget
	for rows.Next() {
		var t procTarget
		if err := rows.Scan(&t.pluginID, &t.pid, &t.pgid, &t.startTime); err == nil {
			targets = append(targets, t)
		}
	}
	rows.Close()

	for _, t := range targets {
		if osproc.IsProcessAlive(t.pid, t.startTime) {
			_ = osproc.KillProcessGroup(t.pgid, syscall.SIGKILL)
		}
		_, _ = s.st.Pool().Exec(ctx, `
			UPDATE kernel_plugin_processes
			   SET state='killed', stop_mode='force', stopped_at=now(), last_error='orphan_cascade_killed'
			 WHERE plugin_id=$1`, t.pluginID)
		s.report("supervisor", "plugin_process", t.pluginID, "orphan_cascade_killed", "warning",
			fmt.Sprintf("owner bot %s 下线，级联回收插件进程 (PID %d)", ownerBotID, t.pid))
	}
}

// StartupCleanup 在 hub 启动时扫描并清理上一轮残留的运行中插件进程。
func (s *Supervisor) StartupCleanup(ctx context.Context) error {
	rows, err := s.st.Pool().Query(ctx, `
		SELECT plugin_id, pid, pgid, pid_start_time
		  FROM kernel_plugin_processes
		 WHERE state IN ('starting','running','stopping')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type procTarget struct {
		pluginID  string
		pid       int
		pgid      int
		startTime int64
	}
	var targets []procTarget
	for rows.Next() {
		var t procTarget
		if err := rows.Scan(&t.pluginID, &t.pid, &t.pgid, &t.startTime); err == nil {
			targets = append(targets, t)
		}
	}
	rows.Close()

	for _, t := range targets {
		if osproc.IsProcessAlive(t.pid, t.startTime) {
			_ = osproc.KillProcessGroup(t.pgid, syscall.SIGKILL)
		}
		_, _ = s.st.Pool().Exec(ctx, `
			UPDATE kernel_plugin_processes
			   SET state='killed', stop_mode='force', stopped_at=now(), last_error='hub_restart_cascade_cleanup'
			 WHERE plugin_id=$1`, t.pluginID)
	}
	return nil
}

// KillPluginProcess 强制终止插件进程（PluginMvp §6.5：user_bot -> create_bot -> supervisor 执行）。
func (s *Supervisor) KillPluginProcess(ctx context.Context, pluginID, reason, actorID string) error {
	var pid, pgid int
	var startTime int64
	var state string
	err := s.st.Pool().QueryRow(ctx, `
		SELECT pid, pgid, pid_start_time, state
		  FROM kernel_plugin_processes WHERE plugin_id=$1`, pluginID).Scan(&pid, &pgid, &startTime, &state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("插件进程 %s 不存在", pluginID)
		}
		return fmt.Errorf("查询插件进程: %w", err)
	}
	if state == "stopped" || state == "killed" || state == "failed" {
		return nil // 幂等返回
	}

	// 保留 PID 黑名单校验（防误杀系统/hub 自身进程）
	selfPID := os.Getpid()
	selfPPID := os.Getppid()
	if pid <= 1 || pid == selfPID || pid == selfPPID || pgid <= 1 || pgid == selfPID || pgid == selfPPID {
		return fmt.Errorf("目标 PID %d/PGID %d 命中保留黑名单，拒绝执行", pid, pgid)
	}

	// 存活与 starttime 双校验
	if osproc.IsProcessAlive(pid, startTime) {
		if err := osproc.KillProcessGroup(pgid, syscall.SIGKILL); err != nil {
			return fmt.Errorf("向进程组 %d 发送 SIGKILL: %w", pgid, err)
		}
	}

	// 审计双写：kernel_security_decisions
	_, _ = s.st.Pool().Exec(ctx, `
		INSERT INTO kernel_security_decisions
		  (subject_type, subject_id, action, decision, requested_by, decided_by, reason)
		VALUES
		  ('plugin_process', $1, 'force_kill', 'approved', $2, 'security_bot', $3)`,
		pluginID, actorID, reason)

	// 更新登记表
	_, err = s.st.Pool().Exec(ctx, `
		UPDATE kernel_plugin_processes
		   SET state='killed', stop_mode='force', stopped_at=now(), last_error=$2
		 WHERE plugin_id=$1`, pluginID, reason)
	return err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// recordFailure 累计启动失败；连续达阈值即熔断并指数退避（1s/2s/4s…封顶 60s）。
func (s *Supervisor) recordFailure(botID, reason string) {
	s.mu.Lock()
	s.failures[botID]++
	n := s.failures[botID]
	backoff := time.Second * time.Duration(1<<min(n-1, 6))
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	s.backoff[botID] = time.Now().Add(backoff)
	s.mu.Unlock()
	if n >= s.maxFailures {
		s.report("supervisor", "bot", botID, "bot_stalled", "critical",
			fmt.Sprintf("连续 %d 次启动失败（%s），已熔断", n, reason))
	}
}

func (s *Supervisor) stopProcess(botID string, p *process, reason string) {
	// 只对自己 exec 的子进程发信号；不触碰任何插件进程（M2 起才有 L2）
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.pid, syscall.SIGTERM)
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.st.Pool().Exec(ctx, `
		UPDATE bot_instances SET observed_state='stopped', stopped_at=now(), last_error=$2 WHERE instance_id=$1`,
		p.instanceID, reason)
	_ = s.setObserved(ctx, botID, "dormant")
}

func (s *Supervisor) setObserved(ctx context.Context, botID, state string) error {
	_, err := s.st.Pool().Exec(ctx, `UPDATE bots SET observed_state=$2, updated_at=now() WHERE bot_id=$1`, botID, state)
	if err != nil {
		return fmt.Errorf("更新 observed_state: %w", err)
	}
	return nil
}

func (s *Supervisor) report(sourceType, targetType, targetID, failureClass, severity, detail string) {
	if s.OnHealth != nil {
		s.OnHealth(sourceType, targetType, targetID, failureClass, severity, detail)
	}
}

// Reconcile 一轮调和：为有待处理任务的 lazy Bot 及常驻 resident Bot 拉起进程。只读 desired，不改 desired。
func (s *Supervisor) Reconcile(ctx context.Context) (started int, errs []error) {
	rows, err := s.st.Pool().Query(ctx, `
		SELECT b.bot_id
		  FROM bots b
		  JOIN bot_manifest m ON m.bot_id = b.bot_id
		 WHERE b.enabled = true AND b.desired_state='start'
		   AND (
		     (m.activation_mode='lazy' AND EXISTS (
		       SELECT 1 FROM kernel_jobs j
		        WHERE j.queue_name = b.bot_id AND j.status IN ('queued','retry_backoff')
		          AND (j.next_retry_at IS NULL OR j.next_retry_at <= now())
		     ))
		     OR (m.activation_mode='resident' OR b.min_instances > 0)
		   )
		 ORDER BY b.bot_id`)
	if err != nil {
		return 0, []error{fmt.Errorf("扫描待启动 Bot: %w", err)}
	}
	var botIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, []error{fmt.Errorf("读取待启动 Bot: %w", err)}
		}
		botIDs = append(botIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, []error{err}
	}
	for _, id := range botIDs {
		s.mu.Lock()
		_, running := s.procs[id]
		until, cooling := s.backoff[id]
		s.mu.Unlock()
		if running || (cooling && time.Now().Before(until)) {
			continue
		}
		if err := s.Start(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("启动 %s: %w", id, err))
			continue
		}
		started++
	}
	return started, errs
}

// Run 是 reconcile 主循环，阻塞直到 ctx 结束。
func (s *Supervisor) Run(ctx context.Context) error {
	// 启动时清理上一轮异常残留的插件进程（PluginMvp §6.6）
	_ = s.StartupCleanup(ctx)
	// 启动初始调和（拉起预置的 resident bots）
	_, _ = s.Reconcile(ctx)

	ticker := time.NewTicker(s.reconcile)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.Shutdown()
			return nil
		case <-ticker.C:
			if _, errs := s.Reconcile(ctx); len(errs) > 0 && ctx.Err() == nil {
				for _, e := range errs {
					fmt.Fprintln(os.Stderr, "supervisor:", e)
				}
			}
		}
	}
}

// Shutdown 回收自己拉起的全部 Bot 进程（先子后父顺序由调用方保证）。
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	procs := make([]*process, 0, len(s.procs))
	for _, p := range s.procs {
		procs = append(procs, p)
	}
	s.mu.Unlock()
	for _, p := range procs {
		s.stopProcess(p.botID, p, "hub_shutdown")
	}
}

// DetectHeartbeatTimeout 检测心跳失联（连续 3 次缺失，间隔 10s）。
func (s *Supervisor) DetectHeartbeatTimeout(ctx context.Context, timeout time.Duration) (int, error) {
	rows, err := s.st.Pool().Query(ctx, `
		SELECT instance_id, bot_id FROM bot_instances
		 WHERE observed_state='ready' AND stopped_at IS NULL
		   AND (last_heartbeat_at IS NULL OR last_heartbeat_at < now() - make_interval(secs => $1))`,
		int(timeout.Seconds()))
	if err != nil {
		return 0, fmt.Errorf("扫描心跳超时实例: %w", err)
	}
	defer rows.Close()
	type inst struct {
		id    int64
		botID string
	}
	var stale []inst
	for rows.Next() {
		var i inst
		if err := rows.Scan(&i.id, &i.botID); err != nil {
			return 0, fmt.Errorf("读取超时实例: %w", err)
		}
		stale = append(stale, i)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, i := range stale {
		if _, err := s.st.Pool().Exec(ctx, `
			UPDATE bot_instances SET observed_state='crashed', stopped_at=now(), last_error='heartbeat_timeout'
			 WHERE instance_id=$1`, i.id); err != nil {
			return 0, fmt.Errorf("标记心跳超时: %w", err)
		}
		s.report("supervisor", "bot", i.botID, "bot_stalled", "warning", "心跳超时")
	}
	return len(stale), nil
}
