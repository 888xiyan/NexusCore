// Command cli 是用户(user_bot)的投递与管理代理：M1 的第一个用户界面。
// 依据：内核 MVP 详设 §5.2 / §6.1、开发规划原则 2（CLI 先于 UI）。
// CLI 不是总线主体：它以 user_bot 身份投递信封，凭证由 hub 启动时签发。
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/version"
	pb "github.com/888xiyan/NexusCore/proto/gen/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
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
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	httpBase := fs.String("hub-http", defaultHTTP, "hub HTTP 入口")
	grpcAddr := fs.String("hub-grpc", defaultGRPC, "hub gRPC 控制面")
	credFile := fs.String("credential-file", defaultCredFile, "user_bot 凭证文件")
	text := fs.String("text", "", "要投递的文本")
	botID := fs.String("bot", "echo_bot", "目标 Bot")
	wait := fs.Bool("wait", false, "投递后等待回复事件")
	timeout := fs.Duration("timeout", 60*time.Second, "等待超时")
	jobID := fs.Int64("job", 0, "任务 ID")
	decision := fs.String("decision", "", "continue / replay / discard")
	kind := fs.String("kind", "bot", "凭证主体类型")
	subject := fs.String("subject", "", "凭证主体 ID")
	group := fs.String("group", "user_bot", "Consumer Group")
	_ = fs.Parse(os.Args[2:])

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cred, err := readCredential(*credFile)
	if err != nil && cmd != "help" {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	switch cmd {
	case "send":
		if *text == "" {
			fmt.Fprintln(os.Stderr, "send 需要 -text")
			os.Exit(2)
		}
		if err := doSend(ctx, *httpBase, cred, *botID, *text, *wait, *timeout, *grpcAddr); err != nil {
			fmt.Fprintln(os.Stderr, "投递失败:", err)
			os.Exit(1)
		}
	case "resume-list":
		if err := doGet(ctx, *httpBase+"/v1/jobs/suspended", cred); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "resume-confirm":
		if *jobID == 0 || *decision == "" {
			fmt.Fprintln(os.Stderr, "resume-confirm 需要 -job 与 -decision（continue/replay/discard）")
			os.Exit(2)
		}
		body, _ := json.Marshal(map[string]string{"decision": *decision})
		if err := doPost(ctx, fmt.Sprintf("%s/v1/jobs/%d/resume", *httpBase, *jobID), cred, body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "replay":
		if *jobID == 0 {
			fmt.Fprintln(os.Stderr, "replay 需要 -job")
			os.Exit(2)
		}
		if err := doPost(ctx, fmt.Sprintf("%s/v1/jobs/%d/replay", *httpBase, *jobID), cred, []byte("{}")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "fixture":
		if err := doPost(ctx, *httpBase+"/v1/dev/fixture", cred, []byte("{}")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "credential":
		if *subject == "" {
			fmt.Fprintln(os.Stderr, "credential 需要 -subject")
			os.Exit(2)
		}
		body, _ := json.Marshal(map[string]string{"kind": *kind, "subject": *subject})
		if err := doPost(ctx, *httpBase+"/v1/credentials", cred, body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "subscribe":
		if err := doSubscribe(ctx, *grpcAddr, cred, *group, ""); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "metrics":
		if err := doGet(ctx, *httpBase+"/metrics", ""); err != nil {
			fmt.Fprintln(os.Stderr, err)
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

func usage() {
	fmt.Fprint(os.Stderr, `NexusCore cli（user_bot 的投递与管理代理）

用法：cli <子命令> [参数]

  send            -text "..." [-bot echo_bot] [-wait] [-timeout 60s]
  subscribe       [-group user_bot] [-timeout 60s]
  resume-list     列出挂起任务与副作用清单
  resume-confirm  -job N -decision continue|replay|discard
  replay          -job N（DLQ 重放，必经人工确认）
  fixture         绑定 M1 冒烟夹具二进制（M2 起由插件 install 取代）
  credential      -kind bot|agent|plugin -subject ID
  metrics         查看内核指标

公共参数：-hub-http / -hub-grpc / -credential-file
`)
}

func readCredential(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 user_bot 凭证 %s: %w（hub 启动时签发）", path, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func doPost(ctx context.Context, url, cred string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cred != "" {
		req.Header.Set("X-Nexus-Credential", cred)
	}
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
	return nil
}

func doGet(ctx context.Context, url, cred string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if cred != "" {
		req.Header.Set("X-Nexus-Credential", cred)
	}
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
	return nil
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
		// 路由提示：通讯元数据，由入口适配器携带，不进信封
		"bot_id":     botID,
		"queue_name": botID,
		"job_type":   "bot.request",
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

// doSubscribe 消费订阅组事件；wantCorrelation 非空时只认该因果链的 bot.reply，
// 避免把历史遗留事件当成本次请求的回复。
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
