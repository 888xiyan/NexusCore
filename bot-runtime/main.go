// Command bot-runtime 是 Bot 独立二进制运行时（lazy 模式）。
// 依据：内核 MVP 详设 §3.9 / §6.1、架构总结 §5.5。
// M1 内置 echo 模式作为冒烟夹具：不持有任何本地长期状态，退出即丢弃全部内存态。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/888xiyan/NexusCore/hub/supervisor"
	"github.com/888xiyan/NexusCore/hub/transport"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/osproc"
	"github.com/888xiyan/NexusCore/internal/version"
	"github.com/888xiyan/NexusCore/memory"
	pb "github.com/888xiyan/NexusCore/proto/gen/v1"
)

func main() {
	var (
		botID         = flag.String("bot-id", "", "Bot 身份（必填）")
		hubGRPC       = flag.String("hub-grpc", "127.0.0.1:7101", "hub gRPC 控制面地址")
		hubHTTP       = flag.String("hub-http", "127.0.0.1:7100", "hub HTTP 入口地址")
		hubTransport  = flag.String("hub-transport", "127.0.0.1:7102", "hub 传输路由地址")
		idleTTL       = flag.Int("idle-ttl", 300, "无任务空闲多久后自退出（秒）")
		configVersion = flag.Int64("config-version", 0, "启动时加载的配置版本")
		agentID       = flag.String("agent-id", "loopback_agent", "echo 工具的目标 Agent")
		agentCLI      = flag.String("agent-cli", "", "默认调度的本地 CLI 名称（如 codex, pi, claude, nexus-mock-agent）")
		agentBound    = flag.Bool("agent-bound", false, "清单里保存了探针绑定时才允许派发 CLI")
		pluginID      = flag.String("plugin-id", "", "所属插件 ID（若为 owner bot 则 exec 插件进程）")
		pluginBinary  = flag.String("plugin-binary", "", "插件进程独立二进制路径")
		pluginDigest  = flag.String("plugin-digest", "", "插件进程制品摘要")
		memoryDir     = flag.String("memory-dir", "", "单体记忆存储基准目录（默认为 data/bots）")
		showVersion   = flag.Bool("version", false, "打印版本号")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("bot-runtime", version.Version)
		return
	}
	if *botID == "" {
		fmt.Fprintln(os.Stderr, "必须提供 -bot-id")
		os.Exit(2)
	}
	cred := os.Getenv(supervisor.BotCredentialEnv)
	if cred == "" {
		fmt.Fprintln(os.Stderr, "缺少 "+supervisor.BotCredentialEnv+"：凭证由 supervisor 注入")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conn, err := grpc.NewClient(*hubGRPC, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "连接 hub gRPC 失败:", err)
		os.Exit(1)
	}
	defer conn.Close()
	client := pb.NewBotRuntimeClient(conn)

	memMgr := memory.NewManager(*memoryDir)
	_ = memMgr.EnsureDirs(*botID)
	injectedMem, _ := memMgr.LoadInjectedContext(*botID)

	b := &bot{
		id: *botID, cred: cred, client: client, httpBase: "http://" + *hubHTTP,
		transportAddr: *hubTransport, agentID: *agentID, agentCLI: *agentCLI, agentBound: *agentBound, configVersion: *configVersion,
		idleTTL:  time.Duration(*idleTTL) * time.Second,
		pluginID: *pluginID, pluginBinary: *pluginBinary, pluginDigest: *pluginDigest,
		memMgr: memMgr, injectedMemory: injectedMem,
	}
	if err := b.register(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Register 失败:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "bot-runtime %s ready (config_version=%d, injected_memory_lines=%d)\n", b.id, b.configVersion, memory.CountLines(b.injectedMemory))

	if b.pluginBinary != "" {
		if err := b.startPluginProcess(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "owner bot exec 插件进程失败: %v\n", err)
			// start 已先回收自己创建的子树；非零退出交给 supervisor 重试。
			os.Exit(1)
		}
	}

	go b.heartbeatLoop(ctx)
	if err := b.runTasks(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "任务流结束:", err)
	}
	// 先子后父：退出前优雅关闭插件进程（PluginMvp §6.4）
	if err := b.stopPluginProcess(); err != nil {
		fmt.Fprintln(os.Stderr, "关闭插件进程失败:", err)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := b.deregister(cleanupCtx); err != nil {
		fmt.Fprintln(os.Stderr, "Deregister 失败:", err)
	}
}

