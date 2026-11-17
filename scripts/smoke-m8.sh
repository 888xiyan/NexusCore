#!/usr/bin/env bash
# M8 冒烟验收：基础模块无头化沉淀、接口契约入配置中心、DAG 拓扑加载与依赖治理
# 依据：开发规划 §3 M8、doc/milestones/M8-基础模块与依赖治理.md。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "SMOKE M8 FAIL: $1" >&2; exit 1; }

step "步骤 1：编译 Go 微内核、所有基础模块与业务插件"
make build >/dev/null
make build-plugins >/dev/null

[[ -f "bin/hub" ]] || fail "bin/hub 不存在"
[[ -f "bin/bot-runtime" ]] || fail "bin/bot-runtime 不存在"
[[ -f "bin/agent-daemon" ]] || fail "bin/agent-daemon 不存在"
[[ -f "bin/cli" ]] || fail "bin/cli 不存在"
[[ -f "bin/migrate" ]] || fail "bin/migrate 不存在"
[[ -f "bin/plugins/plugin_groupchat" ]] || fail "bin/plugins/plugin_groupchat 不存在"
[[ -f "bin/plugins/plugin_agent_discovery" ]] || fail "bin/plugins/plugin_agent_discovery 不存在"
[[ -f "bin/plugins/plugin_bot_creator" ]] || fail "bin/plugins/plugin_bot_creator 不存在"
echo "Go 微内核与插件二进制全量编译完成"

step "步骤 2：基础无头模块与治理引擎代码纯净度与单测覆盖率校验"
# 2.1 校验基础模块目录中 100% 剥离 UI 逻辑（无 vue, html, css 文件与 import）
python3 -c '
import os, sys

dirs = [
    "internal/modules/groupchat",
    "internal/modules/agentmesh",
    "internal/modules/botfactory",
    "hub/plugin"
]

forbidden_exts = {".vue", ".html", ".css", ".scss", ".less"}
for d in dirs:
    for root, _, files in os.walk(d):
        for f in files:
            ext = os.path.splitext(f)[1].lower()
            if ext in forbidden_exts:
                sys.exit(f"违反无头架构：在 {root}/{f} 中发现前端 UI 文件")
            if f.endswith(".go"):
                content = open(os.path.join(root, f), "r", encoding="utf-8").read()
                if "html/template" in content:
                    sys.exit(f"违反无头架构：在 {root}/{f} 中发现 HTML 模板依赖")

print("✔ 基础模块与治理引擎 100% 纯净无头化校验通过（零 UI 文件、零 HTML 依赖）")
'

# 2.2 验证各包单测覆盖率 ≥ 85%
python3 -c '
import subprocess, re, sys

cmd = ["go", "test", "-cover", "./internal/modules/...", "./hub/plugin/..."]
res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
if res.returncode != 0:
    print(res.stderr)
    sys.exit("单测执行失败")

for line in res.stdout.strip().split("\n"):
    if "coverage:" in line and "% of statements" in line:
        match = re.search(r"coverage:\s+([0-9.]+)%", line)
        if match:
            cov = float(match.group(1))
            pkg = line.split()[1]
            print(f" - {pkg}: 覆盖率 {cov}%")
            if cov < 85.0:
                sys.exit(f"覆盖率不足 85%: {pkg} 为 {cov}%")

print("✔ 基础无头模块与治理引擎单测覆盖率全量 ≥ 85% 达标")
'

step "步骤 3：接口契约入配置中心与 Manifest v2 依赖声明解析校验"
go test -v ./hub/plugin -run "TestValidateInterfaceDefinition|TestValidateInterfacesDoc|TestValidateDependencySpec|TestManifestV2Compatibility"
echo "✔ 接口契约元数据 Schema 与 Manifest v2 依赖语法校验通过"

step "步骤 4：基于 Kahn 算法的 DAG 拓扑排序加载顺序校验"
go test -v ./hub/plugin -run "TestBuildDAGAndTopologicalSort|TestManifestV1DepsInDAG"
echo "✔ Kahn 拓扑排序算法与加载顺序校验通过（基础模块优先加载就绪）"

step "步骤 5：依赖治理安全刚性防线：循环依赖注入与最大深度 MaxDepth > 2 静态拦截"
go test -v ./hub/plugin -run "TestDetectCycle|TestCheckMaxDepth"
echo "✔ 循环依赖三色标记法 DFS 拦截与 MaxDepth <= 2 层级防线验证通过"

step "步骤 6：反向依赖安全卸载与两阶段滚动热更新演练"
go test -v ./hub/plugin -run "TestCheckUninstallReverseDependencies|TestCheckUpgradeCompatibilityAndRollingPlan"
echo "✔ 被依赖模块单点卸载阻断、显式级联逆序安全卸载与平滑热更校验通过"

step "步骤 7：业务插件纯后台无 UI 拉起群聊协同与防死循环安全铁律"
go test -v ./internal/modules/groupchat -run "TestRoomLifecycle|TestAntiCascadeRoleFilter|TestPostMessageAndSlidingWindow"
go test -v ./internal/modules -run "TestScenario1_PMGroupChatCollaboration"
echo "✔ 纯后台无 UI 群聊协同闭环，防死循环铁律（Bot 回复 @ 强制降级纯展示、0 下游任务）通过"

step "步骤 8：探针网格受控调度与噪音日志两阶段智能蒸馏"
go test -v ./internal/modules/agentmesh -run "TestCommandWhitelistAndTimeoutClamping|TestDistillationEngineRules|TestRealOSCommandExecution"
go test -v ./internal/modules -run "TestScenario2_CICDProbeBuildDistillation"
echo "✔ 探针受控调度、超时/白名单保护与海量日志两阶段蒸馏（Bot 上下文零膨胀）通过"

step "步骤 9：Bot 蓝图工厂合规校验、记忆硬约束与配置中心提案编排"
go test -v ./internal/modules/botfactory -run "TestValidateBlueprint|TestAssemblyAndPropose|TestRestartInstance"
go test -v ./internal/modules -run "TestScenario3_SpecializedBotDerivation"
echo "✔ Bot 工厂保留名拦截、200 行单体记忆硬拦截、配置中心提案与优雅重启通过"

step "步骤 10：四方跨模块协同编排全链路实战演练（End-to-End Orchestration）"
go test -v ./internal/modules -run "TestTier3_PairwiseCrossFeatureInteractions"
echo "✔ 七组跨特性与四方协同端到端实战编排全链路演练全绿通过"

echo -e "\n========================================================"
echo "🎉 恭喜！NexusCore M8 基础模块无头化沉淀与依赖治理全链路验收成功！"
echo "========================================================"
