package plugin

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildDAGAndTopologicalSort(t *testing.T) {
	plugins := map[string]*Manifest{
		"plugin_groupchat": {
			PluginID:   "plugin_groupchat",
			Name:       "GroupChat",
			Version:    "1.0.0",
			PluginType: "workspace",
		},
		"plugin_agent_mesh": {
			PluginID:   "plugin_agent_mesh",
			Name:       "Agent Mesh",
			Version:    "1.2.0",
			PluginType: "workspace",
		},
		"plugin_pm": {
			PluginID:   "plugin_pm",
			Name:       "PM Plugin",
			Version:    "1.0.0",
			PluginType: "workspace",
			Dependencies: []DependencySpec{
				{
					Interface:    "nexus.groupchat.v1",
					VersionRange: "^1.0.0",
					Optional:     false,
				},
			},
		},
		"plugin_cicd": {
			PluginID:   "plugin_cicd",
			Name:       "CI/CD Plugin",
			Version:    "1.0.0",
			PluginType: "workspace",
			Dependencies: []DependencySpec{
				{
					Interface:    "nexus.agent_mesh.v1",
					VersionRange: ">=1.0.0 <2.0.0",
					Optional:     false,
				},
				{
					Interface:    "nexus.optional.service",
					VersionRange: "*",
					Optional:     true, // 可选依赖无提供者不阻断
				},
			},
		},
	}

	providerIndex := map[string]string{
		"nexus.groupchat.v1":  "plugin_groupchat",
		"nexus.agent_mesh.v1": "plugin_agent_mesh",
	}

	dag, err := BuildDAG(plugins, providerIndex)
	if err != nil {
		t.Fatalf("BuildDAG 失败: %v", err)
	}

	order, err := TopologicalSort(dag)
	if err != nil {
		t.Fatalf("TopologicalSort 失败: %v", err)
	}

	// 验证基础插件先于业务插件
	gcIdx := -1
	pmIdx := -1
	amIdx := -1
	cicdIdx := -1

	for i, id := range order {
		switch id {
		case "plugin_groupchat":
			gcIdx = i
		case "plugin_pm":
			pmIdx = i
		case "plugin_agent_mesh":
			amIdx = i
		case "plugin_cicd":
			cicdIdx = i
		}
	}

	if gcIdx >= pmIdx {
		t.Errorf("plugin_groupchat (%d) 必须在 plugin_pm (%d) 之前启动", gcIdx, pmIdx)
	}
	if amIdx >= cicdIdx {
		t.Errorf("plugin_agent_mesh (%d) 必须在 plugin_cicd (%d) 之前启动", amIdx, cicdIdx)
	}
}

func TestBuildDAGUnmetDependency(t *testing.T) {
	plugins := map[string]*Manifest{
		"plugin_pm": {
			PluginID:   "plugin_pm",
			Name:       "PM",
			Version:    "1.0.0",
			PluginType: "workspace",
			Dependencies: []DependencySpec{
				{
					Interface: "nexus.missing.interface",
					Optional:  false,
				},
			},
		},
	}

	_, err := BuildDAG(plugins, map[string]string{})
	if err == nil {
		t.Fatalf("缺失必选依赖未报错")
	}
	if !errors.Is(err, ErrUnmetDependency) {
		t.Errorf("错误类型不符合 ErrUnmetDependency: %v", err)
	}
}

func TestBuildDAGIncompatibleVersion(t *testing.T) {
	plugins := map[string]*Manifest{
		"plugin_groupchat": {
			PluginID:   "plugin_groupchat",
			Name:       "GroupChat",
			Version:    "2.0.0", // 提供 2.0.0
			PluginType: "workspace",
		},
		"plugin_pm": {
			PluginID:   "plugin_pm",
			Name:       "PM",
			Version:    "1.0.0",
			PluginType: "workspace",
			Dependencies: []DependencySpec{
				{
					Interface:    "nexus.groupchat.v1",
					VersionRange: "^1.0.0", // 要求 ^1.0.0 不兼容 2.0.0
					Optional:     false,
				},
			},
		},
	}

	providerIndex := map[string]string{
		"nexus.groupchat.v1": "plugin_groupchat",
	}

	_, err := BuildDAG(plugins, providerIndex)
	if err == nil {
		t.Fatalf("版本不兼容未报错")
	}
	if !errors.Is(err, ErrIncompatibleVersion) {
		t.Errorf("错误类型不符合 ErrIncompatibleVersion: %v", err)
	}
}

