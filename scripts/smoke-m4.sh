#!/usr/bin/env bash
# ==============================================================================
# NexusCore M4 冒烟验收测试 (11-Step Hermetic End-to-End Smoke Verification)
# 
# 验证范围：
# 1. Frontend 独立工程编译构建与产物校验 (npm run build -> dist/index.html)
# 2. 数据库迁移、测试实体清理与制品摘要同步
# 3. 待测 Bot (ops_bot, qa_bot) 注册与单体记忆栈 (MEMORY.md ≤ 200 行硬约束)
# 4. Hub 微内核网关启动 (:7200, :7201, :7202) 与受控 loopback_agent 探针接入
# 5. GroupChatPlugin 独立插件服务启动与生命周期 (:7300)
# 6. Frontend 静态预览服务启动 (:5173) 与 HTTP 200 探活
# 7. Gateway CORS 预检与 SSE 实时事件流端点验证
# 8. 用户显式 @ops_bot 触发调度、loopback_agent 执行与 Transcripts 流水回投
# 9. 防死循环铁律：Bot 回复中含 @qa_bot 保持静默 (0 jobs)
# 10. Bot 单体记忆管理 (200 vs 201 行硬约束强校验) 与重启策略 (Restart Policy) 生效
# 11. Agent 实时执行帧捕获、状态流转、终端回显与优雅停机
#
# 依据：开发规划 §3 M4、AGENTS.md、ORIGINAL_REQUEST.md、PROJECT.md、TEST_INFRA.md。
# ==============================================================================
set -euo pipefail

# ------------------------------------------------------------------------------
# 基础路径与环境初始化
# ------------------------------------------------------------------------------
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
FRONTEND_PORT="5173"
FRONTEND_URL="http://127.0.0.1:$FRONTEND_PORT"

RUN_DIR="$ROOT/data/smoke_m4"
MEMORY_BASE_DIR="$ROOT/data/bots"
mkdir -p "$RUN_DIR" "$NEXUS_KERNEL_PAYLOAD_DIR" "$MEMORY_BASE_DIR"

# ------------------------------------------------------------------------------
# 动态检测 PostgreSQL 容器 (兼容 nexus-pg-m1 与 nexus-pg-m2)
# ------------------------------------------------------------------------------
PG_CONTAINER="${NEXUS_DEV_PG_CONTAINER:-}"
if [[ -z "$PG_CONTAINER" ]]; then
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "nexus-pg-m2"; then
    PG_CONTAINER="nexus-pg-m2"
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "nexus-pg-m1"; then
    PG_CONTAINER="nexus-pg-m1"
  else
    PG_CONTAINER="nexus-pg-m1"
  fi
fi

psql_exec() {
  docker exec -i "$PG_CONTAINER" psql -U postgres -d nexus -tAc "$1"
}

