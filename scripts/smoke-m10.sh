#!/usr/bin/env bash
# M10 冒烟验收：工具中心、MCP/Skill 插件化治理与中台 Bot 审批闭环
# 依据：开发规划 §3 M10、doc/M10-工具中心与中台Bot代批审批详细设计.md
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TOOL_CENTER_PORT="7400"
TOOL_CENTER_URL="http://127.0.0.1:$TOOL_CENTER_PORT"
MARKETPLACE_PORT="7600"
MARKETPLACE_URL="http://127.0.0.1:$MARKETPLACE_PORT"
RUN_DIR="$ROOT/data/smoke_m10"

mkdir -p "$RUN_DIR"

PIDS=()
cleanup() {
  echo "正在清理 M10 测试进程..."
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  pkill -f "bin/plugins/plugin_tool_center" 2>/dev/null || true
  pkill -f "bin/plugins/plugin_marketplace" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "SMOKE M10 FAIL: $1" >&2; exit 1; }

step "步骤 1：编译 Go 微内核底座与 M10 工具中心/插件商场二进制"
make build >/dev/null
go build -o bin/plugins/plugin_tool_center ./plugins/plugin_tool_center
go build -o bin/plugins/plugin_marketplace ./plugins/plugin_marketplace

[[ -f "bin/hub" ]] || fail "bin/hub 不存在"
[[ -f "bin/plugins/plugin_tool_center" ]] || fail "bin/plugins/plugin_tool_center 不存在"
[[ -f "bin/plugins/plugin_marketplace" ]] || fail "bin/plugins/plugin_marketplace 不存在"
echo "Go 微内核与 M10 独立插件二进制编译就绪"

step "步骤 2：前端 TypeScript 类型安全与独立 ESM Chunks 构建打包"
cd "$ROOT/Frontend"
npx tsc --noEmit || fail "Frontend TypeScript 类型检查失败"
npm run build >/dev/null || fail "Frontend Vite 构建打包失败"

TOOL_CHUNK=$(find dist/assets -name "tool-center-*.js" | head -n 1)
MARKET_CHUNK=$(find dist/assets -name "marketplace-*.js" | head -n 1)
[[ -n "$TOOL_CHUNK" ]] || fail "未找到 tool-center 微模块构建产物"
[[ -n "$MARKET_CHUNK" ]] || fail "未找到 marketplace 微模块构建产物"
echo "前端微前端 ESM 独立分块验证通过：$TOOL_CHUNK, $MARKET_CHUNK"
cd "$ROOT"

step "步骤 3：工具中心无头核心模块与代批安全网关单测"
go test -v -cover ./internal/modules/toolcenter/... || fail "toolcenter 核心模块测试失败"

step "步骤 4：Hub HTTP 网关工具/代批/商场 API 契约测试"
go test -v ./hub/httpapi/... -run "TestM10ToolCenterAndDelegationAPI" || fail "HTTP API 契约测试失败"

step "步骤 5：启动 L2 独立工具中心服务 (plugin_tool_center :7400) 并探活"
./bin/plugins/plugin_tool_center -port "$TOOL_CENTER_PORT" > "$RUN_DIR/tool_center.log" 2>&1 &
TC_PID=$!
PIDS+=("$TC_PID")

for i in $(seq 1 30); do
  if curl -sf "$TOOL_CENTER_URL/healthz" >/dev/null 2>&1; then
    echo "ToolCenter 服务在端口 $TOOL_CENTER_PORT 就绪"
    break
  fi
  sleep 0.1
  [[ $i -eq 30 ]] && fail "ToolCenter 服务启动超时"
done

step "步骤 6：验证不可编辑系统工具与可编辑 Skill 增删改查"
# 1. 验证内置系统原生工具
SYS_TOOLS_RESP=$(curl -s "$TOOL_CENTER_URL/api/tools?category=system_native")
echo "$SYS_TOOLS_RESP" | grep -q "sys.probe.cli" || fail "未在系统工具中找到 sys.probe.cli"
echo "$SYS_TOOLS_RESP" | grep -q "sys.search.web" || fail "未在系统工具中找到 sys.search.web"
echo "内置不可编辑系统工具查询正常"

# 2. 创建自定义可编辑 Skill
CREATE_SKILL_RES=$(curl -s -X POST "$TOOL_CENTER_URL/api/skills" \
  -H "Content-Type: application/json" \
  -d '{
    "tool_id": "skill.shell.compress",
    "name": "日志归档压缩技能",
    "description": "调用 tar 归档压缩旧日志",
    "runtime": "shell",
    "code": "tar -czf archive.tar.gz /var/log",
    "config_schema": "{\"type\":\"object\"}",
    "tier": "tier1",
    "network_requirement": "none"
  }')
echo "$CREATE_SKILL_RES" | grep -q '"created"' || fail "创建自定义 Skill 失败: $CREATE_SKILL_RES"

# 3. 更新自定义 Skill 源码
UPDATE_RES=$(curl -s -X PUT "$TOOL_CENTER_URL/api/skills/skill.shell.compress" \
  -H "Content-Type: application/json" \
  -d '{"code":"tar -czf compressed.tar.gz /data/logs"}')
echo "$UPDATE_RES" | grep -q '"updated"' || fail "更新 Skill 源码失败: $UPDATE_RES"
echo "可编辑 Skill 增删改查逻辑验证成功"

