#!/usr/bin/env bash
# M11 冒烟：Bot 身份可列举；安装 / 升级 / 停用 / 卸载的成功与失败都留在既有内核审计里。
# 依据：开发规划 M11，需求规格 R1 / R2 / R4。不改 plugin_type CHECK，不新建审计表。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${NEXUS_DEV_PG_PORT:-55432}"
export NEXUS_PG_MIGRATE_DSN="${NEXUS_PG_MIGRATE_DSN:-postgres://nexus_migrator:nexus_dev_migrate@127.0.0.1:${PORT}/nexus}"
export NEXUS_PG_KERNEL_DSN="${NEXUS_PG_KERNEL_DSN:-postgres://nexus_kernel:nexus_dev_kernel@127.0.0.1:${PORT}/nexus}"
export NEXUS_KERNEL_CREDENTIAL_KEY="${NEXUS_KERNEL_CREDENTIAL_KEY:-0123456789abcdef0123456789abcdef}"
export NEXUS_KERNEL_SNAPSHOT_KEY="${NEXUS_KERNEL_SNAPSHOT_KEY:-abcdef0123456789abcdef0123456789}"
export NEXUS_KERNEL_PAYLOAD_DIR="${NEXUS_KERNEL_PAYLOAD_DIR:-$ROOT/data/payloads}"

HTTP="127.0.0.1:7211"
HTTP_URL="http://$HTTP"
GRPC="127.0.0.1:7212"
TRANSPORT="127.0.0.1:7213"
RUN_DIR="$ROOT/data/smoke_m11"
mkdir -p "$RUN_DIR" "$NEXUS_KERNEL_PAYLOAD_DIR"
CRED="$RUN_DIR/user_bot.cred"

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "SMOKE M11 FAIL: $1" >&2; exit 1; }
if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "nexus-pg-m2"; then
  PG_CONTAINER="nexus-pg-m2"
elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "nexus-pg-m1"; then
  PG_CONTAINER="nexus-pg-m1"
else
  PG_CONTAINER="nexus-pg-m1"
fi
psql_exec() { docker exec -i "$PG_CONTAINER" psql -U postgres -d nexus -tAc "$1"; }
cli() { ./bin/cli "$@" -hub-http "$HTTP_URL" -credential-file "$CRED"; }

step "步骤 1：编译 hub、cli 与 bot-runtime"
go build -o bin/hub ./hub
go build -o bin/cli ./cli
go build -o bin/bot-runtime ./bot-runtime

step "步骤 2：迁移并确认不改 plugin_type、不新建审计表"
go run ./cmd/migrate -cmd up -dsn "$NEXUS_PG_MIGRATE_DSN" -dir migrations/kernel >/dev/null
TYPE_CHECK=$(psql_exec "SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname='kernel' AND t.relname='kernel_plugins' AND pg_get_constraintdef(c.oid) LIKE '%plugin_type%';")
echo "$TYPE_CHECK" | grep -q "ingress" || fail "plugin_type CHECK 丢失: $TYPE_CHECK"
echo "$TYPE_CHECK" | grep -q "foundation" && fail "plugin_type CHECK 不应收录产品分类: $TYPE_CHECK"
AUDIT_TABLE=$(psql_exec "SELECT to_regclass('kernel.kernel_plugin_audit_logs');")
[[ -z "$AUDIT_TABLE" ]] || fail "不应新建 kernel_plugin_audit_logs"
echo "plugin_type 仍为四值，审计表未新建"

IDS="'plugin_m11_biz','plugin_m11_digest','plugin_m11_badcat','plugin_m11_theme','plugin_m11_prefix'"
BOTS="'user_m11_bot','m11_owner_bot','m11_nl_bot'"
psql_exec "DELETE FROM kernel.kernel_config_audit WHERE doc_id IN (SELECT doc_id FROM kernel.kernel_config_documents WHERE scope_type='plugin' AND config_key='lifecycle' AND scope_id IN ($IDS));"
psql_exec "DELETE FROM kernel.kernel_config_documents WHERE scope_type='plugin' AND scope_id IN ($IDS);"
psql_exec "DELETE FROM kernel.kernel_security_decisions WHERE subject_id IN ($IDS);"
psql_exec "DELETE FROM kernel.kernel_plugin_processes WHERE plugin_id IN ($IDS);"
psql_exec "DELETE FROM kernel.kernel_plugin_grants WHERE plugin_id IN ($IDS);"
psql_exec "DELETE FROM kernel.kernel_plugin_event_registry WHERE plugin_id IN ($IDS);"
psql_exec "DELETE FROM kernel.bot_instances WHERE bot_id IN ($BOTS);"
psql_exec "DELETE FROM kernel.bots WHERE bot_id IN ($BOTS);"
psql_exec "DELETE FROM kernel.bot_manifest WHERE bot_id IN ($BOTS);"
psql_exec "DELETE FROM kernel.kernel_plugins WHERE plugin_id IN ($IDS);"

