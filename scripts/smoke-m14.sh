#!/usr/bin/env bash
# M14 冒烟：商场用一句话搜到插件，下载只得到提案；
# user_bot 在交互终端按 checksum 批准后才安装；被依赖时单点卸载被拒绝。
# 依据：开发规划 M14，需求规格 R7 / R10 / R15。不新建内核审计表。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${NEXUS_DEV_PG_PORT:-55432}"
export NEXUS_PG_MIGRATE_DSN="${NEXUS_PG_MIGRATE_DSN:-postgres://nexus_migrator:nexus_dev_migrate@127.0.0.1:${PORT}/nexus}"
export NEXUS_PG_KERNEL_DSN="${NEXUS_PG_KERNEL_DSN:-postgres://nexus_kernel:nexus_dev_kernel@127.0.0.1:${PORT}/nexus}"
export NEXUS_KERNEL_CREDENTIAL_KEY="${NEXUS_KERNEL_CREDENTIAL_KEY:-0123456789abcdef0123456789abcdef}"
export NEXUS_KERNEL_SNAPSHOT_KEY="${NEXUS_KERNEL_SNAPSHOT_KEY:-abcdef0123456789abcdef0123456789}"
export NEXUS_KERNEL_PAYLOAD_DIR="${NEXUS_KERNEL_PAYLOAD_DIR:-$ROOT/data/payloads}"

MARKETPLACE_PORT="7614"
MARKETPLACE_URL="http://127.0.0.1:$MARKETPLACE_PORT"
HTTP="127.0.0.1:7214"
HTTP_URL="http://$HTTP"
GRPC="127.0.0.1:7215"
TRANSPORT="127.0.0.1:7216"
RUN_DIR="$ROOT/data/smoke_m14"
DATA_DIR="$RUN_DIR/market"
mkdir -p "$RUN_DIR" "$NEXUS_KERNEL_PAYLOAD_DIR"
rm -rf "$DATA_DIR"
mkdir -p "$DATA_DIR/artifacts"
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
fail() { echo "SMOKE M14 FAIL: $1" >&2; exit 1; }
if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "nexus-pg-m2"; then
  PG_CONTAINER="nexus-pg-m2"
elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "nexus-pg-m1"; then
  PG_CONTAINER="nexus-pg-m1"
else
  PG_CONTAINER="nexus-pg-m1"
fi
psql_exec() { docker exec -i "$PG_CONTAINER" psql -U postgres -d nexus -tAc "$1"; }
cli() { ./bin/cli "$@" -hub-http "$HTTP_URL" -credential-file "$CRED"; }

step "步骤 1：单测，并确认没有新建审计表"
if grep -R -E "CREATE TABLE.*(kernel_plugin_audit_logs|kernel_marketplace)" migrations/kernel >/dev/null; then
  fail "内核迁移出现了禁止的商场或审计表"
fi
go test ./internal/modules/marketplace/ ./hub/plugin/ ./hub/httpapi/ -count=1 \
  -run 'TestSearchSentenceShowsRiskAndDownloadStaysProposal|TestPlanUninstallBlocksSingleAndReturnsReverseOrder|TestDownloadRecordDoesNotInstall|TestPresentedChecksumAndDependencyDepth|TestCascadeUninstallReverseOrder|TestM10ToolCenterAndDelegationAPI' \
  || fail "M14 单测失败"
echo "目录检查、checksum 与逆拓扑单测通过"

step "步骤 2：准备目录制品并启动商场"
python3 - "$DATA_DIR" <<'PY'
import hashlib, json, os, sys
root = sys.argv[1]
arts = os.path.join(root, "artifacts")
os.makedirs(arts, exist_ok=True)

def manifest(pid, name, desc, category, deps, bot):
    body = {
        "plugin_id": pid,
        "name": name,
        "version": "1.0.0",
        "plugin_type": "workspace",
        "plugin_category": category,
        "description": desc,
        "deps": deps,
        "bot_templates": [{"bot_id": bot, "display_name": name, "activation_mode": "lazy"}],
        "schema_version": 1,
    }
    raw = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode()
    path = os.path.join(arts, pid + ".bin")
    open(path, "wb").write(raw)
    digest = hashlib.sha256(raw).hexdigest()
    return {
        "plugin_id": pid,
        "name": name,
        "description": desc,
        "risk_level": "low" if category == "foundation" else "medium",
        "tags": [name, desc],
        "digest": digest,
        "artifact": "artifacts/" + pid + ".bin",
        "manifest": body,
    }

