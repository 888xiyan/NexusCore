// Command cli 是用户(user_bot)的投递与管理代理：M1 的第一个用户界面与 M2 的全套运营客户端。
// 依据：内核 MVP 详设 §5.2 / §6.1、BotMvp §7.3、PluginMvp §11、ConfigCenter §7。
// CLI 不是总线主体：它以 user_bot 身份投递信封，凭证由 hub 启动时签发。
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/version"
	pb "github.com/888xiyan/NexusCore/proto/gen/v1"
)

const defaultHTTP = "http://127.0.0.1:7100"
const defaultGRPC = "127.0.0.1:7101"
const defaultCredFile = "data/user_bot.cred"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	if cmd == "-version" || cmd == "version" {
		fmt.Println("cli", version.Version)
		return
	}

	httpBase := getEnvOr("NEXUS_HUB_HTTP", defaultHTTP)
	grpcAddr := getEnvOr("NEXUS_HUB_GRPC", defaultGRPC)
	credFile := getEnvOr("NEXUS_CREDENTIAL_FILE", defaultCredFile)

	// 处理全局参数提取（支持在任何位置指定 -hub-http 或 -credential-file）
	filteredArgs := make([]string, 0, len(os.Args)-2)
	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "-hub-http" && i+1 < len(os.Args) {
			httpBase = os.Args[i+1]
			i++
		} else if strings.HasPrefix(arg, "-hub-http=") {
			httpBase = strings.TrimPrefix(arg, "-hub-http=")
		} else if arg == "-hub-grpc" && i+1 < len(os.Args) {
			grpcAddr = os.Args[i+1]
			i++
		} else if strings.HasPrefix(arg, "-hub-grpc=") {
			grpcAddr = strings.TrimPrefix(arg, "-hub-grpc=")
		} else if arg == "-credential-file" && i+1 < len(os.Args) {
			credFile = os.Args[i+1]
			i++
		} else if strings.HasPrefix(arg, "-credential-file=") {
			credFile = strings.TrimPrefix(arg, "-credential-file=")
		} else {
			filteredArgs = append(filteredArgs, arg)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cred, err := readCredential(credFile)
	if err != nil && cmd != "help" && cmd != "-h" && cmd != "--help" {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	switch cmd {
	case "send":
		fs := flag.NewFlagSet("send", flag.ExitOnError)
		text := fs.String("text", "", "要投递的文本")
		botID := fs.String("bot", "echo_bot", "目标 Bot")
		wait := fs.Bool("wait", false, "投递后等待回复事件")
		timeout := fs.Duration("timeout", 60*time.Second, "等待超时")
		_ = fs.Parse(filteredArgs)
		if *text == "" {
			fmt.Fprintln(os.Stderr, "send 需要 -text")
			os.Exit(2)
		}
		if err := doSend(ctx, httpBase, cred, *botID, *text, *wait, *timeout, grpcAddr); err != nil {
			fmt.Fprintln(os.Stderr, "投递失败:", err)
			os.Exit(1)
		}

	case "resume-list":
		if err := doGet(ctx, httpBase+"/v1/jobs/suspended", cred); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "resume-confirm":
		fs := flag.NewFlagSet("resume-confirm", flag.ExitOnError)
		jobID := fs.Int64("job", 0, "任务 ID")
		decision := fs.String("decision", "", "continue / replay / discard")
		_ = fs.Parse(filteredArgs)
		if *jobID == 0 || *decision == "" {
			fmt.Fprintln(os.Stderr, "resume-confirm 需要 -job 与 -decision（continue/replay/discard）")
			os.Exit(2)
		}
		body, _ := json.Marshal(map[string]string{"decision": *decision})
		if err := doPost(ctx, fmt.Sprintf("%s/v1/jobs/%d/resume", httpBase, *jobID), cred, body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "replay":
		fs := flag.NewFlagSet("replay", flag.ExitOnError)
		jobID := fs.Int64("job", 0, "任务 ID")
		_ = fs.Parse(filteredArgs)
		if *jobID == 0 {
			fmt.Fprintln(os.Stderr, "replay 需要 -job")
			os.Exit(2)
		}
		if err := doPost(ctx, fmt.Sprintf("%s/v1/jobs/%d/replay", httpBase, *jobID), cred, []byte("{}")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "fixture":
		if err := doPost(ctx, httpBase+"/v1/dev/fixture", cred, []byte("{}")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "credential":
		fs := flag.NewFlagSet("credential", flag.ExitOnError)
		kind := fs.String("kind", "bot", "凭证主体类型")
		subject := fs.String("subject", "", "凭证主体 ID")
		_ = fs.Parse(filteredArgs)
		if *subject == "" {
			fmt.Fprintln(os.Stderr, "credential 需要 -subject")
			os.Exit(2)
		}
		body, _ := json.Marshal(map[string]string{"kind": *kind, "subject": *subject})
		if err := doPost(ctx, httpBase+"/v1/credentials", cred, body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "subscribe":
		fs := flag.NewFlagSet("subscribe", flag.ExitOnError)
		group := fs.String("group", "user_bot", "Consumer Group")
		_ = fs.Parse(filteredArgs)
		if err := doSubscribe(ctx, grpcAddr, cred, *group, ""); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "metrics":
		if err := doGet(ctx, httpBase+"/metrics", ""); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	// M2: 配置中心 CLI
	case "config":
		if err := handleConfig(ctx, httpBase, cred, filteredArgs); err != nil {
			fmt.Fprintln(os.Stderr, "config 错误:", err)
			os.Exit(1)
		}

	// M2: Bot 管理 CLI
	case "bot":
		if err := handleBot(ctx, httpBase, cred, filteredArgs); err != nil {
			fmt.Fprintln(os.Stderr, "bot 错误:", err)
			os.Exit(1)
		}

	// M2: create-bot pull
	case "create-bot":
		if err := handleCreateBot(ctx, httpBase, cred, filteredArgs); err != nil {
			fmt.Fprintln(os.Stderr, "create-bot 错误:", err)
			os.Exit(1)
		}

	// M2: 插件生命周期与进程 CLI
	case "plugin":
		if err := handlePlugin(ctx, httpBase, cred, filteredArgs); err != nil {
			fmt.Fprintln(os.Stderr, "plugin 错误:", err)
			os.Exit(1)
		}

	// M2: security CLI 契约 (BotMvp §7.3)
	case "security-list", "security-approve", "security-revoke":
		if err := handleSecurity(ctx, httpBase, cred, cmd, filteredArgs, configInteraction{
			input: os.Stdin, output: os.Stderr,
			isTerminal: func() bool { return isTerminal(os.Stdin) },
		}); err != nil {
			fmt.Fprintln(os.Stderr, "security 错误:", err)
			os.Exit(1)
		}

	// M2: DLQ 运营 CLI
	case "dlq":
		if err := handleDLQ(ctx, httpBase, cred, filteredArgs); err != nil {
			fmt.Fprintln(os.Stderr, "dlq 错误:", err)
			os.Exit(1)
		}

	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "未知子命令:", cmd)
		usage()
		os.Exit(2)
	}
}

func getEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func usage() {
	fmt.Fprint(os.Stderr, `NexusCore cli（user_bot 的投递与管理代理）

用法：cli <子命令> [参数]

【核心投递与总线】
  send            -text "..." [-bot echo_bot] [-wait] [-timeout 60s]
  subscribe       [-group user_bot]
  resume-list     列出挂起任务与副作用清单
  resume-confirm  -job N -decision continue|replay|discard
  replay          -job N（DLQ 重放，必经人工确认）
  credential      -kind bot|agent|plugin -subject ID
  metrics         查看内核指标

【M2: 配置中心】
  config list [--scope plugin|bot|agent|global]
  config get <scope_type> <scope_id> [--version N] [--key default]
  config history <scope_type> <scope_id> [--key default]
  config propose --file spec.json --scope <type> --id <scope_id> [--base-version N]
  config diff <proposal_id>
  config review <proposal_id> --decision approved|rejected [--reason "..."]
  config approve <proposal_id> --checksum <sha256>
  config reject <proposal_id> --reason "..."
  config activate <proposal_id>
  config rollback <scope_type> <scope_id> --to <version>
  config pending [--scope <type>]
  config secret add --provider env|file|keyring --locator NAME [--description "..."]
  config set --file spec.json --scope <type> --id <scope_id>  # 一次完成提案、审批、激活

【M2: Bot 管理】
  bot create -id <bot_id> [-name <display_name>] [-mode lazy|resident] [-prompt <ref>] [-model <m>]
  bot list
  bot enable <bot_id> [--mode lazy|resident] [--enabled true|false]
  create-bot pull

【M2: 插件运营与进程控制】
  plugin install <manifest.json>
  plugin download-record <plugin_id> --digest <sha256> --result ok|rejected [--reason "..."]
  plugin list
  plugin lifecycle <plugin_id>
  plugin upgrade <manifest.json>
  plugin disable <plugin_id>
  plugin uninstall <plugin_id> [--confirm] [--keep]
  plugin ps
  plugin stop <plugin_id>
  plugin start <plugin_id>
  plugin kill <plugin_id> --confirm [--reason "..."]
  security-list [pending]
  security-approve <decision_id> [--checksum <sha256>]
  security-revoke <decision_id> --reason "..."

【M2: DLQ 运营】
  dlq list
  dlq replay -job N
  dlq archive -job N

公共参数：-hub-http / -hub-grpc / -credential-file
`)
}

// ---------------------- M2: 配置中心实现 ----------------------

// configInteraction 将终端检测与输入输出绑定；测试可注入交互，不提供命令行绕过开关。
type configInteraction struct {
	input      io.Reader
	output     io.Writer
	isTerminal func() bool
}

func isTerminal(file *os.File) bool {
	// /dev/null 也是字符设备，不能仅依赖 ModeCharDevice 判断。
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}

func (ui configInteraction) requireTerminal() error {
	if ui.isTerminal == nil || !ui.isTerminal() {
		return errors.New("配置批准必须在真实交互终端进行，禁止 stdin 重定向或 --yes 跳过确认")
	}
	return nil
}

func handleConfig(ctx context.Context, httpBase, cred string, args []string) error {
	return handleConfigInteractive(ctx, httpBase, cred, args, configInteraction{
		input: os.Stdin, output: os.Stderr,
		isTerminal: func() bool { return isTerminal(os.Stdin) },
	})
}

func handleConfigInteractive(ctx context.Context, httpBase, cred string, args []string, ui configInteraction) error {
	if len(args) == 0 {
		return errors.New("缺少 config 子命令 (list, get, history, propose, diff, review, approve, reject, activate, rollback, pending, secret, set)")
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "list":
		fs := flag.NewFlagSet("config list", flag.ExitOnError)
		scope := fs.String("scope", "", "过滤范围 (plugin, bot, agent, global)")
		_ = fs.Parse(rest)
		url := httpBase + "/v1/config/list"
		if *scope != "" {
			url += "?scope=" + *scope
		}
		return doGet(ctx, url, cred)

	case "get":
		if len(rest) < 2 {
			return errors.New("用法: config get <scope_type> <scope_id> [--version N] [--key default]")
		}
		st := rest[0]
		sid := rest[1]
		fs := flag.NewFlagSet("config get", flag.ExitOnError)
		ver := fs.Int64("version", 0, "版本号（默认 active）")
		key := fs.String("key", "default", "配置键")
		_ = fs.Parse(rest[2:])
		url := fmt.Sprintf("%s/v1/config/get?scope_type=%s&scope_id=%s&key=%s", httpBase, st, sid, *key)
		if *ver > 0 {
			url += fmt.Sprintf("&version=%d", *ver)
		}
		return doGet(ctx, url, cred)

	case "history":
		if len(rest) < 2 {
			return errors.New("用法: config history <scope_type> <scope_id> [--key default]")
		}
		st := rest[0]
		sid := rest[1]
		fs := flag.NewFlagSet("config history", flag.ExitOnError)
		key := fs.String("key", "default", "配置键")
		_ = fs.Parse(rest[2:])
		url := fmt.Sprintf("%s/v1/config/history?scope_type=%s&scope_id=%s&key=%s", httpBase, st, sid, *key)
		return doGet(ctx, url, cred)

	case "propose":
		fs := flag.NewFlagSet("config propose", flag.ExitOnError)
		file := fs.String("file", "", "spec JSON 文件路径")
		scope := fs.String("scope", "", "范围类型 (plugin, bot, agent, global)")
		id := fs.String("id", "", "主体 ID")
		key := fs.String("key", "default", "配置键")
		baseVer := fs.Int64("base-version", 0, "基准版本号")
		_ = fs.Parse(rest)
		if *file == "" || *scope == "" || *id == "" {
			return errors.New("config propose 需要 --file, --scope, --id")
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("读取配置文件: %w", err)
		}
		body, _ := json.Marshal(map[string]any{
			"scope_type":   *scope,
			"scope_id":     *id,
			"config_key":   *key,
			"spec":         json.RawMessage(data),
			"base_version": *baseVer,
		})
		return doPost(ctx, httpBase+"/v1/config/propose", cred, body)

	case "diff":
		if len(rest) == 0 {
			return errors.New("用法: config diff <proposal_id>")
		}
		return doGet(ctx, httpBase+"/v1/config/diff/"+rest[0], cred)

	case "review":
		if len(rest) == 0 {
			return errors.New("用法: config review <proposal_id> --decision approved|rejected [--reason \"...\"]")
		}
		propID := rest[0]
		fs := flag.NewFlagSet("config review", flag.ExitOnError)
		dec := fs.String("decision", "approved", "审核决定")
		reason := fs.String("reason", "security_bot review", "审核原因")
		_ = fs.Parse(rest[1:])
		body, _ := json.Marshal(map[string]string{
			"proposal_id": propID,
			"decision":    *dec,
			"reason":      *reason,
		})
		return doPost(ctx, httpBase+"/v1/config/review", cred, body)

	case "approve":
		if len(rest) == 0 {
			return errors.New("用法: config approve <proposal_id> --checksum <sha256>")
		}
		propID := rest[0]
		fs := flag.NewFlagSet("config approve", flag.ContinueOnError)
		csum := fs.String("checksum", "", "待批准配置的 sha256 checksum")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("config approve 不接受额外参数或 --yes 跳过确认")
		}
		if *csum == "" {
			return errors.New("config approve 必须提供 --checksum 校验和")
		}
		return approveConfigInteractive(ctx, httpBase, cred, propID, *csum, ui)

	case "reject":
		if len(rest) == 0 {
			return errors.New("用法: config reject <proposal_id> --reason \"...\"")
		}
		propID := rest[0]
		fs := flag.NewFlagSet("config reject", flag.ExitOnError)
		reason := fs.String("reason", "rejected by user", "驳回原因")
		_ = fs.Parse(rest[1:])
		body, _ := json.Marshal(map[string]string{
			"proposal_id": propID,
			"reason":      *reason,
		})
		return doPost(ctx, httpBase+"/v1/config/reject", cred, body)

	case "activate":
		if len(rest) == 0 {
			return errors.New("用法: config activate <proposal_id>")
		}
		propID := rest[0]
		body, _ := json.Marshal(map[string]string{
			"proposal_id": propID,
		})
		return doPost(ctx, httpBase+"/v1/config/activate", cred, body)

	case "rollback":
		if len(rest) < 2 {
			return errors.New("用法: config rollback <scope_type> <scope_id> --to <version>")
		}
		st := rest[0]
		sid := rest[1]
		fs := flag.NewFlagSet("config rollback", flag.ExitOnError)
		to := fs.Int64("to", 0, "回滚到的版本号")
		key := fs.String("key", "default", "配置键")
		_ = fs.Parse(rest[2:])
		if *to <= 0 {
			return errors.New("config rollback 必须提供 --to <version>")
		}
		body, _ := json.Marshal(map[string]any{
			"scope_type": st,
			"scope_id":   sid,
			"config_key": *key,
			"to_version": *to,
		})
		return doPost(ctx, httpBase+"/v1/config/rollback", cred, body)

	case "pending":
		fs := flag.NewFlagSet("config pending", flag.ExitOnError)
		scope := fs.String("scope", "", "过滤范围")
		_ = fs.Parse(rest)
		url := httpBase + "/v1/config/pending"
		if *scope != "" {
			url += "?scope=" + *scope
		}
		return doGet(ctx, url, cred)

	case "secret":
		if len(rest) < 2 || rest[0] != "add" {
			return errors.New("用法: config secret add --provider env|file|keyring --locator NAME [--description \"...\"]")
		}
		fs := flag.NewFlagSet("config secret add", flag.ExitOnError)
		provider := fs.String("provider", "env", "提供者 (env, file, keyring)")
		locator := fs.String("locator", "", "定位符")
		desc := fs.String("description", "", "描述")
		_ = fs.Parse(rest[1:])
		if *locator == "" {
			return errors.New("config secret add 需要 --locator")
		}
		body, _ := json.Marshal(map[string]string{
			"provider":    *provider,
			"locator":     *locator,
			"description": *desc,
		})
		return doPost(ctx, httpBase+"/v1/config/secret", cred, body)

	case "set":
		// set 是交互式便利命令：propose -> diff -> approve -> activate
		// 必须在读取文件和任何 HTTP 请求之前拒绝非终端输入，避免创建无人审批的提案。
		if err := ui.requireTerminal(); err != nil {
			return err
		}
		fs := flag.NewFlagSet("config set", flag.ContinueOnError)
		file := fs.String("file", "", "spec JSON 文件路径")
		scope := fs.String("scope", "", "范围类型")
		id := fs.String("id", "", "主体 ID")
		key := fs.String("key", "default", "配置键")
		baseVer := fs.Int64("base-version", 0, "基准版本号")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("config set 不接受额外参数或 --yes 跳过确认")
		}
		if *file == "" || *scope == "" || *id == "" {
			return errors.New("config set 需要 --file, --scope, --id")
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("读取配置文件: %w", err)
		}
		// 1. Propose
		pBody, err := json.Marshal(map[string]any{
			"scope_type":   *scope,
			"scope_id":     *id,
			"config_key":   *key,
			"spec":         json.RawMessage(data),
			"base_version": *baseVer,
		})
		if err != nil {
			return fmt.Errorf("编码配置提案: %w", err)
		}
		pResp, err := postAndReturn(ctx, httpBase+"/v1/config/propose", cred, pBody)
		if err != nil {
			return fmt.Errorf("提案失败: %w", err)
		}
		var pResult struct {
			ProposalID string `json:"proposal_id"`
		}
		if err := json.Unmarshal(pResp, &pResult); err != nil {
			return fmt.Errorf("解析提案响应: %w", err)
		}
		if pResult.ProposalID == "" {
			return fmt.Errorf("提案未返回 proposal_id: %s", string(pResp))
		}

		// 2-3. 展示服务端 diff，交互确认后使用同一 checksum 批准。
		if err := approveConfigInteractive(ctx, httpBase, cred, pResult.ProposalID, "", ui); err != nil {
			return err
		}

		// 4. Activate
		actBody, _ := json.Marshal(map[string]string{"proposal_id": pResult.ProposalID})
		if err := doPost(ctx, httpBase+"/v1/config/activate", cred, actBody); err != nil {
			return fmt.Errorf("激活失败: %w", err)
		}
		fmt.Println("配置已激活并生效")
		return nil

	default:
		return fmt.Errorf("未知 config 子命令: %s", sub)
	}
}

// approveConfigInteractive 仅在展示并确认同一份服务端 diff 后批准。
// expectedChecksum 为空仅用于 set；独立 approve 命令必须由调用方提供校验和。
func approveConfigInteractive(ctx context.Context, httpBase, cred, proposalID, expectedChecksum string, ui configInteraction) error {
	if err := ui.requireTerminal(); err != nil {
		return err
	}
	data, err := getAndReturn(ctx, httpBase+"/v1/config/diff/"+proposalID, cred)
	if err != nil {
		return fmt.Errorf("获取 diff 失败: %w", err)
	}
	var diff struct {
		Base     json.RawMessage `json:"base"`
		Proposal json.RawMessage `json:"proposal"`
		Diff     json.RawMessage `json:"diff,omitempty"`
		Checksum string          `json:"checksum"`
	}
	if err := json.Unmarshal(data, &diff); err != nil {
		return fmt.Errorf("解析 diff 响应: %w", err)
	}
	checksum, err := hex.DecodeString(diff.Checksum)
	if err != nil || len(checksum) != sha256.Size {
		return errors.New("diff 响应缺少有效的 sha256 checksum，拒绝批准")
	}
	if expectedChecksum != "" && expectedChecksum != diff.Checksum {
		return errors.New("提供的 checksum 与服务端 diff 不一致，拒绝批准")
	}
	if len(diff.Proposal) == 0 || bytes.Equal(bytes.TrimSpace(diff.Proposal), []byte("null")) {
		return errors.New("diff 响应缺少 proposal，拒绝批准")
	}
	// 服务端以 base / proposal 提供对照；首次提案的 base 明确显示为 null。
	pretty, err := json.MarshalIndent(diff, "", "  ")
	if err != nil {
		return fmt.Errorf("格式化 diff: %w", err)
	}
	if _, err := fmt.Fprintf(ui.output, "提案: %s\nchecksum（前 12 位）: %.12s\nDiff（base → proposal）:\n%s\n输入 yes 并回车以批准，其他输入取消: ", proposalID, diff.Checksum, pretty); err != nil {
		return fmt.Errorf("显示批准确认: %w", err)
	}
	answer, err := bufio.NewReader(ui.input).ReadString('\n')
	if err != nil {
		// 即使 EOF 前有 yes，也不视为完成了确认。
		return fmt.Errorf("读取批准确认失败，未批准: %w", err)
	}
	if strings.TrimSpace(answer) != "yes" {
		return errors.New("用户取消，未批准配置")
	}
	body, _ := json.Marshal(map[string]string{
		"proposal_id": proposalID,
		"checksum":    diff.Checksum,
	})
	if err := doPost(ctx, httpBase+"/v1/config/approve", cred, body); err != nil {
		return fmt.Errorf("批准失败: %w", err)
	}
	return nil
}

// ---------------------- M2: Bot 管理实现 ----------------------

func handleBot(ctx context.Context, httpBase, cred string, args []string) error {
	if len(args) == 0 {
		return errors.New("缺少 bot 子命令 (create, list, enable)")
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "create":
		fs := flag.NewFlagSet("bot create", flag.ExitOnError)
		id := fs.String("id", "", "Bot ID (^[a-z0-9_]{5,64}$)")
		name := fs.String("name", "", "显示名称")
		mode := fs.String("mode", "lazy", "activation_mode (lazy/resident)")
		prompt := fs.String("prompt", "", "prompt 引用")
		model := fs.String("model", "", "模型规格")
		profile := fs.String("profile", "", "记忆 profile (用户自建默认 personal_stack_v1)")
		_ = fs.Parse(rest)
		if *id == "" {
			return errors.New("bot create 需要 -id")
		}
		if *name == "" {
			*name = *id
		}
		body, _ := json.Marshal(map[string]any{
			"bot_id":          *id,
			"display_name":    *name,
			"activation_mode": *mode,
			"capabilities":    []string{"echo"},
			"prompt_ref":      *prompt,
			"model":           *model,
			"memory_profile":  *profile,
		})
		return doPost(ctx, httpBase+"/v1/bots", cred, body)

	case "list":
		return doGet(ctx, httpBase+"/v1/bots", cred)

	case "enable":
		if len(rest) == 0 {
			return errors.New("用法: bot enable <bot_id> [--mode lazy|resident] [--enabled true|false]")
		}
		botID := rest[0]
		fs := flag.NewFlagSet("bot enable", flag.ExitOnError)
		mode := fs.String("mode", "", "activation_mode")
		enabled := fs.Bool("enabled", true, "是否启用")
		_ = fs.Parse(rest[1:])
		body, _ := json.Marshal(map[string]any{
			"mode":    *mode,
			"enabled": *enabled,
		})
		return doPost(ctx, fmt.Sprintf("%s/v1/bots/%s/enable", httpBase, botID), cred, body)

	default:
		return fmt.Errorf("未知 bot 子命令: %s", sub)
	}
}

func handleCreateBot(ctx context.Context, httpBase, cred string, args []string) error {
	if len(args) == 0 || args[0] != "pull" {
		return errors.New("用法: create-bot pull")
	}
	return doPost(ctx, httpBase+"/v1/create-bot/pull", cred, []byte("{}"))
}

// ---------------------- M2: 插件管理实现 ----------------------

func handlePlugin(ctx context.Context, httpBase, cred string, args []string) error {
	if len(args) == 0 {
		return errors.New("缺少 plugin 子命令 (install, download-record, list, lifecycle, upgrade, disable, uninstall, ps, stop, start, kill)")
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "install":
		if len(rest) == 0 {
			return errors.New("用法: plugin install <manifest.json>")
		}
		data, err := os.ReadFile(rest[0])
		if err != nil {
			return fmt.Errorf("读取插件清单: %w", err)
		}
		return doPost(ctx, httpBase+"/v1/plugins/install", cred, data)

	case "download-record":
		if len(rest) == 0 {
			return errors.New("用法: plugin download-record <plugin_id> --digest <sha256> --result ok|rejected [--reason \"...\"]")
		}
		pluginID := rest[0]
		fs := flag.NewFlagSet("plugin download-record", flag.ContinueOnError)
		digest := fs.String("digest", "", "制品 SHA-256")
		result := fs.String("result", "", "ok 或 rejected")
		reason := fs.String("reason", "", "拒绝或提案说明")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		if *digest == "" || *result == "" {
			return errors.New("plugin download-record 需要 --digest 与 --result")
		}
		body, err := json.Marshal(map[string]string{
			"plugin_id": pluginID,
			"digest":    *digest,
			"result":    *result,
			"reason":    *reason,
		})
		if err != nil {
			return err
		}
		return doPost(ctx, httpBase+"/v1/plugins/download-audit", cred, body)

	case "list":
		return doGet(ctx, httpBase+"/v1/plugins", cred)

	case "lifecycle":
		if len(rest) == 0 {
			return errors.New("用法: plugin lifecycle <plugin_id>")
		}
		return doGet(ctx, httpBase+"/v1/plugins/"+rest[0]+"/lifecycle", cred)

	case "upgrade":
		if len(rest) == 0 {
			return errors.New("用法: plugin upgrade <manifest.json>")
		}
		data, err := os.ReadFile(rest[0])
		if err != nil {
			return fmt.Errorf("读取插件清单: %w", err)
		}
		return doPost(ctx, httpBase+"/v1/plugins/upgrade", cred, data)

	case "disable":
		if len(rest) == 0 {
			return errors.New("用法: plugin disable <plugin_id>")
		}
		return postPluginControl(ctx, httpBase+"/v1/plugins/"+rest[0]+"/disable", cred, nil)

	case "uninstall":
		if len(rest) == 0 {
			return errors.New("用法: plugin uninstall <plugin_id> [--confirm] [--keep]")
		}
		pluginID := rest[0]
		fs := flag.NewFlagSet("plugin uninstall", flag.ExitOnError)
		confirm := fs.Bool("confirm", false, "清理元数据与表")
		keep := fs.Bool("keep", false, "保留类型化表")
		cascade := fs.Bool("cascade", false, "按逆拓扑先停依赖方，再停当前插件")
		_ = fs.Parse(rest[1:])
		fields := map[string]any{"confirm": *confirm, "keep": *keep}
		if *cascade {
			fields["cascade"] = true
		}
		return postPluginControl(ctx, httpBase+"/v1/plugins/"+pluginID+"/uninstall", cred, fields)

	case "ps":
		return doGet(ctx, httpBase+"/v1/plugins/processes", cred)

	case "stop":
		if len(rest) == 0 {
			return errors.New("用法: plugin stop <plugin_id>")
		}
		return postPluginControl(ctx, httpBase+"/v1/plugins/"+rest[0]+"/process/stop", cred, nil)

	case "start":
		if len(rest) == 0 {
			return errors.New("用法: plugin start <plugin_id>")
		}
		return postPluginControl(ctx, httpBase+"/v1/plugins/"+rest[0]+"/process/start", cred, nil)

	case "kill":
		if len(rest) == 0 {
			return errors.New("用法: plugin kill <plugin_id> --confirm [--reason \"...\"]")
		}
		pluginID := rest[0]
		fs := flag.NewFlagSet("plugin kill", flag.ExitOnError)
		confirm := fs.Bool("confirm", false, "必须提供二次确认")
		reason := fs.String("reason", "user_bot_force_kill", "终止原因")
		_ = fs.Parse(rest[1:])
		if !*confirm {
			return errors.New("plugin kill 必须提供 --confirm 二次确认 (PluginMvp §6.5 规则 2)")
		}
		return postPluginControl(ctx, httpBase+"/v1/plugins/"+pluginID+"/process/kill", cred, map[string]any{
			"confirm": *confirm, "reason": *reason,
		})

	default:
		return fmt.Errorf("未知 plugin 子命令: %s", sub)
	}
}

// postPluginControl 在客户端生成完整因果链，仅用于插件生命周期控制，不改变清单契约。
func postPluginControl(ctx context.Context, url, cred string, fields map[string]any) error {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("生成插件控制幂等键: %w", err)
	}
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["envelope"] = envelope.NewRoot("user_bot", envelope.ChannelCLI, envelope.MsgControl, "cli_plugin_"+hex.EncodeToString(key))
	body, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("编码插件控制请求: %w", err)
	}
	return doPost(ctx, url, cred, body)
}

