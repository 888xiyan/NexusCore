package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/osproc"
	pb "github.com/888xiyan/NexusCore/proto/gen/v1"
)

// 子进程使用当前测试二进制，不依赖 hub、数据库或外部编译器。
func TestMain(m *testing.M) {
	if mode := os.Getenv("NEXUS_TEST_PLUGIN_MODE"); mode != "" {
		runPluginHelper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runPluginHelper(mode string) {
	term := make(chan os.Signal, 1)
	if mode == "stubborn" || mode == "tree" {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signal.Notify(term, syscall.SIGTERM)
	}
	if mode == "tree" || mode == "leader-first" {
		exe, _ := os.Executable()
		cmd := exec.Command(exe)
		for _, env := range os.Environ() {
			if !strings.HasPrefix(env, "NEXUS_TEST_PLUGIN_MODE=") && !strings.HasPrefix(env, "NEXUS_TEST_PLUGIN_READY=") {
				cmd.Env = append(cmd.Env, env)
			}
		}
		cmd.Env = append(cmd.Env, "NEXUS_TEST_PLUGIN_MODE=stubborn", "NEXUS_TEST_PLUGIN_READY="+os.Getenv("NEXUS_TEST_PLUGIN_READY")+".leaf")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
	}
	info, err := osproc.ReadProcessInfo(os.Getpid())
	if err != nil {
		os.Exit(3)
	}
	body, _ := json.Marshal(info)
	if err := os.WriteFile(os.Getenv("NEXUS_TEST_PLUGIN_READY"), body, 0600); err != nil {
		os.Exit(4)
	}
	if mode == "stubborn" || mode == "tree" {
		for {
			_ = syscall.Pause()
		}
	}
	<-term
}

type processReport struct {
	Envelope       envelope.Envelope `json:"envelope"`
	PluginID       string            `json:"plugin_id"`
	OwnerBotID     string            `json:"owner_bot_id"`
	OwnerPID       int               `json:"owner_pid"`
	OwnerStartTime int64             `json:"owner_start_time"`
	PID            int               `json:"pid"`
	PGID           int               `json:"pgid"`
	Start          int64             `json:"pid_start_time"`
	State          string            `json:"state"`
	LastError      string            `json:"last_error"`
}

func decodeReport(t *testing.T, r *http.Request) processReport {
	t.Helper()
	var rep processReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		t.Errorf("decode report: %v", err)
	}
	if err := rep.Envelope.Validate(); err != nil {
		t.Errorf("invalid health envelope: %v", err)
	}
	if rep.Envelope.Channel != envelope.ChannelHealth || rep.Envelope.MsgType != envelope.MsgEvent || rep.Envelope.FromBotID != rep.OwnerBotID {
		t.Errorf("incorrect health actor/channel: %+v", rep)
	}
	if r.Header.Get("X-Nexus-Credential") != "test-credential" {
		t.Error("missing credential")
	}
	owner, err := osproc.ReadProcessInfo(os.Getpid())
	if err != nil || rep.OwnerPID != os.Getpid() || rep.OwnerStartTime != owner.StartTime {
		t.Errorf("incorrect owner identity: %+v, %v", rep, err)
	}
	if rep.PID <= 1 || rep.PGID != rep.PID || rep.Start <= 0 {
		t.Errorf("invalid child identity: %+v", rep)
	}
	return rep
}

func newPluginBot(t *testing.T, mode string, handler http.HandlerFunc) (*bot, string) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("NEXUS_TEST_PLUGIN_MODE", mode)
	t.Setenv("NEXUS_TEST_PLUGIN_READY", ready)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	b := &bot{
		id: "owner_bot", cred: "test-credential", httpBase: server.URL,
		pluginID: "plugin_test", pluginBinary: exe, pluginDigest: fmt.Sprintf("%x", sum),
		pluginStopGrace: 80 * time.Millisecond,
	}
	t.Cleanup(func() {
		if err := b.stopPluginProcess(); err != nil {
			t.Logf("cleanup terminal report: %v", err)
		}
	})
	return b, ready
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !check() {
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("timed out waiting for local process")
		}
	}
}

func readReady(t *testing.T, path string) osproc.ProcessInfo {
	t.Helper()
	var info osproc.ProcessInfo
	eventually(t, func() bool {
		body, err := os.ReadFile(path)
		return err == nil && json.Unmarshal(body, &info) == nil && info.StartTime > 0
	})
	return info
}

func childInstance(b *bot) *pluginProcess {
	b.pluginMu.Lock()
	defer b.pluginMu.Unlock()
	return b.child
}