func TestDetectCycle(t *testing.T) {
	// 1. 无环图
	dagAcyclic := NewDependencyDAG()
	dagAcyclic.Nodes["a"] = &DAGNode{PluginID: "a"}
	dagAcyclic.Nodes["b"] = &DAGNode{PluginID: "b"}
	dagAcyclic.Dependencies["b"] = []string{"a"} // b depends on a
	dagAcyclic.Dependents["a"] = []string{"b"}

	hasCycle, path := DetectCycle(dagAcyclic)
	if hasCycle {
		t.Errorf("无环图误报有环: %v", path)
	}

	// 2. 二节点环路 a <-> b
	dag2Cycle := NewDependencyDAG()
	dag2Cycle.Nodes["plugin_a"] = &DAGNode{PluginID: "plugin_a"}
	dag2Cycle.Nodes["plugin_b"] = &DAGNode{PluginID: "plugin_b"}
	dag2Cycle.Dependencies["plugin_a"] = []string{"plugin_b"}
	dag2Cycle.Dependencies["plugin_b"] = []string{"plugin_a"}

	hasCycle2, path2 := DetectCycle(dag2Cycle)
	if !hasCycle2 {
		t.Fatalf("二节点环路未检测出")
	}
	if len(path2) < 3 || path2[0] != path2[len(path2)-1] {
		t.Errorf("二节点环路路径不正确: %v", path2)
	}

	// 3. 三节点环路 a -> b -> c -> a
	dag3Cycle := NewDependencyDAG()
	dag3Cycle.Nodes["a"] = &DAGNode{PluginID: "a"}
	dag3Cycle.Nodes["b"] = &DAGNode{PluginID: "b"}
	dag3Cycle.Nodes["c"] = &DAGNode{PluginID: "c"}
	dag3Cycle.Dependencies["a"] = []string{"b"}
	dag3Cycle.Dependencies["b"] = []string{"c"}
	dag3Cycle.Dependencies["c"] = []string{"a"}

	hasCycle3, path3 := DetectCycle(dag3Cycle)
	if !hasCycle3 {
		t.Fatalf("三节点环路未检测出")
	}
	if path3[0] != path3[len(path3)-1] {
		t.Errorf("三节点环路未闭合: %v", path3)
	}

	// 4. 自环 a -> a
	dagSelf := NewDependencyDAG()
	dagSelf.Nodes["self_a"] = &DAGNode{PluginID: "self_a"}
	dagSelf.Dependencies["self_a"] = []string{"self_a"}

	hasCycleSelf, pathSelf := DetectCycle(dagSelf)
	if !hasCycleSelf {
		t.Fatalf("自依赖环路未检测出")
	}
	if len(pathSelf) != 2 || pathSelf[0] != "self_a" || pathSelf[1] != "self_a" {
		t.Errorf("自依赖环路路径不符: %v", pathSelf)
	}

	// 拓扑排序在有环时必须报错
	_, err := TopologicalSort(dag2Cycle)
	if err == nil || !errors.Is(err, ErrCircularDependency) {
		t.Errorf("有环图拓扑排序未返回 ErrCircularDependency: %v", err)
	}
}

