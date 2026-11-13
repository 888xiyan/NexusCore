package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/osproc"
	"github.com/888xiyan/NexusCore/internal/pgtest"
)

func newMonitor(t *testing.T) (*Monitor, *pgtest.DB) {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.Open(context.Background(), db.KernelDSN)
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(st.Close)
	return New(st, nil, nil, time.Millisecond), db
}

// 所有种子仅写 pgtest 的 nexus_test_monitor，不操作开发库。
func seedReports(t *testing.T, db *pgtest.DB) {
	t.Helper()
	db.Exec(t, `INSERT INTO bot_instances
		(bot_id, observed_state, pid, binary_digest, config_version, last_heartbeat_at)
		VALUES ('echo_bot', 'ready', 12345, repeat('0',64), 0, now() - INTERVAL '1 minute')`)
	for _, state := range []string{"running", "starting", "stopping"} {
		id := "plugin_" + state
		db.Exec(t, `INSERT INTO kernel_plugins
			(plugin_id, plugin_type, display_name, namespace, manifest_digest, manifest_json, config_version)
			VALUES ($1, 'executor', $1, $2, repeat('0',64), '{}', 1)`, id, state)
		db.Exec(t, `INSERT INTO kernel_plugin_processes
			(process_id, plugin_id, owner_bot_id, owner_instance_id, pid, pgid, pid_start_time,
			 binary_digest, config_version, state, started_at, stop_requested_at)
			VALUES ($1, $1, 'echo_bot', 'test-instance', 0, 0, 0, repeat('0',64), 0, $2,
			 now() - INTERVAL '1 minute', now() - INTERVAL '1 minute')`, id, state)
	}
}

