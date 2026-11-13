#!/usr/bin/env bash
# M3 冒烟验收：GroupChatPlugin 插件生命周期、仅用户@触发路由、简单 Agent 操控 loopback_agent、
# Claude Code 四层单体记忆（MEMORY.md ≤ 200 行硬约束、Transcripts、Auto Dream）与防死循环闭环。
# 依据：开发规划 §3 M3、AGENTS.md。
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

RUN_DIR="$ROOT/data/smoke_m3"
MEMORY_BASE_DIR="$ROOT/data/bots"
mkdir -p "$RUN_DIR" "$NEXUS_KERNEL_PAYLOAD_DIR" "$MEMORY_BASE_DIR"

PIDS=()
cleanup() {
  echo "正在清理测试进程..."
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  # 兜底清理可能残留的 L2 插件进程
  pkill -f "bin/plugins/plugin_groupchat" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "SMOKE M3 FAIL: $1" >&2; exit 1; }

psql_exec() { docker exec -i nexus-pg-m1 psql -U postgres -d nexus -tAc "$1"; }

step "步骤 1：编译内核、简单 Agent 运行时与 GroupChatPlugin 插件"
make build >/dev/null
make build-plugins >/dev/null
[[ -f "bin/plugins/plugin_groupchat" ]] || fail "plugin_groupchat 二进制不存在"
echo "编译完成：bin/{hub,bot-runtime,agent-daemon,cli} 与 bin/plugins/plugin_groupchat"

step "步骤 2：更新插件制品摘要与准备数据库"
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

# 运行数据库迁移
go run ./cmd/migrate -cmd up -dsn "$NEXUS_PG_MIGRATE_DSN" -dir migrations/kernel >/dev/null

# 清理上一轮测试实体
psql_exec "DELETE FROM kernel.kernel_jobs WHERE bot_id IN ('groupchat_bot', 'ops_bot', 'qa_bot');"
psql_exec "DELETE FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_groupchat';"
psql_exec "DELETE FROM kernel.kernel_plugin_grants WHERE plugin_id='plugin_groupchat';"
psql_exec "DELETE FROM kernel.kernel_plugin_event_registry WHERE plugin_id='plugin_groupchat';"
psql_exec "DELETE FROM kernel.bot_instances WHERE bot_id IN ('groupchat_bot', 'ops_bot', 'qa_bot');"
psql_exec "DELETE FROM kernel.bots WHERE bot_id IN ('groupchat_bot', 'ops_bot', 'qa_bot');"
psql_exec "DELETE FROM kernel.bot_manifest WHERE bot_id IN ('groupchat_bot', 'ops_bot', 'qa_bot');"
psql_exec "DELETE FROM kernel.kernel_security_decisions WHERE subject_id='plugin_groupchat';"
psql_exec "DELETE FROM kernel.kernel_plugins WHERE plugin_id='plugin_groupchat';"

BOT_RUNTIME_DIGEST=$(sha256sum "$ROOT/bin/bot-runtime" | awk '{print $1}')
psql_exec "UPDATE kernel.bot_manifest SET binary_path='$ROOT/bin/bot-runtime', binary_digest='$BOT_RUNTIME_DIGEST', manifest_digest='$BOT_RUNTIME_DIGEST', updated_at=now() WHERE bot_id IN ('echo_bot','user_bot','create_bot','monitor_bot','security_bot');"

# 注册待测 Bot: ops_bot 与 qa_bot (lazy 激活模式)
psql_exec "
INSERT INTO kernel.bot_manifest (bot_id, role, managed_by, display_name, capabilities, binary_path, binary_digest, manifest_digest, activation_mode)
VALUES 
  ('ops_bot', 'plugin', 'user_bot', 'Ops Bot', '[\"ops\", \"agent_control\"]'::jsonb, '$ROOT/bin/bot-runtime', '$BOT_RUNTIME_DIGEST', '$BOT_RUNTIME_DIGEST', 'lazy'),
  ('qa_bot', 'plugin', 'user_bot', 'QA Bot', '[\"qa\", \"test_runner\"]'::jsonb, '$ROOT/bin/bot-runtime', '$BOT_RUNTIME_DIGEST', '$BOT_RUNTIME_DIGEST', 'lazy')
ON CONFLICT (bot_id) DO UPDATE SET binary_path=EXCLUDED.binary_path, binary_digest=EXCLUDED.binary_digest;

INSERT INTO kernel.bots (bot_id, desired_state, desired_by, observed_state, enabled)
VALUES 
  ('ops_bot', 'start', 'create_bot', 'dormant', true),
  ('qa_bot', 'start', 'create_bot', 'dormant', true)
