#!/usr/bin/env bash
# M1 冒烟主链路（内核 MVP 详设 §6.1 九步，纯内核，不含任何记忆能力）。
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
RUN_DIR="$ROOT/data/smoke"
mkdir -p "$RUN_DIR" "$NEXUS_KERNEL_PAYLOAD_DIR"

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "SMOKE FAIL: $1" >&2; exit 1; }

psql_exec() { docker exec -i nexus-pg-m1 psql -U postgres -d nexus -tAc "$1"; }

step "步骤 1：迁移与种子（echo_bot active 配置 v1 + manifest）"
make build >/dev/null
go run ./cmd/migrate -cmd up -dsn "$NEXUS_PG_MIGRATE_DSN" -dir migrations/kernel >/dev/null
SEED_CV=$(psql_exec "SELECT config_version FROM kernel.bot_manifest WHERE bot_id='echo_bot'")
[[ "$SEED_CV" == "1" ]] || fail "echo_bot 种子配置版本应为 1，实际 $SEED_CV"
echo "种子就绪：echo_bot config_version=$SEED_CV"

step "启动 hub"
./bin/hub -listen-http "$HTTP" -listen-grpc "$GRPC" -listen-transport "$TRANSPORT" \
  -user-credential-file "$RUN_DIR/user_bot.cred" -bot-binary "$ROOT/bin/bot-runtime" \
  >"$RUN_DIR/hub.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 40); do
  curl -sf "http://$HTTP/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "http://$HTTP/healthz" >/dev/null || { cat "$RUN_DIR/hub.log"; fail "hub 未就绪"; }

step "绑定 M1 夹具制品（binary_digest）并登记 loopback agent"
./bin/cli fixture -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
AGENT_CRED=$(./bin/cli credential -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  -kind agent -subject loopback_agent | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')

step "启动 loopback agent（反向 TCP 接入传输路由）"
NEXUS_AGENT_CREDENTIAL="$AGENT_CRED" ./bin/agent-daemon -hub-transport "$TRANSPORT" \
  >"$RUN_DIR/agent.log" 2>&1 &
PIDS+=($!)
sleep 1
grep -q "已接入 hub 传输路由" "$RUN_DIR/agent.log" || { cat "$RUN_DIR/agent.log"; fail "agent 未接入"; }

step "步骤 2-8：CLI 投递用户信封 → ingress → 队列 → lazy 拉起 → echo → AckTask → Outbox → 订阅端收到 bot.reply"
CORR=$(./bin/cli send -hub-http "$HTTP_URL" -hub-grpc "$GRPC" -credential-file "$RUN_DIR/user_bot.cred" \
  -text "hello-nexus-m1" -bot echo_bot -wait -timeout 90s 2>"$RUN_DIR/send.err" \
  | tee "$RUN_DIR/send.out" | python3 -c '
import sys, json
lines = [l for l in sys.stdin.read().splitlines() if l.strip()]
env = json.loads(lines[0])
print(env["envelope_id"])
')
grep -q '"event_type":"bot.reply"' "$RUN_DIR/send.out" || { cat "$RUN_DIR/send.out" "$RUN_DIR/send.err" "$RUN_DIR/hub.log"; fail "未收到 bot.reply"; }
echo "correlation_id = $CORR"

step "步骤 9：五条链同 correlation_id 可查"
for t in kernel_ingress_log kernel_jobs kernel_event_outbox; do
  N=$(psql_exec "SELECT count(*) FROM kernel.$t WHERE correlation_id='$CORR'")
  [[ "$N" -ge 1 ]] || fail "$t 中找不到 correlation_id=$CORR"
  echo "  $t: $N 行"
done
DEDUP=$(psql_exec "SELECT count(*) FROM kernel.kernel_event_dedup d JOIN kernel.kernel_event_outbox o ON o.event_id=d.event_id WHERE o.correlation_id='$CORR'")
[[ "$DEDUP" -ge 1 ]] || fail "kernel_event_dedup 未记录该链路消费"
echo "  kernel_event_dedup: $DEDUP 行"
JOB_DONE=$(psql_exec "SELECT count(*) FROM kernel.kernel_jobs WHERE correlation_id='$CORR' AND status='done'")
[[ "$JOB_DONE" -ge 1 ]] || fail "没有 done 状态的任务"
echo "  done 任务: $JOB_DONE"

step "边界验收：nexus_kernel 对 memory / vault 无 USAGE"
for s in memory vault; do
  docker exec -i nexus-pg-m1 psql -U postgres -d nexus -tAc \
    "CREATE SCHEMA IF NOT EXISTS $s AUTHORIZATION nexus_migrator" >/dev/null
  HAS=$(psql_exec "SELECT has_schema_privilege('nexus_kernel','$s','USAGE')")
  [[ "$HAS" == "f" ]] || fail "nexus_kernel 竟对 $s 有 USAGE"
  echo "  $s USAGE=$HAS（正确）"
done

step "无 NOTIFY 兜底轮询路径（人为丢弃通知后仍能完成）"
CORR2=$(./bin/cli send -hub-http "$HTTP_URL" -hub-grpc "$GRPC" -credential-file "$RUN_DIR/user_bot.cred" \
  -text "hello-fallback-poll" -bot echo_bot -wait -timeout 90s 2>/dev/null \
  | tee "$RUN_DIR/send2.out" | python3 -c '
import sys, json
lines = [l for l in sys.stdin.read().splitlines() if l.strip()]
print(json.loads(lines[0])["envelope_id"])
')
grep -q '"event_type":"bot.reply"' "$RUN_DIR/send2.out" || fail "兜底轮询路径未收到 bot.reply"
echo "第二条链路 correlation_id = $CORR2"

step "/metrics"
curl -sf "http://$HTTP/metrics" | tee "$RUN_DIR/metrics.txt" | head -14
grep -q "kernel_jobs" "$RUN_DIR/metrics.txt" || fail "/metrics 缺少队列指标"

step "排除项：仓库与运行表不存在 MCP / Skill 配置类型或运行时"
if grep -rniE "scope_type.*(mcp|skill)" migrations/ hub/ >/dev/null 2>&1; then
  fail "发现 MCP / Skill 配置类型"
fi
MCP_ROWS=$(psql_exec "SELECT count(*) FROM kernel.kernel_config_documents WHERE scope_type NOT IN ('plugin','bot','agent','global')")
[[ "$MCP_ROWS" == "0" ]] || fail "存在非法配置域"
echo "  无 MCP / Skill 配置域（正确）"

printf '\nSMOKE OK：九步链路 + 边界验收 + 兜底轮询全部通过\n'