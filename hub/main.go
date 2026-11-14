// Command hub 是内核网关：持久化队列与事件、Bot 注册运行时与配置中心、跨设备传输路由。
// 依据：内核 MVP 详设 §2 / §3。内核不含任何记忆与知识能力（属 plugin_memory，M3 起）。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/eventbus"
	"github.com/888xiyan/NexusCore/hub/grpcd"
	"github.com/888xiyan/NexusCore/hub/httpapi"
	"github.com/888xiyan/NexusCore/hub/ingress"
	"github.com/888xiyan/NexusCore/hub/monitor"
	"github.com/888xiyan/NexusCore/hub/outbox"
	"github.com/888xiyan/NexusCore/hub/plugin"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/hub/supervisor"
	"github.com/888xiyan/NexusCore/hub/transport"
	"github.com/888xiyan/NexusCore/internal/version"
	"google.golang.org/grpc"
)

func main() {
	var (
		showVersion   = flag.Bool("version", false, "打印版本号")
		httpAddr      = flag.String("listen-http", "127.0.0.1:7100", "HTTP 入口与 /metrics 监听地址")
		grpcAddr      = flag.String("listen-grpc", "127.0.0.1:7101", "BotRuntime gRPC 监听地址")
		transportAddr = flag.String("listen-transport", "127.0.0.1:7102", "传输路由监听地址")
		insecureLocal = flag.Bool("insecure-local", true, "MVP 仅回环 trust（生产需 mTLS）")
		credFile      = flag.String("user-credential-file", "data/user_bot.cred", "user_bot 凭证输出文件")
		botBinary     = flag.String("bot-binary", "bin/bot-runtime", "M1 夹具 Bot 二进制路径")
		reconcile     = flag.Duration("reconcile-interval", 2*time.Second, "supervisor reconcile 周期")
		wakeupPoll    = flag.Duration("wakeup-poll", 5*time.Second, "队列兜底轮询间隔（通知丢失不影响正确性）")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("hub", version.Version)
		return
	}
	if !*insecureLocal {
		fmt.Fprintln(os.Stderr, "M1 只实现回环 trust；mTLS 属后续里程碑")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, options{
		httpAddr: *httpAddr, grpcAddr: *grpcAddr, transportAddr: *transportAddr,
		credFile: *credFile, botBinary: *botBinary, reconcile: *reconcile, wakeupPoll: *wakeupPoll,
	}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "hub 退出:", err)
		os.Exit(1)
	}
}

type options struct {
	httpAddr      string
	grpcAddr      string
	transportAddr string
	credFile      string
	botBinary     string
	reconcile     time.Duration
	wakeupPoll    time.Duration
}