type bot struct {
	id            string
	cred          string
	client        pb.BotRuntimeClient
	httpBase      string
	transportAddr string
	agentID       string
	agentCLI      string
	agentBound    bool
	configVersion int64
	idleTTL       time.Duration

	pluginID     string
	pluginBinary string
	pluginDigest string

	memMgr         *memory.Manager
	injectedMemory string

	// 独立于任务租约锁：启动、停止、终态报告完全串行，旧代报告成功前不能开新代。
	pluginMu        sync.Mutex
	child           *pluginProcess
	pluginStopGrace time.Duration // 零值使用 PluginMvp §6.4 默认 15s

	mu     sync.Mutex
	leases map[string]string // job_id -> lease_token
	last   time.Time
}

// callContext 为每次 RPC 生成新的 control 根信封（§6.10 规则 2）。
func (b *bot) callContext(ctx context.Context) context.Context {
	env := envelope.NewRoot(b.id, envelope.ChannelTransport, envelope.MsgControl, "rpc_"+randomID())
	md := env.Metadata()
	md[envelope.MetaCredential] = b.cred
	pairs := make([]string, 0, len(md)*2)
	for k, v := range md {
		pairs = append(pairs, k, v)
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}

func randomID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (b *bot) register(ctx context.Context) error {
	resp, err := b.client.Register(b.callContext(ctx), &pb.RegisterRequest{
		BotId: b.id, Capabilities: []string{"echo"}, ConfigVersion: b.configVersion,
	})
	if err != nil {
		return err
	}
	b.cred = resp.Token
	b.leases = map[string]string{}
	b.last = time.Now()
	return nil
}

func (b *bot) deregister(ctx context.Context) error {
	_, err := b.client.Deregister(b.callContext(ctx), &pb.DeregisterRequest{BotId: b.id})
	return err
}

const (
	pluginReportTimeout = 5 * time.Second
	pluginExitPoll      = 25 * time.Millisecond
)

// pluginProcess 只属于一次 exec，所有可变字段均由 bot.pluginMu 保护。
// 未 Wait 的组长（含僵尸）钉住 PID，清理完整进程组后才能回收这个身份锚点。
// done 在唯一一次 Wait 后关闭；终态上报失败仍保留本实例供下一次控制重试。
type pluginProcess struct {
	cmd       *exec.Cmd
	pid       int
	pgid      int
	start     int64
	done      chan struct{}
	waited    bool
	state     string
	lastError string
}

func (b *bot) startPluginProcess(ctx context.Context) error {
	b.pluginMu.Lock()
	defer b.pluginMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p := b.child; p != nil {
		if !p.waited && osproc.IsProcessAlive(p.pid, p.start) {
			return nil // 同代重复 start，不重复 exec。
		}
		if err := b.finishPluginProcessLocked(p, "", ""); err != nil {
			return fmt.Errorf("上一代插件尚未完成终态报告: %w", err)
		}
	}
	if b.pluginID == "" || b.pluginBinary == "" {
		return fmt.Errorf("启动插件进程: 缺少 plugin_id 或 plugin_binary（非 owner bot）")
	}
	if err := verifyPluginDigest(b.pluginBinary, b.pluginDigest); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(b.pluginBinary)
	cmd.Env = append(os.Environ(),
		"NEXUS_HUB_HTTP="+b.httpBase,
		supervisor.BotCredentialEnv+"="+b.cred,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec 插件进程: %w", err)
	}
	info, err := osproc.ReadProcessInfo(cmd.Process.Pid)
	if err != nil {
		// 没有可靠身份绝不能猜 pgid 发信号。此异常路径仍只 Wait 一次；
		// /proc 对同用户不可读时必须 fail closed，而不是误杀其他进程。
		go func() {
			if waitErr := cmd.Wait(); waitErr != nil {
				fmt.Fprintln(os.Stderr, "回收无法识别的插件子进程:", waitErr)
			}
		}()
		return fmt.Errorf("读取新插件进程身份（拒绝未校验信号）: %w", err)
	}
	p := &pluginProcess{cmd: cmd, pid: info.PID, pgid: info.PGRP, start: info.StartTime, done: make(chan struct{})}
	b.child = p
	if info.PGRP != info.PID || info.StartTime <= 0 || info.PPID != os.Getpid() {
		return fmt.Errorf("新插件进程身份不合法: pid=%d pgid=%d ppid=%d start=%d", info.PID, info.PGRP, info.PPID, info.StartTime)
	}
	if err := b.reportPluginProcess(ctx, "running", p.pid, p.pgid, p.start, ""); err != nil {
		// 首报失败不能留下未登记的进程；清理不继承已取消的请求 context。
		cleanupErr := b.finishPluginProcessLocked(p, "failed", err.Error())
		return errors.Join(fmt.Errorf("插件 running 首报失败: %w", err), cleanupErr)
	}
	go b.watchPluginProcess(p)
	return nil
}

func verifyPluginDigest(path, digest string) error {
	want, err := hex.DecodeString(digest)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("插件制品摘要必须为 64 位 SHA256 十六进制")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开插件制品: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("计算插件制品 SHA256: %w", err)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("插件制品 SHA256 与 plugin_digest 不符，拒绝执行")
	}
	return nil
}

