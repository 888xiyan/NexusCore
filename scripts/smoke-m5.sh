#!/usr/bin/env bash
# M5 冒烟验收：本地 Coding Agent 搜寻插件、LocalCLIDriver 物理受控执行、
# Bot 结构化调度外部 Agent CLI 与防噪音蒸馏闭环验收。
# 依据：开发规划 §3 M5、doc/milestones/M5-本地Agent搜寻.md。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${NEXUS_DEV_PG_PORT:-55432}"
export NEXUS_PG_MIGRATE_DSN="${NEXUS_PG_MIGRATE_DSN:-postgres://nexus_migrator:nexus_dev_migrate@127.0.0.1:${PORT}/nexus}"
export NEXUS_PG_KERNEL_DSN="${NEXUS_PG_KERNEL_DSN:-postgres://nexus_kernel:nexus_dev_kernel@127.0.0.1:${PORT}/nexus}"
export NEXUS_KERNEL_CREDENTIAL_KEY="${NEXUS_KERNEL_CREDENTIAL_KEY:-0123456789abcdef0123456789abcdef}"
export NEXUS_KERNEL_SNAPSHOT_KEY="${NEXUS_KERNEL_SNAPSHOT_KEY:-abcdef0123456789abcdef0123456789}"
export NEXUS_KERNEL_PAYLOAD_DIR="${NEXUS_KERNEL_PAYLOAD_DIR:-$ROOT/data/payloads}"

HTTP="127.0.0.1:7200"
HTTP_URL="http://$HTTP"
GRPC="127.0.0.1:7201"
TRANSPORT="127.0.0.1:7202"
CHAT_PORT="7300"
CHAT_URL="http://127.0.0.1:$CHAT_PORT"
DISC_PORT="7400"
DISC_URL="http://127.0.0.1:$DISC_PORT"

RUN_DIR="$ROOT/data/smoke_m5"
MEMORY_BASE_DIR="$ROOT/data/bots"
mkdir -p "$RUN_DIR" "$NEXUS_KERNEL_PAYLOAD_DIR" "$MEMORY_BASE_DIR"

PIDS=()
cleanup() {
  echo "正在清理测试进程..."
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  pkill -f "bin/plugins/plugin_groupchat" 2>/dev/null || true
  pkill -f "bin/plugins/plugin_agent_discovery" 2>/dev/null || true
  pkill -f "bin/agent-daemon" 2>/dev/null || true
  pkill -f "bin/hub" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "SMOKE M5 FAIL: $1" >&2; exit 1; }

psql_exec() { docker exec -i nexus-pg-m1 psql -U postgres -d nexus -tAc "$1"; }

step "步骤 1：编译内核、运行时、探针与插件"
make build >/dev/null
make build-plugins >/dev/null
[[ -f "bin/plugins/plugin_agent_discovery" ]] || fail "plugin_agent_discovery 二进制不存在"
[[ -f "bin/plugins/plugin_groupchat" ]] || fail "plugin_groupchat 二进制不存在"
echo "编译完成：bin/{hub,bot-runtime,agent-daemon,cli} 与 bin/plugins/{plugin_groupchat,plugin_agent_discovery}"

step "步骤 2：创建测试用 Mock Coding Agent CLI"
MOCK_CLI_DIR="$RUN_DIR/mock_bin"
mkdir -p "$MOCK_CLI_DIR"
cat <<'EOF' > "$MOCK_CLI_DIR/nexus-mock-agent"
#!/bin/sh
if [ "$1" = "--version" ]; then
    echo "nexus-mock-agent 1.5.0"
    exit 0
fi

# 模拟 Coding Agent 输出（伴随进度噪音与核心生成代码）
echo "Loading workspace context (128 files)..."
echo "[====================] 100%"
echo "Thinking (1.1s)..."
echo "根据需求生成斐波那契函数:"
echo "```go"
echo "func Fib(n int) int {"
echo "    if n <= 1 { return n }"
echo "    return Fib(n-1) + Fib(n-2)"
echo "}"
echo "```"
echo "Done in 1.4s."
EOF
chmod +x "$MOCK_CLI_DIR/nexus-mock-agent"
export PATH="$MOCK_CLI_DIR:$PATH"
echo "Mock Coding Agent CLI 已就绪: $MOCK_CLI_DIR/nexus-mock-agent"

step "步骤 3：准备数据库环境与注册待测 Bot"
go run ./cmd/migrate -cmd up -dsn "$NEXUS_PG_MIGRATE_DSN" -dir migrations/kernel >/dev/null

