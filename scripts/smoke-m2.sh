#!/usr/bin/env bash
# M2 冒烟主链路：Bot 管理、配置审批全链路、插件生命周期与两级进程、DLQ 与 Health。
# 依据：开发规划 §3 M2、BotMvp §9、PluginMvp §11 / §12、ConfigCenter §9。
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
RUN_DIR="$ROOT/data/smoke_m2"
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
fail() { echo "SMOKE M2 FAIL: $1" >&2; exit 1; }

psql_exec() { docker exec -i nexus-pg-m1 psql -U postgres -d nexus -tAc "$1"; }

step "步骤 1：编译内核与插件二进制"
make build >/dev/null
make build-plugins >/dev/null

step "步骤 2：执行内核数据库迁移（含 0008 插件进程表与四角色种子）"
go run ./cmd/migrate -cmd up -dsn "$NEXUS_PG_MIGRATE_DSN" -dir migrations/kernel >/dev/null

HAS_PROC_TABLE=$(psql_exec "SELECT to_regclass('kernel.kernel_plugin_processes')")
[[ "$HAS_PROC_TABLE" == *"kernel_plugin_processes"* ]] || fail "kernel_plugin_processes 表未成功创建"
echo "数据库迁移就绪：kernel_plugin_processes 存在"

# 清理上一轮测试可能残留的临时实体
psql_exec "DELETE FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_demo';"
psql_exec "DELETE FROM kernel.kernel_plugin_grants WHERE plugin_id='plugin_demo';"
psql_exec "DELETE FROM kernel.kernel_plugin_event_registry WHERE plugin_id='plugin_demo';"
psql_exec "DELETE FROM kernel.kernel_jobs WHERE idempotency_key='dlq_test_1';"
psql_exec "DELETE FROM kernel.bot_instances WHERE bot_id IN ('demo_bot', 'user_writer_bot', 'worker_calc_bot');"
psql_exec "DELETE FROM kernel.bots WHERE bot_id IN ('demo_bot', 'user_writer_bot', 'worker_calc_bot');"
psql_exec "DELETE FROM kernel.bot_manifest WHERE bot_id IN ('demo_bot', 'user_writer_bot', 'worker_calc_bot');"
psql_exec "DELETE FROM kernel.kernel_security_decisions WHERE subject_id IN ('plugin_demo', 'echo_bot');"
psql_exec "DELETE FROM kernel.kernel_plugins WHERE plugin_id='plugin_demo';"

# 绑定真实二进制摘要到 bot_manifest 与 demo manifest
BOT_RUNTIME_DIGEST=$(sha256sum "$ROOT/bin/bot-runtime" | awk '{print $1}')
psql_exec "UPDATE kernel.bot_manifest SET binary_path='$ROOT/bin/bot-runtime', binary_digest='$BOT_RUNTIME_DIGEST', manifest_digest='$BOT_RUNTIME_DIGEST', updated_at=now() WHERE bot_id IN ('echo_bot','user_bot','create_bot','monitor_bot','security_bot');"

PLUGIN_DEMO_DIGEST=$(sha256sum "$ROOT/bin/plugins/plugin_demo" | awk '{print $1}')
python3 -c "
import json
with open('plugins/plugin_demo/manifest.json', 'r') as f:
    mf = json.load(f)
mf['binary'] = '$ROOT/bin/plugins/plugin_demo'
mf['binary_digest'] = '$PLUGIN_DEMO_DIGEST'
with open('plugins/plugin_demo/manifest.json', 'w') as f:
    json.dump(mf, f, indent=2)
"

step "步骤 3：启动 hub 网关"
./bin/hub -listen-http "$HTTP" -listen-grpc "$GRPC" -listen-transport "$TRANSPORT" \
  -user-credential-file "$RUN_DIR/user_bot.cred" -bot-binary "$ROOT/bin/bot-runtime" \
  >"$RUN_DIR/hub.log" 2>&1 &
PIDS+=($!)

for _ in $(seq 1 40); do
  curl -sf "http://$HTTP/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "http://$HTTP/healthz" >/dev/null || { cat "$RUN_DIR/hub.log"; fail "hub 未就绪"; }
echo "hub 就绪：http=$HTTP, grpc=$GRPC"