entries = [
    manifest("plugin_m14_mesh", "探针网格", "本地探针", "foundation", [], "m14_mesh_bot"),
    manifest("plugin_m14_browser", "浏览器", "提供网页浏览与页面快照", "business", ["plugin_m14_mesh"], "m14_browser_bot"),
    manifest("plugin_m14_cyclea", "环A", "循环依赖甲", "business", ["plugin_m14_cycleb"], "m14_cyclea_bot"),
    manifest("plugin_m14_cycleb", "环B", "循环依赖乙", "business", ["plugin_m14_cyclea"], "m14_cycleb_bot"),
    manifest("plugin_m14_d1", "一层", "基础层", "foundation", [], "m14_d1_bot"),
    manifest("plugin_m14_d2", "二层", "中间层", "business", ["plugin_m14_d1"], "m14_d2_bot"),
    manifest("plugin_m14_d3", "三层", "过深层", "business", ["plugin_m14_d2"], "m14_d3_bot"),
]
open(os.path.join(root, "catalog.json"), "w").write(json.dumps({"plugins": entries}, ensure_ascii=False))
print("catalog", len(entries))
PY
go build -o bin/plugins/plugin_marketplace ./plugins/plugin_marketplace
go build -o bin/hub ./hub
go build -o bin/cli ./cli
go build -o bin/bot-runtime ./bot-runtime
./bin/plugins/plugin_marketplace -port "$MARKETPLACE_PORT" -data "$DATA_DIR" >"$RUN_DIR/marketplace.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 30); do
  curl -sf "$MARKETPLACE_URL/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -sf "$MARKETPLACE_URL/healthz" >/dev/null 2>&1 || fail "商场未就绪"

step "步骤 3：一句话检索，展示依赖、深度、环路和摘要"
SEARCH=$(curl -s "$MARKETPLACE_URL/api/marketplace/search?q=$(python3 -c 'import urllib.parse; print(urllib.parse.quote("帮我找一个能浏览网页的浏览器"))')")
python3 -c 'import json,sys
body=json.loads(sys.argv[1])
hits=body.get("plugins") or []
assert len(hits)==1 and hits[0]["plugin_id"]=="plugin_m14_browser", hits
hit=hits[0]
assert hit["installed"] is False, hit
assert hit["digest_match"] is True, hit
assert hit["has_cycle"] is False, hit
assert hit["depth"]==2 and hit["max_depth"]==2, hit
assert hit["dependencies"]==["plugin_m14_mesh"], hit
' "$SEARCH" || fail "检索结果不符: $SEARCH"
echo "一句话命中浏览器，目录项未标成已安装"

step "步骤 4：下载只得到提案；错误摘要、环路和深度被拒绝"
DIGEST=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["plugins"][0]["digest"])' "$SEARCH")
DL=$(curl -s -X POST "$MARKETPLACE_URL/api/marketplace/download" \
  -H 'Content-Type: application/json' \
  -d "{\"plugin_id\":\"plugin_m14_browser\",\"digest\":\"$DIGEST\"}")
python3 -c 'import json,sys
body=json.loads(sys.argv[1])
assert body.get("status")=="proposed" and body.get("installed") is False and body.get("process_started") is False, body
assert body.get("proposal_id") and body.get("manifest_checksum"), body
open(sys.argv[2],"w").write(json.dumps(body["manifest"], ensure_ascii=False))
' "$DL" "$RUN_DIR/browser.json" || fail "下载未停留在提案: $DL"
BAD=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$MARKETPLACE_URL/api/marketplace/download" \
  -H 'Content-Type: application/json' \
  -d '{"plugin_id":"plugin_m14_browser","digest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}')
echo "$BAD" | grep -q "HTTP_CODE:400" || fail "错误摘要应返回 400: $BAD"
echo "$BAD" | grep -q "摘要不符" || fail "错误摘要应写明摘要不符: $BAD"
CYCLE=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$MARKETPLACE_URL/api/marketplace/download" \
  -H 'Content-Type: application/json' -d '{"plugin_id":"plugin_m14_cyclea","digest":""}')
echo "$CYCLE" | grep -q "HTTP_CODE:400" || fail "环路应返回 400: $CYCLE"
echo "$CYCLE" | grep -q "循环依赖" || fail "环路原因不符: $CYCLE"
DEEP=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$MARKETPLACE_URL/api/marketplace/download" \
  -H 'Content-Type: application/json' -d '{"plugin_id":"plugin_m14_d3","digest":""}')
echo "$DEEP" | grep -q "HTTP_CODE:400" || fail "深度超限应返回 400: $DEEP"
echo "$DEEP" | grep -q "依赖层级" || fail "深度原因不符: $DEEP"
INST=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$MARKETPLACE_URL/api/marketplace/install" \
  -H 'Content-Type: application/json' -d '{"plugin_id":"plugin_m14_browser","user_confirmed":true}')
echo "$INST" | grep -q "HTTP_CODE:409" || fail "商场安装入口应拒绝激活: $INST"
echo "$INST" | grep -q "不能把插件标成已安装" || fail "安装拒绝原因不符: $INST"
echo "$INST" | grep -q "installed_and_activated" && fail "商场不应返回已激活"
test ! -f "$DATA_DIR/installed.json" || fail "下载不应写入已安装集合"
echo "下载只留下提案"