func approvePluginInteractive(ctx context.Context, httpBase, cred string, decisionID int64, checksum string, ui configInteraction) error {
	data, err := getAndReturn(ctx, httpBase+"/v1/plugins/security?status=pending", cred)
	if err != nil {
		return fmt.Errorf("读取待审决定失败: %w", err)
	}
	var decisions []struct {
		DecisionID int64  `json:"decision_id"`
		Checksum   string `json:"checksum"`
		DecidedBy  string `json:"decided_by"`
	}
	if err := json.Unmarshal(data, &decisions); err != nil {
		return fmt.Errorf("解析待审决定: %w", err)
	}
	var bound string
	for _, d := range decisions {
		if d.DecisionID == decisionID {
			bound = d.Checksum
			break
		}
	}
	if bound == "" || bound != checksum {
		return errors.New("提供的 checksum 与 security 审查绑定不一致，拒绝批准")
	}
	if err := ui.requireTerminal(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(ui.output, "插件安装审查: %d\nchecksum（前 12 位）: %.12s\n预审: security_bot\n输入 yes 并回车以批准，其他输入取消: ", decisionID, checksum); err != nil {
		return fmt.Errorf("显示批准确认: %w", err)
	}
	answer, err := bufio.NewReader(ui.input).ReadString('\n')
	if err != nil {
		return fmt.Errorf("读取批准确认失败，未批准: %w", err)
	}
	if strings.TrimSpace(answer) != "yes" {
		return errors.New("用户取消，未批准插件安装")
	}
	body, _ := json.Marshal(map[string]any{
		"decision_id": decisionID,
		"checksum":    checksum,
	})
	if err := doPost(ctx, httpBase+"/v1/plugins/security/approve", cred, body); err != nil {
		return fmt.Errorf("批准失败: %w", err)
	}
	return nil
}

func handleSecurity(ctx context.Context, httpBase, cred, cmd string, args []string, ui configInteraction) error {
	switch cmd {
	case "security-list":
		status := ""
		if len(args) > 0 {
			status = args[0]
		}
		url := httpBase + "/v1/plugins/security"
		if status != "" {
			url += "?status=" + status
		}
		return doGet(ctx, url, cred)

	case "security-approve":
		if len(args) == 0 {
			return errors.New("用法: security-approve <decision_id> [--checksum <sha256>]")
		}
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return errors.New("decision_id 必须是整数")
		}
		fs := flag.NewFlagSet("security-approve", flag.ContinueOnError)
		csum := fs.String("checksum", "", "审查绑定的清单 sha256")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("security-approve 不接受额外参数或 --yes 跳过确认")
		}
		payload := map[string]any{"decision_id": id}
		if *csum != "" {
			if err := approvePluginInteractive(ctx, httpBase, cred, id, *csum, ui); err != nil {
				return err
			}
			return nil
		}
		body, _ := json.Marshal(payload)
		return doPost(ctx, httpBase+"/v1/plugins/security/approve", cred, body)

	case "security-revoke":
		if len(args) == 0 {
			return errors.New("用法: security-revoke <decision_id> [--reason \"...\"]")
		}
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return errors.New("decision_id 必须是整数")
		}
		reason := "security_bot revoked"
		if len(args) > 1 {
			reason = strings.Join(args[1:], " ")
		}
		body, _ := json.Marshal(map[string]any{"decision_id": id, "reason": reason})
		return doPost(ctx, httpBase+"/v1/plugins/security/revoke", cred, body)

	default:
		return fmt.Errorf("未知 security 命令: %s", cmd)
	}
}