step "步骤 3：启动 hub"
./bin/hub -listen-http "$HTTP" -listen-grpc "$GRPC" -listen-transport "$TRANSPORT" \
  -user-credential-file "$CRED" -bot-binary "$ROOT/bin/bot-runtime" \
  >"$RUN_DIR/hub.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 40); do
  curl -sf "$HTTP_URL/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "$HTTP_URL/healthz" >/dev/null || { cat "$RUN_DIR/hub.log"; fail "hub 未就绪"; }

step "步骤 4：Bot 身份与保留名"
cli bot create -id user_m11_bot -name "M11 User" >/dev/null
if cli bot create -id create_m11_bot -name "Nope" >/dev/null 2>&1; then
  fail "create_ 前缀应被拒绝"
fi
if cli bot create -id user_bot -name "Nope" >/dev/null 2>&1; then
  fail "保留名 user_bot 应被拒绝"
fi

cli bot list >"$RUN_DIR/bots-before.json"
python3 - "$RUN_DIR/bots-before.json" <<'PY'
import json, sys
bots = {b["bot_id"]: b for b in json.load(open(sys.argv[1]))}
def need(bot, identity):
    got = bots.get(bot, {})
    if got.get("identity") != identity:
        raise SystemExit(f"{bot} 身份 {got.get('identity')}，期望 {identity}")
need("user_bot", "system")
need("user_m11_bot", "user")
print("  user_bot=system, user_m11_bot=user")
PY

CODE=$(curl -s -o "$RUN_DIR/mem201.json" -w '%{http_code}' -X PUT "$HTTP_URL/v1/bots/user_m11_bot/memory" \
  -H 'Content-Type: application/json' -d "$(python3 -c 'import json; print(json.dumps({"content":"line\n"*201}))')")
[[ "$CODE" == "400" ]] || fail "201 行记忆应返回 400，实际 $CODE"
CODE=$(curl -s -o "$RUN_DIR/mem1.json" -w '%{http_code}' -X PUT "$HTTP_URL/v1/bots/user_m11_bot/memory" \
  -H 'Content-Type: application/json' -d '{"content":"# m11\n"}')
[[ "$CODE" == "200" ]] || fail "1 行记忆应保存，实际 $CODE $(cat "$RUN_DIR/mem1.json")"
echo "  保留名拒绝，MEMORY.md 200 行约束仍在"

step "步骤 5：带 plugin_category 的业务插件，列表能区分 owner 与用户 Bot"
cat >"$RUN_DIR/biz.json" <<'EOF'
{
  "plugin_id": "plugin_m11_biz",
  "name": "M11 Business",
  "version": "1.0.0",
  "plugin_type": "workspace",
  "plugin_category": "business",
  "bot_templates": [
    {"bot_id": "m11_owner_bot", "display_name": "M11 Owner", "activation_mode": "lazy"},
    {"bot_id": "m11_nl_bot", "display_name": "M11 NL", "activation_mode": "lazy"}
  ]
}
EOF
INSTALL=$(cli plugin install "$RUN_DIR/biz.json")
DECISION=$(echo "$INSTALL" | python3 -c 'import sys,json; print(json.load(sys.stdin)["decision_id"])')
cli security-approve "$DECISION" >/dev/null

cli bot list >"$RUN_DIR/bots-after.json"
python3 - "$RUN_DIR/bots-after.json" <<'PY'
import json, sys
bots = {b["bot_id"]: b for b in json.load(open(sys.argv[1]))}
owner = bots["m11_owner_bot"]
if owner.get("identity") != "owner" or "business" not in owner.get("identities", []):
    raise SystemExit(f"owner 身份不符: {owner}")
nl = bots["m11_nl_bot"]
if nl.get("identity") != "business":
    raise SystemExit(f"业务 Bot 身份不符: {nl}")
if bots["user_m11_bot"].get("identity") != "user":
    raise SystemExit("用户 Bot 身份被插件安装改变")
print("  m11_owner_bot=owner+business, m11_nl_bot=business, user_m11_bot=user")
PY

CAT=$(cli plugin list | python3 -c 'import sys,json; rows={p["plugin_id"]:p for p in json.load(sys.stdin)}; print(rows["plugin_m11_biz"].get("plugin_category",""))')
[[ "$CAT" == "business" ]] || fail "插件列表分类应为 business，实际 $CAT"

step "步骤 6：升级留底，摘要不符与非法分类的失败也能查到"
python3 - <<PY
import json
p="$RUN_DIR/biz.json"
mf=json.load(open(p))
mf["version"]="1.1.0"
json.dump(mf, open(p,"w"))
PY
cli plugin upgrade "$RUN_DIR/biz.json" >/dev/null