step "步骤 5：重启商场后提案仍在，且未安装"
kill "${PIDS[-1]}" 2>/dev/null || true
wait "${PIDS[-1]}" 2>/dev/null || true
unset 'PIDS[-1]'
./bin/plugins/plugin_marketplace -port "$MARKETPLACE_PORT" -data "$DATA_DIR" >"$RUN_DIR/marketplace.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 30); do
  curl -sf "$MARKETPLACE_URL/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -sf "$MARKETPLACE_URL/healthz" >/dev/null 2>&1 || fail "商场重启失败"
python3 -c 'import json; props=json.load(open("'"$DATA_DIR"'/proposals.json")); assert props and props[-1]["installed"] is False and props[-1]["plugin_id"]=="plugin_m14_browser"' \
  || fail "重启后提案丢失"
echo "提案在商场目录中，installed 仍为 false"

step "步骤 6：迁移并启动 hub，下载留底不改变安装状态"
if ! docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$PG_CONTAINER"; then
  "$ROOT/scripts/dev-pg.sh"
fi
go run ./cmd/migrate -cmd up -dsn "$NEXUS_PG_MIGRATE_DSN" -dir migrations/kernel >/dev/null
AUDIT_TABLE=$(psql_exec "SELECT to_regclass('kernel.kernel_plugin_audit_logs');")
[[ -z "$AUDIT_TABLE" ]] || fail "不应新建 kernel_plugin_audit_logs"
IDS="'plugin_m14_mesh','plugin_m14_browser'"
BOTS="'m14_mesh_bot','m14_browser_bot'"
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

./bin/hub -listen-http "$HTTP" -listen-grpc "$GRPC" -listen-transport "$TRANSPORT" \
  -user-credential-file "$CRED" -bot-binary "$ROOT/bin/bot-runtime" \
  -marketplace-url "$MARKETPLACE_URL" \
  >"$RUN_DIR/hub.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 40); do
  curl -sf "$HTTP_URL/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "$HTTP_URL/healthz" >/dev/null || { cat "$RUN_DIR/hub.log"; fail "hub 未就绪"; }

MANIFEST_SUM=$(python3 -c 'import json; print(json.load(open("'"$RUN_DIR"'/browser.json")) and "")' )
# manifest checksum comes from the download response saved beside the search; re-read proposal
MANIFEST_SUM=$(python3 -c 'import json; print(json.load(open("'"$DATA_DIR"'/proposals.json"))[-1]["manifest_checksum"])')
cli plugin download-record plugin_m14_browser --digest "$DIGEST" --result ok --reason "下载提案" >/dev/null
cli plugin download-record plugin_m14_browser --digest "bad" --result rejected --reason "摘要不符" >/dev/null
COUNT=$(psql_exec "SELECT count(*) FROM kernel.kernel_plugins WHERE plugin_id='plugin_m14_browser';")
[[ "$COUNT" == "0" ]] || fail "下载留底后不应出现在 kernel_plugins: $COUNT"
ACTIONS=$(psql_exec "SELECT string_agg(a.action || ':' || (d.spec->>'result'), ',' ORDER BY a.audit_id) FROM kernel.kernel_config_audit a JOIN kernel.kernel_config_documents d ON d.doc_id=a.doc_id WHERE d.scope_id='plugin_m14_browser' AND d.config_key='lifecycle' AND a.action='download';")
[[ "$ACTIONS" == "download:ok,download:rejected" ]] || fail "下载留底不符: $ACTIONS"
echo "下载成败已写入既有审计，插件仍未安装"

step "步骤 7：security_bot 预审后，user_bot 按 checksum 在交互终端批准"
MESH_RAW=$(python3 -c 'import json; print(json.dumps(next(p["manifest"] for p in json.load(open("'"$DATA_DIR"'/catalog.json"))["plugins"] if p["plugin_id"]=="plugin_m14_mesh"), ensure_ascii=False))')
printf '%s' "$MESH_RAW" >"$RUN_DIR/mesh.json"
INSTALL_MESH=$(cli plugin install "$RUN_DIR/mesh.json")
MESH_DECISION=$(echo "$INSTALL_MESH" | python3 -c 'import json,sys; print(json.load(sys.stdin)["decision_id"])')
PENDING=$(cli security-list pending)
python3 -c 'import json,sys
rows=json.loads(sys.argv[1])
hit=[r for r in rows if r.get("subject_id")=="plugin_m14_mesh"]
assert hit and hit[0]["decision"]=="pending" and hit[0]["decided_by"]=="security_bot", hit
assert len(hit[0].get("checksum") or "")==64
open(sys.argv[2],"w").write(hit[0]["checksum"])
' "$PENDING" "$RUN_DIR/mesh.checksum" || fail "预审决定不符: $PENDING"
MESH_SUM=$(cat "$RUN_DIR/mesh.checksum")
if cli security-approve "$MESH_DECISION" --checksum "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" >/dev/null 2>"$RUN_DIR/bad-approve.txt"; then
  fail "错误 checksum 不应批准"
