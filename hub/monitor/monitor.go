package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/hub/supervisor"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/osproc"
)

type Monitor struct {
	st       *store.Store
	sup      *supervisor.Supervisor
	cfg      *config.Module
	interval time.Duration

	// OnReport 供测试或 hub 监听健康与漂移事件
	OnReport func(sourceType, targetType, targetID, failureClass, severity, detail string)
}

func New(st *store.Store, sup *supervisor.Supervisor, cfg *config.Module, interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Monitor{st: st, sup: sup, cfg: cfg, interval: interval}
}

// Run 启动监督者周期巡检循环。
func (m *Monitor) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := m.ScanOnce(ctx); err != nil {
				log.Printf("monitor: 巡检失败: %v", err)
			}
		}
	}
}

// ScanOnce 执行一轮巡检：Bot 心跳超时、插件进程存活、配置版本漂移。
func (m *Monitor) ScanOnce(ctx context.Context) error {
	// 一项失败不阻断其他巡检，但必须向调用方返回所有错误。
	return errors.Join(m.scanBots(ctx), m.scanPluginProcesses(ctx), m.scanConfigDrift(ctx))
}

func (m *Monitor) report(ctx context.Context, sourceType, targetType, targetID, failureClass, severity, detail string) error {
	// monitor_bot 是此次观测的报告源，主动开启根因果链，而非补造上游历史。
	env := envelope.NewRoot("monitor_bot", envelope.ChannelHealth, envelope.MsgEvent, "")
	env.IdempotencyKey = env.EnvelopeID
	if m.OnReport != nil {
		// 保留历史回调参数（sourceType=monitor_bot）及写库前通知的语义。
		m.OnReport(sourceType, targetType, targetID, failureClass, severity, detail)
	}
	// 表未单列 root/path/trace，将完整信封随结构化证据保存。
	evidence, err := json.Marshal(struct {
		Detail   string             `json:"detail"`
		Envelope *envelope.Envelope `json:"envelope"`
	}{Detail: detail, Envelope: env})
	if err != nil {
		return fmt.Errorf("编码监控报告证据 correlation_id=%s: %w", env.CorrelationID, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = m.st.Pool().Exec(ctx, `
		INSERT INTO kernel_health_reports
			(report_id, source_type, source_id, target_type, target_id, failure_class, severity,
			 evidence_json, observed_at, correlation_id, causation_id, idempotency_key)
		VALUES ($1, 'bot', $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11)`,
		env.EnvelopeID, env.FromBotID, targetType, targetID, failureClass, severity,
		evidence, env.ReceivedAt, env.CorrelationID, env.CausationID, env.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("写入监控报告 target=%s/%s failure_class=%s correlation_id=%s: %w",
			targetType, targetID, failureClass, env.CorrelationID, err)
	}
	return nil
}

// scanBots 巡检 Bot 实例心跳超时 (>30s)
func (m *Monitor) scanBots(ctx context.Context) error {
	rows, err := m.st.Pool().Query(ctx, `
		SELECT instance_id, bot_id, pid
		  FROM bot_instances
		 WHERE stopped_at IS NULL AND observed_state='ready'
		   AND last_heartbeat_at < now() - INTERVAL '30 seconds'`)
	if err != nil {
		return fmt.Errorf("查询 Bot 心跳超时: %w", err)
	}
	defer rows.Close()

	type stalledBot struct {
		instanceID int64
		botID      string
		pid        int
	}
	var stalled []stalledBot
	var scanErr error
	for rows.Next() {
		var b stalledBot
		if err := rows.Scan(&b.instanceID, &b.botID, &b.pid); err != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("读取 Bot 心跳超时记录: %w", err))
			break
		}
		stalled = append(stalled, b)
	}
	if err := rows.Err(); err != nil {
		scanErr = errors.Join(scanErr, fmt.Errorf("遍历 Bot 心跳超时记录: %w", err))
	}
	rows.Close()

	for _, b := range stalled {
		scanErr = errors.Join(scanErr, m.report(ctx, "monitor_bot", "bot", b.botID, "bot_stalled", "warning",
			fmt.Sprintf("Bot %s (PID %d) 心跳超时超过 30s", b.botID, b.pid)))
	}
	return scanErr
}