// ---------------------- M2: DLQ 运营实现 ----------------------

func handleDLQ(ctx context.Context, httpBase, cred string, args []string) error {
	if len(args) == 0 {
		return errors.New("缺少 dlq 子命令 (list, replay, archive)")
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "list":
		return doGet(ctx, httpBase+"/v1/dlq", cred)
	case "replay":
		fs := flag.NewFlagSet("dlq replay", flag.ExitOnError)
		jobID := fs.Int64("job", 0, "任务 ID")
		_ = fs.Parse(rest)
		if *jobID == 0 {
			return errors.New("dlq replay 需要 -job")
		}
		return doPost(ctx, fmt.Sprintf("%s/v1/jobs/%d/replay", httpBase, *jobID), cred, []byte("{}"))
	case "archive":
		fs := flag.NewFlagSet("dlq archive", flag.ExitOnError)
		jobID := fs.Int64("job", 0, "任务 ID")
		_ = fs.Parse(rest)
		if *jobID == 0 {
			return errors.New("dlq archive 需要 -job")
		}
		return doPost(ctx, fmt.Sprintf("%s/v1/dlq/%d/archive", httpBase, *jobID), cred, []byte("{}"))
	default:
		return fmt.Errorf("未知 dlq 子命令: %s", sub)
	}
}

