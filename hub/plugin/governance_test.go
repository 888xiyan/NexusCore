package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
)

func TestInterfaceProviderIndex(t *testing.T) {
	idx := NewInterfaceProviderIndex()

	doc1 := &PluginInterfacesDoc{
		PluginID:      "plugin_groupchat",
		PluginVersion: "1.0.0",
		ProvidesInterfaces: []InterfaceDefinition{
			{
				InterfaceID:  "nexus.groupchat.v1",
				Version:      "1.0.0",
				Protocol:     "http_rest",
				Endpoint:     "http://127.0.0.1:7300/v1/chat",
				HealthCheck:  "/health",
				Capabilities: []string{"rooms", "messages"},
				Stability:    "stable",
			},
		},
	}

	doc2 := &PluginInterfacesDoc{
		PluginID:      "plugin_groupchat_v2",
		PluginVersion: "1.2.0",
		ProvidesInterfaces: []InterfaceDefinition{
			{
				InterfaceID:  "nexus.groupchat.v1",
				Version:      "1.2.0",
				Protocol:     "http_rest",
				Endpoint:     "http://127.0.0.1:7301/v1/chat",
				HealthCheck:  "/health",
				Capabilities: []string{"rooms", "messages", "threads"},
				Stability:    "stable",
			},
		},
	}

	if err := idx.RegisterDoc(doc1); err != nil {
		t.Fatalf("RegisterDoc 1 失败: %v", err)
	}
	if err := idx.RegisterDoc(doc2); err != nil {
		t.Fatalf("RegisterDoc 2 失败: %v", err)
	}

	// 匹配最高满足版本 (1.2.0 满足 ^1.0.0)
	entry, err := idx.Resolve("nexus.groupchat.v1", "^1.0.0")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if entry.Version != "1.2.0" || entry.PluginID != "plugin_groupchat_v2" {
		t.Errorf("Resolve 未选取最高兼容版本: %+v", entry)
	}

	// 范围约束仅允许 1.0.x
	entry1, err := idx.Resolve("nexus.groupchat.v1", "~1.0.0")
	if err != nil {
		t.Fatalf("Resolve ~1.0.0 失败: %v", err)
	}
	if entry1.Version != "1.0.0" || entry1.PluginID != "plugin_groupchat" {
		t.Errorf("Resolve ~1.0.0 结果不符: %+v", entry1)
	}

	// 获取提供者 Map
	provMap := idx.GetProviderMap()
	if provMap["nexus.groupchat.v1"] != "plugin_groupchat_v2" {
		t.Errorf("GetProviderMap 未返回最高版本提供者: %v", provMap)
	}

	// 注销 plugin_groupchat_v2
	idx.UnregisterPlugin("plugin_groupchat_v2")
	entryAfter, err := idx.Resolve("nexus.groupchat.v1", "^1.0.0")
	if err != nil {
		t.Fatalf("注销后 Resolve 失败: %v", err)
	}
	if entryAfter.PluginID != "plugin_groupchat" {
		t.Errorf("注销后未回退到残留提供者: %+v", entryAfter)
	}

	// 查询不存在的接口
	if _, err := idx.Resolve("nexus.nonexistent", "*"); err == nil {
		t.Errorf("查询不存在接口未报错")
	}

	// 不满足版本范围
	if _, err := idx.Resolve("nexus.groupchat.v1", ">=2.0.0"); err == nil {
		t.Errorf("版本范围不满足未报错")
	}
}