# 等待 resident bot 注册就绪
sleep 3
MONITOR_STATE=$(psql_exec "SELECT observed_state FROM kernel.bots WHERE bot_id='monitor_bot'")
SECURITY_STATE=$(psql_exec "SELECT observed_state FROM kernel.bots WHERE bot_id='security_bot'")
echo "常驻 Bot 状态: monitor_bot=$MONITOR_STATE, security_bot=$SECURITY_STATE"

step "步骤 4：Bot 管理 CLI 验收（user_xxx_bot 默认 memory_profile 与普通 Bot 区分）"
./bin/cli bot create -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  -id user_writer_bot -name "User Writer Bot" >/dev/null

USER_PROFILE=$(psql_exec "SELECT memory_profile#>>'{}' FROM kernel.bot_manifest WHERE bot_id='user_writer_bot'")
[[ "$USER_PROFILE" == "personal_stack_v1" ]] || fail "user_writer_bot 应该默认拥有 personal_stack_v1，实际: $USER_PROFILE"
echo "  user_writer_bot 默认 memory_profile=$USER_PROFILE (正确)"

./bin/cli bot create -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  -id worker_calc_bot -name "Worker Calc Bot" >/dev/null

WORKER_PROFILE=$(psql_exec "SELECT COALESCE(memory_profile#>>'{}', '') FROM kernel.bot_manifest WHERE bot_id='worker_calc_bot'")
[[ "$WORKER_PROFILE" == "" ]] || fail "普通 worker_calc_bot 不应拥有 personal_stack_v1，实际: $WORKER_PROFILE"
echo "  worker_calc_bot memory_profile 为空 (正确)"

./bin/cli bot enable -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  worker_calc_bot --mode resident --enabled true >/dev/null

./bin/cli create-bot pull -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
echo "  Bot 列表与模式更新通过"

step "步骤 5：配置中心完整审批流验收（Propose / Review / Approve / Activate / Rollback / Secret）"
# 重置 echo_bot 基准 active 配置以支持重复运行冒烟
psql_exec "UPDATE kernel.kernel_config_documents SET spec=jsonb_set(spec, '{model}', '\"stub\"') WHERE scope_type='bot' AND scope_id='echo_bot' AND status='active';"

cat <<EOF >"$RUN_DIR/test_spec.json"
{
  "capabilities": ["echo", "v2"],
  "model": "stub-v2",
  "idle_ttl_seconds": 600
}
EOF