func run(ctx context.Context, opt options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dsn, err := store.KernelDSNFromEnv()
	if err != nil {
		return err
	}
	credKey, err := registry.KeyFromEnv()
	if err != nil {
		return err
	}
	snapOpt, err := ingress.OptionsFromEnv()
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer st.Close()

	reg, err := registry.New(st, credKey, registry.DefaultTTL)
	if err != nil {
		return err
	}
	cfg := config.New(st)
	sched := scheduler.New(st, scheduler.Options{})
	ing, err := ingress.New(st, reg, sched, snapOpt)
	if err != nil {
		return err
	}
	disp := outbox.New(st, sched, outbox.Options{})
	bus := eventbus.New(st, sched)

	router := transport.NewRouter(st, reg)
	sup := supervisor.New(st, reg, sched, cfg, supervisor.Options{
		GRPCAddr: opt.grpcAddr, HTTPAddr: opt.httpAddr, TransportAddr: opt.transportAddr, ReconcileInterval: opt.reconcile,
	})
	if opt.botBinary != "" {
		if err := sup.ConfigureRuntime(opt.botBinary); err != nil {
			return fmt.Errorf("配置 supervisor 可信运行时: %w", err)
		}
	}
	router.OnViolation = func(peer, reason string) {
		fmt.Fprintf(os.Stderr, "transport 违规 peer=%s: %s\n", peer, reason)
	}

	// user_bot 常驻席位：启动即签发凭证，CLI 作为其投递代理读取
	if err := bootstrapUserBot(ctx, reg, opt.credFile); err != nil {
		return err
	}

	pm := plugin.NewManager(st, cfg, sup, reg)
	api := httpapi.New(st, reg, ing, sched, cfg, disp, router)
	api.SetPluginManager(pm)
	api.SetSupervisor(sup)
	api.FixtureBinder = func(ctx context.Context) error { return bindFixture(ctx, st, opt.botBinary) }

	mon := monitor.New(st, sup, cfg, 5*time.Second)

	gs := grpc.NewServer()
	grpcdServer := grpcd.NewServer(st, reg, ing, sched, bus, sup, scheduler.DefaultLeaseTTL, 200*time.Millisecond)
	grpcdServer.OnStatusChange = func(botID string, status string) {
		api.BroadcastBotStatus(botID, status)
	}
	grpcdServer.RegisterService(gs)

	grpcLn, err := net.Listen("tcp", opt.grpcAddr)
	if err != nil {
		return fmt.Errorf("监听 gRPC: %w", err)
	}
	trLn, err := net.Listen("tcp", opt.transportAddr)
	if err != nil {
		grpcLn.Close()
		return fmt.Errorf("监听传输端口: %w", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 5)
	start := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("%s: %w", name, err)
			}
		}()
	}

	start("grpcd", func() error { return gs.Serve(grpcLn) })
	start("transport", func() error { return router.Serve(ctx, trLn) })
	start("httpapi", func() error { return api.Server(ctx, opt.httpAddr) })
	start("supervisor", func() error { return sup.Run(ctx) })
	start("monitor", func() error { return mon.Run(ctx) })
	start("outbox", func() error { return disp.Run(ctx) })
	start("queue", func() error { return queueLoop(ctx, st, sched, opt.wakeupPoll) })

	fmt.Fprintf(os.Stderr, "hub %s 就绪：http=%s grpc=%s transport=%s\n",
		version.Version, opt.httpAddr, opt.grpcAddr, opt.transportAddr)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		cancel()
		return err
	}

	gs.GracefulStop()
	_ = grpcLn.Close()
	_ = trLn.Close()
	sup.Shutdown()
	wg.Wait()
	fmt.Fprintln(os.Stderr, "hub 已优雅退出")
	return nil
}

// queueLoop 是调度唤醒：LISTEN 只做唤醒，兜底轮询与过期租约扫描才是正确性来源。
func queueLoop(ctx context.Context, st *store.Store, sched *scheduler.Scheduler, poll time.Duration) error {
	wake := make(chan struct{}, 1)
	notify := func(string) {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	go func() {
		_ = st.Listen(ctx, store.ListenOptions{
			Channel: "kernel_jobs_wakeup", Handler: notify,
			OnReconnect: func() { notify("") }, // 重连后全量扫描补偿
		})
	}()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-wake:
		}
		// 过期租约处置：幂等任务自动重试，含已执行副作用的任务挂起等人工确认
		if _, _, err := sched.DetectExpiredLeases(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "过期租约检测:", err)
		}
	}
}

func bootstrapUserBot(ctx context.Context, reg *registry.Registry, path string) error {
	cred, err := reg.IssueCredential(ctx, config.KindBot, config.UserBot)
	if err != nil {
		return fmt.Errorf("签发 user_bot 凭证: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建凭证目录: %w", err)
	}
	if err := os.WriteFile(path, []byte(cred.Token), 0o600); err != nil {
		return fmt.Errorf("写入 user_bot 凭证: %w", err)
	}
	return nil
}

// bindFixture 是 M1 冒烟夹具：把 echo_bot 的制品摘要绑定到真实二进制，并登记 loopback agent。
// M2 起由插件 install + security_bot 审查流程取代；此处不做任何安全审查，仅限回环开发。
func bindFixture(ctx context.Context, st *store.Store, binary string) error {
	abs, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	f, err := os.Open(abs)
	if err != nil {
		return fmt.Errorf("打开夹具二进制 %s: %w", abs, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("计算夹具摘要: %w", err)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if _, err := st.Pool().Exec(ctx, `
		UPDATE bot_manifest SET binary_path=$2, binary_digest=$3, manifest_digest=$3, updated_at=now()
		 WHERE bot_id=$1`, "echo_bot", abs, digest); err != nil {
		return fmt.Errorf("绑定 echo_bot 制品: %w", err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO kernel_agents (agent_id, display_name, capabilities, status)
		VALUES ('loopback_agent','M1 回环执行夹具','["echo"]'::jsonb,'online')
		ON CONFLICT (agent_id) DO UPDATE SET status='online', capabilities=EXCLUDED.capabilities, updated_at=now()`); err != nil {
		return fmt.Errorf("登记 loopback agent: %w", err)
	}
	return nil
}