ON CONFLICT (bot_id) DO UPDATE SET enabled=true, desired_state='start';
"
echo "ops_bot 与 qa_bot 注册完成"

step "步骤 3：初始化 ops_bot 的 Claude Code 单体记忆栈 (MEMORY.md ≤ 200 行约束)"
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
[[ "$MEM_LINES" -le 200 ]] || fail "MEMORY.md 初始超限: $MEM_LINES 行"
echo "ops_bot 单体记忆初始化成功（MEMORY.md 当前 $MEM_LINES 行，严格 ≤ 200 行）"

step "步骤 4：启动 hub 微内核网关与 loopback agent"
./bin/hub -listen-http "$HTTP" -listen-grpc "$GRPC" -listen-transport "$TRANSPORT" \
  -user-credential-file "$RUN_DIR/user_bot.cred" -bot-binary "$ROOT/bin/bot-runtime" \
  >"$RUN_DIR/hub.log" 2>&1 &
PIDS+=($!)

for _ in $(seq 1 40); do
  curl -sf "http://$HTTP/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "http://$HTTP/healthz" >/dev/null || { cat "$RUN_DIR/hub.log"; fail "hub 未就绪"; }
echo "hub 已启动就绪"

# 签发 loopback_agent 凭证并启动 agent-daemon
./bin/cli fixture -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
AGENT_CRED=$(./bin/cli credential -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  -kind agent -subject loopback_agent | python3 -c 'import sys, json; print(json.load(sys.stdin)["token"])')

NEXUS_AGENT_CREDENTIAL="$AGENT_CRED" ./bin/agent-daemon -hub-transport "$TRANSPORT" \
  >"$RUN_DIR/agent.log" 2>&1 &
PIDS+=($!)
sleep 1
grep -q "已接入 hub 传输路由" "$RUN_DIR/agent.log" || { cat "$RUN_DIR/agent.log"; fail "agent 未接入传输路由"; }
echo "loopback agent 已成功反向接入 hub 传输路由"

