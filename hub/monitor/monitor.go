package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/hub/supervisor"
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
			_ = m.ScanOnce(ctx)
		}
	}
}

// ScanOnce 执行一轮巡检：Bot 心跳超时、插件进程存活、配置版本漂移。
func (m *Monitor) ScanOnce(ctx context.Context) error {
	_ = m.scanBots(ctx)
	_ = m.scanPluginProcesses(ctx)
	_ = m.scanConfigDrift(ctx)
	return nil
}

func (m *Monitor) report(sourceType, targetType, targetID, failureClass, severity, detail string) {
	if m.OnReport != nil {
		m.OnReport(sourceType, targetType, targetID, failureClass, severity, detail)
	}
	// 记录到 kernel_health_reports 表
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = m.st.Pool().Exec(ctx, `
		INSERT INTO kernel_health_reports (source_type, source_id, target_type, target_id, failure_class, severity, detail)
		VALUES ($1, 'monitor_bot', $2, $3, $4, $5, $6)`,
		sourceType, targetType, targetID, failureClass, severity, detail)
}

// scanBots 巡检 Bot 实例心跳超时 (>30s)
func (m *Monitor) scanBots(ctx context.Context) error {
	rows, err := m.st.Pool().Query(ctx, `
		SELECT instance_id, bot_id, pid
		  FROM bot_instances
		 WHERE stopped_at IS NULL AND observed_state='ready'
		   AND last_heartbeat_at < now() - INTERVAL '30 seconds'`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type stalledBot struct {
		instanceID int64
		botID      string
		pid        int
	}
	var stalled []stalledBot
	for rows.Next() {
		var b stalledBot
		if err := rows.Scan(&b.instanceID, &b.botID, &b.pid); err == nil {
			stalled = append(stalled, b)
		}
	}
	rows.Close()

	for _, b := range stalled {
		m.report("monitor_bot", "bot", b.botID, "bot_stalled", "warning",
			fmt.Sprintf("Bot %s (PID %d) 心跳超时超过 30s", b.botID, b.pid))
	}
	return nil
}

// scanPluginProcesses 巡检 kernel_plugin_processes 表
func (m *Monitor) scanPluginProcesses(ctx context.Context) error {
	rows, err := m.st.Pool().Query(ctx, `
		SELECT plugin_id, pid, pgid, pid_start_time, state, started_at, stop_requested_at
		  FROM kernel_plugin_processes
		 WHERE state IN ('starting','running','stopping')`)
	if err != nil {
		return err
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
	for rows.Next() {
		var it procItem
		if err := rows.Scan(&it.pluginID, &it.pid, &it.pgid, &it.startTime, &it.state, &it.startedAt, &it.stopRequestedAt); err == nil {
			list = append(list, it)
		}
	}
	rows.Close()

	now := time.Now()
	for _, it := range list {
		switch it.state {
		case "running":
			if !osproc.IsProcessAlive(it.pid, it.startTime) {
				m.report("monitor_bot", "plugin_process", it.pluginID, "plugin_process_vanished", "warning",
					fmt.Sprintf("插件进程 %s (PID %d) 已消失，等待 owner bot 处置", it.pluginID, it.pid))
			} else {
				// 存活刷新 last_seen_at
				_, _ = m.st.Pool().Exec(ctx, `
					UPDATE kernel_plugin_processes SET last_seen_at=now() WHERE plugin_id=$1`, it.pluginID)
			}
		case "starting":
			if now.Sub(it.startedAt) > 30*time.Second {
				m.report("monitor_bot", "plugin_process", it.pluginID, "plugin_process_start_failed", "warning",
					fmt.Sprintf("插件进程 %s 启动超时超过 30s", it.pluginID))
			}
		case "stopping":
			if it.stopRequestedAt != nil && now.Sub(*it.stopRequestedAt) > 25*time.Second {
				m.report("monitor_bot", "plugin_process", it.pluginID, "plugin_process_stop_stuck", "warning",
					fmt.Sprintf("插件进程 %s 关闭阻塞超过 25s，建议执行 plugin kill --confirm", it.pluginID))
			}
		}
	}
	return nil
}

// scanConfigDrift 对比实例加载的 config_version 与 active 配置
func (m *Monitor) scanConfigDrift(ctx context.Context) error {
	// Bot 实例配置漂移
	rows, err := m.st.Pool().Query(ctx, `
		SELECT i.bot_id, i.config_version, b.config_version
		  FROM bot_instances i
		  JOIN bots b ON b.bot_id = i.bot_id
		 WHERE i.stopped_at IS NULL AND b.config_version IS NOT NULL
		   AND i.config_version <> b.config_version`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var botID string
			var instVer, activeVer int64
			if err := rows.Scan(&botID, &instVer, &activeVer); err == nil {
				m.report("monitor_bot", "bot", botID, "config_version_drift", "info",
					fmt.Sprintf("Bot %s 配置版本漂移: 运行版本 %d != 期望版本 %d", botID, instVer, activeVer))
			}
		}
	}

	// 插件进程配置漂移
	pRows, err := m.st.Pool().Query(ctx, `
		SELECT p.plugin_id, p.config_version, k.config_version
		  FROM kernel_plugin_processes p
		  JOIN kernel_plugins k ON k.plugin_id = p.plugin_id
		 WHERE p.state='running' AND k.config_version IS NOT NULL
		   AND p.config_version <> k.config_version`)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var pluginID string
			var pVer, activeVer int64
			if err := pRows.Scan(&pluginID, &pVer, &activeVer); err == nil {
				m.report("monitor_bot", "plugin_process", pluginID, "config_version_drift", "info",
					fmt.Sprintf("插件 %s 配置版本漂移: 运行版本 %d != 期望版本 %d", pluginID, pVer, activeVer))
			}
		}
	}
	return nil
}
