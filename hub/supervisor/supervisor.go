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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/osproc"
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
	exited        chan struct{} // 只在 Wait 完成后关闭；不表示 L2 已回收
	waitErr       error         // exited 关闭前写入
	cleanupMu     sync.Mutex    // watch 与主动 stop 的回收互斥
	finished      bool          // cleanupMu 保护
	stopping      bool          // Supervisor.mu 保护
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

	// opMu 串行化启动、停止及启动清理，不持 mu 等待 Register/Wait。
	opMu          sync.Mutex
	shutdown      bool // opMu 保护；Shutdown 后不再允许启动
	runtimeMu     sync.RWMutex
	runtimePath   string
	runtimeDigest string

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
	boundAgentCLI  string
	boundAgentID   string
}

func (s *Supervisor) loadManifest(ctx context.Context, botID string) (*manifest, error) {
	var m manifest
	var cv *int64
	var parentPlugin *string
	var rawCaps []byte
	err := s.st.Pool().QueryRow(ctx, `
		SELECT m.bot_id, m.binary_path, m.binary_digest, m.activation_mode,
		       m.idle_ttl_seconds, m.start_timeout_seconds, m.config_version, m.parent_plugin,
		       m.capabilities
		  FROM bot_manifest m JOIN bots b ON b.bot_id = m.bot_id
		 WHERE m.bot_id=$1 AND b.enabled = true AND b.desired_state='start'`, botID).
		Scan(&m.botID, &m.binaryPath, &m.binaryDigest, &m.activationMode, &m.idleTTL, &m.startTimeout, &cv, &parentPlugin, &rawCaps)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("bot %q 未注册或未处于 desired_state=start", botID)
	}
	if err != nil {
		return nil, fmt.Errorf("读取 bot manifest: %w", err)
	}
	if cv != nil {
		m.configVersion = *cv
	}

	if len(rawCaps) > 0 {
		var caps []string
		if err := json.Unmarshal(rawCaps, &caps); err == nil {
			for _, capItem := range caps {
				if strings.HasPrefix(capItem, "agent-cli:") {
					m.boundAgentCLI = strings.TrimPrefix(capItem, "agent-cli:")
				} else if strings.HasPrefix(capItem, "cli:") {
					m.boundAgentCLI = strings.TrimPrefix(capItem, "cli:")
				} else if strings.HasPrefix(capItem, "agent-id:") {
					m.boundAgentID = strings.TrimPrefix(capItem, "agent-id:")
				} else if strings.HasPrefix(capItem, "agent:") {
					m.boundAgentID = strings.TrimPrefix(capItem, "agent:")
				}
			}
		}
	}

	if parentPlugin != nil && *parentPlugin != "" {
		var rawMf []byte
		err := s.st.Pool().QueryRow(ctx, `
			SELECT p.manifest_json FROM kernel_plugins p
			 WHERE p.plugin_id=$1 AND p.status='enabled'
			   AND EXISTS (SELECT 1 FROM kernel_security_decisions d
			       WHERE d.subject_type='plugin' AND d.subject_id=p.plugin_id
			         AND d.action='approve' AND d.decision='approved'
			         AND d.requested_by='user_bot' AND d.checksum=p.manifest_digest)`, *parentPlugin).Scan(&rawMf)
		if err != nil {
			return nil, fmt.Errorf("插件 %s 未启用、清单未经批准或读取失败: %w", *parentPlugin, err)
		}
		var pm pluginManifest
		if err := json.Unmarshal(rawMf, &pm); err != nil {
			return nil, fmt.Errorf("解析插件 %s 清单: %w", *parentPlugin, err)
		}
		if len(pm.BotTemplates) == 0 {
			return nil, fmt.Errorf("插件 %s 清单没有 owner bot", *parentPlugin)
		}
		if pm.BotTemplates[0].BotID == botID && pm.Binary != "" {
			m.pluginID, m.pluginBinary, m.pluginDigest = *parentPlugin, pm.Binary, pm.BinaryDigest
		}
	}

	return &m, nil
}

