package plugin

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrUnmetDependency           = errors.New("未满足的必选插件接口依赖")
	ErrCircularDependency        = errors.New("检测到循环依赖环路")
	ErrExceedsMaxDependencyDepth = errors.New("依赖层级超过最大限制 (MaxDepth <= 2)")
	ErrReverseDependencyBlocked  = errors.New("存在下游依赖插件，禁止卸载")
	ErrIncompatibleVersion       = errors.New("插件版本与依赖范围不兼容")
)

// DAGNode 依赖图中的插件节点
type DAGNode struct {
	PluginID     string
	Manifest     *Manifest
	Dependencies []DependencySpec
}

// DependencyDAG 插件依赖有向无环图
type DependencyDAG struct {
	Nodes        map[string]*DAGNode // 插件 ID -> 节点
	Adjacency    map[string][]string // 提供者 -> 消费者 (用于 Kahn 拓扑排序: 入度为 0 的基础插件先启动)
	Dependents   map[string][]string // 提供者 -> 依赖它的消费者清单
	Dependencies map[string][]string // 消费者 -> 它所依赖的提供者清单
}

// NewDependencyDAG 初始化依赖图
func NewDependencyDAG() *DependencyDAG {
	return &DependencyDAG{
		Nodes:        make(map[string]*DAGNode),
		Adjacency:    make(map[string][]string),
		Dependents:   make(map[string][]string),
		Dependencies: make(map[string][]string),
	}
}

// BuildDAG 构建插件依赖 DAG 图
// plugins: 当前系统内安装或拟安装的插件清单集合
// providerIndex: 接口提供者映射 (interfaceID -> providerPluginID)
func BuildDAG(plugins map[string]*Manifest, providerIndex map[string]string) (*DependencyDAG, error) {
	dag := NewDependencyDAG()

	// 1. 初始化所有节点
	for pID, m := range plugins {
		if m == nil {
			continue
		}
		id := m.PluginID
		if id == "" {
			id = pID
		}
		dag.Nodes[id] = &DAGNode{
			PluginID:     id,
			Manifest:     m,
			Dependencies: m.Dependencies,
		}
		dag.Adjacency[id] = nil
		dag.Dependents[id] = nil
		dag.Dependencies[id] = nil
	}

	// 2. 解析依赖边
	for pID, node := range dag.Nodes {
		m := node.Manifest
		addedDeps := make(map[string]bool)

		// A) 处理 Manifest v2 的 Dependencies (面向接口契约)
		for _, dep := range m.Dependencies {
			if err := ValidateDependencySpec(&dep); err != nil {
				return nil, err
			}

			providerID := ""
			if providerIndex != nil {
				providerID = providerIndex[dep.Interface]
			}

			if providerID == "" {
				if dep.Optional {
					continue // 可选依赖若无提供者则忽略
				}
				return nil, fmt.Errorf("%w: 插件 %q 必选接口 %q 未找到提供者", ErrUnmetDependency, pID, dep.Interface)
			}

			// 自依赖 (即循环依赖)
			if providerID == pID {
				dag.Dependencies[pID] = append(dag.Dependencies[pID], pID)
				dag.Dependents[pID] = append(dag.Dependents[pID], pID)
				dag.Adjacency[pID] = append(dag.Adjacency[pID], pID)
				continue
			}

			// 检查提供者插件是否存在于待分析集群中
			providerPlugin, exists := plugins[providerID]
			if !exists {
				if dep.Optional {
					continue
				}
				return nil, fmt.Errorf("%w: 插件 %q 依赖的接口 %q 提供者 %q 未就绪", ErrUnmetDependency, pID, dep.Interface, providerID)
			}

			// 检查 SemVer 版本范围兼容性
			if dep.VersionRange != "" && providerPlugin.Version != "" {
				matched, err := MatchSemVer(providerPlugin.Version, dep.VersionRange)
				if err != nil || !matched {
					return nil, fmt.Errorf("%w: 插件 %q (版本 %q) 不满足插件 %q 声明的接口 %q 版本范围 %q",
						ErrIncompatibleVersion, providerID, providerPlugin.Version, pID, dep.Interface, dep.VersionRange)
				}
			}

			if !addedDeps[providerID] {
				addedDeps[providerID] = true
				dag.Dependencies[pID] = append(dag.Dependencies[pID], providerID)
				dag.Dependents[providerID] = append(dag.Dependents[providerID], pID)
				dag.Adjacency[providerID] = append(dag.Adjacency[providerID], pID)
			}
		}

		// B) 处理 Manifest v1 的 Deps (直接依赖插件 ID，兼容历史版本)
		for _, depPID := range m.Deps {
			depPID = strings.TrimSpace(depPID)
			if depPID == "" {
				continue
			}

			if depPID == pID {
				dag.Dependencies[pID] = append(dag.Dependencies[pID], pID)
				dag.Dependents[pID] = append(dag.Dependents[pID], pID)
				dag.Adjacency[pID] = append(dag.Adjacency[pID], pID)
				continue
			}

			if _, exists := plugins[depPID]; !exists {
				return nil, fmt.Errorf("%w: 插件 %q 依赖插件 %q，但未安装该插件", ErrUnmetDependency, pID, depPID)
			}

			if !addedDeps[depPID] {
				addedDeps[depPID] = true
				dag.Dependencies[pID] = append(dag.Dependencies[pID], depPID)
				dag.Dependents[depPID] = append(dag.Dependents[depPID], pID)
				dag.Adjacency[depPID] = append(dag.Adjacency[depPID], pID)
			}
		}
	}

	return dag, nil
}