// ---------------------- HTTP 客户端辅助函数 ----------------------

func readCredential(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 user_bot 凭证 %s: %w（hub 启动时签发）", path, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func doPost(ctx context.Context, url, cred string, body []byte) error {
	respData, err := postAndReturn(ctx, url, cred, body)
	if err != nil {
		return err
	}
	fmt.Println(string(bytes.TrimSpace(respData)))
	return nil
}

func postAndReturn(ctx context.Context, url, cred string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cred != "" {
		req.Header.Set("X-Nexus-Credential", cred)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, data)
	}
	return data, nil
}

func doGet(ctx context.Context, url, cred string) error {
	data, err := getAndReturn(ctx, url, cred)
	if err != nil {
		return err
	}
	fmt.Println(string(bytes.TrimSpace(data)))
	return nil
}

func getAndReturn(ctx context.Context, url, cred string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if cred != "" {
		req.Header.Set("X-Nexus-Credential", cred)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, data)
	}
	return data, nil
}

func doSend(ctx context.Context, httpBase, cred, botID, text string, wait bool, timeout time.Duration, grpcAddr string) error {
	env := envelope.NewRoot("user_bot", envelope.ChannelCLI, envelope.MsgRequest, "cli_"+fmt.Sprint(time.Now().UnixNano()))
	payload, err := json.Marshal(map[string]any{"text": text, "target_bot": botID})
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"envelope":       env,
		"payload_base64": base64.StdEncoding.EncodeToString(payload),
		"bot_id":         botID,
		"queue_name":     botID,
		"job_type":       "bot.request",
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, httpBase+"/v1/ingress", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Nexus-Credential", cred)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, data)
	}
	fmt.Println(string(bytes.TrimSpace(data)))
	if !wait {
		return nil
	}
	fmt.Fprintln(os.Stderr, "等待 bot.reply ...")
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return doSubscribe(waitCtx, grpcAddr, cred, "user_bot", env.EnvelopeID)
}

func doSubscribe(ctx context.Context, grpcAddr, cred, group, wantCorrelation string) error {
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pb.NewBotRuntimeClient(conn)

	env := envelope.NewRoot("user_bot", envelope.ChannelCLI, envelope.MsgControl, "sub_"+fmt.Sprint(time.Now().UnixNano()))
	md := env.Metadata()
	md[envelope.MetaCredential] = cred
	pairs := make([]string, 0, len(md)*2)
	for k, v := range md {
		pairs = append(pairs, k, v)
	}
	callCtx := metadata.AppendToOutgoingContext(ctx, pairs...)
	stream, err := client.SubscribeEvents(callCtx, &pb.SubscribeRequest{
		ConsumerGroup: group, RequestId: env.EnvelopeID,
	})
	if err != nil {
		return err
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return err
		}
		out, _ := json.Marshal(map[string]string{
			"event_id": ev.EventId, "event_type": ev.EventType,
			"correlation_id": ev.CorrelationId, "payload_ref": ev.PayloadRef,
		})
		fmt.Println(string(out))
		if wantCorrelation != "" && ev.EventType == "bot.reply" && ev.CorrelationId == wantCorrelation {
			return nil
		}
	}
}

// 辅助计算 sha256 checksum
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