# ------------------------------------------------------------------------------
# 进程管理与安全退出 (Trap)
# ------------------------------------------------------------------------------
PIDS=()
cleanup() {
  echo ""
  echo "正在执行环境回收与测试进程清理..."
  # 1. 逆序发送 SIGTERM 优雅停止各测试进程
  for ((i=${#PIDS[@]}-1; i>=0; i--)); do
    pid="${PIDS[i]}"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
    fi
  done
  
  # 兜底发送 TERM 信号给已知测试子进程
  pkill -TERM -f "vite.*5173" 2>/dev/null || true
  pkill -TERM -f "node.*5173" 2>/dev/null || true
  pkill -TERM -f "bin/plugins/plugin_groupchat" 2>/dev/null || true
  pkill -TERM -f "bin/bot-runtime" 2>/dev/null || true
  
  # 2. 超时监控与 SIGKILL 升级 (最多等待 4 秒，按 0.2 秒轮询)
  for pid in "${PIDS[@]}"; do
    local cycles=0
    while kill -0 "$pid" 2>/dev/null; do
      sleep 0.2
      cycles=$((cycles + 1))
      if [[ "$cycles" -ge 20 ]]; then
        kill -9 "$pid" 2>/dev/null || true
        break
      fi
    done
  done
  
  # 3. 彻底回收任何可能遗留的孤儿进程
  pkill -9 -f "vite.*5173" 2>/dev/null || true
  pkill -9 -f "node.*5173" 2>/dev/null || true
  pkill -9 -f "bin/plugins/plugin_groupchat" 2>/dev/null || true
  pkill -9 -f "bin/bot-runtime" 2>/dev/null || true
  
  # 4. 回收已终止子进程资源，杜绝僵尸进程
  for pid in "${PIDS[@]}"; do
    wait "$pid" 2>/dev/null || true
  done
  echo "清理完成。"
}
trap cleanup EXIT INT TERM

step() { printf '\n\033[34m=== %s ===\033[0m\n' "$1"; }
fail() { printf '\n\033[31mSMOKE M4 FAIL: %s\033[0m\n' "$1" >&2; exit 1; }

# ==============================================================================
# 步骤 1：编译 Go 内核/插件制品与构建 Frontend 独立工程
# ==============================================================================
step "步骤 1：编译 Go 内核/插件制品与构建 Frontend 独立工程"
make build >/dev/null
make build-plugins >/dev/null
[[ -f "bin/hub" ]] || fail "bin/hub 二进制不存在"
[[ -f "bin/bot-runtime" ]] || fail "bin/bot-runtime 二进制不存在"
[[ -f "bin/agent-daemon" ]] || fail "bin/agent-daemon 二进制不存在"
[[ -f "bin/cli" ]] || fail "bin/cli 二进制不存在"
[[ -f "bin/plugins/plugin_groupchat" ]] || fail "bin/plugins/plugin_groupchat 二进制不存在"
echo "  Go 二进制编译完成: bin/{hub,bot-runtime,agent-daemon,cli} 与 bin/plugins/plugin_groupchat"

if [[ -d "$ROOT/Frontend" && -f "$ROOT/Frontend/package.json" ]]; then
  echo "  正在执行 Frontend 工程构建 (npm run build)..."
  (cd "$ROOT/Frontend" && npm run build >"$RUN_DIR/frontend_build.log" 2>&1) || {
    cat "$RUN_DIR/frontend_build.log"
    fail "Frontend 打包失败 (npm run build 退出码非 0)"
  }
  [[ -f "$ROOT/Frontend/dist/index.html" ]] || fail "Frontend 打包产物 dist/index.html 缺失"
  echo "  Frontend 构建成功，已生成 dist/index.html"
else
  fail "Frontend 工程目录或 package.json 不存在"
fi

# ==============================================================================
# 步骤 2：数据库迁移、测试实体清理与插件清单摘要同步
# ==============================================================================
step "步骤 2：数据库迁移、测试实体清理与插件清单摘要同步"
go run ./cmd/migrate -cmd up -dsn "$NEXUS_PG_MIGRATE_DSN" -dir migrations/kernel >/dev/null

# 清理上一轮测试可能残留的实体
psql_exec "DELETE FROM kernel.kernel_jobs WHERE bot_id IN ('groupchat_bot', 'ops_bot', 'qa_bot', 'echo_bot');"
psql_exec "DELETE FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_groupchat';"
psql_exec "DELETE FROM kernel.kernel_plugin_grants WHERE plugin_id='plugin_groupchat';"
psql_exec "DELETE FROM kernel.kernel_plugin_event_registry WHERE plugin_id='plugin_groupchat';"
psql_exec "DELETE FROM kernel.bot_instances WHERE bot_id IN ('groupchat_bot', 'ops_bot', 'qa_bot');"
psql_exec "DELETE FROM kernel.bots WHERE bot_id IN ('groupchat_bot', 'ops_bot', 'qa_bot');"
psql_exec "DELETE FROM kernel.bot_manifest WHERE bot_id IN ('groupchat_bot', 'ops_bot', 'qa_bot');"
psql_exec "DELETE FROM kernel.kernel_security_decisions WHERE subject_id='plugin_groupchat';"
psql_exec "DELETE FROM kernel.kernel_plugins WHERE plugin_id='plugin_groupchat';"

# 更新二进制摘要到 bot_manifest 与 GroupChatPlugin manifest.json
BOT_RUNTIME_DIGEST=$(sha256sum "$ROOT/bin/bot-runtime" | awk '{print $1}')
psql_exec "UPDATE kernel.bot_manifest SET binary_path='$ROOT/bin/bot-runtime', binary_digest='$BOT_RUNTIME_DIGEST', manifest_digest='$BOT_RUNTIME_DIGEST', updated_at=now() WHERE bot_id IN ('echo_bot','user_bot','create_bot','monitor_bot','security_bot');"

GROUPCHAT_DIGEST=$(sha256sum "$ROOT/bin/plugins/plugin_groupchat" | awk '{print $1}')
python3 -c "
import json
with open('GroupChatPlugin/manifest.json', 'r') as f:
    mf = json.load(f)
mf['binary'] = '$ROOT/bin/plugins/plugin_groupchat'
mf['binary_digest'] = '$GROUPCHAT_DIGEST'
with open('GroupChatPlugin/manifest.json', 'w') as f:
    json.dump(mf, f, indent=2)
"
echo "  数据库就绪，制品摘要已同步"

# ==============================================================================
# 步骤 3：初始化待测 Bot 与 Claude Code 单体记忆栈 (MEMORY.md ≤ 200 行硬约束)
# ==============================================================================
step "步骤 3：初始化待测 Bot 与 Claude Code 单体记忆栈 (MEMORY.md ≤ 200 行硬约束)"
psql_exec "
INSERT INTO kernel.bot_manifest (bot_id, role, managed_by, display_name, capabilities, binary_path, binary_digest, manifest_digest, activation_mode, model, prompt_ref)
VALUES 
  ('ops_bot', 'plugin', 'user_bot', 'Ops Bot', '[\"ops\", \"agent_control\"]'::jsonb, '$ROOT/bin/bot-runtime', '$BOT_RUNTIME_DIGEST', '$BOT_RUNTIME_DIGEST', 'lazy', 'claude-3-5-sonnet', 'prompts/ops_bot.txt'),
  ('qa_bot', 'plugin', 'user_bot', 'QA Bot', '[\"qa\", \"test_runner\"]'::jsonb, '$ROOT/bin/bot-runtime', '$BOT_RUNTIME_DIGEST', '$BOT_RUNTIME_DIGEST', 'lazy', 'claude-3-5-sonnet', 'prompts/qa_bot.txt')
ON CONFLICT (bot_id) DO UPDATE SET binary_path=EXCLUDED.binary_path, binary_digest=EXCLUDED.binary_digest;

INSERT INTO kernel.bots (bot_id, desired_state, desired_by, observed_state, enabled)
VALUES 
  ('ops_bot', 'start', 'create_bot', 'dormant', true),
  ('qa_bot', 'start', 'create_bot', 'dormant', true)
ON CONFLICT (bot_id) DO UPDATE SET enabled=true, desired_state='start';
"

OPS_MEM_DIR="$MEMORY_BASE_DIR/ops_bot/memory"
rm -rf "$OPS_MEM_DIR"
mkdir -p "$OPS_MEM_DIR/transcripts" "$OPS_MEM_DIR/notes" "$OPS_MEM_DIR/representations"

cat <<EOF >"$OPS_MEM_DIR/MEMORY.md"
# ops_bot 长期核心记忆（Injected Context）

> 铁律约束: 本文件行数必须严格 ≤ 200 行，零二次方膨胀，高 Prompt Caching 命中
- 角色定位: 简单轻量 Agent，只调用系统提供的功能与工具
- 禁执行 Shell: 禁止自身执行任何 bash/sh 命令；所有受控物理操作必须调度 agent-daemon
- 禁长思考: 单次推理超时硬封顶 15s，杜绝递归发散
- 关键约束: 遇到构建环境检查请求，调用 loopback_agent 验证环境并输出结构化卡片
EOF

MEM_LINES=$(wc -l <"$OPS_MEM_DIR/MEMORY.md")
[[ "$MEM_LINES" -le 200 ]] || fail "初始 MEMORY.md 超限: $MEM_LINES 行"
echo "  ops_bot 单体记忆初始化就绪: $MEM_LINES 行 (严格 ≤ 200 行)"

# ==============================================================================
# 步骤 4：启动 Hub 微内核网关与 loopback agent
# ==============================================================================
step "步骤 4：启动 Hub 微内核网关与 loopback agent"
./bin/hub -listen-http "$HTTP" -listen-grpc "$GRPC" -listen-transport "$TRANSPORT" \
  -user-credential-file "$RUN_DIR/user_bot.cred" -bot-binary "$ROOT/bin/bot-runtime" \
  >"$RUN_DIR/hub.log" 2>&1 &
PIDS+=($!)

for _ in $(seq 1 40); do
  curl -sf "$HTTP_URL/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "$HTTP_URL/healthz" >/dev/null || { cat "$RUN_DIR/hub.log"; fail "Hub 未能在超时内就绪"; }
echo "  Hub 已启动就绪: http=$HTTP, grpc=$GRPC, transport=$TRANSPORT"

# 签发 loopback_agent 凭证并启动 agent-daemon
./bin/cli fixture -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
AGENT_CRED=$(./bin/cli credential -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  -kind agent -subject loopback_agent | python3 -c 'import sys, json; print(json.load(sys.stdin)["token"])')

NEXUS_AGENT_CREDENTIAL="$AGENT_CRED" ./bin/agent-daemon -hub-transport "$TRANSPORT" \
  >"$RUN_DIR/agent.log" 2>&1 &
PIDS+=($!)

for _ in $(seq 1 20); do
  grep -q "已接入 hub 传输路由" "$RUN_DIR/agent.log" 2>/dev/null && break
  sleep 0.5
done
grep -q "已接入 hub 传输路由" "$RUN_DIR/agent.log" || { cat "$RUN_DIR/agent.log"; fail "agent-daemon 未接入传输路由"; }
echo "  loopback agent 已成功反向接入 Hub 传输路由"

# ==============================================================================
# 步骤 5：安装并激活 GroupChatPlugin 独立插件 (L2 进程)
# ==============================================================================
step "步骤 5：安装并激活 GroupChatPlugin 独立插件 (L2 进程)"
INSTALL_RESP=$(./bin/cli plugin install GroupChatPlugin/manifest.json -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred")
DECISION_ID=$(echo "$INSTALL_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["decision_id"])')
./bin/cli security-approve "$DECISION_ID" -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null

PLUGIN_STATUS=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_groupchat'")
[[ "$PLUGIN_STATUS" == "enabled" ]] || fail "GroupChatPlugin 状态应为 enabled，实际: $PLUGIN_STATUS"

# 触发 supervisor 调和启动 owner bot (groupchat_bot) 并由其拉起 L2 进程 plugin_groupchat
./bin/cli create-bot pull -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null

for _ in $(seq 1 40); do
  curl -sf "$CHAT_URL/v1/chat/health" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "$CHAT_URL/v1/chat/health" >/dev/null || { cat "$RUN_DIR/hub.log"; fail "GroupChatPlugin 未能在端口 $CHAT_PORT 就绪"; }
echo "  GroupChatPlugin 独立插件服务已就绪: $CHAT_URL"

# ==============================================================================
# 步骤 6：启动 Frontend 静态预览服务 (:5173) 并探测健康
# ==============================================================================
step "步骤 6：启动 Frontend 静态预览服务 (:5173) 并探测健康"
(cd "$ROOT/Frontend" && exec npx vite preview --port "$FRONTEND_PORT" --host 127.0.0.1 >"$RUN_DIR/frontend.log" 2>&1) &
PIDS+=($!)

for _ in $(seq 1 40); do
  curl -sf "$FRONTEND_URL" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "$FRONTEND_URL" >/dev/null || { cat "$RUN_DIR/frontend.log"; fail "Frontend 静态预览服务未就绪"; }
FRONTEND_HTML=$(curl -sf "$FRONTEND_URL")
echo "$FRONTEND_HTML" | grep -qi "html" || fail "Frontend 响应不包含 HTML"
echo "  Frontend 静态预览服务就绪: $FRONTEND_URL (HTTP 200)"

# ==============================================================================
# 步骤 7：验证 Hub 网关 CORS 与 SSE 实时事件流
# ==============================================================================
step "步骤 7：验证 Hub 网关 CORS 与 SSE 实时事件流"
# 1. 验证 CORS preflight 响应头
CORS_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X OPTIONS "$HTTP_URL/v1/events/stream" \
  -H "Origin: http://127.0.0.1:5173" \
  -H "Access-Control-Request-Method: GET" \
  -H "Access-Control-Request-Headers: Content-Type, Authorization, X-Nexus-Credential")
[[ "$CORS_STATUS" -eq 200 || "$CORS_STATUS" -eq 204 ]] || fail "CORS 预检状态码异常: $CORS_STATUS"

CORS_HEADER=$(curl -sI -X OPTIONS "$HTTP_URL/v1/events/stream" \
  -H "Origin: http://127.0.0.1:5173" \
  -H "Access-Control-Request-Method: GET" | grep -i "access-control-allow-origin" || true)
[[ -n "$CORS_HEADER" ]] || fail "CORS 响应头缺失 Access-Control-Allow-Origin"
echo "  CORS preflight 跨域检查通过"

# 2. 验证 SSE 流端点响应类型
SSE_HEADER=$(curl -sI -N "$HTTP_URL/v1/events/stream" | tr -d '\r')
echo "$SSE_HEADER" | grep -qi "text/event-stream" || fail "/v1/events/stream Content-Type 必须为 text/event-stream"
echo "  SSE 事件流接入点验证通过 (text/event-stream)"

# 3. 启动后台 SSE 监听捕获后续事件
curl -s -N "$HTTP_URL/v1/events/stream" >"$RUN_DIR/sse_events.log" 2>&1 &
PIDS+=($!)
sleep 1

# ==============================================================================
# 步骤 8：群聊核心交互验收——用户 @ops_bot 触发调度 loopback_agent 并回投回复
# ==============================================================================
step "步骤 8：群聊核心交互验收——用户 @ops_bot 触发调度 loopback_agent 并回投回复"
CHAT_RESP=$(curl -sf -X POST "$CHAT_URL/v1/chat/rooms/dev_room/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "sender_id": "user_tester",
    "role": "user",
    "content": "请 @ops_bot 检查构建环境并汇报"
  }')
echo "  群聊响应: $CHAT_RESP"

ROUTED=$(echo "$CHAT_RESP" | python3 -c 'import sys, json; print(",".join(json.load(sys.stdin).get("routed_bots", [])))')
MSG_ID=$(echo "$CHAT_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["message"]["id"])')
[[ "$ROUTED" == "ops_bot" ]] || fail "群聊路由应且仅应唤醒 [ops_bot]，实际: $ROUTED"

# 等待 supervisor 拉起 ops_bot 执行任务完成
echo "  等待 ops_bot 调度 loopback_agent 并回投回复..."
JOB_DONE=0
for _ in $(seq 1 30); do
  DONE_COUNT=$(psql_exec "SELECT count(*) FROM kernel.kernel_jobs WHERE correlation_id='$MSG_ID' AND status='done'")
  if [[ "$DONE_COUNT" -ge 1 ]]; then
    JOB_DONE=1
    break
  fi
  sleep 1
done
[[ "$JOB_DONE" -eq 1 ]] || { cat "$RUN_DIR/hub.log"; fail "ops_bot 未能在超时时间内完成任务"; }
echo "  ops_bot 成功完成任务，状态为 done"

# 验证 Transcripts 原始会话流水追加证据
TRANS_FILE=$(ls -1 "$OPS_MEM_DIR/transcripts/"*.jsonl 2>/dev/null | head -n 1 || true)
[[ -n "$TRANS_FILE" ]] || fail "ops_bot 记忆中未找到 Transcripts 流水"
TRANS_CONTENT=$(cat "$TRANS_FILE" | head -n 1)
echo "$TRANS_CONTENT" | grep -q "调度 Agent: loopback_agent" || fail "流水中缺少受控 Agent 调度记录"
echo "  ops_bot 单体会话流水验证通过"

# ==============================================================================
# 步骤 9：防死循环铁律验收——Bot 回复中含 @qa_bot，qa_bot 保持静默
# ==============================================================================
step "步骤 9：防死循环铁律验收——Bot 回复中含 @qa_bot，qa_bot 保持静默"
BOT_CHAT_RESP=$(curl -sf -X POST "$CHAT_URL/v1/chat/rooms/dev_room/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "sender_id": "ops_bot",
    "role": "bot",
    "content": "构建环境已检查完毕，请 @qa_bot 执行集成验收测试"
  }')
echo "  Bot 回复响应: $BOT_CHAT_RESP"

BOT_ROUTED=$(echo "$BOT_CHAT_RESP" | python3 -c 'import sys, json; print(len(json.load(sys.stdin).get("routed_bots") or []))')
[[ "$BOT_ROUTED" -eq 0 ]] || fail "Bot 回复中的 @ 严禁唤醒任何下游 Bot！实际唤醒数量: $BOT_ROUTED"

QA_JOBS=$(psql_exec "SELECT count(*) FROM kernel.kernel_jobs WHERE bot_id='qa_bot'")
[[ "$QA_JOBS" -eq 0 ]] || fail "qa_bot 竟然收到任务唤醒 (违反防死循环铁律)，任务数: $QA_JOBS"
echo "  防死循环铁律通过：Bot 间互 @ 保持纯文本展示，qa_bot 任务数为 0"

# ==============================================================================
# 步骤 10：Bot 画像抽屉与单体记忆管理验收——<=200行强拒绝、合规更新与重启策略
# ==============================================================================
step "步骤 10：Bot 画像抽屉与单体记忆管理验收——<=200行强拒绝、合规更新与重启策略"
# 1. 查询 Bot 详情
BOT_DETAIL=$(curl -sf "$HTTP_URL/v1/bots/ops_bot") || fail "GET /v1/bots/ops_bot 失败"
echo "  Bot 详情查询成功: $(echo "$BOT_DETAIL" | python3 -c 'import sys, json; b=json.load(sys.stdin); print(b.get("bot_id"), b.get("role"), b.get("memory_lines"))')"

# 2. 查询单体记忆详情
MEM_GET_RESP=$(curl -sf "$HTTP_URL/v1/bots/ops_bot/memory") || fail "GET /v1/bots/ops_bot/memory 失败"
echo "$MEM_GET_RESP" | grep -q '"max_lines":200' || fail "记忆响应缺少 max_lines:200 声明"

# 3. 边界值硬约束测试：提交 201 行记忆，断言 HTTP 400 严格拒绝
python3 -c "
import json
lines = ['- 超限记忆测试行 %d' % i for i in range(201)]
print(json.dumps({'content': '\n'.join(lines)}))
" >"$RUN_DIR/overflow_memory.json"

HTTP_CODE=$(curl -s -o "$RUN_DIR/overflow_resp.json" -w "%{http_code}" -X PUT "$HTTP_URL/v1/bots/ops_bot/memory" \
  -H "Content-Type: application/json" \
  --data-binary @"$RUN_DIR/overflow_memory.json")
[[ "$HTTP_CODE" == "400" ]] || fail "单体记忆超过 200 行必须返回 HTTP 400 强行拒绝！实际: $HTTP_CODE"
grep -q "超过 200 行硬约束限制" "$RUN_DIR/overflow_resp.json" || fail "400 响应错误提示信息不符合预期"
echo "  MEMORY.md 超过 200 行 (201 行) 超限强拒绝验收通过 (HTTP 400)"

# 4. 边界值合规测试：提交恰好 200 行记忆，断言 HTTP 200 成功
python3 -c "
import json
lines = ['- 合规记忆测试行 %d' % i for i in range(1, 200)]
lines.append('- 关键约束: 遇到构建环境检查请求优先调度 loopback_agent 探针')
print(json.dumps({'content': '\n'.join(lines) + '\n'}))
" >"$RUN_DIR/valid_memory.json"

SAVE_CODE=$(curl -s -o "$RUN_DIR/valid_resp.json" -w "%{http_code}" -X PUT "$HTTP_URL/v1/bots/ops_bot/memory" \
  -H "Content-Type: application/json" \
  --data-binary @"$RUN_DIR/valid_memory.json")
[[ "$SAVE_CODE" == "200" ]] || fail "合规 200 行单体记忆保存失败，状态码: $SAVE_CODE"
grep -q '"lines":200' "$RUN_DIR/valid_resp.json" || fail "保存响应缺少 lines:200 确认"
UPDATED_LINES=$(wc -l <"$OPS_MEM_DIR/MEMORY.md")
[[ "$UPDATED_LINES" -eq 200 ]] || fail "磁盘实际行数不为 200: $UPDATED_LINES"
echo "  合规单体记忆保存成功 (HTTP 200, 200 行)"

# 5. 触发 Bot 重启 (Restart Policy)
RESTART_RESP=$(curl -sf -X POST "$HTTP_URL/v1/bots/ops_bot/restart") || fail "POST /v1/bots/ops_bot/restart 失败"
echo "  Bot 重启响应: $RESTART_RESP"
echo "$RESTART_RESP" | grep -q "restarted" || fail "重启响应缺少 restarted 标记"

# 验证 Hub 依然存活健康
curl -sf "$HTTP_URL/healthz" >/dev/null || fail "Bot 重启导致 Hub 异常崩溃"

# 6. 发送新会话验证新启动的 Bot 装载了 200 行新记忆
NEW_CHAT_RESP=$(curl -sf -X POST "$CHAT_URL/v1/chat/rooms/dev_room/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "sender_id": "user_tester",
    "role": "user",
    "content": "请 @ops_bot 再次检查环境并汇报新记忆"
  }')
NEW_MSG_ID=$(echo "$NEW_CHAT_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["message"]["id"])')

for _ in $(seq 1 30); do
  DONE_COUNT=$(psql_exec "SELECT count(*) FROM kernel.kernel_jobs WHERE correlation_id='$NEW_MSG_ID' AND status='done'")
  if [[ "$DONE_COUNT" -ge 1 ]]; then
    break
  fi
  sleep 1
done

LATEST_TRANS=$(ls -t1 "$OPS_MEM_DIR/transcripts/"*.jsonl | head -n 1)
grep -q "已注入 MEMORY.md (200 行)" "$LATEST_TRANS" || fail "重启后的 Bot 未生效 200 行新记忆"
echo "  Bot 重启生效验收通过 (已重载新记忆，当前生效 200 行)"

# ==============================================================================
# 步骤 11：Agent 实时执行帧捕获验证与优雅停机
# ==============================================================================
step "步骤 11：Agent 实时执行帧捕获验证与优雅停机"
# 1. 验证 agent-daemon 终端执行输出有记录
grep -q "loopback_agent" "$RUN_DIR/agent.log" || fail "agent.log 中缺少 loopback_agent 执行记录"
echo "  Agent 探针执行日志终端回显正常"

# 2. 验证 SSE 事件流捕获到了 agent_event 广播与帧属性 (frames, status, elapsed_ms)
sleep 1
grep -q "agent_event" "$RUN_DIR/sse_events.log" || fail "SSE 事件流中未捕获到 agent_event 广播"
grep -q "loopback_agent" "$RUN_DIR/sse_events.log" || fail "SSE 事件流中缺少 loopback_agent 标识"
grep -q "elapsed_ms" "$RUN_DIR/sse_events.log" || fail "SSE 事件流中缺少 elapsed_ms 执行耗时统计"
echo "  Agent 实时执行帧捕获与 SSE 事件流验证通过 (agent_event, status, elapsed_ms)"

# 3. 优雅关闭插件服务并验证两级进程回收
./bin/cli plugin stop plugin_groupchat -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
sleep 1
PROC_STATE=$(psql_exec "SELECT state FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_groupchat'")
[[ "$PROC_STATE" == "stopped" ]] || fail "GroupChatPlugin 停机状态异常: $PROC_STATE"
echo "  GroupChatPlugin 优雅停机状态验证通过: $PROC_STATE"

printf '\n\033[32m==============================================================================\033[0m\n'
printf '\033[32mSMOKE M4 OK：11 步端到端链路验收全部通过！ (Frontend / Gateway / Chat / Memory / Agent)\033[0m\n'
printf '\033[32m==============================================================================\033[0m\n'