func TestCheckMaxDepth(t *testing.T) {
	// 场景 1: 单基础模块，深度 1 <= 2 (通过)
	dag1 := NewDependencyDAG()
	dag1.Nodes["plugin_groupchat"] = &DAGNode{PluginID: "plugin_groupchat"}
	if err := CheckMaxDepth(dag1, 2); err != nil {
		t.Errorf("深度 1 校验失败: %v", err)
	}

	// 场景 2: 基础模块 + 业务插件，深度 2 <= 2 (通过)
	dag2 := NewDependencyDAG()
	dag2.Nodes["plugin_groupchat"] = &DAGNode{PluginID: "plugin_groupchat"}
	dag2.Nodes["plugin_pm"] = &DAGNode{PluginID: "plugin_pm"}
	dag2.Dependencies["plugin_pm"] = []string{"plugin_groupchat"}
	if err := CheckMaxDepth(dag2, 2); err != nil {
		t.Errorf("深度 2 校验失败: %v", err)
	}

	// 场景 3: 深度 3 (Kernel -> groupchat -> pm -> custom_tool)，超过 MaxDepth=2 (阻断)
	dag3 := NewDependencyDAG()
	dag3.Nodes["plugin_groupchat"] = &DAGNode{PluginID: "plugin_groupchat"}
	dag3.Nodes["plugin_pm"] = &DAGNode{PluginID: "plugin_pm"}
	dag3.Nodes["plugin_custom_tool"] = &DAGNode{PluginID: "plugin_custom_tool"}
	dag3.Dependencies["plugin_pm"] = []string{"plugin_groupchat"}
	dag3.Dependencies["plugin_custom_tool"] = []string{"plugin_pm"}

	err := CheckMaxDepth(dag3, 2)
	if err == nil {
		t.Fatalf("深度 3 未阻断")
	}
	if !errors.Is(err, ErrExceedsMaxDependencyDepth) {
		t.Errorf("错误类型不符合 ErrExceedsMaxDependencyDepth: %v", err)
	}
	if !strings.Contains(err.Error(), "超过最大允许深度 2") {
		t.Errorf("错误信息不包含深度描述: %v", err)
	}
}

func TestManifestV1DepsInDAG(t *testing.T) {
	// 验证对 Manifest v1 Deps 的无缝建图
	plugins := map[string]*Manifest{
		"plugin_base": {
			PluginID:   "plugin_base",
			Name:       "Base",
			Version:    "1.0.0",
			PluginType: "workspace",
		},
		"plugin_app": {
			PluginID:   "plugin_app",
			Name:       "App",
			Version:    "1.0.0",
			PluginType: "workspace",
			Deps:       []string{"plugin_base"},
		},
	}

	dag, err := BuildDAG(plugins, nil)
	if err != nil {
		t.Fatalf("Manifest v1 Deps 建图失败: %v", err)
	}

	order, err := TopologicalSort(dag)
	if err != nil {
		t.Fatalf("Manifest v1 拓扑排序失败: %v", err)
	}

	if len(order) != 2 || order[0] != "plugin_base" || order[1] != "plugin_app" {
		t.Errorf("拓扑排序结果不符合预期: %v", order)
	}
}