func TestCheckUninstallReverseDependencies(t *testing.T) {
	// 构建拓扑:
	// plugin_groupchat <- plugin_pm <- plugin_custom_tool
	// plugin_agent_mesh (独立无下游)
	dag := NewDependencyDAG()
	dag.Nodes["plugin_groupchat"] = &DAGNode{PluginID: "plugin_groupchat"}
	dag.Nodes["plugin_pm"] = &DAGNode{PluginID: "plugin_pm"}
	dag.Nodes["plugin_custom_tool"] = &DAGNode{PluginID: "plugin_custom_tool"}
	dag.Nodes["plugin_agent_mesh"] = &DAGNode{PluginID: "plugin_agent_mesh"}

	dag.Dependencies["plugin_pm"] = []string{"plugin_groupchat"}
	dag.Dependents["plugin_groupchat"] = []string{"plugin_pm"}
	dag.Adjacency["plugin_groupchat"] = []string{"plugin_pm"}

	dag.Dependencies["plugin_custom_tool"] = []string{"plugin_pm"}
	dag.Dependents["plugin_pm"] = []string{"plugin_custom_tool"}
	dag.Adjacency["plugin_pm"] = []string{"plugin_custom_tool"}

	// 1. 卸载无依赖插件: 直接成功
	seq, err := CheckUninstallReverseDependencies(dag, "plugin_agent_mesh", false)
	if err != nil {
		t.Fatalf("卸载无依赖插件报错: %v", err)
	}
	if len(seq) != 1 || seq[0] != "plugin_agent_mesh" {
		t.Errorf("卸载序列不符: %v", seq)
	}

	// 2. 卸载有下游的插件 (cascade == false): 阻断
	_, err = CheckUninstallReverseDependencies(dag, "plugin_groupchat", false)
	if err == nil {
		t.Fatalf("存在下游依赖未阻断卸载")
	}
	if !errors.Is(err, ErrReverseDependencyBlocked) {
		t.Errorf("错误类型不符: %v", err)
	}
	if !strings.Contains(err.Error(), "plugin_pm") || !strings.Contains(err.Error(), "plugin_custom_tool") {
		t.Errorf("错误信息未列出所有下游依赖: %v", err)
	}

	// 3. 级联卸载 (cascade == true): 逆拓扑顺序下线
	cascadeSeq, err := CheckUninstallReverseDependencies(dag, "plugin_groupchat", true)
	if err != nil {
		t.Fatalf("级联卸载检查失败: %v", err)
	}

	// 期望顺序: 最下游 custom_tool 先下线，然后 pm，最后 groupchat
	if len(cascadeSeq) != 3 {
		t.Fatalf("级联下线序列长度不符: %v", cascadeSeq)
	}
	if cascadeSeq[0] != "plugin_custom_tool" || cascadeSeq[1] != "plugin_pm" || cascadeSeq[2] != "plugin_groupchat" {
		t.Errorf("级联下线序列不符合逆拓扑顺序: %v", cascadeSeq)
	}

	// 4. 不存在的插件
	if _, err := CheckUninstallReverseDependencies(dag, "plugin_missing", false); err == nil {
		t.Errorf("不存在的插件未报错")
	}

	// 5. 存在环路的子图在级联卸载时报错
	cycleDAG := NewDependencyDAG()
	cycleDAG.Nodes["plugin_cycle_a"] = &DAGNode{PluginID: "plugin_cycle_a"}
	cycleDAG.Nodes["plugin_cycle_b"] = &DAGNode{PluginID: "plugin_cycle_b"}
	cycleDAG.Dependencies["plugin_cycle_a"] = []string{"plugin_cycle_b"}
	cycleDAG.Dependents["plugin_cycle_b"] = []string{"plugin_cycle_a"}
	cycleDAG.Adjacency["plugin_cycle_b"] = []string{"plugin_cycle_a"}

	cycleDAG.Dependencies["plugin_cycle_b"] = []string{"plugin_cycle_a"}
	cycleDAG.Dependents["plugin_cycle_a"] = []string{"plugin_cycle_b"}
	cycleDAG.Adjacency["plugin_cycle_a"] = []string{"plugin_cycle_b"}

	if _, err := CheckUninstallReverseDependencies(cycleDAG, "plugin_cycle_a", true); err == nil {
		t.Errorf("环路子图级联卸载未报错")
	}
}