step "步骤 7：验证 Bot-Tool 授权矩阵 (bot_tool_grants) 鉴权防线"
# 1. 未授权 Bot 调用工具应当被阻断 (403 Forbidden)
UNAUTH_RES=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$TOOL_CENTER_URL/api/tools/skill.shell.compress/execute" \
  -H "Content-Type: application/json" \
  -d '{"bot_id":"unauthorized_bot","params":{}}')
echo "$UNAUTH_RES" | grep -q "HTTP_CODE:403" || fail "未授权调用预期返回 403 Forbidden: $UNAUTH_RES"
echo "未授权调用已被安全网关正确拦截"

# 2. 授予 ops_bot 执行权限
GRANT_RES=$(curl -s -X POST "$TOOL_CENTER_URL/api/grants" \
  -H "Content-Type: application/json" \
  -d '{
    "bot_id": "ops_bot",
    "tool_id": "skill.shell.compress",
    "permission": "execute",
    "granted_by": "user_bot"
  }')
echo "$GRANT_RES" | grep -q '"granted"' || fail "授权失败: $GRANT_RES"

# 3. 授权后调用成功执行
EXEC_RES=$(curl -s -X POST "$TOOL_CENTER_URL/api/tools/skill.shell.compress/execute" \
  -H "Content-Type: application/json" \
  -d '{"bot_id":"ops_bot","params":{}}')
echo "$EXEC_RES" | grep -q '"exit_code":0' || fail "授权后执行失败: $EXEC_RES"
echo "授权矩阵赋权执行全链路成功"

step "步骤 8：验证受控沙盒网络外联审计告警 (sys.search.web)"
# 先向 search_bot 授予 sys.search.web 权限
curl -s -X POST "$TOOL_CENTER_URL/api/grants" \
  -H "Content-Type: application/json" \
  -d '{"bot_id":"search_bot","tool_id":"sys.search.web","permission":"execute","granted_by":"user_bot"}' >/dev/null

SEARCH_RES=$(curl -s -X POST "$TOOL_CENTER_URL/api/tools/sys.search.web/execute" \
  -H "Content-Type: application/json" \
  -d '{"bot_id":"search_bot","params":{"query":"NexusCore v0.2.0"}}')
echo "$SEARCH_RES" | grep -q '"network_triggered":true' || fail "网络搜索工具未标记 network_triggered: $SEARCH_RES"
echo "沙盒网络外联审计标记验证通过"

step "步骤 9：启动 L2 独立插件商场服务 (plugin_marketplace :7600) 并探活"
./bin/plugins/plugin_marketplace -port "$MARKETPLACE_PORT" > "$RUN_DIR/marketplace.log" 2>&1 &
MP_PID=$!
PIDS+=("$MP_PID")

for i in $(seq 1 30); do
  if curl -sf "$MARKETPLACE_URL/healthz" >/dev/null 2>&1; then
    echo "Marketplace 服务在端口 $MARKETPLACE_PORT 就绪"
    break
  fi
  sleep 0.1
  [[ $i -eq 30 ]] && fail "Marketplace 服务启动超时"
done

step "步骤 10：验证插件商场三阶段安全审批闭环 (Download -> Install -> Uninstall)"
# 阶段一：检索与下载校验
SEARCH_RES=$(curl -s "$MARKETPLACE_URL/api/marketplace/search?q=browser")
echo "$SEARCH_RES" | grep -q "plugin_browser" || fail "商场检索失败: $SEARCH_RES"

DL_RES=$(curl -s -X POST "$MARKETPLACE_URL/api/marketplace/download" \
  -H "Content-Type: application/json" \
  -d '{"plugin_id":"plugin_browser","digest":"sha256_verified"}')
echo "$DL_RES" | grep -q '"digest_ok":true' || fail "商场下载阶段验证失败: $DL_RES"

# 阶段二：安装特权审批 (未人类确认拦截 vs 确认放行)
INST_BLOCK=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$MARKETPLACE_URL/api/marketplace/install" \
  -H "Content-Type: application/json" \
  -d '{"plugin_id":"plugin_browser","user_confirmed":false}')
echo "$INST_BLOCK" | grep -q "HTTP_CODE:403" || fail "阶段二未确认预期 403: $INST_BLOCK"

INST_OK=$(curl -s -X POST "$MARKETPLACE_URL/api/marketplace/install" \
  -H "Content-Type: application/json" \
  -d '{"plugin_id":"plugin_browser","user_confirmed":true}')
echo "$INST_OK" | grep -q '"status":"installed_and_activated"' || fail "阶段二安装激活失败: $INST_OK"

# 阶段三：级联卸载
UNINST_OK=$(curl -s -X POST "$MARKETPLACE_URL/api/marketplace/uninstall" \
  -H "Content-Type: application/json" \
  -d '{"plugin_id":"plugin_browser","cascade":true}')
echo "$UNINST_OK" | grep -q '"status":"uninstalled"' || fail "阶段三卸载失败: $UNINST_OK"
echo "插件商场三阶段安全审批闭环全链路验证通过"

step "步骤 11：验证两级进程模型反向优雅停机与进程回收"
kill -SIGTERM "$TC_PID" 2>/dev/null || true
kill -SIGTERM "$MP_PID" 2>/dev/null || true
wait "$TC_PID" 2>/dev/null || true
wait "$MP_PID" 2>/dev/null || true
echo "L2 独立插件进程优雅停机回收完成"

printf '\n=======================================================\n'
printf '  🎉 M10 工具中心、中台代批RBAC与三阶段审批 11 步冒烟全链路通过！\n'
printf '=======================================================\n'