func TestDAGEdgeCases(t *testing.T) {
	// 1. 空图与 nil 图防御
	if hasCycle, _ := DetectCycle(nil); hasCycle {
		t.Errorf("nil DAG 不应有环")
	}
	if hasCycle, _ := DetectCycle(NewDependencyDAG()); hasCycle {
		t.Errorf("空 DAG 不应有环")
	}

	if order, err := TopologicalSort(nil); err != nil || order != nil {
		t.Errorf("nil DAG 拓扑排序应返回 nil, nil")
	}
	if order, err := TopologicalSort(NewDependencyDAG()); err != nil || order != nil {
		t.Errorf("空 DAG 拓扑排序应返回 nil, nil")
	}

	if err := CheckMaxDepth(nil, 2); err != nil {
		t.Errorf("nil DAG CheckMaxDepth 应为 nil")
	}
	if err := CheckMaxDepth(NewDependencyDAG(), 2); err != nil {
		t.Errorf("空 DAG CheckMaxDepth 应为 nil")
	}

	// 2. 有环图 CheckMaxDepth 报错
	dagCyclic := NewDependencyDAG()
	dagCyclic.Nodes["a"] = &DAGNode{PluginID: "a"}
	dagCyclic.Nodes["b"] = &DAGNode{PluginID: "b"}
	dagCyclic.Dependencies["a"] = []string{"b"}
	dagCyclic.Dependencies["b"] = []string{"a"}
	if err := CheckMaxDepth(dagCyclic, 2); err == nil || !errors.Is(err, ErrCircularDependency) {
		t.Errorf("有环图 CheckMaxDepth 未报错 ErrCircularDependency: %v", err)
	}

	// 3. BuildDAG 边界:
	// - m == nil
	// - m.PluginID 为空串 (回退到 map key)
	// - v2 自依赖
	// - v1 自依赖
	// - v1 空白依赖跳过
	// - v1 重复依赖去重
	// - 可选依赖提供者不在 plugins 中
	plugins := map[string]*Manifest{
		"nil_plugin": nil,
		"plugin_empty_id": {
			PluginID:   "", // 回退到 key
			Name:       "Empty ID",
			Version:    "1.0.0",
			PluginType: "workspace",
			Dependencies: []DependencySpec{
				{
					Interface:    "nexus.self.service",
					VersionRange: "*",
				},
				{
					Interface: "nexus.optional.not_in_cluster",
					Optional:  true,
				},
			},
			Deps: []string{"", "plugin_empty_id", "plugin_other", "plugin_other"},
		},
		"plugin_other": {
			PluginID:   "plugin_other",
			Name:       "Other",
			Version:    "1.0.0",
			PluginType: "workspace",
		},
	}

	providerIndex := map[string]string{
		"nexus.self.service":              "plugin_empty_id",
		"nexus.optional.not_in_cluster": "plugin_not_in_cluster",
	}

	dag, err := BuildDAG(plugins, providerIndex)
	if err != nil {
		t.Fatalf("BuildDAG 边界用例失败: %v", err)
	}
	if _, exists := dag.Nodes["plugin_empty_id"]; !exists {
		t.Errorf("空 PluginID 回退未生效")
	}

	// 4. 非可选依赖提供者未就绪
	pluginsBad := map[string]*Manifest{
		"plugin_caller": {
			PluginID:   "plugin_caller",
			Name:       "Caller",
			Version:    "1.0.0",
			PluginType: "workspace",
			Dependencies: []DependencySpec{
				{
					Interface: "nexus.required.not_in_cluster",
					Optional:  false,
				},
			},
		},
	}
	_, err = BuildDAG(pluginsBad, map[string]string{
		"nexus.required.not_in_cluster": "plugin_absent",
	})
	if err == nil || !errors.Is(err, ErrUnmetDependency) {
		t.Errorf("非可选依赖提供者不存在未报错 ErrUnmetDependency: %v", err)
	}

	// 5. v1 依赖未就绪插件
	pluginsBadV1 := map[string]*Manifest{
		"plugin_v1_caller": {
			PluginID:   "plugin_v1_caller",
			Name:       "Caller",
			Version:    "1.0.0",
			PluginType: "workspace",
			Deps:       []string{"plugin_non_existent"},
		},
	}
	_, err = BuildDAG(pluginsBadV1, nil)
	if err == nil || !errors.Is(err, ErrUnmetDependency) {
		t.Errorf("v1 依赖不存在插件未报错 ErrUnmetDependency: %v", err)
	}

	// 6. BuildDAG 中含非法 DependencySpec
	pluginsInvalidDep := map[string]*Manifest{
		"plugin_bad_dep": {
			PluginID:   "plugin_bad_dep",
			Name:       "Bad Dep",
			Version:    "1.0.0",
			PluginType: "workspace",
			Dependencies: []DependencySpec{
				{Interface: "invalid id with spaces"},
			},
		},
	}
	if _, err := BuildDAG(pluginsInvalidDep, nil); err == nil {
		t.Errorf("BuildDAG 包含非法 DependencySpec 未报错")
	}
}