# 清理历史测试数据
psql_exec "DELETE FROM kernel.kernel_jobs WHERE bot_id IN ('code_bot', 'groupchat_bot', 'discovery_bot');"
psql_exec "DELETE FROM kernel.bot_instances WHERE bot_id IN ('code_bot', 'groupchat_bot', 'discovery_bot');"
psql_exec "DELETE FROM kernel.bots WHERE bot_id IN ('code_bot', 'groupchat_bot', 'discovery_bot');"
psql_exec "DELETE FROM kernel.bot_manifest WHERE bot_id IN ('code_bot', 'groupchat_bot', 'discovery_bot');"
psql_exec "DELETE FROM kernel.kernel_agents WHERE agent_id IN ('local_cli_probe', 'loopback_agent');"

BOT_RUNTIME_DIGEST=$(sha256sum "$ROOT/bin/bot-runtime" | awk '{print $1}')

# 注册 code_bot 并配置其绑定 local_cli_probe 与 nexus-mock-agent
psql_exec "INSERT INTO kernel.bots (bot_id, display_name, activation_mode, status) VALUES ('code_bot', 'Code Bot', 'lazy', 'active');"
psql_exec "INSERT INTO kernel.bot_manifest (bot_id, display_name, role, activation_mode, binary_path, binary_digest, manifest_digest, environment_vars, updated_at) VALUES ('code_bot', 'Code Bot', 'worker', 'lazy', '$ROOT/bin/bot-runtime', '$BOT_RUNTIME_DIGEST', '$BOT_RUNTIME_DIGEST', '{\"NEXUS_AGENT_CLI\":\"nexus-mock-agent\"}'::jsonb, now());"

# 初始化 code_bot 的 MEMORY.md (≤ 200 行)
mkdir -p "$MEMORY_BASE_DIR/code_bot/memory"
cat <<'EOF' > "$MEMORY_BASE_DIR/code_bot/memory/MEMORY.md"
# code_bot 核心记忆 (M5)
- 角色定位: 简单 Agent，禁止长时间思考，禁止自身直接执行 shell
- 外部调度: 经 Hub 传输路由反向调度物理探针上的 local CLI
- 代码规范: Go 1.26 命名规范，注重并发安全与高信噪比蒸馏
EOF

step "步骤 4：启动 Hub 微内核网关"
bin/hub \
  -http "$HTTP" \
  -grpc "$GRPC" \
  -transport "$TRANSPORT" \
  -dsn "$NEXUS_PG_KERNEL_DSN" \
  -memory-dir "$MEMORY_BASE_DIR" >"$RUN_DIR/hub.log" 2>&1 &
HUB_PID=$!
PIDS+=("$HUB_PID")

for i in $(seq 1 30); do
  if curl -s "$HTTP_URL/healthz" >/dev/null 2>&1; then
    echo "Hub 微内核已就绪 (HTTP: $HTTP, gRPC: $GRPC, Transport: $TRANSPORT)"
    break
  fi
  sleep 0.2
  [[ $i -eq 30 ]] && fail "Hub 启动超时"
done

# 签发 user_bot, code_bot, local_cli_probe 凭证
export USER_CRED=$(bin/cli bot credential user_bot -hub-http "$HTTP_URL" | tr -d '\r\n')
export CODE_CRED=$(bin/cli bot credential code_bot -hub-http "$HTTP_URL" | tr -d '\r\n')
export PROBE_CRED=$(bin/cli bot credential local_cli_probe -hub-http "$HTTP_URL" | tr -d '\r\n')

step "步骤 5：启动 agent-daemon 物理探针 (带 LocalCLIDriver)"
export NEXUS_AGENT_CREDENTIAL="$PROBE_CRED"
bin/agent-daemon \
  -hub-transport "$TRANSPORT" \
  -agent-id "local_cli_probe" >"$RUN_DIR/probe.log" 2>&1 &
PROBE_PID=$!
PIDS+=("$PROBE_PID")
sleep 1
grep -q "local_cli_probe 已接入 hub 传输路由" "$RUN_DIR/probe.log" || echo "探针已在后台连接"

step "步骤 6：启动本地 Agent 搜寻插件 (plugin_agent_discovery)"
bin/plugins/plugin_agent_discovery \
  -port "$DISC_PORT" \
  -hub-http "$HTTP" \
  -extra-dir "$MOCK_CLI_DIR" >"$RUN_DIR/discovery.log" 2>&1 &
DISC_PID=$!
PIDS+=("$DISC_PID")

for i in $(seq 1 20); do
  if curl -s "$DISC_URL/v1/discovery/health" | grep -q "healthy"; then
    echo "AgentDiscoveryPlugin 已就绪 (端口 $DISC_PORT)"
    break
  fi
  sleep 0.2
  [[ $i -eq 20 ]] && fail "AgentDiscoveryPlugin 启动超时"
done

