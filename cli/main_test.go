package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/888xiyan/NexusCore/internal/envelope"
)

const testChecksum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const testProposalID = "proposal_test"

func writeTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(path, []byte(`{"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestConfigApprovalInteraction(t *testing.T) {
	validDiff := `{"base":{"spec":{"enabled":false}},"proposal":{"spec":{"enabled":true}},"checksum":"` + testChecksum + `"}`
	for _, command := range []string{"approve", "set"} {
		t.Run(command, func(t *testing.T) {
			for _, tt := range []struct {
				name          string
				answer        string
				nonTTY        bool
				checksum      string
				diff          string
				diffStatus    int
				approveStatus int
				extraArgs     []string
				wantErr       string
				wantApprove   bool
				wantPrompt    bool
				noRequests    bool
			}{
				{name: "golden", answer: "yes\n", wantApprove: true, wantPrompt: true},
				{name: "reject", answer: "no\n", wantErr: "用户取消", wantPrompt: true},
				{name: "empty_answer", answer: "\n", wantErr: "用户取消", wantPrompt: true},
				{name: "eof", wantErr: "读取批准确认失败", wantPrompt: true},
				{name: "yes_without_enter", answer: "yes", wantErr: "读取批准确认失败", wantPrompt: true},
				{name: "non_tty", answer: "yes\n", nonTTY: true, wantErr: "真实交互终端", noRequests: true},
				{name: "checksum_mismatch", answer: "yes\n", checksum: strings.Repeat("f", 64), wantErr: "checksum 与服务端 diff 不一致"},
				{name: "missing_checksum", answer: "yes\n", diff: `{"proposal":{}}`, wantErr: "有效的 sha256 checksum"},
				{name: "short_checksum", answer: "yes\n", diff: `{"proposal":{},"checksum":"abc"}`, wantErr: "有效的 sha256 checksum"},
				{name: "missing_proposal", answer: "yes\n", diff: `{"checksum":"` + testChecksum + `"}`, wantErr: "缺少 proposal"},
				{name: "malformed_diff", answer: "yes\n", diff: `{`, wantErr: "解析 diff 响应"},
				{name: "diff_http_failure", answer: "yes\n", diffStatus: http.StatusForbidden, wantErr: "获取 diff 失败"},
				{name: "approval_http_failure", answer: "yes\n", approveStatus: http.StatusConflict, wantErr: "批准失败", wantApprove: true, wantPrompt: true},
				{name: "no_yes_bypass", answer: "yes\n", extraArgs: []string{"--yes"}, wantErr: "flag provided but not defined", noRequests: true},
				{name: "no_trailing_yes_bypass", answer: "yes\n", extraArgs: []string{"unexpected", "--yes"}, wantErr: "不接受额外参数", noRequests: true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					if command == "set" && tt.checksum != "" {
						return // set 绑定 diff 中的 checksum，不接受命令行 checksum。
					}
					var output bytes.Buffer
					var requests []string
					var mu sync.Mutex
					readConfirmation := false
					answer := strings.NewReader(tt.answer)
					ui := configInteraction{
						input: readerFunc(func(p []byte) (int, error) {
							// 读取确认之前必须已经完整展示提案和新旧配置。
							for _, text := range []string{testProposalID, "checksum（前 12 位）: " + testChecksum[:12], "Diff", `"base"`, `"proposal"`, `"enabled": false`, `"enabled": true`} {
								if !strings.Contains(output.String(), text) {
									t.Errorf("确认前缺少展示 %q: %s", text, output.String())
								}
							}
							mu.Lock()
							readConfirmation = true
							mu.Unlock()
							return answer.Read(p)
						}),
						output: &output,
						isTerminal: func() bool {
							return !tt.nonTTY
						},
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						defer mu.Unlock()
						requests = append(requests, r.Method+" "+r.URL.Path)
						if r.Header.Get("X-Nexus-Credential") != "test-cred" {
							t.Error("缺少 user_bot 凭证")
						}
						switch r.URL.Path {
						case "/v1/config/propose":
							var body struct {
								ScopeType string          `json:"scope_type"`
								ScopeID   string          `json:"scope_id"`
								Spec      json.RawMessage `json:"spec"`
							}
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							if body.ScopeType != "bot" || body.ScopeID != "demo_bot" || string(body.Spec) != `{"enabled":true}` {
								t.Errorf("提案请求错误: %+v", body)
							}
							fmt.Fprintf(w, `{"proposal_id":%q}`, testProposalID)
						case "/v1/config/diff/" + testProposalID:
							if tt.diffStatus != 0 {
								w.WriteHeader(tt.diffStatus)
							}
							diff := validDiff
							if tt.diff != "" {
								diff = tt.diff
							}
							fmt.Fprint(w, diff)
						case "/v1/config/approve":
							if !readConfirmation {
								t.Error("未经交互读取即批准")
							}
							var body map[string]string
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							if body["proposal_id"] != testProposalID || body["checksum"] != testChecksum {
								t.Errorf("批准未绑定展示的提案和完整 checksum: %v", body)
							}
							if tt.approveStatus != 0 {
								w.WriteHeader(tt.approveStatus)
							}
							fmt.Fprint(w, `{}`)
						case "/v1/config/activate":
							var body map[string]string
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							if body["proposal_id"] != testProposalID {
								t.Errorf("激活了错误的提案: %v", body)
							}
							fmt.Fprint(w, `{}`)
						default:
							t.Errorf("意外请求: %s", r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
						}
					}))
					defer server.Close()
					csum := testChecksum
					if tt.checksum != "" {
						csum = tt.checksum
					}
					args := []string{"approve", testProposalID, "--checksum", csum}
					if command == "set" {
						args = []string{"set", "--file", writeTestConfig(t), "--scope", "bot", "--id", "demo_bot"}
					}
					args = append(args, tt.extraArgs...)
					err := handleConfigInteractive(context.Background(), server.URL, "test-cred", args, ui)
					if tt.wantErr == "" {
						if err != nil {
							t.Fatal(err)
						}
					} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("错误 = %v，期望包含 %q", err, tt.wantErr)
					}
					var want []string
					if !tt.noRequests {
						if command == "set" {
							want = append(want, "POST /v1/config/propose")
						}
						want = append(want, "GET /v1/config/diff/"+testProposalID)
						if tt.wantApprove {
							want = append(want, "POST /v1/config/approve")
						}
						if command == "set" && tt.wantErr == "" {
							want = append(want, "POST /v1/config/activate")
						}
					}
					mu.Lock()
					defer mu.Unlock()
					if !reflect.DeepEqual(requests, want) {
						t.Errorf("请求顺序 = %v，期望 %v", requests, want)
					}
					if readConfirmation != tt.wantPrompt {
						t.Errorf("读取确认 = %v，期望 %v", readConfirmation, tt.wantPrompt)
					}
				})
			}
		})
	}
}

func TestConfigRejectsRedirectedStdin(t *testing.T) {
	file, err := os.Open(writeTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	pipe, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	if _, err := io.WriteString(writer, "yes\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	for name, input := range map[string]*os.File{"file": file, "dev_null": null, "pipe_yes": pipe} {
		t.Run(name, func(t *testing.T) {
			if isTerminal(input) {
				t.Fatal("重定向输入被识别为终端")
			}
			original := os.Stdin
			os.Stdin = input
			defer func() { os.Stdin = original }()
			for _, args := range [][]string{
				{"approve", testProposalID, "--checksum", testChecksum},
				{"set", "--file", "/nonexistent/spec.json", "--scope", "bot", "--id", "demo_bot"},
			} {
				// 无效 URL 和不存在的配置路径证明终端检查先于文件读取和网络操作。
				err := handleConfig(context.Background(), ":invalid-url", "", args)
				if err == nil || !strings.Contains(err.Error(), "真实交互终端") {
					t.Errorf("%s 未优先拒绝非 TTY: %v", args[0], err)
				}
			}
		})
	}
}

func TestIsTerminalAcceptsPTY(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("环境不支持 PTY: %v", err)
	}
	defer master.Close()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	if !isTerminal(slave) {
		t.Fatal("真实伪终端未被识别")
	}
}

func TestConfigConfirmationIOFailure(t *testing.T) {
	for _, failure := range []string{"read", "write"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("交互失败仍写入: %s", r.Method)
				}
				fmt.Fprintf(w, `{"proposal":{"spec":{}},"checksum":%q}`, testChecksum)
			}))
			defer server.Close()
			ioErr := errors.New("交互 I/O 错误")
			ui := configInteraction{input: strings.NewReader("yes\n"), output: io.Discard, isTerminal: func() bool { return true }}
			if failure == "read" {
				ui.input = readerFunc(func([]byte) (int, error) { return 0, ioErr })
			} else {
				ui.output = writerFunc(func([]byte) (int, error) { return 0, ioErr })
			}
			err := approveConfigInteractive(context.Background(), server.URL, "", testProposalID, testChecksum, ui)
			if !errors.Is(err, ioErr) {
				t.Errorf("应保留交互错误: %v", err)
			}
		})
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestPluginLifecycleEnvelopes(t *testing.T) {
	var mu sync.Mutex
	ids, keys := map[string]bool{}, map[string]bool{}
	for _, tt := range []struct {
		command string
		path    string
		args    []string
		fields  map[string]any
	}{
		{command: "start", path: "process/start"},
		{command: "stop", path: "process/stop"},
		{command: "disable", path: "disable"},
		{command: "uninstall", path: "uninstall", fields: map[string]any{"confirm": false, "keep": false}},
		{command: "uninstall", path: "uninstall", args: []string{"--confirm", "--keep"}, fields: map[string]any{"confirm": true, "keep": true}},
		{command: "kill", path: "process/kill", args: []string{"--confirm"}, fields: map[string]any{"confirm": true, "reason": "user_bot_force_kill"}},
		{command: "kill", path: "process/kill", args: []string{"--confirm", "--reason", "manual test"}, fields: map[string]any{"confirm": true, "reason": "manual test"}},
	} {
		t.Run(tt.command, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/plugins/plugin_demo/"+tt.path {
					t.Errorf("错误请求: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("X-Nexus-Credential") != "test-cred" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("请求头缺失: %v", r.Header)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				var env envelope.Envelope
				if err := json.Unmarshal(body["envelope"], &env); err != nil {
					t.Errorf("缺少或无法解析 envelope: %v", err)
					return
				}
				if err := env.Validate(); err != nil {
					t.Errorf("信封不完整: %v", err)
				}
				if env.FromBotID != "user_bot" || env.Channel != envelope.ChannelCLI || env.MsgType != envelope.MsgControl {
					t.Errorf("错误的控制信封身份: %+v", env)
				}
				if env.CausationID != "" || env.RootEnvelopeID != env.EnvelopeID || env.CorrelationID != env.EnvelopeID || len(env.CausalPath) != 1 || env.CausalPath[0].Actor != "user_bot" {
					t.Errorf("非完整客户端根信封: %+v", env)
				}
				if ids[env.EnvelopeID] || keys[env.IdempotencyKey] {
					t.Error("独立命令复用了信封或幂等键")
				}
				ids[env.EnvelopeID], keys[env.IdempotencyKey] = true, true
				delete(body, "envelope")
				if len(body) != len(tt.fields) {
					t.Errorf("额外请求字段不符: %v", body)
				}
				for name, want := range tt.fields {
					var got any
					if err := json.Unmarshal(body[name], &got); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("字段 %s = %v，期望 %v", name, got, want)
					}
				}
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			for range 2 {
				args := append([]string{tt.command, "plugin_demo"}, tt.args...)
				if err := handlePlugin(context.Background(), server.URL, "test-cred", args); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != 2 {
				t.Errorf("请求次数 = %d，期望 2", calls)
			}
		})
	}
}

func TestPluginKillStillRequiresConfirm(t *testing.T) {
	err := handlePlugin(context.Background(), ":invalid-url", "", []string{"kill", "plugin_demo"})
	if err == nil || !strings.Contains(err.Error(), "必须提供 --confirm") {
		t.Errorf("未拒绝缺少确认的 kill: %v", err)
	}
}

func TestPluginManifestRequestsUnchanged(t *testing.T) {
	manifest := `{"plugin_id":"plugin_demo","binary":"demo","bot_templates":[{"bot_id":"demo_bot"}]}`
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"install", "upgrade"} {
		t.Run(command, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/v1/plugins/"+command || string(body) != manifest {
					t.Errorf("清单请求契约改变: %s %s", r.URL.Path, body)
				}
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			if err := handlePlugin(context.Background(), server.URL, "", []string{command, path}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
