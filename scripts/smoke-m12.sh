#!/usr/bin/env bash
# M12 冒烟：工具插件自有存储。创建 Skill 并授权，重启后仍在；
# 不可编辑工具拒绝修改；未点名 Bot 不能获得互联网搜索授权。
# 依据：开发规划 M12，需求规格 R6 / R14 / R16。不新建内核表，不把正文写入配置中心。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TOOL_CENTER_PORT="7412"
TOOL_CENTER_URL="http://127.0.0.1:$TOOL_CENTER_PORT"
RUN_DIR="$ROOT/data/smoke_m12"
DATA_DIR="$RUN_DIR/tool_store"
mkdir -p "$RUN_DIR"
rm -rf "$DATA_DIR"

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "SMOKE M12 FAIL: $1" >&2; exit 1; }

step "步骤 1：确认内核不新增工具表，并编译工具插件"
if grep -R -E "CREATE TABLE.*(kernel_tool_trajectories|kernel_bot_tool_grants|kernel_plugin_audit_logs)" migrations/kernel >/dev/null; then
  fail "内核迁移出现了禁止的工具/授权/审计表"
fi
go test ./internal/modules/toolcenter/ ./hub/httpapi/ -count=1 -run 'TestPersistentCatalogSurvivesRestart|TestToolCenterService_BuiltinAndSkills|TestToolCenterService_ExecutionAndNetworkAudit|TestRewriteToolPath|TestToolRelayDoesNotStoreSkillSource|TestM10ToolCenterAndDelegationAPI' \
  || fail "工具持久化或 Hub 转发测试失败"
go build -o bin/plugins/plugin_tool_center ./plugins/plugin_tool_center
echo "内核迁移未新增工具表，单测通过"

start_plugin() {
  ./bin/plugins/plugin_tool_center -port "$TOOL_CENTER_PORT" -data "$DATA_DIR" >"$RUN_DIR/tool_center.log" 2>&1 &
  local pid=$!
  PIDS+=("$pid")
  for _ in $(seq 1 30); do
    curl -sf "$TOOL_CENTER_URL/healthz" >/dev/null 2>&1 && return 0
    sleep 0.1
  done
  fail "ToolCenter 启动超时"
}

step "步骤 2：启动工具插件并创建 Skill、授权用户 Bot"
start_plugin
CREATE=$(curl -s -X POST "$TOOL_CENTER_URL/api/skills" \
  -H "Content-Type: application/json" \
  -d '{"tool_id":"skill.m12.notes","name":"笔记技能","code":"print(\"v1\")","runtime":"python"}')
echo "$CREATE" | grep -q '"created"' || fail "创建 Skill 失败: $CREATE"
GRANT=$(curl -s -X POST "$TOOL_CENTER_URL/api/grants" \
  -H "Content-Type: application/json" \
  -d '{"bot_id":"user_m12_bot","tool_id":"skill.m12.notes","permission":"execute","granted_by":"user_bot"}')
echo "$GRANT" | grep -q '"granted"' || fail "授权失败: $GRANT"

step "步骤 3：不可编辑工具拒绝修改和删除；未点名 Bot 不能获得搜索授权"
UPD=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X PUT "$TOOL_CENTER_URL/api/skills/sys.probe.cli" \
  -H "Content-Type: application/json" \
  -d '{"code":"print(\"hacked\")"}')
echo "$UPD" | grep -q "HTTP_CODE:400" || fail "修改系统工具应返回 400: $UPD"
DEL=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X DELETE "$TOOL_CENTER_URL/api/skills/sys.search.web")
echo "$DEL" | grep -q "HTTP_CODE:400" || fail "删除系统工具应返回 400: $DEL"
UNNAMED=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$TOOL_CENTER_URL/api/grants" \
  -H "Content-Type: application/json" \
  -d '{"bot_id":"user_other_bot","tool_id":"sys.search.web","permission":"execute","granted_by":"user_bot"}')
