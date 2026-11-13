#!/usr/bin/env bash
# M1 崩溃恢复演练（内核 MVP 详设 §6.8 / §8.4）。
# 用真实 SIGKILL 注入 hub 崩溃、Bot 进程失联与 LISTEN 断连，验证恢复后链路无损。
# 需要本地 PostgreSQL 夹具：先跑 scripts/dev-pg.sh。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${NEXUS_DEV_PG_PORT:-55432}"
export NEXUS_PG_MIGRATE_DSN="${NEXUS_PG_MIGRATE_DSN:-postgres://nexus_migrator:nexus_dev_migrate@127.0.0.1:${PORT}/nexus}"
export NEXUS_PG_KERNEL_DSN="${NEXUS_PG_KERNEL_DSN:-postgres://nexus_kernel:nexus_dev_kernel@127.0.0.1:${PORT}/nexus}"
export NEXUS_KERNEL_CREDENTIAL_KEY="${NEXUS_KERNEL_CREDENTIAL_KEY:-0123456789abcdef0123456789abcdef}"
export NEXUS_KERNEL_SNAPSHOT_KEY="${NEXUS_KERNEL_SNAPSHOT_KEY:-abcdef0123456789abcdef0123456789}"
export NEXUS_KERNEL_PAYLOAD_DIR="${NEXUS_KERNEL_PAYLOAD_DIR:-$ROOT/data/payloads}"

HTTP="${NEXUS_HUB_HTTP:-127.0.0.1:7100}"
HTTP_URL="http://$HTTP"
GRPC="${NEXUS_HUB_GRPC:-127.0.0.1:7101}"
TRANSPORT="${NEXUS_HUB_TRANSPORT:-127.0.0.1:7102}"
RUN_DIR="$ROOT/data/drill"
mkdir -p "$RUN_DIR" "$NEXUS_KERNEL_PAYLOAD_DIR"

HUB_PID=""
AGENT_PID=""
cleanup() {
  [[ -n "$HUB_PID" ]] && kill -9 "$HUB_PID" 2>/dev/null || true
  [[ -n "$AGENT_PID" ]] && kill "$AGENT_PID" 2>/dev/null || true
  pkill -f "$ROOT/bin/bot-runtime" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "DRILL FAIL: $1" >&2; exit 1; }
psql_exec() { docker exec -i nexus-pg-m1 psql -U postgres -d nexus -tAc "$1"; }

start_hub() {
  local log="$1"
  ./bin/hub -listen-http "$HTTP" -listen-grpc "$GRPC" -listen-transport "$TRANSPORT" \
    -user-credential-file "$RUN_DIR/user_bot.cred" -bot-binary "$ROOT/bin/bot-runtime" \
    >"$log" 2>&1 &
  HUB_PID=$!
  for _ in $(seq 1 60); do
    curl -sf "$HTTP_URL/healthz" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  cat "$log"
  fail "hub 未就绪"
}

# wait_chain <correlation_id>：等该因果链出现 done 任务与 bot.reply 事件
wait_chain() {
  local corr="$1" tries="${2:-90}"
  for _ in $(seq 1 "$tries"); do
    local done_n reply_n
    done_n=$(psql_exec "SELECT count(*) FROM kernel.kernel_jobs WHERE correlation_id='$corr' AND status='done'")
    reply_n=$(psql_exec "SELECT count(*) FROM kernel.kernel_event_outbox WHERE correlation_id='$corr' AND event_type='bot.reply'")
    [[ "$done_n" -ge 1 && "$reply_n" -ge 1 ]] && return 0
    sleep 1
  done
  return 1
}

send_nowait() {
  ./bin/cli send -hub-http "$HTTP_URL" -hub-grpc "$GRPC" -credential-file "$RUN_DIR/user_bot.cred" \
    -text "$1" -bot echo_bot 2>/dev/null | python3 -c '
import sys, json
lines = [l for l in sys.stdin.read().splitlines() if l.strip()]
print(json.loads(lines[0])["envelope_id"])
'
}