step "步骤 5：GroupChatPlugin 标准插件安装与生命周期闭环"
INSTALL_RESP=$(./bin/cli plugin install GroupChatPlugin/manifest.json -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred")
DECISION_ID=$(echo "$INSTALL_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["decision_id"])')
echo "GroupChatPlugin 已提交安装，审查 ID: $DECISION_ID"

# 批准安全审查
./bin/cli security-approve "$DECISION_ID" -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
PLUGIN_STATUS=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_groupchat'")
[[ "$PLUGIN_STATUS" == "enabled" ]] || fail "插件状态应为 enabled，实际: $PLUGIN_STATUS"
echo "GroupChatPlugin 已激活，status=enabled"

# 触发 supervisor 调和启动 owner bot (groupchat_bot)，由它 exec L2 进程 plugin_groupchat
./bin/cli create-bot pull -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null

# 等待 GroupChat 独立进程 `:7300/v1/chat/health` 就绪
for _ in $(seq 1 40); do
  curl -sf "$CHAT_URL/v1/chat/health" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "$CHAT_URL/v1/chat/health" >/dev/null || { cat "$RUN_DIR/hub.log"; fail "GroupChatPlugin 服务未能就绪"; }
echo "GroupChatPlugin (L2) 独立插件服务已就绪，端口 $CHAT_PORT"

step "步骤 6：群聊交互验收——用户发消息显式 @ops_bot 触发调用"
# 用户向群聊发送消息：@ops_bot 检查构建环境
CHAT_RESP=$(curl -sf -X POST "$CHAT_URL/v1/chat/rooms/dev_room/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "sender_id": "user_alice",
    "role": "user",
    "content": "请 @ops_bot 检查构建环境并汇报"
  }')
echo "群聊消息响应: $CHAT_RESP"

ROUTED=$(echo "$CHAT_RESP" | python3 -c 'import sys, json; print(",".join(json.load(sys.stdin).get("routed_bots", [])))')
MSG_ID=$(echo "$CHAT_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["message"]["id"])')
[[ "$ROUTED" == "ops_bot" ]] || fail "预期路由唤醒 [ops_bot]，实际: $ROUTED"
echo "GroupChatPlugin 准确识别用户 @，路由唤醒 ops_bot (correlation_id=$MSG_ID)"

# 等待 supervisor 拉起 ops_bot 处理任务并回投 reply
echo "等待 ops_bot 调度 loopback_agent 并回投回复..."
JOB_DONE=0
for _ in $(seq 1 30); do
  DONE_COUNT=$(psql_exec "SELECT count(*) FROM kernel.kernel_jobs WHERE correlation_id='$MSG_ID' AND status='done'")
  if [[ "$DONE_COUNT" -ge 1 ]]; then
    JOB_DONE=1
    break
  fi
  sleep 1
done
[[ "$JOB_DONE" -eq 1 ]] || { cat "$RUN_DIR/hub.log"; fail "ops_bot 任务未在超时时间内完成"; }
echo "ops_bot 成功完成任务，状态为 done"

# 验证 Transcripts 原始会话流水追加
TRANSCRIPT_FILES=$(ls -1 "$OPS_MEM_DIR/transcripts/" 2>/dev/null || true)
[[ -n "$TRANSCRIPT_FILES" ]] || fail "ops_bot 记忆中未找到 Transcripts 流水文件"
TRANS_CONTENT=$(cat "$OPS_MEM_DIR/transcripts/"*.jsonl | head -n 1)
echo "ops_bot 单体记忆生成流水: $TRANS_CONTENT"
echo "$TRANS_CONTENT" | grep -q "调度 Agent: loopback_agent" || fail "流水中缺少受控 Agent 调度证据"

step "步骤 7：防死循环铁律验收——Bot 回复中含 @qa_bot，qa_bot 保持静默"
# 模拟 Bot 回复流入群聊（包含 @qa_bot）
BOT_CHAT_RESP=$(curl -sf -X POST "$CHAT_URL/v1/chat/rooms/dev_room/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "sender_id": "ops_bot",
    "role": "bot",
    "content": "构建环境已检查完毕，请 @qa_bot 执行集成验收测试"
  }')
echo "Bot 回复响应: $BOT_CHAT_RESP"

BOT_ROUTED=$(echo "$BOT_CHAT_RESP" | python3 -c 'import sys, json; print(len(json.load(sys.stdin).get("routed_bots") or []))')
[[ "$BOT_ROUTED" -eq 0 ]] || fail "Bot 回复中的 @ 严禁唤醒任何下游 Bot！实际唤醒数量: $BOT_ROUTED"

# 验证 qa_bot 数据库中任务计数为 0
QA_JOBS=$(psql_exec "SELECT count(*) FROM kernel.kernel_jobs WHERE bot_id='qa_bot'")
[[ "$QA_JOBS" -eq 0 ]] || fail "qa_bot 竟然收到了任务唤醒 (违反防死循环铁律)，任务数: $QA_JOBS"
echo "防死循环验证通过：Bot 回复中的 @ 仅作纯文本展示，qa_bot 任务数为 0，完全保持静默！"

step "步骤 8：Auto Dream 四阶段离线整合与 MEMORY.md ≤ 200 行硬约束验收"
# 写入离散笔记
cat <<EOF >"$OPS_MEM_DIR/notes/n1.json"
{
  "id": "n1",
  "bot_id": "ops_bot",
  "category": "pitfall",
  "title": "缺少 Go 依赖",
  "content": "构建前需确认 go 模块已下载",
  "evidence": "sess_01",
  "confidence": 0.95
}
EOF

cat <<EOF >"$OPS_MEM_DIR/notes/n2.json"
{
  "id": "n2",
  "bot_id": "ops_bot",
  "category": "preference",
  "title": "执行规范",
  "content": "统一通过反向 TCP 经 Hub 传输路由操控探针",
  "confidence": 1.0
}
EOF

# 运行 Auto Dream 测试工具整合单体记忆
go run -v "$ROOT/scripts/run_dream_helper.go" -bot-id "ops_bot" -data-dir "$MEMORY_BASE_DIR"

# 验证表征生成与 MEMORY.md 约束
REP_FILES=$(ls -1 "$OPS_MEM_DIR/representations/" 2>/dev/null || true)
[[ -n "$REP_FILES" ]] || fail "Auto Dream 未生成任何主题表征 Wiki"
echo "已生成结构化主题表征 Wiki: $REP_FILES"

DREAM_LINES=$(wc -l <"$OPS_MEM_DIR/MEMORY.md")
[[ "$DREAM_LINES" -le 200 ]] || fail "Auto Dream 整合后超限: $DREAM_LINES 行"
grep -q "反向 TCP 经 Hub 传输路由操控探针" "$OPS_MEM_DIR/MEMORY.md" || fail "MEMORY.md 未包含整合后的偏好规则"
echo "Auto Dream 整合完成，MEMORY.md 现为 $DREAM_LINES 行（严格 ≤ 200 行铁律保持）"

step "步骤 9：优雅退出与两级进程回收"
./bin/cli plugin stop plugin_groupchat -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
sleep 1
PROC_STATE=$(psql_exec "SELECT state FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_groupchat'")
echo "GroupChatPlugin 优雅停机状态: $PROC_STATE"

printf '\n\033[32m=== M3 全链路验收全部通过！ ===\033[0m\n'