// ConfigureRuntime 只从 hub 明确指定的 -bot-binary 建立可信制品，不读写配置/种子 DB。
func (s *Supervisor) ConfigureRuntime(path string) error {
	if path == "" {
		return errors.New("bot-runtime 路径不能为空")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析 bot-runtime 绝对路径: %w", err)
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return fmt.Errorf("解析 bot-runtime 实际路径: %w", err)
	}
	digest, err := artifactDigest(absolute)
	if err != nil {
		return err
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimePath != "" && (s.runtimePath != absolute || s.runtimeDigest != digest) {
		return errors.New("可信 bot-runtime 已配置，拒绝替换")
	}
	s.runtimePath, s.runtimeDigest = absolute, digest
	return nil
}

// RuntimeArtifact 返回启动时固定的路径和摘要，每次使用前重新验证文件。
func (s *Supervisor) RuntimeArtifact() (path, digest string, err error) {
	s.runtimeMu.RLock()
	path, digest = s.runtimePath, s.runtimeDigest
	s.runtimeMu.RUnlock()
	if path == "" {
		return "", "", errors.New("可信 bot-runtime 尚未配置")
	}
	if err := verifyDigest(path, digest); err != nil {
		return "", "", err
	}
	return path, digest, nil
}

func artifactDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开制品 %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("读取制品属性: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("制品 %s 不是可执行普通文件", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("计算制品摘要: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyDigest 制品校验：sha256 必须与 manifest 一致，不匹配拒绝执行。
func verifyDigest(path, want string) error {
	got, err := artifactDigest(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%w: 期望 %s，实际 %s", ErrDigestMismatch, want, got)
	}
	return nil
}

// Start 拉起一个 lazy Bot：校验制品 → 签发凭证 → exec → 登记实例 → 等 Register。
func (s *Supervisor) Start(ctx context.Context, botID string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.startLocked(ctx, botID)
}

// RestartBot 优雅重启指定 Bot 进程：终止旧进程 → 回收 L2 插件进程 → 重新拉起新实例。
func (s *Supervisor) RestartBot(ctx context.Context, botID string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.shutdown {
		return errors.New("supervisor 已关闭，拒绝重启 Bot")
	}

	s.mu.Lock()
	p, running := s.procs[botID]
	if running {
		p.stopping = true
	}
	s.mu.Unlock()

	if running {
		s.stopProcess(botID, p, "manual_restart")
		if err := s.finishProcess(ctx, p); err != nil {
			return fmt.Errorf("清理旧代 Bot %s: %w", botID, err)
		}
	}

	s.mu.Lock()
	delete(s.backoff, botID)
	delete(s.failures, botID)
	s.mu.Unlock()

	return s.startLocked(ctx, botID)
}

func (s *Supervisor) startLocked(ctx context.Context, botID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.shutdown {
		return errors.New("supervisor 已关闭，拒绝启动 Bot")
	}
	s.mu.Lock()
	p, running := s.procs[botID]
	stopping := running && p.stopping
	s.mu.Unlock()
	if running {
		select {
		case <-p.exited:
			if err := s.finishProcess(ctx, p); err != nil {
				return fmt.Errorf("旧代 Bot %s 回收未完成: %w", botID, err)
			}
		default:
			if stopping {
				return fmt.Errorf("Bot %s 仍在停止，拒绝重启", botID)
			}
			return nil
		}
	}
	s.mu.Lock()
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
	// 直接 Start 也必须处理旧代 L2，不能依赖 Run 的启动扫描。
	if err := s.cleanupPluginProcesses(ctx, botID, "", "restart_cleanup"); err != nil {
		return fmt.Errorf("Bot %s 旧代插件回收失败: %w", botID, err)
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
	if m.boundAgentCLI != "" {
		args = append(args, "-agent-cli", m.boundAgentCLI)
	}
	if m.boundAgentID != "" {
		args = append(args, "-agent-id", m.boundAgentID, "-agent-bound")
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

	p = &process{
		botID: botID, cmd: cmd, pid: cmd.Process.Pid, configVersion: m.configVersion,
		startedAt: time.Now(), registered: make(chan struct{}), exited: make(chan struct{}),
	}
	instanceID, err := s.insertInstance(ctx, botID, p)
	if err != nil {
		killErr := cmd.Process.Kill()
		waitErr := cmd.Wait()
		return errors.Join(err, killErr, waitErr)
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
		// desired/插件状态可能在 exec 和 Register 之间改变，不能把逃逸进程报为成功。
		if _, err := s.loadManifest(ctx, botID); err != nil {
			return errors.Join(err, s.stopAfterStart(p, "desired_changed"))
		}
		select {
		case <-p.exited:
			return errors.Join(fmt.Errorf("bot %s 在 Register 后退出", botID), s.finishProcess(ctx, p))
		default:
		}
		s.mu.Lock()
		s.failures[botID] = 0
		delete(s.backoff, botID)
		s.mu.Unlock()
		if err := s.setObserved(ctx, botID, "ready"); err != nil {
			return errors.Join(err, s.stopAfterStart(p, "ready_update_failed"))
		}
		return nil
	case <-p.exited:
		s.recordFailure(botID, "exited_before_register")
		return errors.Join(fmt.Errorf("bot %s 在 Register 前退出", botID), s.finishProcess(ctx, p))
	case <-time.After(startTimeout):
		// 启动超时：终止进程，任务留在队列由后续 reconcile 重试。
		s.recordFailure(botID, "start_timeout")
		return errors.Join(fmt.Errorf("bot %s 启动超时（%s）", botID, startTimeout), s.stopAfterStart(p, "start_timeout"))
	case <-ctx.Done():
		return errors.Join(ctx.Err(), s.stopAfterStart(p, "context_cancelled"))
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
	p.waitErr = p.cmd.Wait()
	close(p.exited)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if p.waitErr != nil {
		s.recordFailure(botID, "crashed")
		s.report("supervisor", "bot", botID, "bot_crash", "warning", p.waitErr.Error())
	}
	if err := s.finishProcess(ctx, p); err != nil {
		s.logFailure(botID, err)
	}
}

// finishProcess 只有 L2 清理及观测状态写入全部成功才释放 procs 中的旧代。
func (s *Supervisor) finishProcess(ctx context.Context, p *process) error {
	p.cleanupMu.Lock()
	defer p.cleanupMu.Unlock()
	return s.finishProcessLocked(ctx, p)
}

func (s *Supervisor) finishProcessLocked(ctx context.Context, p *process) error {
	if p.finished {
		return nil
	}
	select {
	case <-p.exited:
	default:
		return fmt.Errorf("Bot %s 尚未退出", p.botID)
	}
	if err := s.cascadeKillPluginProcess(ctx, p.botID, p.instanceID); err != nil {
		return err
	}
	state := "stopped"
	if p.waitErr != nil {
		state = "crashed"
	}
	if _, err := s.st.Pool().Exec(ctx, `
		UPDATE bot_instances SET observed_state=$2, stopped_at=now(), last_error=$3
		 WHERE instance_id=$1 AND bot_id=$4`, p.instanceID, state, errString(p.waitErr), p.botID); err != nil {
		return fmt.Errorf("记录 Bot %s 退出: %w", p.botID, err)
	}
	if err := s.setObserved(ctx, p.botID, "dormant"); err != nil {
		return err
	}
	s.mu.Lock()
	if s.procs[p.botID] == p {
		delete(s.procs, p.botID)
	}
	s.mu.Unlock()
	p.finished = true
	return nil
}

type pluginManifest struct {
	Binary       string `json:"binary"`
	BinaryDigest string `json:"binary_digest"`
	BotTemplates []struct {
		BotID string `json:"bot_id"`
	} `json:"bot_templates"`
}

type pluginProcess struct {
	pluginID, ownerBotID, ownerInstanceID, digest, state string
	pid, pgid                                            int
	startTime                                            int64
}

type processQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// ValidatePluginProcess 是登记前的附加黑名单校验，不要求目标已有登记。
// 归属/PPID 校验由登记入口负责；kill 还会校验被锁住的登记行及 owner 代际。
func (s *Supervisor) ValidatePluginProcess(ctx context.Context, pluginID string, pid, pgid int, startTime int64) error {
	return s.validatePluginProcess(ctx, s.st.Pool(), pluginID, pid, pgid, startTime)
}

func (s *Supervisor) validatePluginProcess(ctx context.Context, q processQuerier, pluginID string, pid, pgid int, startTime int64) error {
	if pluginID == "" || pid <= 1 || pgid != pid || startTime <= 0 {
		return fmt.Errorf("非法插件进程标识: plugin=%s PID=%d PGID=%d start=%d", pluginID, pid, pgid, startTime)
	}
	for _, protected := range []int{os.Getpid(), os.Getppid()} {
		if pid == protected {
			return fmt.Errorf("PID %d 命中 hub/parent 黑名单", pid)
		}
		info, err := osproc.ReadProcessInfo(protected)
		if err != nil {
			return fmt.Errorf("读取受保护进程 %d: %w", protected, err)
		}
		if pgid == info.PGRP {
			return fmt.Errorf("PGID %d 命中 hub/parent 黑名单", pgid)
		}
	}
	s.mu.Lock()
	for _, p := range s.procs {
		if pid == p.pid || pgid == p.pid {
			s.mu.Unlock()
			return fmt.Errorf("PID/PGID %d 命中 Bot 进程黑名单", pid)
		}
	}
	s.mu.Unlock()
	var protected bool
	// bot_instances 的 start_time 不是 /proc ticks；即使心跳已标记 crashed，
	// 也不能据此判定 OS 进程已死。重叠的历史登记同样保守拒绝。
	err := q.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM bot_instances WHERE pid=$1 OR pgid=$1)
		OR EXISTS (SELECT 1 FROM kernel_plugin_processes
		            WHERE plugin_id<>$2 AND (pid=$1 OR pgid=$1))`, pid, pluginID).Scan(&protected)
	if err != nil {
		return fmt.Errorf("读取插件进程黑名单: %w", err)
	}
	if protected {
		return fmt.Errorf("PID/PGID %d 命中 Bot/其他插件黑名单", pid)
	}
	info, err := osproc.ReadProcessInfo(pid)
	if err != nil {
		return fmt.Errorf("读取插件进程 %d: %w", pid, err)
	}
	if info.PGRP != pgid || info.StartTime != startTime {
		return fmt.Errorf("插件进程 %d 的 PGID/starttime 与登记不符", pid)
	}
	// 不能仅凭登记宣称是插件。检查组内每个活进程，避免数据库被带入目标组。
	return inspectProcessGroup(pgid, func(member int, info *osproc.ProcessInfo) error {
		if info.State == 'Z' || info.State == 'X' {
			return nil
		}
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", member))
		if err != nil {
			return fmt.Errorf("无法确认进程 %d 非数据库: %w", member, err)
		}
		comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", member))
		if err != nil {
			return fmt.Errorf("读取进程 %d 名称: %w", member, err)
		}
		if databaseProcessName(filepath.Base(strings.TrimSuffix(exe, " (deleted)"))) || databaseProcessName(strings.TrimSpace(string(comm))) {
			return fmt.Errorf("PID %d 命中数据库进程黑名单", member)
		}
		return nil
	})
}

func databaseProcessName(name string) bool {
	switch strings.ToLower(name) {
	case "postgres", "postmaster", "pg_ctl", "pgbouncer", "pgpool", "mysqld", "mariadbd", "mongod", "redis-server", "cockroach":
		return true
	}
	return strings.HasPrefix(strings.ToLower(name), "postgres:")
}

func inspectProcessGroup(pgid int, visit func(int, *osproc.ProcessInfo) error) error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return fmt.Errorf("扫描进程组: %w", err)
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		info, err := osproc.ReadProcessInfo(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("扫描进程 %d: %w", pid, err)
		}
		if info.PGRP == pgid {
			if err := visit(pid, info); err != nil {
				return err
			}
		}
	}
	return nil
}

// cascadeKillPluginProcess 只回收退出的 instanceID，旧回调不能命中新 owner。
func (s *Supervisor) cascadeKillPluginProcess(ctx context.Context, ownerBotID string, instanceID int64) error {
	return s.cleanupPluginProcesses(ctx, ownerBotID, strconv.FormatInt(instanceID, 10), "orphan_cascade_killed")
}

func (s *Supervisor) cleanupPluginProcesses(ctx context.Context, ownerBotID, instanceID, reason string) error {
	rows, err := s.st.Pool().Query(ctx, `
		SELECT plugin_id, owner_bot_id, owner_instance_id, pid, pgid, pid_start_time
		 FROM kernel_plugin_processes
		 WHERE ($1='' OR owner_bot_id=$1) AND ($2='' OR owner_instance_id=$2)`, ownerBotID, instanceID)
	if err != nil {
		return fmt.Errorf("扫描待回收插件: %w", err)
	}
	var targets []pluginProcess
	for rows.Next() {
		var p pluginProcess
		if err := rows.Scan(&p.pluginID, &p.ownerBotID, &p.ownerInstanceID, &p.pid, &p.pgid, &p.startTime); err != nil {
			rows.Close()
			return fmt.Errorf("读取待回收插件: %w", err)
		}
		targets = append(targets, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("扫描待回收插件: %w", err)
	}
	var errs []error
	for _, target := range targets {
		if err := s.killPluginProcess(ctx, target.pluginID, reason, "supervisor", &target); err != nil {
			errs = append(errs, fmt.Errorf("回收插件 %s: %w", target.pluginID, err))
		}
	}
	return errors.Join(errs...)
}

// StartupCleanup 在 hub 启动时清理旧代，包括 crashed/failed 行里可能残留的子树。
func (s *Supervisor) StartupCleanup(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.cleanupPluginProcesses(ctx, "", "", "hub_restart_cascade_cleanup")
}

// KillPluginProcess 仅允许 user_bot/create_bot 发起；信号、审计与代际更新持有同一登记行锁。
func (s *Supervisor) KillPluginProcess(ctx context.Context, pluginID, reason, actorID string) error {
	if actorID != "user_bot" && actorID != "create_bot" {
		err := fmt.Errorf("actor %s 无权强制终止插件进程", actorID)
		return errors.Join(err, s.auditKill(ctx, pluginID, actorID, "rejected", err.Error()))
	}
	return s.killPluginProcess(ctx, pluginID, reason, actorID, nil)
}

func (s *Supervisor) auditKill(ctx context.Context, pluginID, actorID, decision, reason string) error {
	_, err := s.st.Pool().Exec(ctx, `INSERT INTO kernel_security_decisions
		(subject_type,subject_id,action,decision,requested_by,decided_by,reason)
		VALUES ('plugin_process',$1,'force_kill',$2,$3,'supervisor',$4)`, pluginID, decision, actorID, reason)
	if err != nil {
		return fmt.Errorf("记录插件回收审计: %w", err)
	}
	return nil
}

func (s *Supervisor) killPluginProcess(ctx context.Context, pluginID, reason, actorID string, expected *pluginProcess) (retErr error) {
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始插件回收事务: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(rollbackCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			retErr = errors.Join(retErr, fmt.Errorf("回滚插件回收事务: %w", err))
		}
		if retErr != nil {
			retErr = errors.Join(retErr, s.auditKill(rollbackCtx, pluginID, actorID, "rejected", retErr.Error()))
		}
	}()
	var p pluginProcess
	p.pluginID = pluginID
	err = tx.QueryRow(ctx, `SELECT owner_bot_id, owner_instance_id, pid, pgid, pid_start_time, binary_digest, state
		FROM kernel_plugin_processes WHERE plugin_id=$1 FOR UPDATE`, pluginID).
		Scan(&p.ownerBotID, &p.ownerInstanceID, &p.pid, &p.pgid, &p.startTime, &p.digest, &p.state)
	if errors.Is(err, pgx.ErrNoRows) && expected != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("锁定插件进程 %s: %w", pluginID, err)
	}
	if expected != nil && (p.ownerBotID != expected.ownerBotID || p.ownerInstanceID != expected.ownerInstanceID ||
		p.pid != expected.pid || p.pgid != expected.pgid || p.startTime != expected.startTime) {
		return fmt.Errorf("插件 %s 回收期间登记代际已改变，拒绝信号", pluginID)
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT k.manifest_json FROM kernel_plugins k
		JOIN bot_manifest b ON b.parent_plugin=k.plugin_id AND b.bot_id=$2
		LEFT JOIN bot_instances i ON i.bot_id=b.bot_id AND (i.instance_id::text=$3 OR i.pid::text=$3)
		WHERE k.plugin_id=$1 FOR SHARE OF k, b`, pluginID, p.ownerBotID, p.ownerInstanceID).Scan(&raw)
	if err != nil {
		return fmt.Errorf("校验插件 owner/instance 关联: %w", err)
	}
	var mf pluginManifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return fmt.Errorf("解析插件进程清单: %w", err)
	}
	if len(mf.BotTemplates) == 0 || mf.BotTemplates[0].BotID != p.ownerBotID || mf.Binary == "" || mf.BinaryDigest != p.digest {
		return fmt.Errorf("插件 %s 的登记与当前清单 owner/制品不符", pluginID)
	}
	validationErr := s.validatePluginProcess(ctx, tx, pluginID, p.pid, p.pgid, p.startTime)
	if validationErr != nil && !errors.Is(validationErr, os.ErrNotExist) {
		return validationErr
	}
	// 不存在不是“校验通过后向 PGID 发信号”：只能标记旧代消失，绝不碰复用 PID。
	if validationErr == nil {
		if err := osproc.SignalProcessGroup(p.pid, p.pgid, p.startTime, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("安全终止插件 %s: %w", pluginID, err)
		}
	}
	if err := waitPluginGroupExit(ctx, p.pgid); err != nil {
		return err
	}
	if p.state == "stopped" || p.state == "killed" || p.state == "failed" {
		// 终态没有活子树才可幂等返回，不信任终态文本代替进程检查。
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO kernel_security_decisions
		(subject_type,subject_id,action,decision,requested_by,decided_by,reason)
		VALUES ('plugin_process',$1,'force_kill','approved',$2,'supervisor',$3)`, pluginID, actorID, reason); err != nil {
		return fmt.Errorf("记录插件回收审计: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE kernel_plugin_processes
		SET state='killed', stop_mode='force', stopped_at=now(), updated_at=now(), last_error=$2
		WHERE plugin_id=$1 AND pid=$3 AND pgid=$4 AND pid_start_time=$5
		  AND owner_bot_id=$6 AND owner_instance_id=$7`, pluginID, reason, p.pid, p.pgid, p.startTime, p.ownerBotID, p.ownerInstanceID)
	if err != nil {
		return fmt.Errorf("更新插件回收代际: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("插件 %s 登记代际改变", pluginID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交插件回收事务: %w", err)
	}
	return nil
}

func waitPluginGroupExit(ctx context.Context, pgid int) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive := false
		if err := inspectProcessGroup(pgid, func(_ int, p *osproc.ProcessInfo) error {
			if p.State != 'Z' && p.State != 'X' {
				alive = true
			}
			return nil
		}); err != nil {
			return err
		}
		if !alive {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待插件进程组 %d 退出: %w", pgid, ctx.Err())
		case <-ticker.C:
		}
	}
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

func (s *Supervisor) stopAfterStart(p *process, reason string) error {
	s.stopProcess(p.botID, p, reason)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.finishProcess(ctx, p)
}

func (s *Supervisor) logFailure(botID string, err error) {
	fmt.Fprintf(os.Stderr, "supervisor: bot %s 回收失败: %v\n", botID, err)
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
	s.opMu.Lock()
	s.shutdown = true
	s.opMu.Unlock()

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