fi
grep -q "不一致" "$RUN_DIR/bad-approve.txt" || { cat "$RUN_DIR/bad-approve.txt"; fail "错误 checksum 原因不符"; }
if cli security-approve "$MESH_DECISION" --checksum "$MESH_SUM" >/dev/null 2>"$RUN_DIR/notty.txt"; then
  fail "非交互终端不应批准"
fi
grep -q "真实交互终端" "$RUN_DIR/notty.txt" || { cat "$RUN_DIR/notty.txt"; fail "缺少终端拒绝提示"; }
python3 - "$MESH_DECISION" "$MESH_SUM" <<PY
import os, pty, subprocess, sys
dec, checksum = sys.argv[1], sys.argv[2]
master, slave = pty.openpty()
proc = subprocess.Popen(
    ["./bin/cli", "security-approve", dec, "--checksum", checksum, "-hub-http", "$HTTP_URL", "-credential-file", "$CRED"],
    stdin=slave, stdout=slave, stderr=slave)
os.write(master, b"yes\n")
code = proc.wait(timeout=20)
os.close(master)
if code != 0:
    raise SystemExit(code)
PY
STATUS=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_m14_mesh';")
[[ "$STATUS" == "enabled" ]] || fail "批准后 mesh 应为 enabled，实际: $STATUS"
echo "user_bot 按 checksum 批准后 mesh 已启用"

step "步骤 8：安装依赖方后，单点卸载基础插件被拒绝，级联按逆序卸下"
INSTALL_APP=$(cli plugin install "$RUN_DIR/browser.json")
APP_DECISION=$(echo "$INSTALL_APP" | python3 -c 'import json,sys; print(json.load(sys.stdin)["decision_id"])')
APP_PENDING=$(cli security-list pending)
APP_SUM=$(python3 -c 'import json,sys
rows=json.loads(sys.argv[1])
hit=[r for r in rows if str(r.get("decision_id"))==sys.argv[2]]
assert hit, rows
print(hit[0]["checksum"])
' "$APP_PENDING" "$APP_DECISION")
python3 - "$APP_DECISION" "$APP_SUM" <<PY
import os, pty, subprocess, sys
dec, checksum = sys.argv[1], sys.argv[2]
master, slave = pty.openpty()
proc = subprocess.Popen(
    ["./bin/cli", "security-approve", dec, "--checksum", checksum, "-hub-http", "$HTTP_URL", "-credential-file", "$CRED"],
    stdin=slave, stdout=slave, stderr=slave)
os.write(master, b"yes\n")
code = proc.wait(timeout=20)
os.close(master)
if code != 0:
    raise SystemExit(code)
PY
APP_STATUS=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_m14_browser';")
[[ "$APP_STATUS" == "enabled" ]] || fail "浏览器应为 enabled，实际: $APP_STATUS"
if cli plugin uninstall plugin_m14_mesh >/dev/null 2>"$RUN_DIR/uninstall.txt"; then
  fail "被依赖时单点卸载应被拒绝"
fi
grep -q "依赖" "$RUN_DIR/uninstall.txt" || { cat "$RUN_DIR/uninstall.txt"; fail "单点卸载原因不符"; }
STILL=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_m14_mesh';")
[[ "$STILL" == "enabled" ]] || fail "拒绝后 mesh 应仍为 enabled: $STILL"
cli plugin uninstall plugin_m14_mesh --cascade >/dev/null
ORDER=$(psql_exec "SELECT string_agg(scope_id, ',' ORDER BY created_at, scope_id) FROM kernel.kernel_config_documents WHERE config_key='lifecycle' AND scope_id IN ($IDS) AND (spec->>'action')='uninstall' AND (spec->>'result')='ok';")
[[ "$ORDER" == "plugin_m14_browser,plugin_m14_mesh" ]] || fail "级联卸载顺序应为先浏览器后网格: $ORDER"
MESH_GONE=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_m14_mesh';")
APP_GONE=$(psql_exec "SELECT status FROM kernel.kernel_plugins WHERE plugin_id='plugin_m14_browser';")
[[ "$MESH_GONE" == "uninstalled" && "$APP_GONE" == "uninstalled" ]] || fail "级联后状态 mesh=$MESH_GONE browser=$APP_GONE"
echo "单点卸载被拒绝，级联先停依赖方再停基础插件"

echo
echo "SMOKE M14 PASS"
