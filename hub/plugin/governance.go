package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
)

// ProviderEntry 记录在全局索引中的接口提供者条目
type ProviderEntry struct {
	PluginID  string              `json:"plugin_id"`
	Version   string              `json:"version"`
	Interface InterfaceDefinition `json:"interface"`
	Active    bool                `json:"active"`
}

// InterfaceProviderIndex 内存全局接口提供者索引表
type InterfaceProviderIndex struct {
	mu        sync.RWMutex
	providers map[string][]ProviderEntry // interface_id -> []ProviderEntry
}

// NewInterfaceProviderIndex 初始化提供者索引表
func NewInterfaceProviderIndex() *InterfaceProviderIndex {
	return &InterfaceProviderIndex{
		providers: make(map[string][]ProviderEntry),
	}
}

// RegisterDoc 注册或更新插件接口规范文档到全局索引表
func (idx *InterfaceProviderIndex) RegisterDoc(doc *PluginInterfacesDoc) error {
	if err := ValidateInterfacesDoc(doc); err != nil {
		return err
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	for _, iface := range doc.ProvidesInterfaces {
		list := idx.providers[iface.InterfaceID]
		found := false
		for i, entry := range list {
			if entry.PluginID == doc.PluginID {
				list[i] = ProviderEntry{
					PluginID:  doc.PluginID,
					Version:   doc.PluginVersion,
					Interface: iface,
					Active:    true,
				}
				found = true
				break
			}
		}
		if !found {
			list = append(list, ProviderEntry{
				PluginID:  doc.PluginID,
				Version:   doc.PluginVersion,
				Interface: iface,
				Active:    true,
			})
		}
		idx.providers[iface.InterfaceID] = list
	}
	return nil
}

// UnregisterPlugin 注销指定插件的所有接口提供
func (idx *InterfaceProviderIndex) UnregisterPlugin(pluginID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	for ifaceID, list := range idx.providers {
		var filtered []ProviderEntry
		for _, entry := range list {
			if entry.PluginID != pluginID {
				filtered = append(filtered, entry)
			}
		}
		if len(filtered) == 0 {
			delete(idx.providers, ifaceID)
		} else {
			idx.providers[ifaceID] = filtered
		}
	}
}

// Resolve 根据接口名和 SemVer 范围匹配最佳提供者 (优先选取满足范围的最高版本)
func (idx *InterfaceProviderIndex) Resolve(interfaceID string, versionRange string) (*ProviderEntry, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	list, exists := idx.providers[interfaceID]
	if !exists || len(list) == 0 {
		return nil, fmt.Errorf("%w: 未找到接口 %q 的提供者", ErrUnmetDependency, interfaceID)
	}

	var candidates []ProviderEntry
	for _, entry := range list {
		if !entry.Active {
			continue
		}
		if versionRange == "" || versionRange == "*" {
			candidates = append(candidates, entry)
			continue
		}
		matched, err := MatchSemVer(entry.Version, versionRange)
		if err == nil && matched {
			candidates = append(candidates, entry)
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: 接口 %q 存在提供者但均不满足版本范围 %q", ErrIncompatibleVersion, interfaceID, versionRange)
	}

	// 排序选取最高版本提供者
	sort.Slice(candidates, func(i, j int) bool {
		cmp, err := CompareVersions(candidates[i].Version, candidates[j].Version)
		if err != nil {
			return false
		}
		return cmp > 0
	})

	best := candidates[0]
	return &best, nil
}

// GetProviderMap 投影生成 interface_id -> plugin_id 映射字典 (供 BuildDAG 直接使用)
func (idx *InterfaceProviderIndex) GetProviderMap() map[string]string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	res := make(map[string]string)
	for ifaceID, list := range idx.providers {
		var activeList []ProviderEntry
		for _, e := range list {
			if e.Active {
				activeList = append(activeList, e)
			}
		}
		if len(activeList) == 0 {
			continue
		}
		// 选最高版本
		sort.Slice(activeList, func(i, j int) bool {
			cmp, err := CompareVersions(activeList[i].Version, activeList[j].Version)
			if err != nil {
				return false
			}
			return cmp > 0
		})
		res[ifaceID] = activeList[0].PluginID
	}
	return res
}

// CheckUninstallReverseDependencies 校验卸载时的反向依赖关系
// 若存在活跃的下游依赖且 cascade == false，阻断并返回 ErrReverseDependencyBlocked
// 若 cascade == true，计算逆拓扑排序下线序列 (业务插件先停，基础插件后停)
func CheckUninstallReverseDependencies(dag *DependencyDAG, pluginID string, cascade bool) ([]string, error) {
	if dag == nil {
		return nil, ErrPluginNotFound
	}
	if _, exists := dag.Nodes[pluginID]; !exists {
		return nil, fmt.Errorf("%w: %q", ErrPluginNotFound, pluginID)
	}

	// 查找所有依赖该 pluginID 的下游插件 (包括直接依赖与间接依赖)
	dependents := findTransitiveDependents(dag, pluginID)

	if len(dependents) > 0 && !cascade {
		return nil, fmt.Errorf("%w: 仍有 %d 个业务插件依赖该模块: %s",
			ErrReverseDependencyBlocked, len(dependents), strings.Join(dependents, ", "))
	}

	if len(dependents) == 0 {
		return []string{pluginID}, nil
	}

	// 当 cascade == true:
	// 构建仅包含 pluginID 和其下游 dependents 的子图，对其进行逆拓扑排序
	subNodes := make(map[string]*DAGNode)
	subNodes[pluginID] = dag.Nodes[pluginID]
	for _, depID := range dependents {
		subNodes[depID] = dag.Nodes[depID]
	}

	subDAG := &DependencyDAG{
		Nodes:        subNodes,
		Adjacency:    make(map[string][]string),
		Dependents:   make(map[string][]string),
		Dependencies: make(map[string][]string),
	}

	for id := range subNodes {
		for _, dep := range dag.Dependencies[id] {
			if _, inSub := subNodes[dep]; inSub {
				subDAG.Dependencies[id] = append(subDAG.Dependencies[id], dep)
				subDAG.Dependents[dep] = append(subDAG.Dependents[dep], id)
				subDAG.Adjacency[dep] = append(subDAG.Adjacency[dep], id)
			}
		}
	}

	// 拓扑排序 (启动序列): provider -> consumer
	order, err := TopologicalSort(subDAG)
	if err != nil {
		return nil, err
	}

	// 逆拓扑序列 (停机序列): consumer -> provider
	reverseOrder := make([]string, len(order))
	for i, id := range order {
		reverseOrder[len(order)-1-i] = id
	}

	return reverseOrder, nil
}

func findTransitiveDependents(dag *DependencyDAG, pluginID string) []string {
	visited := make(map[string]bool)
	var queue []string

	for _, dep := range dag.Dependents[pluginID] {
		if !visited[dep] {
			visited[dep] = true
			queue = append(queue, dep)
		}
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		for _, dep := range dag.Dependents[curr] {
			if !visited[dep] {
				visited[dep] = true
				queue = append(queue, dep)
			}
		}
	}

	var res []string
	for dep := range visited {
		if dep != pluginID {
			res = append(res, dep)
		}
	}
	sort.Strings(res)
	return res
}

// UpgradeCheckResult 升级兼容性预审结果
type UpgradeCheckResult struct {
	Compatible       bool               `json:"compatible"`
	UpgradeType      string             `json:"upgrade_type"` // "patch" | "minor" | "major"
	IncompatibleDeps []IncompatibleDep  `json:"incompatible_deps,omitempty"`
	Reason           string             `json:"reason,omitempty"`
}

// IncompatibleDep 记录不兼容的下游依赖项
type IncompatibleDep struct {
	DependentPlugin string `json:"dependent_plugin"`
	Interface       string `json:"interface"`
	RequiredRange   string `json:"required_range"`
	NewVersion      string `json:"new_version"`
}

// CheckUpgradeCompatibility 检查插件升级对下游依赖的兼容性
func CheckUpgradeCompatibility(oldVersionStr, newVersionStr string, interfaceID string, dependents map[string][]DependencySpec) (*UpgradeCheckResult, error) {
	oldVer, err := ParseVersion(oldVersionStr)
	if err != nil {
		return nil, fmt.Errorf("解析旧版本失败: %w", err)
	}
	newVer, err := ParseVersion(newVersionStr)
	if err != nil {
		return nil, fmt.Errorf("解析新版本失败: %w", err)
	}

	var upgradeType string
	if newVer.Major > oldVer.Major {
		upgradeType = "major"
	} else if newVer.Minor > oldVer.Minor {
		upgradeType = "minor"
	} else {
		upgradeType = "patch"
	}

	var incompatible []IncompatibleDep
	for depPID, specs := range dependents {
		for _, spec := range specs {
			if spec.Interface == interfaceID && spec.VersionRange != "" {
				matched, err := MatchSemVer(newVersionStr, spec.VersionRange)
				if err != nil || !matched {
					incompatible = append(incompatible, IncompatibleDep{
						DependentPlugin: depPID,
						Interface:       interfaceID,
						RequiredRange:   spec.VersionRange,
						NewVersion:      newVersionStr,
					})
				}
			}
		}
	}

	if len(incompatible) > 0 {
		return &UpgradeCheckResult{
			Compatible:       false,
			UpgradeType:      upgradeType,
			IncompatibleDeps: incompatible,
			Reason:           fmt.Sprintf("发现 %d 个下游依赖不兼容新版本 %s", len(incompatible), newVersionStr),
		}, nil
	}

	return &UpgradeCheckResult{
		Compatible:  true,
		UpgradeType: upgradeType,
		Reason:      fmt.Sprintf("%s 升级兼容所有下游依赖", upgradeType),
	}, nil
}

// RollingUpdatePlan 蓝绿热更新执行计划契约
type RollingUpdatePlan struct {
	PluginID      string        `json:"plugin_id"`
	OldVersion    string        `json:"old_version"`
	NewVersion    string        `json:"new_version"`
	UpgradeType   string        `json:"upgrade_type"`
	DrainPeriod   time.Duration `json:"drain_period"`   // 旧进程排空窗口 (默认 5s)
	HealthRetries int           `json:"health_retries"` // 探活重试次数 (默认 3 次)
	Steps         []string      `json:"steps"`
}

// PlanRollingUpdate 生成平滑热更新契约计划
func PlanRollingUpdate(oldVer, newVer string, pluginID string, interfaceID string, dependents map[string][]DependencySpec) (*RollingUpdatePlan, error) {
	check, err := CheckUpgradeCompatibility(oldVer, newVer, interfaceID, dependents)
	if err != nil {
		return nil, err
	}
	if !check.Compatible {
		return nil, fmt.Errorf("%w: %s", ErrIncompatibleVersion, check.Reason)
	}

	steps := []string{
		"1. 拉起新版本 L2 二进制进程 (独立临时端口/UnixSocket)",
		"2. 对新进程进行连续 3 次探活检查 (/health 返回 200 OK)",
		"3. 原子更新全局路由与总线消息转发，新流量切换至新进程",
		"4. 向旧进程发送 SIGTERM 优雅停机信号，进入 5s 连接排空期",
		"5. 确认旧进程安全退出，回收登记信息，完成热更新",
	}

	return &RollingUpdatePlan{
		PluginID:      pluginID,
		OldVersion:    oldVer,
		NewVersion:    newVer,
		UpgradeType:   check.UpgradeType,
		DrainPeriod:   5 * time.Second,
		HealthRetries: 3,
		Steps:         steps,
	}, nil
}

// RegisterPluginInterfacesConfig 向配置中心登记插件接口文档 (scope_type='plugin', config_key='interfaces')
func RegisterPluginInterfacesConfig(ctx context.Context, cfg *config.Module, actor config.Actor, doc *PluginInterfacesDoc) (string, error) {
	if err := ValidateInterfacesDoc(doc); err != nil {
		return "", err
	}
	raw, err := SerializeInterfacesDoc(doc)
	if err != nil {
		return "", err
	}

	if cfg == nil {
		// Mock 或纯内存环境: 计算 SHA256 校验和并返回
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:]), nil
	}

	// 真实配置中心环境: 提交提案
	return cfg.Propose(ctx, actor, config.ScopePlugin, doc.PluginID, "interfaces", json.RawMessage(raw), 0)
}