echo "$UNNAMED" | grep -q "HTTP_CODE:403" || fail "未点名 Bot 授予搜索应返回 403: $UNNAMED"

step "步骤 4：Skill 修改留下版本；MCP 拒绝明文并接受 secret_ref"
UPD_SKILL=$(curl -s -X PUT "$TOOL_CENTER_URL/api/skills/skill.m12.notes" \
  -H "Content-Type: application/json" \
  -d '{"code":"print(\"v2\")"}')
echo "$UPD_SKILL" | grep -q '"updated"' || fail "更新 Skill 失败: $UPD_SKILL"
REVS=$(curl -s "$TOOL_CENTER_URL/api/skills/skill.m12.notes/revisions")
python3 -c 'import json,sys; body=json.loads(sys.argv[1]); codes=[item.get("code") for item in body.get("revisions",[])];
assert "print(\"v1\")" in codes, codes' "$REVS" || fail "版本记录缺少旧正文: $REVS"
BAD_MCP=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$TOOL_CENTER_URL/api/mcp" \
  -H "Content-Type: application/json" \
  -d '{"tool_id":"mcp.bad","name":"Bad","endpoint_ref":"https://example.test?api_key=abcd"}')
echo "$BAD_MCP" | grep -q "HTTP_CODE:400" || fail "明文 MCP 应被拒绝: $BAD_MCP"
OK_MCP=$(curl -s -X POST "$TOOL_CENTER_URL/api/mcp" \
  -H "Content-Type: application/json" \
  -d '{"tool_id":"mcp.github","name":"GitHub","endpoint_ref":"stdio://github","secret_ref":"secret://mcp/github"}')
echo "$OK_MCP" | grep -q '"created"' || fail "MCP 登记失败: $OK_MCP"

step "步骤 5：未授权调用被拒绝并写入插件轨迹"
UNAUTH=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$TOOL_CENTER_URL/api/tools/skill.m12.notes/execute" \
  -H "Content-Type: application/json" \
  -d '{"bot_id":"user_stranger_bot","params":{"q":"x"}}')
echo "$UNAUTH" | grep -q "HTTP_CODE:403" || fail "未授权调用应返回 403: $UNAUTH"
TRACES=$(curl -s "$TOOL_CENTER_URL/api/traces?bot_id=user_stranger_bot")
echo "$TRACES" | grep -q '"allowed":false' || fail "未授权轨迹未留下: $TRACES"

step "步骤 6：重启工具插件后分类、正文与授权仍在"
kill "${PIDS[-1]}" 2>/dev/null || true
wait "${PIDS[-1]}" 2>/dev/null || true
unset 'PIDS[-1]'
start_plugin
TOOL=$(curl -s "$TOOL_CENTER_URL/api/tools/skill.m12.notes")
python3 -c 'import json,sys; tool=json.loads(sys.argv[1]);
assert tool.get("code")=="print(\"v2\")", tool.get("code");
assert tool.get("category")=="editable_skill", tool.get("category")' "$TOOL" \
  || fail "重启后 Skill 正文或分类丢失: $TOOL"
GRANTS=$(curl -s "$TOOL_CENTER_URL/api/grants?bot_id=user_m12_bot")
echo "$GRANTS" | grep -q 'skill.m12.notes' || fail "重启后授权丢失: $GRANTS"
MCP=$(curl -s "$TOOL_CENTER_URL/api/tools/mcp.github")
python3 -c 'import json,sys; tool=json.loads(sys.argv[1]);
assert tool.get("secret_ref")=="secret://mcp/github", tool;
assert not tool.get("code"), tool' "$MCP" || fail "重启后 MCP 登记不符: $MCP"
if grep -q 'print("v2")' "$RUN_DIR/tool_center.log"; then
  fail "插件日志不应打印 Skill 正文"
fi
echo "重启后 Skill、授权与 MCP 登记仍在工具目录 $DATA_DIR"

echo
echo "SMOKE M12 PASS"