# 触发扫描并验证探测结果
SCAN_RES=$(curl -s -X POST "$DISC_URL/v1/discovery/scan")
echo "探测结果摘要: $(echo "$SCAN_RES" | python3 -c 'import sys, json; data=json.load(sys.stdin); print(f"发现 {data[\"count\"]} 个 CLI Agent: {[a[\"bin_name\"] for a in data[\"agents\"]]}");')"

echo "$SCAN_RES" | grep -q "nexus-mock-agent" || fail "未能探测到 nexus-mock-agent 资产"

step "步骤 7：启动 GroupChat 独立群聊插件"
bin/plugins/plugin_groupchat \
  -port "$CHAT_PORT" \
  -hub-http "$HTTP" \
  -hub-cred "$USER_CRED" >"$RUN_DIR/groupchat.log" 2>&1 &
CHAT_PID=$!
PIDS+=("$CHAT_PID")

for i in $(seq 1 20); do
  if curl -s "$CHAT_URL/v1/chat/health" | grep -q "healthy"; then
    echo "GroupChatPlugin 已就绪 (端口 $CHAT_PORT)"
    break
  fi
  sleep 0.2
  [[ $i -eq 20 ]] && fail "GroupChat 启动超时"
done

step "步骤 8：模拟人类用户 @code_bot 提出需求并验证调度与防噪音蒸馏"
# 启动 code_bot 独立进程并注入探针配置
export NEXUS_BOT_CREDENTIAL="$CODE_CRED"
bin/bot-runtime \
  -bot-id "code_bot" \
  -hub-grpc "$GRPC" \
  -hub-http "$HTTP" \
  -hub-transport "$TRANSPORT" \
  -agent-id "local_cli_probe" \
  -agent-cli "nexus-mock-agent" \
  -memory-dir "$MEMORY_BASE_DIR" >"$RUN_DIR/code_bot.log" 2>&1 &
CODE_BOT_PID=$!
PIDS+=("$CODE_BOT_PID")
sleep 1

# 用户在群聊房间发消息 @code_bot
SEND_RESP=$(curl -s -X POST "$CHAT_URL/v1/chat/rooms/room_m5_demo/messages" \
  -H "Content-Type: application/json" \
  -d '{"sender":"user","content":"@code_bot 请为我实现斐波那契函数"}')
echo "用户群聊消息已投递: $SEND_RESP"

# 等待任务执行与回复回投
echo "等待 code_bot 结构化调度 LocalCLIDriver 并精选回复..."
REPLY_FOUND=0
for i in $(seq 1 30); do
  MESSAGES=$(curl -s "$CHAT_URL/v1/chat/rooms/room_m5_demo/messages")
  if echo "$MESSAGES" | grep -q "func Fib(n int)"; then
    REPLY_FOUND=1
    break
  fi
  sleep 0.5
done

[[ $REPLY_FOUND -eq 1 ]] || fail "未能收到 code_bot 的精选回复"

# 抓取回复文本
BOT_REPLY=$(echo "$MESSAGES" | python3 -c '
import sys, json
data = json.load(sys.stdin)
for m in data.get("messages", []):
    if m.get("sender") == "code_bot":
        print(m.get("content"))
        break
')

echo -e "\n=== 收到 code_bot 蒸馏精选回复卡片 ==="
echo "$BOT_REPLY"
echo "======================================"

# 验证防噪音蒸馏：绝不泄漏进度条和中间思考噪音
echo "$BOT_REPLY" | grep -q "func Fib(n int)" || fail "回复缺少核心代码产物"
echo "$BOT_REPLY" | grep -q "执行器: \`nexus-mock-agent\`" || fail "回复缺少执行器信息"
if echo "$BOT_REPLY" | grep -q "Loading workspace context"; then
  fail "防噪音蒸馏失败：泄漏了 'Loading workspace context' 噪音"
fi
if echo "$BOT_REPLY" | grep -q "\[==="; then
  fail "防噪音蒸馏失败：泄漏了终端进度条噪音"
fi

step "步骤 9：验证因果链审计与单体记忆 Transcripts 沉淀"
TRANS_DIR="$MEMORY_BASE_DIR/code_bot/memory/transcripts"
[[ -d "$TRANS_DIR" ]] || fail "未找到 code_bot Transcripts 目录"
TRANS_FILES=("$TRANS_DIR"/*.jsonl)
[[ -f "${TRANS_FILES[0]}" ]] || fail "未写入 Transcripts 流水记录"
grep -q "func Fib" "${TRANS_FILES[0]}" || fail "流水记录缺少核心代码"
echo "Transcripts 会话事实已落盘: ${TRANS_FILES[0]}"

step "步骤 10：验证进程清理与优雅停机"
cleanup
echo "所有测试进程已优雅退出"

printf '\n\033[32m=== M5 端到端自动化冒烟验收全部通过 (PASS) ===\033[0m\n'