func assertReaped(t *testing.T, p *pluginProcess) {
	t.Helper()
	select {
	case <-p.done:
	default:
		t.Fatal("child Wait not completed")
	}
	if p.cmd.ProcessState == nil {
		t.Fatal("cmd.Wait was not called")
	}
	if _, err := osproc.ReadProcessInfo(p.pid); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child not reaped: %v", err)
	}
}

func TestPluginDigestMismatchDoesNotExec(t *testing.T) {
	var requests atomic.Int32
	b, ready := newPluginBot(t, "graceful", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	})
	for _, digest := range []string{strings.Repeat("0", 64), "", "not-a-digest"} {
		b.pluginDigest = digest
		if err := b.startPluginProcess(context.Background()); err == nil {
			t.Fatalf("accepted digest %q", digest)
		}
	}
	if childInstance(b) != nil || requests.Load() != 0 {
		t.Fatal("digest failure executed/reported a process")
	}
	if _, err := os.Stat(ready); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("digest failure ran helper: %v", err)
	}
}

func TestPluginRunningReportFailureReapsGroup(t *testing.T) {
	for _, failure := range []string{"500", "timeout", "context"} {
		t.Run(failure, func(t *testing.T) {
			var b *bot
			var ready string
			var child, leaf osproc.ProcessInfo
			var saved *pluginProcess
			var cancel context.CancelFunc
			ctx := context.Background()
			if failure == "context" {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
			}
			b, ready = newPluginBot(t, "tree", func(w http.ResponseWriter, r *http.Request) {
				rep := decodeReport(t, r)
				if rep.State == "running" {
					child = readReady(t, ready)
					leaf = readReady(t, ready+".leaf")
					// start 持有 pluginMu；HTTP 请求已同步发布该实例。
					saved = b.child
					if failure == "500" {
						w.WriteHeader(http.StatusInternalServerError)
					} else {
						if cancel != nil {
							cancel()
						}
						<-r.Context().Done()
					}
					return
				}
				if rep.State != "failed" || rep.PID != child.PID || rep.Start != child.StartTime {
					t.Errorf("incorrect failed generation: %+v", rep)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			began := time.Now()
			if err := b.startPluginProcess(ctx); err == nil {
				t.Fatal("running report failure was ignored")
			}
			if time.Since(began) > 7*time.Second {
				t.Fatal("report HTTP timeout exceeded 5s budget plus cleanup")
			}
			assertReaped(t, saved)
			eventually(t, func() bool { return !osproc.IsProcessAlive(leaf.PID, leaf.StartTime) })
			if childInstance(b) != nil {
				t.Fatal("successful failed report retained old generation")
			}
		})
	}
}

func TestPluginStopStartAndRepeatedStop(t *testing.T) {
	var mu sync.Mutex
	var reports []processReport
	b, ready := newPluginBot(t, "graceful", func(w http.ResponseWriter, r *http.Request) {
		rep := decodeReport(t, r)
		mu.Lock()
		reports = append(reports, rep)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	if err := b.startPluginProcess(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := childInstance(b)
	readReady(t, ready)
	if err := b.startPluginProcess(context.Background()); err != nil || childInstance(b) != first {
		t.Fatalf("repeated start replaced live process: %v", err)
	}
	if err := b.stopPluginProcess(); err != nil {
		t.Fatal(err)
	}
	assertReaped(t, first)
	if err := b.stopPluginProcess(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ready); err != nil {
		t.Fatal(err)
	}
	if err := b.startPluginProcess(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := childInstance(b)
	readReady(t, ready)
	if first.pid == second.pid {
		t.Fatal("manual start reused old PID instead of exec")
	}
	if err := b.stopPluginProcess(); err != nil {
		t.Fatal(err)
	}
	assertReaped(t, second)
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 4 {
		t.Fatalf("expected running/stopped per generation, got %+v", reports)
	}
	for i, state := range []string{"running", "stopped", "running", "stopped"} {
		if reports[i].State != state {
			t.Fatalf("report %d = %s, want %s", i, reports[i].State, state)
		}
	}
	if reports[0].PID != reports[1].PID || reports[2].PID != reports[3].PID {
		t.Fatal("terminal report crossed generations")
	}
}

func TestPluginStopReapsDescendants(t *testing.T) {
	for _, mode := range []string{"tree", "leader-first"} {
		t.Run(mode, func(t *testing.T) {
			var terminal processReport
			b, ready := newPluginBot(t, mode, func(w http.ResponseWriter, r *http.Request) {
				rep := decodeReport(t, r)
				if rep.State != "running" {
					terminal = rep
				}
				w.WriteHeader(http.StatusNoContent)
			})
			if err := b.startPluginProcess(context.Background()); err != nil {
				t.Fatal(err)
			}
			p := childInstance(b)
			readReady(t, ready)
			leaf := readReady(t, ready+".leaf")
			if err := b.stopPluginProcess(); err != nil {
				t.Fatal(err)
			}
			assertReaped(t, p)
			eventually(t, func() bool { return !osproc.IsProcessAlive(leaf.PID, leaf.StartTime) })
			if mode == "tree" && (terminal.State != "killed" || terminal.LastError != "stop_timeout") {
				t.Fatalf("missing timeout state: %+v", terminal)
			}
		})
	}
}

func TestPluginTerminalFailureBlocksNextGeneration(t *testing.T) {
	var reject atomic.Bool
	reject.Store(true)
	var running atomic.Int32
	b, ready := newPluginBot(t, "graceful", func(w http.ResponseWriter, r *http.Request) {
		rep := decodeReport(t, r)
		if rep.State == "running" {
			running.Add(1)
		} else if reject.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := b.startPluginProcess(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := childInstance(b)
	readReady(t, ready)
	if err := b.stopPluginProcess(); err == nil {
		t.Fatal("ignored terminal report failure")
	}
	assertReaped(t, first)
	if err := b.startPluginProcess(context.Background()); err == nil {
		t.Fatal("started before old terminal report succeeded")
	}
	if running.Load() != 1 || childInstance(b) != first {
		t.Fatal("lost failed-report generation identity")
	}
	reject.Store(false)
	if err := b.startPluginProcess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if childInstance(b).pid == first.pid || running.Load() != 2 {
		t.Fatal("terminal retry failed to unblock a new exec")
	}
}

func TestPluginConcurrentControl(t *testing.T) {
	b, _ := newPluginBot(t, "graceful", func(w http.ResponseWriter, r *http.Request) {
		decodeReport(t, r)
		w.WriteHeader(http.StatusNoContent)
	})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				err = b.startPluginProcess(context.Background())
			} else {
				err = b.stopPluginProcess()
			}
			if err != nil {
				t.Errorf("concurrent operation: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if err := b.stopPluginProcess(); err != nil {
		t.Fatal(err)
	}
}

type ackClient struct {
	pb.BotRuntimeClient
	requests []*pb.AckTaskRequest
}

func (c *ackClient) AckTask(_ context.Context, req *pb.AckTaskRequest, _ ...grpc.CallOption) (*pb.AckTaskResponse, error) {
	c.requests = append(c.requests, req)
	return &pb.AckTaskResponse{}, nil
}

func TestPluginControlDoesNotEcho(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		ok            bool
	}{
		{"start", `{"plugin_id":"plugin_test","action":"start"}`, true},
		{"stop", `{"plugin_id":"plugin_test","action":"stop"}`, true},
		{"other-plugin", `{"plugin_id":"plugin_other","action":"stop"}`, false},
		{"unsupported-action", `{"plugin_id":"plugin_test","action":"kill"}`, false},
		{"invalid-json", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var unexpected atomic.Int32
			b, _ := newPluginBot(t, "graceful", func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/payload/control":
					_, _ = w.Write([]byte(tc.payload))
				case "/v1/plugins/process/report":
					decodeReport(t, r)
				default:
					unexpected.Add(1)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			var echoCalls atomic.Int32
			go func() {
				if conn, err := listener.Accept(); err == nil {
					echoCalls.Add(1)
					_ = conn.Close()
				}
			}()
			b.transportAddr = listener.Addr().String()
			client := &ackClient{}
			b.client = client
			if tc.name == "stop" {
				if err := b.startPluginProcess(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			err = b.handleTask(context.Background(), &pb.TaskAssignment{
				JobId: "job_control", LeaseToken: "lease", PayloadRef: "control", CausalContextJson: `{"job_type":"plugin.control"}`,
			})
			if (err == nil) != tc.ok {
				t.Fatalf("handleTask error=%v, want ok=%v", err, tc.ok)
			}
			if len(client.requests) != 1 {
				t.Fatalf("expected exactly one AckTask: %+v", client.requests)
			}
			ack := client.requests[0]
			var result pluginControlResult
			if err := json.Unmarshal([]byte(ack.ResultRef), &result); err != nil || result.OK != tc.ok || (!tc.ok && result.Error == "") {
				t.Fatalf("invalid control result: %s, %v", ack.ResultRef, err)
			}
			if ack.JobId != "job_control" || ack.LeaseToken != "lease" || unexpected.Load() != 0 || echoCalls.Load() != 0 {
				t.Fatal("control task escaped into echo/reply or lost lease identity")
			}
			if tc.name == "start" && childInstance(b) == nil {
				t.Fatal("start control did not exec")
			}
			if tc.name == "stop" && childInstance(b) != nil {
				t.Fatal("stop control did not stop")
			}
		})
	}
}
