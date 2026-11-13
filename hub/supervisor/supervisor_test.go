package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/pgtest"
)

func newSupervisor(t *testing.T) (*Supervisor, *pgtest.DB) {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	reg, err := registry.New(st, []byte("0123456789abcdef0123456789abcdef"), registry.DefaultTTL)
	if err != nil {
		t.Fatalf("建 registry: %v", err)
	}
	sup := New(st, reg, scheduler.New(st, scheduler.Options{}), config.New(st), Options{
		GRPCAddr: "127.0.0.1:0", TransportAddr: "127.0.0.1:0",
	})
	t.Cleanup(sup.Shutdown)
	return sup, db
}

// writeArtifact 造一个可执行占位制品并返回其真实 sha256。
func writeArtifact(t *testing.T, body string) (path, digest string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "fake-bot")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return path, hex.EncodeToString(sum[:])
}

func TestVerifyDigest(t *testing.T) {
	path, digest := writeArtifact(t, "#!/bin/sh\nexit 0\n")
	if err := verifyDigest(path, digest); err != nil {
		t.Fatalf("摘要一致却报错: %v", err)
	}
	if err := verifyDigest(path, "deadbeef"); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("摘要不一致应拒绝执行，实际 %v", err)
	}
	if err := verifyDigest(filepath.Join(t.TempDir(), "不存在"), digest); err == nil {
		t.Fatal("制品不存在应报错")
	}
}

// 制品摘要不符：拒绝 exec，且不留下实例登记
func TestStartRejectsDigestMismatch(t *testing.T) {
	sup, db := newSupervisor(t)
	path, _ := writeArtifact(t, "#!/bin/sh\nexit 0\n")
	db.Exec(t, `UPDATE bot_manifest SET binary_path=$1, binary_digest=repeat('1',64) WHERE bot_id='echo_bot'`, path)

	err := sup.Start(context.Background(), "echo_bot")
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("应拒绝摘要不符的制品，实际 %v", err)
	}
	var n int
	if err := db.QueryRow(t, `SELECT count(*) FROM bot_instances WHERE bot_id='echo_bot'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("被拒绝的启动不得登记实例，实际 %d 行", n)
	}
}

// M1 只支持 lazy；resident 明确拒绝而非静默降级
func TestStartRejectsNonLazy(t *testing.T) {
	sup, db := newSupervisor(t)
	path, digest := writeArtifact(t, "#!/bin/sh\nexit 0\n")
	db.Exec(t, `UPDATE bot_manifest SET binary_path=$1, binary_digest=$2, activation_mode='resident' WHERE bot_id='echo_bot'`, path, digest)

	if err := sup.Start(context.Background(), "echo_bot"); !errors.Is(err, ErrNotLazy) {
		t.Fatalf("resident 应被拒绝，实际 %v", err)
	}
}

// desired_state 非 start 时不得拉起（D-20：supervisor 只读 desired）
func TestStartRejectsStoppedDesiredState(t *testing.T) {
	sup, db := newSupervisor(t)
	path, digest := writeArtifact(t, "#!/bin/sh\nexit 0\n")
	db.Exec(t, `UPDATE bot_manifest SET binary_path=$1, binary_digest=$2 WHERE bot_id='echo_bot'`, path, digest)
	db.Exec(t, `UPDATE bots SET desired_state='stop' WHERE bot_id='echo_bot'`)

	if err := sup.Start(context.Background(), "echo_bot"); err == nil {
		t.Fatal("desired_state=stop 时不应启动")
	}
}

// 指数退避 1s/2s/4s…封顶 60s；连续达阈值上报 bot_stalled
func TestRecordFailureBackoffAndCircuitBreak(t *testing.T) {
	sup := New(nil, nil, nil, nil, Options{MaxStartFailures: 3})
	var stalled []string
	sup.OnHealth = func(_, _, targetID, failureClass, severity, _ string) {
		if failureClass == "bot_stalled" {
			if severity != "critical" {
				t.Errorf("bot_stalled 严重度应为 critical，实际 %s", severity)
			}
			stalled = append(stalled, targetID)
		}
	}

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for i, w := range want {
		before := time.Now()
		sup.recordFailure("echo_bot", "exec_failed")
		sup.mu.Lock()
		until := sup.backoff["echo_bot"]
		sup.mu.Unlock()
		got := until.Sub(before)
		if got < w-50*time.Millisecond || got > w+time.Second {
			t.Fatalf("第 %d 次失败退避应约 %s，实际 %s", i+1, w, got)
		}
	}
	if len(stalled) != 1 || stalled[0] != "echo_bot" {
		t.Fatalf("第 3 次失败应上报一次 bot_stalled，实际 %v", stalled)
	}

	for i := 0; i < 10; i++ {
		sup.recordFailure("echo_bot", "exec_failed")
	}
	sup.mu.Lock()
	until := sup.backoff["echo_bot"]
	sup.mu.Unlock()
	if d := time.Until(until); d > maxBackoff+time.Second {
		t.Fatalf("退避应封顶 %s，实际 %s", maxBackoff, d)
	}
}

// 退避期内 Start 直接熔断返回，不再尝试 exec
func TestStartHonorsBackoff(t *testing.T) {
	sup, db := newSupervisor(t)
	path, _ := writeArtifact(t, "#!/bin/sh\nexit 0\n")
	db.Exec(t, `UPDATE bot_manifest SET binary_path=$1, binary_digest=repeat('1',64) WHERE bot_id='echo_bot'`, path)

	if err := sup.Start(context.Background(), "echo_bot"); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("首次应因摘要不符失败，实际 %v", err)
	}
	if err := sup.Start(context.Background(), "echo_bot"); !errors.Is(err, ErrCircuitBroken) {
		t.Fatalf("退避期内应熔断，实际 %v", err)
	}
}

// 心跳超时实例标记 crashed，供 reconcile 重新拉起
func TestDetectHeartbeatTimeoutMarksCrashed(t *testing.T) {
	sup, db := newSupervisor(t)
	db.Exec(t, `INSERT INTO bot_instances (bot_id, observed_state, pid, pgid, binary_digest, config_version, started_at, last_heartbeat_at)
	            VALUES ('echo_bot','ready',424242,424242,repeat('0',64),1,now()-interval '10 minutes',now()-interval '10 minutes')`)

	n, err := sup.DetectHeartbeatTimeout(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应判定 1 个实例心跳超时，实际 %d", n)
	}
	var state, lastErr string
	if err := db.QueryRow(t, `SELECT observed_state, last_error FROM bot_instances WHERE pid=424242`).Scan(&state, &lastErr); err != nil {
		t.Fatal(err)
	}
	if state != "crashed" || lastErr != "heartbeat_timeout" {
		t.Fatalf("应标记 crashed/heartbeat_timeout，实际 %s/%s", state, lastErr)
	}
}

// 无待处理任务时 reconcile 不拉起任何进程
func TestReconcileIdleDoesNotStart(t *testing.T) {
	sup, db := newSupervisor(t)
	path, digest := writeArtifact(t, "#!/bin/sh\nexit 0\n")
	db.Exec(t, `UPDATE bot_manifest SET binary_path=$1, binary_digest=$2 WHERE bot_id='echo_bot'`, path, digest)

	started, errs := sup.Reconcile(context.Background())
	if len(errs) != 0 {
		t.Fatalf("空队列调和不应报错: %v", errs)
	}
	if started != 0 {
		t.Fatalf("空队列不应启动 Bot，实际 %d", started)
	}
}