func TestCheckUpgradeCompatibilityAndRollingPlan(t *testing.T) {
	dependents := map[string][]DependencySpec{
		"plugin_pm": {
			{
				Interface:    "nexus.groupchat.v1",
				VersionRange: "^1.0.0",
				Optional:     false,
			},
		},
		"plugin_cicd": {
			{
				Interface:    "nexus.groupchat.v1",
				VersionRange: ">=1.0.0 <2.0.0",
				Optional:     true,
			},
		},
	}

	// 1. Minor 升级 (1.0.0 -> 1.1.0): 兼容
	checkMinor, err := CheckUpgradeCompatibility("1.0.0", "1.1.0", "nexus.groupchat.v1", dependents)
	if err != nil {
		t.Fatalf("Minor 升级检查报错: %v", err)
	}
	if !checkMinor.Compatible || checkMinor.UpgradeType != "minor" {
		t.Errorf("Minor 升级结果不符: %+v", checkMinor)
	}

	// 2. Patch 升级 (1.0.0 -> 1.0.1): 兼容
	checkPatch, err := CheckUpgradeCompatibility("1.0.0", "1.0.1", "nexus.groupchat.v1", dependents)
	if err != nil {
		t.Fatalf("Patch 升级检查报错: %v", err)
	}
	if !checkPatch.Compatible || checkPatch.UpgradeType != "patch" {
		t.Errorf("Patch 升级结果不符: %+v", checkPatch)
	}

	// 3. Major 破坏性升级 (1.0.0 -> 2.0.0): 下游均不满足，阻断
	checkMajor, err := CheckUpgradeCompatibility("1.0.0", "2.0.0", "nexus.groupchat.v1", dependents)
	if err != nil {
		t.Fatalf("Major 升级检查报错: %v", err)
	}
	if checkMajor.Compatible {
		t.Errorf("破坏性 Major 升级未被阻断")
	}
	if len(checkMajor.IncompatibleDeps) != 2 {
		t.Errorf("不兼容依赖数量不符: %v", checkMajor.IncompatibleDeps)
	}

	// 4. 生成热更新计划 (兼容场景)
	plan, err := PlanRollingUpdate("1.0.0", "1.1.0", "plugin_groupchat", "nexus.groupchat.v1", dependents)
	if err != nil {
		t.Fatalf("生成热更新计划失败: %v", err)
	}
	if plan.DrainPeriod != 5*time.Second || plan.HealthRetries != 3 {
		t.Errorf("热更新参数不符: %+v", plan)
	}
	if len(plan.Steps) != 5 {
		t.Errorf("热更新步骤不符: %v", plan.Steps)
	}

	// 5. 生成热更新计划 (不兼容场景阻断)
	if _, err := PlanRollingUpdate("1.0.0", "2.0.0", "plugin_groupchat", "nexus.groupchat.v1", dependents); err == nil {
		t.Errorf("不兼容升级生成热更新计划未报错")
	}
}

func TestRegisterPluginInterfacesConfig(t *testing.T) {
	doc := &PluginInterfacesDoc{
		PluginID:      "plugin_groupchat",
		PluginVersion: "1.0.0",
		ProvidesInterfaces: []InterfaceDefinition{
			{
				InterfaceID: "nexus.groupchat.v1",
				Version:     "1.0.0",
				Protocol:    "http_rest",
				Endpoint:    "http://127.0.0.1:7300/v1/chat",
			},
		},
	}

	actor := config.BotActor(config.UserBot)
	checksum, err := RegisterPluginInterfacesConfig(context.Background(), nil, actor, doc)
	if err != nil {
		t.Fatalf("RegisterPluginInterfacesConfig 纯内存模式失败: %v", err)
	}
	if len(checksum) != 64 {
		t.Errorf("计算的 SHA256 校验和长度非法: %s", checksum)
	}

	// 非法 doc 报错
	badDoc := &PluginInterfacesDoc{PluginID: "bad"}
	if _, err := RegisterPluginInterfacesConfig(context.Background(), nil, actor, badDoc); err == nil {
		t.Errorf("非法 doc 注册未报错")
	}
}

