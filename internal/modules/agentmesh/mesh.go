package agentmesh

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	HeartbeatInterval = 15 * time.Second
	HeartbeatTimeout  = 30 * time.Second // 2 个心跳周期 (30s) 未响应判定离线
)

// MeshRegistry 探针资产、健康心跳状态机与原始日志索引注册表。
type MeshRegistry struct {
	mu        sync.RWMutex
	probes    map[string]*ProbeInfo
	rawLogs   map[string][]byte
	drivers   map[string]CLIDriverManifest
}

// NewMeshRegistry 创建探针注册表。
func NewMeshRegistry() *MeshRegistry {
	return &MeshRegistry{
		probes:  make(map[string]*ProbeInfo),
		rawLogs: make(map[string][]byte),
		drivers: make(map[string]CLIDriverManifest),
	}
}

// RegisterProbe 注册或更新探针元数据。
func (r *MeshRegistry) RegisterProbe(info *ProbeInfo) error {
	if info == nil || strings.TrimSpace(info.AgentID) == "" {
		return ErrInvalidAgentID
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	agentID := strings.TrimSpace(info.AgentID)
	caps := make([]string, len(info.Capabilities))
	copy(caps, info.Capabilities)

	now := time.Now().Unix()
	lastHB := info.LastHeartbeat
	if lastHB <= 0 {
		lastHB = now
	}

	status := info.Status
	if status == "" {
		status = ProbeStatusOnline
	}

	r.probes[agentID] = &ProbeInfo{
		AgentID:       agentID,
		DeviceName:    info.DeviceName,
		RemoteAddr:    info.RemoteAddr,
		Capabilities:  caps,
		Status:        status,
		LastHeartbeat: lastHB,
	}

	return nil
}

// RecordHeartbeat 更新指定探针的心跳时间戳并探活。
func (r *MeshRegistry) RecordHeartbeat(agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, exists := r.probes[agentID]
	if !exists {
		return ErrPeerUnavailable
	}

	p.LastHeartbeat = time.Now().Unix()
	if p.Status == ProbeStatusOffline {
		p.Status = ProbeStatusOnline
	}
	return nil
}

// CheckHeartbeats 检查并标记超时探针为 offline (超过 30s 未收到心跳)。
func (r *MeshRegistry) CheckHeartbeats(now time.Time) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var offlineAgents []string
	currentUnix := now.Unix()

	for agentID, p := range r.probes {
		if p.Status != ProbeStatusOffline && (currentUnix-p.LastHeartbeat) > int64(HeartbeatTimeout.Seconds()) {
			p.Status = ProbeStatusOffline
			offlineAgents = append(offlineAgents, agentID)
		}
	}

	return offlineAgents
}

// GetProbe 获取指定探针信息。
func (r *MeshRegistry) GetProbe(agentID string) (*ProbeInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, exists := r.probes[agentID]
	if !exists {
		return nil, ErrPeerUnavailable
	}

	// 动态检测心跳超时
	if p.Status != ProbeStatusOffline && (time.Now().Unix()-p.LastHeartbeat) > int64(HeartbeatTimeout.Seconds()) {
		// 返回离线副本
		cp := *p
		cp.Status = ProbeStatusOffline
		return &cp, nil
	}

	cp := *p
	return &cp, nil
}

// ListProbes 按照过滤条件列出探针。
func (r *MeshRegistry) ListProbes(filter ProbeFilter) []*ProbeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*ProbeInfo
	nowUnix := time.Now().Unix()

	for _, p := range r.probes {
		// 动态检测心跳状态
		status := p.Status
		if status != ProbeStatusOffline && (nowUnix-p.LastHeartbeat) > int64(HeartbeatTimeout.Seconds()) {
			status = ProbeStatusOffline
		}

		if filter.AgentID != "" && p.AgentID != filter.AgentID {
			continue
		}
		if filter.Status != "" && status != filter.Status {
			continue
		}
		if filter.Capability != "" {
			matched := false
			for _, capItem := range p.Capabilities {
				if capItem == filter.Capability {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		cp := *p
		cp.Status = status
		result = append(result, &cp)
	}

	return result
}

// RegisterCLIDriver 为指定探针登记 CLI 驱动。
func (r *MeshRegistry) RegisterCLIDriver(agentID string, manifest CLIDriverManifest) error {
	if strings.TrimSpace(agentID) == "" {
		return ErrInvalidAgentID
	}
	if strings.TrimSpace(manifest.DriverName) == "" || strings.TrimSpace(manifest.BinPath) == "" {
		return fmt.Errorf("驱动名称与路径不能为空")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.drivers[agentID] = manifest
	return nil
}

// GetCLIDriver 获取指定探针的驱动清单。
func (r *MeshRegistry) GetCLIDriver(agentID string) (CLIDriverManifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.drivers[agentID]
	return d, ok
}

// StoreRawLog 保存原始日志流索引。
func (r *MeshRegistry) StoreRawLog(ref string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rawLogs[ref] = data
}

// GetRawLog 获取原始日志。
func (r *MeshRegistry) GetRawLog(ref string) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	data, ok := r.rawLogs[ref]
	return data, ok
}