step "准备：迁移 + 构建 + 启动 hub / loopback agent"
make build >/dev/null
go run ./cmd/migrate -cmd up -dsn "$NEXUS_PG_MIGRATE_DSN" -dir migrations/kernel >/dev/null
start_hub "$RUN_DIR/hub-1.log"
./bin/cli fixture -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
AGENT_CRED=$(./bin/cli credential -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  -kind agent -subject loopback_agent | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')
NEXUS_AGENT_CREDENTIAL="$AGENT_CRED" ./bin/agent-daemon -hub-transport "$TRANSPORT" \
  >"$RUN_DIR/agent.log" 2>&1 &
AGENT_PID=$!
sleep 1
grep -q "已接入 hub 传输路由" "$RUN_DIR/agent.log" || { cat "$RUN_DIR/agent.log"; fail "agent 未接入"; }

step "演练 1：hub 崩溃无损（SIGKILL 后入口日志与任务仍在，重启后续跑完成）"
CORR1=$(send_nowait "drill-crash-hub")
kill -9 "$HUB_PID" 2>/dev/null || true
wait "$HUB_PID" 2>/dev/null || true
HUB_PID=""
LOG_N=$(psql_exec "SELECT count(*) FROM kernel.kernel_ingress_log WHERE envelope_id='$CORR1' AND decision='accepted'")
[[ "$LOG_N" -ge 1 ]] || fail "崩溃后入口日志丢失（同事务落库被破坏）"
JOB_N=$(psql_exec "SELECT count(*) FROM kernel.kernel_jobs WHERE correlation_id='$CORR1'")
[[ "$JOB_N" -ge 1 ]] || fail "崩溃后任务丢失"
PRE_STATUS=$(psql_exec "SELECT status FROM kernel.kernel_jobs WHERE correlation_id='$CORR1' ORDER BY job_id LIMIT 1")
echo "  崩溃瞬间：入口日志 $LOG_N 行、任务 $JOB_N 行（状态 $PRE_STATUS）"

start_hub "$RUN_DIR/hub-2.log"
wait_chain "$CORR1" || { cat "$RUN_DIR/hub-2.log"; fail "重启后链路未续跑完成（correlation=$CORR1）"; }
echo "  重启后链路完成：done 任务 + bot.reply 事件均已落库"

step "演练 2：Bot 进程失联由 supervisor 重新拉起"
BOT_PID=$(psql_exec "SELECT pid FROM kernel.bot_instances WHERE bot_id='echo_bot' AND stopped_at IS NULL ORDER BY instance_id DESC LIMIT 1")
[[ -n "$BOT_PID" ]] || fail "找不到在跑的 echo_bot 实例"
kill -9 "$BOT_PID" 2>/dev/null || fail "无法终止 Bot 进程 $BOT_PID"
echo "  已 SIGKILL Bot 进程 pid=$BOT_PID"
CORR2=$(send_nowait "drill-crash-bot")
wait_chain "$CORR2" || { cat "$RUN_DIR/hub-2.log"; fail "Bot 失联后未被重新拉起（correlation=$CORR2）"; }
INST_N=$(psql_exec "SELECT count(*) FROM kernel.bot_instances WHERE bot_id='echo_bot'")
[[ "$INST_N" -ge 2 ]] || fail "应登记至少 2 个实例（原实例 + 重新拉起），实际 $INST_N"
echo "  supervisor 已重新拉起：bot_instances 共 $INST_N 行，链路完成"

step "演练 3：LISTEN 专用连接被强断，兜底轮询仍完成投递"
LISTEN_PID=$(psql_exec "SELECT pid FROM pg_stat_activity WHERE datname='nexus' AND query LIKE 'LISTEN kernel_jobs_wakeup%' AND pid <> pg_backend_pid() LIMIT 1")
if [[ -n "$LISTEN_PID" ]]; then
  psql_exec "SELECT pg_terminate_backend($LISTEN_PID)" >/dev/null
  echo "  已强断 LISTEN 连接 pid=$LISTEN_PID"
else
  echo "  未找到 LISTEN 连接（可能正在重连），仍验证兜底路径"
fi
CORR3=$(send_nowait "drill-listen-cut")
wait_chain "$CORR3" || { cat "$RUN_DIR/hub-2.log"; fail "LISTEN 断连后兜底轮询未完成（correlation=$CORR3）"; }
echo "  兜底轮询 / 重连补偿生效，链路完成"

step "演练 4：崩溃期间无重复副作用（幂等键唯一 + 事件去重）"
DUP=$(psql_exec "SELECT count(*) FROM (SELECT idempotency_key FROM kernel.kernel_jobs WHERE idempotency_key IS NOT NULL GROUP BY idempotency_key HAVING count(*)>1) d")
[[ "$DUP" == "0" ]] || fail "存在重复幂等键任务 $DUP 组"
for c in "$CORR1" "$CORR2" "$CORR3"; do
  R=$(psql_exec "SELECT count(*) FROM kernel.kernel_event_outbox WHERE correlation_id='$c' AND event_type='bot.reply'")
  [[ "$R" == "1" ]] || fail "correlation=$c 的 bot.reply 应恰好 1 条，实际 $R"
done
echo "  三条链路各恰好 1 条 bot.reply，无重复幂等键"

step "演练 5：审计表不可被运行角色篡改（append-only 边界）"
for t in kernel_ingress_log kernel_config_audit; do
  if docker exec -i nexus-pg-m1 psql -U nexus_kernel -d nexus -tAc "DELETE FROM kernel.$t" >/dev/null 2>&1; then
    fail "运行角色竟能 DELETE $t"
  fi
  echo "  $t：运行角色 DELETE 被拒（正确）"
done

printf '\nDRILL OK：hub 崩溃 / Bot 失联 / LISTEN 断连全部恢复，无重复副作用，审计边界成立\n'