func (b *bot) watchPluginProcess(p *pluginProcess) {
	ticker := time.NewTicker(pluginExitPoll)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			b.pluginMu.Lock()
			if b.child != p || p.waited {
				b.pluginMu.Unlock()
				return
			}
			if !osproc.IsProcessAlive(p.pid, p.start) {
				err := b.finishPluginProcessLocked(p, "", "")
				b.pluginMu.Unlock()
				if err != nil {
					fmt.Fprintln(os.Stderr, "回收/上报插件终态失败:", err)
				}
				return
			}
			b.pluginMu.Unlock()
		}
	}
}

func (b *bot) stopPluginProcess() error {
	b.pluginMu.Lock()
	defer b.pluginMu.Unlock()
	p := b.child
	if p == nil {
		return nil
	}
	if p.waited || !osproc.IsProcessAlive(p.pid, p.start) {
		return b.finishPluginProcessLocked(p, "", "")
	}
	if err := osproc.SignalProcessGroup(p.pid, p.pgid, p.start, syscall.SIGTERM); err != nil {
		return fmt.Errorf("优雅关闭插件进程组: %w", err)
	}
	grace := b.pluginStopGrace
	if grace <= 0 {
		grace = 15 * time.Second
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	ticker := time.NewTicker(pluginExitPoll)
	defer ticker.Stop()
	for osproc.IsProcessAlive(p.pid, p.start) {
		select {
		case <-ticker.C:
		case <-timer.C:
			return b.finishPluginProcessLocked(p, "killed", "stop_timeout")
		}
	}
	return b.finishPluginProcessLocked(p, "stopped", "")
}

// finishPluginProcessLocked 先校验并清理组，再唯一一次 Wait，最后报告终态。
// SIGKILL 也清除已退出组长留下的后代；在 Wait 前 /proc 仍能验证原组身份。
func (b *bot) finishPluginProcessLocked(p *pluginProcess, state, lastError string) error {
	if !p.waited {
		if err := osproc.SignalProcessGroup(p.pid, p.pgid, p.start, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("回收插件进程组: %w", err)
		}
		waitErr := p.cmd.Wait()
		p.waited = true
		close(p.done)
		p.state, p.lastError = state, lastError
		if p.state == "" {
			p.state = "stopped"
			if waitErr != nil {
				p.state, p.lastError = "crashed", waitErr.Error()
			}
		}
	}
	if err := b.reportPluginProcess(context.Background(), p.state, p.pid, p.pgid, p.start, p.lastError); err != nil {
		return fmt.Errorf("上报插件终态 %s: %w", p.state, err)
	}
	if b.child == p {
		b.child = nil
	}
	return nil
}

func (b *bot) reportPluginProcess(ctx context.Context, state string, pid, pgid int, startTime int64, lastErr string) error {
	owner, err := osproc.ReadProcessInfo(os.Getpid())
	if err != nil {
		return fmt.Errorf("读取 owner bot 进程身份: %w", err)
	}
	rep := map[string]any{
		"envelope":         envelope.NewRoot(b.id, envelope.ChannelHealth, envelope.MsgEvent, "plugin_process_"+randomID()),
		"plugin_id":        b.pluginID,
		"owner_bot_id":     b.id,
		"owner_pid":        owner.PID,
		"owner_start_time": owner.StartTime,
		"pid":              pid,
		"pgid":             pgid,
		"pid_start_time":   startTime,
		"binary_digest":    b.pluginDigest,
		"config_version":   b.configVersion,
		"state":            state,
		"last_error":       lastErr,
	}
	body, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("编码插件进程报告: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, pluginReportTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.httpBase+"/v1/plugins/process/report", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建插件进程报告请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Nexus-Credential", b.cred)
	client := &http.Client{Timeout: pluginReportTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送插件进程报告: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("插件进程报告被拒（HTTP %d）", resp.StatusCode)
	}
	return nil
}

func (b *bot) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.mu.Lock()
			tokens := make([]string, 0, len(b.leases))
			for _, t := range b.leases {
				tokens = append(tokens, t)
			}
			b.mu.Unlock()
			_, _ = b.client.Heartbeat(b.callContext(ctx), &pb.HeartbeatRequest{
				BotId: b.id, JobLeaseTokens: tokens, ConfigVersion: b.configVersion,
			})
		}
	}
}