// DetectCycle 采用三色标记法 (White-Gray-Black DFS) 进行环路检测
// 返回: 是否存在环路，以及具体的环路路径 (如 ["plugin_a", "plugin_b", "plugin_a"])
func DetectCycle(dag *DependencyDAG) (bool, []string) {
	if dag == nil || len(dag.Nodes) == 0 {
		return false, nil
	}

	const (
		white = 0 // 未访问
		gray  = 1 // 访问中 (递归调用栈中)
		black = 2 // 已完全访问
	)

	colors := make(map[string]int)
	var stack []string

	// 保持确定性遍历顺序
	var allNodes []string
	for pID := range dag.Nodes {
		allNodes = append(allNodes, pID)
	}
	sort.Strings(allNodes)

	var dfs func(u string) (bool, []string)
	dfs = func(u string) (bool, []string) {
		colors[u] = gray
		stack = append(stack, u)

		// 排序依赖节点保证输出路径唯一确定
		deps := make([]string, len(dag.Dependencies[u]))
		copy(deps, dag.Dependencies[u])
		sort.Strings(deps)

		for _, v := range deps {
			if colors[v] == gray {
				// 发现灰色节点: 命中环路!
				idx := -1
				for i, n := range stack {
					if n == v {
						idx = i
						break
					}
				}
				var cycle []string
				if idx >= 0 {
					cycle = append([]string{}, stack[idx:]...)
				} else {
					cycle = append([]string{}, stack...)
				}
				cycle = append(cycle, v)
				return true, cycle
			}
			if colors[v] == white {
				if hasCycle, path := dfs(v); hasCycle {
					return true, path
				}
			}
		}

		colors[u] = black
		stack = stack[:len(stack)-1]
		return false, nil
	}

	for _, node := range allNodes {
		if colors[node] == white {
			if hasCycle, path := dfs(node); hasCycle {
				return true, path
			}
		}
	}

	return false, nil
}

// TopologicalSort 使用 Kahn 算法计算插件拓扑加载顺序
// 规则: 入度为 0 的底层基础插件先启动，基础服务就绪后再启动依赖它的上层业务插件
func TopologicalSort(dag *DependencyDAG) ([]string, error) {
	if dag == nil || len(dag.Nodes) == 0 {
		return nil, nil
	}

	// 先行检查环路
	if hasCycle, cyclePath := DetectCycle(dag); hasCycle {
		return nil, fmt.Errorf("%w: 发现循环依赖 %s", ErrCircularDependency, strings.Join(cyclePath, " -> "))
	}

	// 统计入度 (每个插件依赖多少个底层提供者)
	inDegree := make(map[string]int)
	for pID := range dag.Nodes {
		inDegree[pID] = len(dag.Dependencies[pID])
	}

	// 入度为 0 的节点先入队 (不依赖其他插件的基础无头模块)
	var queue []string
	for pID, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, pID)
		}
	}
	sort.Strings(queue) // 保证序列确定性

	var order []string
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		order = append(order, curr)

		// 依赖 curr 的消费者入度减 1
		var newlyReady []string
		for _, consumer := range dag.Dependents[curr] {
			inDegree[consumer]--
			if inDegree[consumer] == 0 {
				newlyReady = append(newlyReady, consumer)
			}
		}
		sort.Strings(newlyReady)
		queue = append(queue, newlyReady...)
	}

	if len(order) != len(dag.Nodes) {
		return nil, fmt.Errorf("%w: 存在无法解析的依赖约束", ErrCircularDependency)
	}

	return order, nil
}

// CheckMaxDepth 校验关键路径依赖链层级深度 (硬约束 MaxDepth <= 2)
// Level 0: Kernel 底座
// Level 1: Foundational 基础模块 (不依赖其他插件，深度为 1)
// Level 2: Business 垂直业务插件 (依赖 Level 1 基础模块，深度为 2)
// Level 3+: 禁止业务插件间深层纵向调用 (深度 > 2 直接阻断)
func CheckMaxDepth(dag *DependencyDAG, maxDepth int) error {
	if dag == nil || len(dag.Nodes) == 0 {
		return nil
	}

	if hasCycle, cyclePath := DetectCycle(dag); hasCycle {
		return fmt.Errorf("%w: %s", ErrCircularDependency, strings.Join(cyclePath, " -> "))
	}

	memo := make(map[string]int)

	var getDepth func(nodeID string) int
	getDepth = func(nodeID string) int {
		if d, ok := memo[nodeID]; ok {
			return d
		}
		deps := dag.Dependencies[nodeID]
		if len(deps) == 0 {
			memo[nodeID] = 1
			return 1
		}
		maxDepDepth := 0
		for _, depID := range deps {
			if _, exists := dag.Nodes[depID]; exists {
				depDepth := getDepth(depID)
				if depDepth > maxDepDepth {
					maxDepDepth = depDepth
				}
			}
		}
		d := 1 + maxDepDepth
		memo[nodeID] = d
		return d
	}

	var allNodes []string
	for nodeID := range dag.Nodes {
		allNodes = append(allNodes, nodeID)
	}
	sort.Strings(allNodes)

	for _, nodeID := range allNodes {
		depth := getDepth(nodeID)
		if depth > maxDepth {
			return fmt.Errorf("%w: 插件 %q 依赖层级深度为 %d，超过最大允许深度 %d",
				ErrExceedsMaxDependencyDepth, nodeID, depth, maxDepth)
		}
	}

	return nil
}