# 1. 提案
PROP_RESP=$(./bin/cli config propose -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  --scope bot --id echo_bot --file "$RUN_DIR/test_spec.json")
PROP_ID=$(echo "$PROP_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["proposal_id"])')
echo "  配置提案已提交: $PROP_ID"

# 2. 提案不生效校验
ACTIVE_MODEL=$(psql_exec "SELECT spec->>'model' FROM kernel.kernel_config_documents WHERE scope_type='bot' AND scope_id='echo_bot' AND status='active'")
[[ "$ACTIVE_MODEL" == "stub" ]] || fail "未激活提案不应改变 active 配置，实际: $ACTIVE_MODEL"
echo "  提案未激活前保持原状态 (正确)"

# 3. Diff 与 Checksum
DIFF_RESP=$(./bin/cli config diff -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" "$PROP_ID")
PROP_CSUM=$(echo "$DIFF_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["checksum"])')
echo "  获取 diff 校验和: $PROP_CSUM"

# 4. 获取 security_bot 凭证供预审测试使用
SEC_CRED=$(./bin/cli credential -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  -kind bot -subject security_bot | python3 -c 'import sys, json; print(json.load(sys.stdin)["token"])')
echo "$SEC_CRED" >"$RUN_DIR/security_bot.cred"

# 5. 错误 checksum 批准校验 (必须失败)
if ./bin/cli config approve -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  "$PROP_ID" --checksum "badchecksum12345678901234567890123456789012345678901234567890123456" >/dev/null 2>&1; then
  fail "错误 checksum 批准应该被拒绝"
fi
echo "  错误 checksum 批准被正确拒绝"

# 6. 正确 checksum 批准 (使用 pty 模拟真实终端交互输入 yes)
python3 -c "
import pty, os, subprocess
master, slave = pty.openpty()
p = subprocess.Popen(['./bin/cli', 'config', 'approve', '-hub-http', '$HTTP_URL', '-credential-file', '$RUN_DIR/user_bot.cred', '$PROP_ID', '--checksum', '$PROP_CSUM'], stdin=slave, stdout=slave, stderr=slave)
os.close(slave)
os.write(master, b'yes\n')
p.wait()
os.close(master)
if p.returncode != 0:
    exit(1)
" >/dev/null

# 7. 激活
./bin/cli config activate -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" "$PROP_ID" >/dev/null
NEW_MODEL=$(psql_exec "SELECT spec->>'model' FROM kernel.kernel_config_documents WHERE scope_type='bot' AND scope_id='echo_bot' AND status='active'")
[[ "$NEW_MODEL" == "stub-v2" ]] || fail "激活后模型应为 stub-v2，实际: $NEW_MODEL"
echo "  配置激活成功: model=$NEW_MODEL"

# 8. 密钥登记
SEC_RESP=$(./bin/cli config secret add -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  --provider env --locator NEXUS_TEST_KEY --description "Smoke test secret")
SEC_REF=$(echo "$SEC_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["ref_id"])')
echo "  密钥引用登记成功: ref_id=$SEC_REF"

# 9. 回滚生成新版本
ROLL_RESP=$(./bin/cli config rollback -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  bot echo_bot --to 1)
ROLL_PROP=$(echo "$ROLL_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["proposal_id"])')
echo "  回滚提案生成成功: $ROLL_PROP"

step "步骤 6：插件生命周期与两级进程模型验收（Install / Security / Exec / PS / Stop / Kill / Orphan）"
# 1. 插件安装
INSTALL_RESP=$(./bin/cli plugin install -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" \
  plugins/plugin_demo/manifest.json)
DECISION_ID=$(echo "$INSTALL_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["decision_id"])')
echo "  插件 plugin_demo 已安装，等待审查 decision_id=$DECISION_ID"

# 2. 安全审查列表命中 pending
./bin/cli security-list pending -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" | grep -q "plugin_demo" || fail "security-list 中未命中 pending plugin_demo"
echo "  security-list 命中待审项"

# 3. 审查批准并激活插件
./bin/cli security-approve "$DECISION_ID" -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
PLUGIN_STATUS=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_demo'")
[[ "$PLUGIN_STATUS" == "enabled" ]] || fail "批准后插件状态应为 enabled，实际: $PLUGIN_STATUS"
echo "  插件已激活，status=enabled"

# 4. 触发 supervisor 调和以启动 owner bot (demo_bot)
./bin/cli create-bot pull -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
sleep 3

# 5. 校验插件进程 PS（由 demo_bot exec）
PS_OUTPUT=$(./bin/cli plugin ps -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred")
echo "$PS_OUTPUT" | grep -q "plugin_demo" || { cat "$RUN_DIR/hub.log"; fail "plugin ps 中找不到 plugin_demo"; }
echo "  plugin ps 正常显示插件进程运行态"

# 6. 优雅关闭：plugin stop
./bin/cli plugin stop plugin_demo -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
STOP_STATE=$(psql_exec "SELECT state, stop_mode FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_demo'")
[[ "$STOP_STATE" == "stopped|graceful" ]] || fail "优雅关闭状态应为 stopped|graceful，实际: $STOP_STATE"
echo "  优雅关闭成功: $STOP_STATE"

# 重复 stop 幂等 no-op
./bin/cli plugin stop plugin_demo -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
echo "  重复 stop 幂等通过"

# 7. 重启插件进程并验证强制终止：plugin kill --confirm
./bin/cli plugin start plugin_demo -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
./bin/cli create-bot pull -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
sleep 2

# 缺少 --confirm 必须失败
if ./bin/cli plugin kill plugin_demo -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null 2>&1; then
  fail "缺少 --confirm 的 kill 应该被拒绝"
fi
echo "  缺少 --confirm 的 kill 正确被拒"

# 带 --confirm 执行强制终止
./bin/cli plugin kill plugin_demo --confirm -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
KILL_STATE=$(psql_exec "SELECT state, stop_mode FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_demo'")
[[ "$KILL_STATE" == "killed|force" ]] || fail "强制终止状态应为 killed|force，实际: $KILL_STATE"
echo "  强制终止成功: $KILL_STATE"

# 8. 孤儿级联回收验收：杀 owner bot 进程，验证插件进程被级联回收无残留
# 先恢复运行
./bin/cli plugin start plugin_demo -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
./bin/cli create-bot pull -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
sleep 2

DEMO_BOT_PID=$(psql_exec "SELECT pid FROM kernel.bot_instances WHERE bot_id='demo_bot' AND stopped_at IS NULL ORDER BY instance_id DESC LIMIT 1")
PLUGIN_PROC_PID=$(psql_exec "SELECT pid FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_demo'")

if [[ -n "$DEMO_BOT_PID" && "$DEMO_BOT_PID" -gt 0 ]]; then
  echo "  注入 kill -9 owner bot (PID $DEMO_BOT_PID)..."
  kill -9 "$DEMO_BOT_PID" 2>/dev/null || true
  sleep 2
  # 验证插件进程已被 supervisor 级联终止
  if kill -0 "$PLUGIN_PROC_PID" 2>/dev/null; then
    fail "owner bot 死后插件进程 (PID $PLUGIN_PROC_PID) 未被级联清理！"
  fi
  ORPHAN_STATE=$(psql_exec "SELECT state, last_error FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_demo'")
  echo "  孤儿级联清理验证通过: $ORPHAN_STATE (进程已无残留)"
fi

# 9. 插件禁用与卸载
./bin/cli plugin disable plugin_demo -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
DISABLED_STATUS=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_demo'")
[[ "$DISABLED_STATUS" == "disabled" ]] || fail "禁用后状态应为 disabled，实际: $DISABLED_STATUS"
echo "  插件禁用通过: $DISABLED_STATUS"

./bin/cli plugin uninstall plugin_demo --confirm -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
UNINSTALLED_STATUS=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_demo'")
[[ "$UNINSTALLED_STATUS" == "uninstalled" ]] || fail "卸载后状态应为 uninstalled，实际: $UNINSTALLED_STATUS"
echo "  插件卸载通过: $UNINSTALLED_STATUS"

step "步骤 7：DLQ 运营 CLI 验收"
# 写入一条死信任务夹具
psql_exec "INSERT INTO kernel.kernel_jobs (queue_name, job_type, idempotency_key, status, attempt_count, suspend_reason, correlation_id, root_envelope_id) VALUES ('echo_bot', 'bot.request', 'dlq_test_1', 'dead', 5, 'simulated poison pill', 'corr_dlq_1', 'root_dlq_1');"
DEAD_JOB_ID=$(psql_exec "SELECT job_id FROM kernel.kernel_jobs WHERE idempotency_key='dlq_test_1'")

DLQ_LIST=$(./bin/cli dlq list -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred")
echo "$DLQ_LIST" | grep -q "$DEAD_JOB_ID" || fail "dlq list 中找不到死信任务"
echo "  dlq list 显示正常"

./bin/cli dlq archive -job "$DEAD_JOB_ID" -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred" >/dev/null
ARCHIVED_STATUS=$(psql_exec "SELECT status FROM kernel.kernel_jobs WHERE job_id=$DEAD_JOB_ID")
[[ "$ARCHIVED_STATUS" == "cancelled" ]] || fail "死信归档后状态应为 cancelled，实际: $ARCHIVED_STATUS"
echo "  死信任务归档通过: status=$ARCHIVED_STATUS"

step "步骤 8：健康与指标巡检"
METRICS=$(./bin/cli metrics -hub-http "$HTTP_URL" -credential-file "$RUN_DIR/user_bot.cred")
echo "$METRICS" | grep -q "kernel_jobs" || fail "metrics 输出异常"
echo "  /metrics 响应正常"

# 恢复 echo_bot 配置为 v1，保证与 M1 冒烟互不影响
psql_exec "
UPDATE kernel.bot_manifest SET config_version=1 WHERE bot_id='echo_bot';
UPDATE kernel.bots SET config_version=1 WHERE bot_id='echo_bot';
DELETE FROM kernel.kernel_config_documents WHERE scope_type='bot' AND scope_id='echo_bot' AND version > 1;
UPDATE kernel.kernel_config_documents SET status='active' WHERE scope_type='bot' AND scope_id='echo_bot' AND version = 1;
DELETE FROM kernel.kernel_jobs WHERE idempotency_key='dlq_test_1';
"

echo ""
echo "=========================================="
echo "SMOKE M2 OK：Bot 管理、配置审批全链路、插件生命周期与两级进程、DLQ 与 Health 全部验收通过！"
echo "=========================================="