// scanPluginProcesses 巡检 kernel_plugin_processes 表
func (m *Monitor) scanPluginProcesses(ctx context.Context) error {
	rows, err := m.st.Pool().Query(ctx, `
		SELECT plugin_id, pid, pgid, pid_start_time, state, started_at, stop_requested_at
		  FROM kernel_plugin_processes
		 WHERE state IN ('starting','running','stopping')`)
	if err != nil {
		return fmt.Errorf("查询插件进程: %w", err)
	}
	defer rows.Close()

	type procItem struct {
		pluginID        string
		pid             int
		pgid            int
		startTime       int64
		state           string
		startedAt       time.Time
		stopRequestedAt *time.Time
	}
	var list []procItem
	var scanErr error
	for rows.Next() {
		var it procItem
		if err := rows.Scan(&it.pluginID, &it.pid, &it.pgid, &it.startTime, &it.state, &it.startedAt, &it.stopRequestedAt); err != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("读取插件进程记录: %w", err))
			break
		}
		list = append(list, it)
	}
	if err := rows.Err(); err != nil {
		scanErr = errors.Join(scanErr, fmt.Errorf("遍历插件进程记录: %w", err))
	}
	rows.Close()

	now := time.Now()
	for _, it := range list {
		switch it.state {
		case "running":
			if !osproc.IsProcessAlive(it.pid, it.startTime) {
				scanErr = errors.Join(scanErr, m.report(ctx, "monitor_bot", "plugin_process", it.pluginID, "plugin_process_vanished", "warning",
					fmt.Sprintf("插件进程 %s (PID %d) 已消失，等待 owner bot 处置", it.pluginID, it.pid)))
			} else {
				// 存活刷新 last_seen_at
				if _, err := m.st.Pool().Exec(ctx, `
					UPDATE kernel_plugin_processes SET last_seen_at=now() WHERE plugin_id=$1`, it.pluginID); err != nil {
					scanErr = errors.Join(scanErr, fmt.Errorf("刷新插件进程 %s 存活时间: %w", it.pluginID, err))
				}
			}
		case "starting":
			if now.Sub(it.startedAt) > 30*time.Second {
				scanErr = errors.Join(scanErr, m.report(ctx, "monitor_bot", "plugin_process", it.pluginID, "plugin_process_start_failed", "warning",
					fmt.Sprintf("插件进程 %s 启动超时超过 30s", it.pluginID)))
			}
		case "stopping":
			if it.stopRequestedAt != nil && now.Sub(*it.stopRequestedAt) > 25*time.Second {
				scanErr = errors.Join(scanErr, m.report(ctx, "monitor_bot", "plugin_process", it.pluginID, "plugin_process_stop_stuck", "warning",
					fmt.Sprintf("插件进程 %s 关闭阻塞超过 25s，建议执行 plugin kill --confirm", it.pluginID)))
			}
		}
	}
	return scanErr
}

// scanConfigDrift 对比实例加载的 config_version 与 active 配置
func (m *Monitor) scanConfigDrift(ctx context.Context) error {
	var scanErr error
	for _, check := range []struct {
		targetType string
		label      string
		query      string
	}{
		{"bot", "Bot", `
			SELECT i.bot_id, i.config_version, b.config_version
			  FROM bot_instances i
			  JOIN bots b ON b.bot_id = i.bot_id
			 WHERE i.stopped_at IS NULL AND b.config_version IS NOT NULL
			   AND i.config_version <> b.config_version`},
		{"plugin_process", "插件", `
			SELECT p.plugin_id, p.config_version, k.config_version
			  FROM kernel_plugin_processes p
			  JOIN kernel_plugins k ON k.plugin_id = p.plugin_id
			 WHERE p.state='running' AND k.config_version IS NOT NULL
			   AND p.config_version <> k.config_version`},
	} {
		rows, err := m.st.Pool().Query(ctx, check.query)
		if err != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("查询%s配置漂移: %w", check.label, err))
			continue
		}
		type drift struct {
			targetID string
			current  int64
			active   int64
		}
		var list []drift
		for rows.Next() {
			var it drift
			if err := rows.Scan(&it.targetID, &it.current, &it.active); err != nil {
				scanErr = errors.Join(scanErr, fmt.Errorf("读取%s配置漂移记录: %w", check.label, err))
				break
			}
			list = append(list, it)
		}
		if err := rows.Err(); err != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("遍历%s配置漂移记录: %w", check.label, err))
		}
		// 先释放读取连接再写报告，避免连接池耗尽时自锁。
		rows.Close()
		for _, it := range list {
			scanErr = errors.Join(scanErr, m.report(ctx, "monitor_bot", check.targetType, it.targetID, "config_version_drift", "info",
				fmt.Sprintf("%s %s 配置版本漂移: 运行版本 %d != 期望版本 %d", check.label, it.targetID, it.current, it.active)))
		}
	}
	return scanErr
}