printf 'not-the-digest' >"$RUN_DIR/fakebin"
cat >"$RUN_DIR/digest.json" <<EOF
{
  "plugin_id": "plugin_m11_digest",
  "name": "Digest",
  "version": "1.0.0",
  "plugin_type": "workspace",
  "plugin_category": "tool",
  "binary": "$RUN_DIR/fakebin",
  "binary_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "bot_templates": [{"bot_id": "m11_digest_bot", "display_name": "Digest", "activation_mode": "lazy"}]
}
EOF
if cli plugin install "$RUN_DIR/digest.json" >/dev/null 2>&1; then
  fail "摘要不符应被拒绝"
fi
cat >"$RUN_DIR/badcat.json" <<'EOF'
{
  "plugin_id": "plugin_m11_badcat",
  "name": "Bad",
  "version": "1.0.0",
  "plugin_type": "workspace",
  "plugin_category": "nope",
  "bot_templates": [{"bot_id": "m11_bad_bot", "display_name": "Bad", "activation_mode": "lazy"}]
}
EOF
if cli plugin install "$RUN_DIR/badcat.json" >/dev/null 2>&1; then
  fail "非法 plugin_category 应被拒绝"
fi
cat >"$RUN_DIR/prefix.json" <<'EOF'
{
  "plugin_id": "plugin_m11_prefix",
  "name": "Prefix",
  "version": "1.0.0",
  "plugin_type": "workspace",
  "plugin_category": "foundation",
  "bot_templates": [{"bot_id": "create_helper_bot", "display_name": "Nope", "activation_mode": "lazy"}]
}
EOF
if cli plugin install "$RUN_DIR/prefix.json" >/dev/null 2>&1; then
  fail "插件注册 create_ 前缀应被拒绝"
fi

cli plugin lifecycle plugin_m11_digest | python3 -c 'import sys,json; rows=json.load(sys.stdin); assert len(rows)==1 and rows[0]["result"]=="rejected" and rows[0]["digest"] and "摘要" in rows[0].get("reason",""), rows'
cli plugin lifecycle plugin_m11_badcat | python3 -c 'import sys,json; rows=json.load(sys.stdin); assert len(rows)==1 and rows[0]["result"]=="rejected" and rows[0]["action"]=="install", rows'
cli plugin lifecycle plugin_m11_prefix | python3 -c 'import sys,json; rows=json.load(sys.stdin); assert rows and rows[-1]["result"]=="rejected", rows'
echo "  失败安装已写入 lifecycle"

step "步骤 7：theme 不注册 Bot、不进进程监管"
cat >"$RUN_DIR/theme.json" <<'EOF'
{
  "plugin_id": "plugin_m11_theme",
  "name": "Day",
  "version": "1.0.0",
  "plugin_type": "workspace",
  "plugin_category": "theme"
}
EOF
THEME_INSTALL=$(cli plugin install "$RUN_DIR/theme.json")
THEME_DECISION=$(echo "$THEME_INSTALL" | python3 -c 'import sys,json; print(json.load(sys.stdin)["decision_id"])')
cli security-approve "$THEME_DECISION" >/dev/null
THEME_BOTS=$(psql_exec "SELECT count(*) FROM kernel.bot_manifest WHERE parent_plugin='plugin_m11_theme';")
[[ "$THEME_BOTS" == "0" ]] || fail "theme 不应注册 Bot，实际 $THEME_BOTS"
THEME_PROC=$(psql_exec "SELECT count(*) FROM kernel.kernel_plugin_processes WHERE plugin_id='plugin_m11_theme';")
[[ "$THEME_PROC" == "0" ]] || fail "theme 不应进入进程监管，实际 $THEME_PROC"
echo "  theme 无 Bot、无进程"

step "步骤 8：停用与卸载留底，且只增不改"
cli plugin disable plugin_m11_biz >/dev/null
cli plugin uninstall plugin_m11_biz --confirm >/dev/null
STATUS=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_m11_biz';")
[[ "$STATUS" == "uninstalled" ]] || fail "卸载后状态应为 uninstalled，实际 $STATUS"
ACTIONS=$(psql_exec "SELECT string_agg(a.action, ',' ORDER BY a.created_at, a.audit_id) FROM kernel.kernel_config_audit a JOIN kernel.kernel_config_documents d ON d.doc_id=a.doc_id WHERE d.scope_id='plugin_m11_biz' AND d.config_key='lifecycle';")
[[ "$ACTIONS" == "install,upgrade,disable,uninstall" ]] || fail "业务插件留底顺序不符: $ACTIONS"
N=$(psql_exec "SELECT count(*) FROM kernel.kernel_config_documents WHERE scope_id='plugin_m11_biz' AND config_key='lifecycle';")
[[ "$N" == "4" ]] || fail "留底应四条独立文档，实际 $N"
echo "  install,upgrade,disable,uninstall 均只增不改"
echo "SMOKE M11 OK"