func TestScanOncePersistsReports(t *testing.T) {
	m, db := newMonitor(t)
	seedReports(t, db)
	type reportKey struct{ targetType, targetID, failureClass, severity string }
	want := map[reportKey]int{
		{"bot", "echo_bot", "bot_stalled", "warning"}:                                   2,
		{"bot", "echo_bot", "config_version_drift", "info"}:                             2,
		{"plugin_process", "plugin_running", "plugin_process_vanished", "warning"}:      2,
		{"plugin_process", "plugin_running", "config_version_drift", "info"}:            2,
		{"plugin_process", "plugin_starting", "plugin_process_start_failed", "warning"}: 2,
		{"plugin_process", "plugin_stopping", "plugin_process_stop_stuck", "warning"}:   2,
	}
	callbacks := make(map[reportKey]string)
	callbackCount := 0
	m.OnReport = func(sourceType, targetType, targetID, failureClass, severity, detail string) {
		if sourceType != "monitor_bot" {
			t.Errorf("历史回调来源被改变: %q", sourceType)
		}
		callbacks[reportKey{targetType, targetID, failureClass, severity}] = detail
		callbackCount++
	}
	before := time.Now().UTC().Truncate(time.Microsecond)
	// 每次新观测拥有独立报告 ID 和幂等键，不发生主键冲突或吞掉重复观测。
	for range 2 {
		if err := m.ScanOnce(context.Background()); err != nil {
			t.Fatalf("巡检: %v", err)
		}
	}
	after := time.Now().UTC()
	rows, err := db.Kernel.Query(context.Background(), `SELECT report_id, source_type, source_id,
		target_type, target_id, failure_class, severity, evidence_json, observed_at,
		correlation_id, causation_id, idempotency_key FROM kernel_health_reports`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := make(map[string]bool)
	for rows.Next() {
		var id, sourceType, sourceID, correlation, idem string
		var key reportKey
		var raw []byte
		var observed time.Time
		var causation *string
		if err := rows.Scan(&id, &sourceType, &sourceID, &key.targetType, &key.targetID,
			&key.failureClass, &key.severity, &raw, &observed, &correlation, &causation, &idem); err != nil {
			t.Fatal(err)
		}
		if sourceType != "bot" || sourceID != "monitor_bot" {
			t.Errorf("非法报告来源: %s/%s", sourceType, sourceID)
		}
		if id == "" || correlation != id || idem != id || seen[id] || causation != nil {
			t.Errorf("根报告标识/因果字段错误: id=%q correlation=%q idem=%q causation=%v", id, correlation, idem, causation)
		}
		seen[id] = true
		if observed.IsZero() || observed.Before(before) || observed.After(after) {
			t.Errorf("观测时间不在扫描窗口内: %v", observed)
		}
		var evidence struct {
			Detail   string            `json:"detail"`
			Envelope envelope.Envelope `json:"envelope"`
		}
		if err := json.Unmarshal(raw, &evidence); err != nil {
			t.Fatalf("解析结构化证据: %v", err)
		}
		if evidence.Detail == "" || evidence.Detail != callbacks[key] {
			t.Errorf("落库证据与回调不一致: %+v %q", key, evidence.Detail)
		}
		env := evidence.Envelope
		if err := env.Validate(); err != nil {
			t.Errorf("报告源未生成完整根信封: %v", err)
		}
		if env.EnvelopeID != id || env.CorrelationID != correlation || env.IdempotencyKey != idem ||
			env.FromBotID != sourceID || env.Channel != envelope.ChannelHealth || env.MsgType != envelope.MsgEvent ||
			!env.ReceivedAt.Truncate(time.Microsecond).Equal(observed) {
			t.Errorf("信封与报告不一致: %+v", env)
		}
		want[key]--
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 12 || callbackCount != 12 {
		t.Errorf("应落库并回调 12 份报告，落库=%d 回调=%d", len(seen), callbackCount)
	}
	for key, remaining := range want {
		if remaining != 0 {
			t.Errorf("报告数量不符: %+v 差额=%d", key, remaining)
		}
	}
}

func readOnlyMonitor(t *testing.T, db *pgtest.DB) *Monitor {
	t.Helper()
	u, err := url.Parse(db.KernelDSN)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	u.RawQuery = q.Encode()
	st, err := store.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return New(st, nil, nil, time.Millisecond)
}

func TestScanOncePropagatesReportFailures(t *testing.T) {
	_, db := newMonitor(t)
	seedReports(t, db)
	// 仅该连接只读，真实 PG 拒绝 INSERT，不修改任何角色权限或表结构。
	m := readOnlyMonitor(t, db)
	callbacks := 0
	m.OnReport = func(_, _, _, _, _, _ string) { callbacks++ }
	err := m.ScanOnce(context.Background())
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("应传播 PG 只读写入错误: %v", err)
	}
	if callbacks != 6 || strings.Count(err.Error(), "写入监控报告") != 6 ||
		strings.Count(err.Error(), "correlation_id=env_") != 6 {
		t.Fatalf("报告失败不可阻断其他扫描，且错误应含因果标识: callbacks=%d err=%v", callbacks, err)
	}
	var count int
	if err := db.Kernel.QueryRow(context.Background(), `SELECT count(*) FROM kernel_health_reports`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("写入失败不应落库: %d", count)
	}
}

func TestScanOncePropagatesRefreshFailure(t *testing.T) {
	_, db := newMonitor(t)
	seedReports(t, db)
	info, err := osproc.ReadProcessInfo(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(t, `UPDATE kernel_plugin_processes SET pid=$1, pgid=$2, pid_start_time=$3
		WHERE plugin_id='plugin_running'`, info.PID, info.PGRP, info.StartTime)
	m := readOnlyMonitor(t, db)
	err = m.ScanOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "刷新插件进程 plugin_running 存活时间") {
		t.Fatalf("应传播存活刷新失败: %v", err)
	}
}

func TestScanOncePropagatesQueryFailures(t *testing.T) {
	m, _ := newMonitor(t)
	m.st.Close()
	err := m.ScanOnce(context.Background())
	if err == nil {
		t.Fatal("连接池已关闭，巡检不应成功")
	}
	for _, label := range []string{"查询 Bot 心跳超时", "查询插件进程", "查询Bot配置漂移", "查询插件配置漂移"} {
		if !strings.Contains(err.Error(), label) {
			t.Errorf("未聚合扫描错误 %q: %v", label, err)
		}
	}
}

func TestScanOncePropagatesRowScanFailure(t *testing.T) {
	m, db := newMonitor(t)
	seedReports(t, db)
	db.Exec(t, `UPDATE bot_instances SET pid=NULL WHERE bot_id='echo_bot'`)
	err := m.ScanOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "读取 Bot 心跳超时记录") {
		t.Fatalf("应传播行解码失败: %v", err)
	}
	var count int
	if err := db.Kernel.QueryRow(context.Background(), `SELECT count(*) FROM kernel_health_reports`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("行解码失败不应阻断其他巡检，报告数=%d", count)
	}
}

func TestReportStructuredDetailAndCancellation(t *testing.T) {
	m, db := newMonitor(t)
	detail := "包含\"引号\"、换行\n和反斜杠\\的证据"
	if err := m.report(context.Background(), "monitor_bot", "bot", "echo_bot", "bot_stalled", "warning", detail); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.Kernel.QueryRow(context.Background(), `SELECT evidence_json->>'detail' FROM kernel_health_reports`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != detail {
		t.Fatalf("证据未完整保留: %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.report(ctx, "monitor_bot", "bot", "echo_bot", "bot_stalled", "warning", detail); !errors.Is(err, context.Canceled) {
		t.Fatalf("写入应遵从扫描 context 并传播取消: %v", err)
	}
	var count int
	if err := db.Kernel.QueryRow(context.Background(), `SELECT count(*) FROM kernel_health_reports`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("已取消报告不应落库: %d", count)
	}
}

type cancelLogWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelLogWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.cancel()
	return n, err
}

func TestRunLogsScanFailure(t *testing.T) {
	m, _ := newMonitor(t)
	m.st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output := &cancelLogWriter{cancel: cancel}
	oldOutput := log.Writer()
	log.SetOutput(output)
	defer log.SetOutput(oldOutput)
	if err := m.Run(ctx); err != nil {
		t.Fatalf("巡检循环取消: %v", err)
	}
	if !strings.Contains(output.String(), "monitor: 巡检失败") || !strings.Contains(output.String(), "查询 Bot 心跳超时") {
		t.Fatalf("周期巡检吞掉错误日志: %q", output.String())
	}
}