func TestGovernanceEdgeCases(t *testing.T) {
	idx := NewInterfaceProviderIndex()

	// 1. RegisterDoc 校验失败
	if err := idx.RegisterDoc(&PluginInterfacesDoc{PluginID: "bad"}); err == nil {
		t.Errorf("非法 doc 注册未报错")
	}

	// 2. 覆盖已存在的提供者条目
	doc := &PluginInterfacesDoc{
		PluginID:      "plugin_provider_one",
		PluginVersion: "1.0.0",
		ProvidesInterfaces: []InterfaceDefinition{
			{
				InterfaceID: "nexus.test.v1",
				Version:     "1.0.0",
				Protocol:    "grpc",
				Endpoint:    "127.0.0.1:50051",
			},
		},
	}
	if err := idx.RegisterDoc(doc); err != nil {
		t.Fatalf("初始注册失败: %v", err)
	}

	docUpdated := &PluginInterfacesDoc{
		PluginID:      "plugin_provider_one",
		PluginVersion: "1.0.1",
		ProvidesInterfaces: []InterfaceDefinition{
			{
				InterfaceID: "nexus.test.v1",
				Version:     "1.0.1",
				Protocol:    "grpc",
				Endpoint:    "127.0.0.1:50051",
			},
		},
	}
	if err := idx.RegisterDoc(docUpdated); err != nil {
		t.Fatalf("更新注册失败: %v", err)
	}

	res, err := idx.Resolve("nexus.test.v1", "*")
	if err != nil || res.Version != "1.0.1" {
		t.Errorf("更新后的提供者版本不符: %+v", res)
	}

	// 空范围匹配
	resEmpty, err := idx.Resolve("nexus.test.v1", "")
	if err != nil || resEmpty.Version != "1.0.1" {
		t.Errorf("空范围解析失败: %+v", resEmpty)
	}

	// 注销后变为空
	idx.UnregisterPlugin("plugin_provider_one")
	if len(idx.GetProviderMap()) != 0 {
		t.Errorf("全部注销后 GetProviderMap 应为空")
	}

	// 3. CheckUninstallReverseDependencies 传入 nil
	if _, err := CheckUninstallReverseDependencies(nil, "plugin_test", false); err == nil {
		t.Errorf("nil DAG 卸载检查未报错")
	}

	// 4. CheckUpgradeCompatibility 版本解析错误
	if _, err := CheckUpgradeCompatibility("bad", "1.0.0", "iface", nil); err == nil {
		t.Errorf("非法旧版本未报错")
	}
	if _, err := CheckUpgradeCompatibility("1.0.0", "bad", "iface", nil); err == nil {
		t.Errorf("非法新版本未报错")
	}

	// 5. PlanRollingUpdate 传入非法版本
	if _, err := PlanRollingUpdate("bad", "1.0.0", "plugin_test", "iface", nil); err == nil {
		t.Errorf("非法版本生成滚动计划未报错")
	}

	// 6. RegisterPluginInterfacesConfig 参数校验失败
	badDoc := &PluginInterfacesDoc{
		PluginID: "bad", // 不合法 plugin_id
	}
	if _, err := RegisterPluginInterfacesConfig(context.Background(), nil, config.BotActor(config.UserBot), badDoc); err == nil {
		t.Errorf("非法文档注册配置中心未报错")
	}

	// 7. Resolve 与 GetProviderMap 多版本与非活跃状态分支覆盖
	idxMulti := NewInterfaceProviderIndex()
	docV1 := &PluginInterfacesDoc{
		PluginID:      "plugin_prov_v1",
		PluginVersion: "1.0.0",
		ProvidesInterfaces: []InterfaceDefinition{
			{InterfaceID: "nexus.shared.service", Version: "1.0.0", Protocol: "grpc", Endpoint: "127.0.0.1:50001"},
		},
	}
	docV2 := &PluginInterfacesDoc{
		PluginID:      "plugin_prov_v2",
		PluginVersion: "1.2.0",
		ProvidesInterfaces: []InterfaceDefinition{
			{InterfaceID: "nexus.shared.service", Version: "1.2.0", Protocol: "grpc", Endpoint: "127.0.0.1:50002"},
		},
	}
	_ = idxMulti.RegisterDoc(docV1)
	_ = idxMulti.RegisterDoc(docV2)

	// 应优先选最高版本 v1.2.0
	bestResolved, err := idxMulti.Resolve("nexus.shared.service", "^1.0.0")
	if err != nil || bestResolved.Version != "1.2.0" {
		t.Errorf("多版本 Resolve 选优失败: got %+v, err %v", bestResolved, err)
	}

	pmap := idxMulti.GetProviderMap()
	if pmap["nexus.shared.service"] != "plugin_prov_v2" {
		t.Errorf("GetProviderMap 未优先选取最高版本提供者: %s", pmap["nexus.shared.service"])
	}

	// 将所有提供者标记为非活跃
	idxMulti.mu.Lock()
	for i := range idxMulti.providers["nexus.shared.service"] {
		idxMulti.providers["nexus.shared.service"][i].Active = false
	}
	idxMulti.mu.Unlock()

	// 全部非活跃时 Resolve 应报错 ErrIncompatibleVersion
	if _, err := idxMulti.Resolve("nexus.shared.service", "*"); err == nil {
		t.Errorf("全部非活跃提供者 Resolve 未报错")
	}
	// 全部非活跃时 GetProviderMap 不应包含该接口
	if len(idxMulti.GetProviderMap()) != 0 {
		t.Errorf("全部非活跃时 GetProviderMap 应为空")
	}
}