func (b *bot) runTasks(ctx context.Context) error {
	stream, err := b.client.StreamTasks(b.callContext(ctx), &pb.StreamTasksRequest{ConsumerGroup: b.id})
	if err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		// idle_ttl：无任务超时即自退出（lazy 语义）
		b.mu.Lock()
		idle := time.Since(b.last)
		b.mu.Unlock()
		if b.idleTTL > 0 && idle > b.idleTTL {
			fmt.Fprintf(os.Stderr, "bot %s 空闲超过 %s，自退出\n", b.id, b.idleTTL)
			return nil
		}
		task, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		b.mu.Lock()
		b.last = time.Now()
		b.leases[task.JobId] = task.LeaseToken
		b.mu.Unlock()

		if err := b.handleTask(ctx, task); err != nil {
			fmt.Fprintln(os.Stderr, "处理任务失败:", err)
		}
		b.mu.Lock()
		delete(b.leases, task.JobId)
		b.last = time.Now()
		b.mu.Unlock()
	}
}

func (b *bot) handleTask(ctx context.Context, task *pb.TaskAssignment) error {
	var causal struct {
		CorrelationID  string `json:"correlation_id"`
		RootEnvelopeID string `json:"root_envelope_id"`
		EnvelopeID     string `json:"envelope_id"`
		JobType        string `json:"job_type"`
	}
	if err := json.Unmarshal([]byte(task.CausalContextJson), &causal); err != nil {
		return fmt.Errorf("解析任务因果上下文: %w", err)
	}

	if causal.JobType == "plugin.control" {
		return b.handlePluginControl(ctx, task)
	}

	// 简单 Agent 硬约束：单次思考/处理封顶 15 秒，杜绝深度递归 CoT 循环
	taskCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// 按 payload_ref 自行拉取业务载荷（内核不做上下文水合推送）
	text := ""
	if task.PayloadRef != "" {
		payload, err := b.fetchPayload(taskCtx, task.PayloadRef)
		if err != nil {
			return err
		}
		var doc struct {
			Text    string `json:"text"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(payload, &doc); err == nil {
			if doc.Text != "" {
				text = doc.Text
			} else {
				text = doc.Content
			}
		} else {
			text = string(payload)
		}
	}

	// 严禁自身执行 Shell：物理与工具操作统一经 Hub Transport 调度目标 agent 执行
	var (
		reply      string
		toolOutput string
		execErr    error
	)
	if b.agentCLI != "" || (b.agentID != "loopback_agent" && b.agentID != "") {
		if err := CLIDispatchAllowed(b.agentBound, b.agentID); err != nil {
			return err
		}
		toolOutput, execErr = b.dispatchAgentCLI(taskCtx, task.JobId, text)
		if execErr != nil {
			return execErr
		}
		reply = toolOutput
	} else {
		toolOutput, execErr = b.echoViaAgent(taskCtx, task.JobId, text)
		if execErr != nil {
			return execErr
		}
		// 组织结构化回复（带 Agent 卡片和记忆上下文，回复仅作纯文本，不级联唤醒）
		reply = fmt.Sprintf("[%s 响应] 已完成指令处理\n- 状态: 成功\n- 调度 Agent: %s\n- 物理执行结果: %s", b.id, b.agentID, toolOutput)
	}

	if b.injectedMemory != "" {
		reply += fmt.Sprintf("\n- 记忆上下文: 已注入 MEMORY.md (%d 行)", memory.CountLines(b.injectedMemory))
	}

	// 记录会话流水到 Transcripts 层（Append-Only）
	if b.memMgr != nil {
		if err := b.memMgr.RecordTranscript(b.id, memory.TranscriptEntry{
			SessionID:      causal.CorrelationID,
			Timestamp:      time.Now(),
			Role:           "bot",
			Content:        reply,
			ToolCall:       b.agentID + ".exec",
			ToolResult:     toolOutput,
			CorrelationID:  causal.CorrelationID,
			RootEnvelopeID: causal.RootEnvelopeID,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "bot %s 记录流水失败: %v\n", b.id, err)
		}
	}

	// 以 reply 信封回投内核，继承原始根，不新开 trace
	if err := b.submitReply(taskCtx, causal, reply); err != nil {
		return err
	}
	_, ackErr := b.client.AckTask(b.callContext(taskCtx), &pb.AckTaskRequest{
		JobId: task.JobId, LeaseToken: task.LeaseToken, ResultRef: reply,
	})
	return ackErr
}

// pluginControlResult 经 AckTask.ResultRef 返回，拒绝也结束本次任务，让调用方可见错误。
// 它不是 echo/reply，不额外投递业务信封；入口对 user_bot 的授权由 hub 执行。
type pluginControlResult struct {
	PluginID string `json:"plugin_id"`
	Action   string `json:"action"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

func (b *bot) handlePluginControl(ctx context.Context, task *pb.TaskAssignment) error {
	var control struct {
		PluginID string `json:"plugin_id"`
		Action   string `json:"action"`
	}
	controlErr := func() error {
		if task.PayloadRef == "" {
			return fmt.Errorf("插件控制任务缺少 payload_ref")
		}
		payload, err := b.fetchPayload(ctx, task.PayloadRef)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(payload, &control); err != nil {
			return fmt.Errorf("解析插件控制载荷: %w", err)
		}
		if b.pluginID == "" || control.PluginID != b.pluginID || b.pluginBinary == "" {
			return fmt.Errorf("拒绝插件控制: plugin_id 不符或当前 bot 不是 owner")
		}
		switch control.Action {
		case "start":
			return b.startPluginProcess(ctx)
		case "stop":
			return b.stopPluginProcess()
		default:
			return fmt.Errorf("拒绝插件控制 action=%q，仅允许 start/stop", control.Action)
		}
	}()
	result := pluginControlResult{PluginID: control.PluginID, Action: control.Action, OK: controlErr == nil}
	if controlErr != nil {
		result.Error = controlErr.Error()
	}
	body, err := json.Marshal(result)
	if err != nil {
		return errors.Join(controlErr, fmt.Errorf("编码插件控制结果: %w", err))
	}
	_, ackErr := b.client.AckTask(b.callContext(ctx), &pb.AckTaskRequest{
		JobId: task.JobId, LeaseToken: task.LeaseToken, ResultRef: string(body),
	})
	if ackErr != nil {
		ackErr = fmt.Errorf("确认插件控制任务: %w", ackErr)
	}
	return errors.Join(controlErr, ackErr)
}

func (b *bot) fetchPayload(ctx context.Context, ref string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.httpBase+"/v1/payload/"+ref, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Nexus-Credential", b.cred)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取载荷: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("拉取载荷被拒（%d）: %s", resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

// echoViaAgent 走内核传输路由：auth → open → data → 收响应 → close。
// payload 是插件协议字节，内核只校验 length / protocol_id / stream_id。
func (b *bot) echoViaAgent(ctx context.Context, jobID, text string) (string, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", b.transportAddr)
	if err != nil {
		return "", fmt.Errorf("连接传输路由: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	if err := transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameAuth, Payload: []byte(b.cred)}); err != nil {
		return "", err
	}
	if _, err := transport.ReadFrame(conn); err != nil {
		return "", fmt.Errorf("传输鉴权应答: %w", err)
	}
	openReq, err := json.Marshal(transport.OpenRequest{
		TargetAgentID: b.agentID, ProtocolID: "nexus.echo.v1", JobID: jobID,
	})
	if err != nil {
		return "", err
	}
	if err := transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameOpen, Payload: openReq}); err != nil {
		return "", err
	}
	ack, err := transport.ReadFrame(conn)
	if err != nil {
		return "", fmt.Errorf("开流应答: %w", err)
	}
	if ack.FrameType != transport.FrameOpenAck {
		return "", fmt.Errorf("期望 open_ack，得到 %s", ack.FrameType)
	}
	reqPayload, err := json.Marshal(map[string]any{"text": text, "job_id": jobID})
	if err != nil {
		return "", err
	}
	if err := transport.WriteFrame(conn, transport.Frame{
		FrameType: transport.FrameData, StreamID: ack.StreamID, ProtocolID: "nexus.echo.v1", Payload: reqPayload,
	}); err != nil {
		return "", err
	}
	resp, err := transport.ReadFrame(conn)
	if err != nil {
		return "", fmt.Errorf("读取 echo 结果: %w", err)
	}
	_ = transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameClose, StreamID: ack.StreamID})
	return string(resp.Payload), nil
}

// dispatchAgentCLI 走传输路由使用 nexus.cli.v1 调度目标本地 Coding Agent，并对输出进行防噪音蒸馏
func (b *bot) dispatchAgentCLI(ctx context.Context, jobID, text string) (string, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", b.transportAddr)
	if err != nil {
		return "", fmt.Errorf("连接传输路由: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	if err := transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameAuth, Payload: []byte(b.cred)}); err != nil {
		return "", err
	}
	if _, err := transport.ReadFrame(conn); err != nil {
		return "", fmt.Errorf("传输鉴权应答: %w", err)
	}

	cmd := b.agentCLI
	if cmd == "" {
		cmd = "nexus-mock-agent"
	}

	openReq, err := json.Marshal(transport.OpenRequest{
		TargetAgentID: b.agentID, ProtocolID: "nexus.cli.v1", JobID: jobID,
	})
	if err != nil {
		return "", err
	}
	if err := transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameOpen, Payload: openReq}); err != nil {
		return "", err
	}
	ack, err := transport.ReadFrame(conn)
	if err != nil {
		return "", fmt.Errorf("开流应答: %w", err)
	}
	if ack.FrameType != transport.FrameOpenAck {
		return "", fmt.Errorf("期望 open_ack，得到 %s", ack.FrameType)
	}

	reqPayload, err := json.Marshal(map[string]any{
		"command":     cmd,
		"prompt":      text,
		"timeout_sec": 30,
		"job_id":      jobID,
	})
	if err != nil {
		return "", err
	}
	if err := transport.WriteFrame(conn, transport.Frame{
		FrameType: transport.FrameData, StreamID: ack.StreamID, ProtocolID: "nexus.cli.v1", Payload: reqPayload,
	}); err != nil {
		return "", err
	}
	resp, err := transport.ReadFrame(conn)
	if err != nil {
		return "", fmt.Errorf("读取 CLI 结果: %w", err)
	}
	_ = transport.WriteFrame(conn, transport.Frame{FrameType: transport.FrameClose, StreamID: ack.StreamID})

	var cliResp struct {
		ExitCode  int    `json:"exit_code"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		ElapsedMS int64  `json:"elapsed_ms"`
		Truncated bool   `json:"truncated"`
		Error     string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp.Payload, &cliResp); err != nil {
		return string(resp.Payload), nil
	}

	if cliResp.Error != "" && cliResp.ExitCode == 0 {
		cliResp.ExitCode = 1
	}

	distilled := DistillCLIOutput(cliResp.Stdout, cliResp.Stderr, cliResp.ExitCode, cliResp.ElapsedMS)
	return fmt.Sprintf("[%s 响应] 已完成指令处理\n- **调用探针**: `%s` (执行器: `%s`)\n%s", b.id, b.agentID, cmd, distilled), nil
}

func (b *bot) submitReply(ctx context.Context, causal struct {
	CorrelationID  string `json:"correlation_id"`
	RootEnvelopeID string `json:"root_envelope_id"`
	EnvelopeID     string `json:"envelope_id"`
	JobType        string `json:"job_type"`
}, result string) error {
	// 继承原始根：causal_path 为 root → 原信封，causation_id 指向原信封
	root := envelope.NewRoot(b.id, envelope.ChannelTransport, envelope.MsgReply, "reply_"+randomID())
	if causal.RootEnvelopeID != "" {
		root.CorrelationID = causal.CorrelationID
		root.RootEnvelopeID = causal.RootEnvelopeID
		root.CausationID = causal.EnvelopeID
		root.CausalPath = []envelope.CausalNode{
			{NodeID: causal.RootEnvelopeID, NodeType: "envelope", Actor: "user_bot", At: time.Now().UTC()},
		}
		if causal.EnvelopeID != causal.RootEnvelopeID {
			root.CausalPath = append(root.CausalPath, envelope.CausalNode{
				NodeID: causal.EnvelopeID, NodeType: "envelope", Actor: "user_bot", At: time.Now().UTC(),
			})
		}
		root.Traceparent = "00-" + root.Traceparent[3:35] + "-" + randomHex8() + "-01"
	}
	payload := []byte(result)
	body, err := json.Marshal(map[string]any{
		"envelope":       root,
		"payload_base64": base64.StdEncoding.EncodeToString(payload),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.httpBase+"/v1/ingress", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Nexus-Credential", b.cred)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("回投 reply 信封: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("reply 信封被拒（%d）: %s", resp.StatusCode, errBody)
	}
	return nil
}

func randomHex8() string {
	return fmt.Sprintf("%016x", time.Now().UnixNano())[:16]
}
