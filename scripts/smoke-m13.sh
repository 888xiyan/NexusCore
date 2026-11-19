#!/usr/bin/env bash
# M13 冒烟：普通工具参数里的 URL 不能外联；已授权搜索走 DNS/HTTPS 并通知 user_bot；
# 未绑定探针的 Bot 不能派发 CLI。手工写入 search_allowlist 轨迹不算通过。
# 依据：开发规划 M13，需求规格 R9 / R11 / R12。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TOOL_CENTER_PORT="7413"
TOOL_CENTER_URL="http://127.0.0.1:$TOOL_CENTER_PORT"
HTTPS_PORT="17443"
RUN_DIR="$ROOT/data/smoke_m13"
DATA_DIR="$RUN_DIR/tool_store"
mkdir -p "$RUN_DIR"
rm -rf "$DATA_DIR"
: >"$RUN_DIR/https_hits.log"

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "SMOKE M13 FAIL: $1" >&2; exit 1; }

step "步骤 1：沙箱单测、探针拒绝外联、未绑定 Bot 拒绝派发 CLI"
go test ./internal/modules/toolcenter/ ./agent-daemon/ ./bot-runtime/ ./hub/httpapi/ -count=1 \
  -run 'TestIsolateBlocksURLOnOrdinaryTool|TestSearchDialNotifiesOnlyAfterHTTPS|TestHTTPURLDoesNotOpenSearch|TestToolCenterService_ExecutionAndNetworkAudit|TestSandboxToolIgnoresCallerNetworkAndBlocksURL|TestUnboundBotDoesNotDialCLI|TestCLIDispatchAllowed|TestMergeAgentBindingKeepsCLI' \
  || fail "M13 单测失败"

step "步骤 2：拉起本地 HTTPS，确认普通工具不会打到它"
openssl req -x509 -newkey rsa:2048 \
  -keyout "$RUN_DIR/key.pem" -out "$RUN_DIR/cert.pem" -days 1 -nodes \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
  >/dev/null 2>&1 || fail "生成本地证书失败"
python3 - "$HTTPS_PORT" "$RUN_DIR/cert.pem" "$RUN_DIR/key.pem" "$RUN_DIR/https_hits.log" <<'PY' &
import http.server, ssl, sys
port, cert, key, log = int(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(log, "a", encoding="utf-8") as fh:
            fh.write(self.path + "\n")
        body = b"search-ok"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, fmt, *args):
        return
httpd = http.server.HTTPServer(("127.0.0.1", port), H)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(cert, key)
httpd.socket = ctx.wrap_socket(httpd.socket, server_side=True)
httpd.serve_forever()
PY
PIDS+=("$!")
for _ in $(seq 1 30); do
  curl -skf "https://127.0.0.1:$HTTPS_PORT/health" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -skf "https://127.0.0.1:$HTTPS_PORT/health" >/dev/null 2>&1 || fail "本地 HTTPS 未就绪"
: >"$RUN_DIR/https_hits.log"

go build -o bin/plugins/plugin_tool_center ./plugins/plugin_tool_center
./bin/plugins/plugin_tool_center -port "$TOOL_CENTER_PORT" -data "$DATA_DIR" \
  -sandbox-ca "$RUN_DIR/cert.pem" -audit-extra "assistant_bot" \
  >"$RUN_DIR/tool_center.log" 2>&1 &
PIDS+=("$!")
for _ in $(seq 1 30); do
  curl -sf "$TOOL_CENTER_URL/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -sf "$TOOL_CENTER_URL/healthz" >/dev/null 2>&1 || fail "ToolCenter 启动超时"

GRANT=$(curl -s -X POST "$TOOL_CENTER_URL/api/grants" \
  -H "Content-Type: application/json" \
  -d '{"bot_id":"user_m13_bot","tool_id":"sys.git.status","permission":"execute","granted_by":"user_bot"}')
echo "$GRANT" | grep -q '"granted"' || fail "授予离线工具失败: $GRANT"
BLOCKED=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$TOOL_CENTER_URL/api/tools/sys.git.status/execute" \
  -H "Content-Type: application/json" \
  -d "{\"bot_id\":\"user_m13_bot\",\"params\":{\"cmd\":\"curl https://localhost:${HTTPS_PORT}/blocked\"}}")
echo "$BLOCKED" | grep -q "HTTP_CODE:403" || fail "普通工具外联应被拒绝: $BLOCKED"
if [[ -s "$RUN_DIR/https_hits.log" ]]; then
  fail "普通工具仍然打到了 HTTPS 服务: $(cat "$RUN_DIR/https_hits.log")"
fi
echo "普通工具外联被拒绝，本地 HTTPS 没有收到请求"

step "步骤 3：已授权搜索走 DNS/HTTPS，并投递给 user_bot"
curl -s -X PUT "$TOOL_CENTER_URL/api/tools/sys.search.web/named-bots" \
  -H "Content-Type: application/json" \
  -d '{"bot_ids":["user_m13_bot"]}' >/dev/null
curl -s -X POST "$TOOL_CENTER_URL/api/grants" \
  -H "Content-Type: application/json" \
  -d '{"bot_id":"user_m13_bot","tool_id":"sys.search.web","permission":"execute","granted_by":"user_bot"}' >/dev/null
SEARCH=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$TOOL_CENTER_URL/api/tools/sys.search.web/execute" \
  -H "Content-Type: application/json" \
  -d "{\"bot_id\":\"user_m13_bot\",\"params\":{\"query\":\"https://localhost:${HTTPS_PORT}/q\"}}")
echo "$SEARCH" | grep -q "HTTP_CODE:200" || fail "搜索外联失败: $SEARCH"
echo "$SEARCH" | grep -q '"network_triggered":true' || fail "搜索未标记已联网: $SEARCH"
echo "$SEARCH" | grep -q '"egress_target":"localhost"' || fail "搜索未记录目标域名: $SEARCH"
grep -q "/q" "$RUN_DIR/https_hits.log" || fail "搜索没有真正请求本地 HTTPS"
AUDIT=$(curl -s "$TOOL_CENTER_URL/api/audit?to=user_bot")
python3 -c 'import json,sys; body=json.loads(sys.argv[1]); notes=body.get("notices") or [];
assert notes and notes[0]["from_bot_id"]=="user_m13_bot" and notes[0]["tool_id"]=="sys.search.web" and notes[0]["domain"]=="localhost", notes' \
  "$AUDIT" || fail "user_bot 没有收到搜索审计: $AUDIT"
EXTRA=$(curl -s "$TOOL_CENTER_URL/api/audit?to=assistant_bot")
python3 -c 'import json,sys; body=json.loads(sys.argv[1]); notes=body.get("notices") or []; assert notes and notes[0]["domain"]=="localhost", notes' \
  "$EXTRA" || fail "可配置接收者没有收到同一条审计: $EXTRA"
TRACES=$(curl -s "$TOOL_CENTER_URL/api/traces?bot_id=user_m13_bot")
python3 -c 'import json,sys; body=json.loads(sys.argv[1]); traces=body.get("traces") or [];
hit=[t for t in traces if t.get("tool_id")=="sys.search.web" and t.get("networked")];
assert hit and hit[-1].get("distilled"), traces' \
  "$TRACES" || fail "工具目录里没有联网轨迹: $TRACES"
echo "搜索已外联，审计已投递给 user_bot"

step "步骤 4：工坊 agents 入口独立，Bot 编辑页从搜寻结果选择探针"
grep -q "path: '/agents'" Frontend/src/plugins/agent-discovery/index.ts || fail "agents 入口不存在"
grep -q "保存探针绑定" Frontend/src/views/BotDrawer.vue || fail "Bot 编辑页缺少探针下拉保存"
if grep -R -E "CREATE TABLE.*kernel_tool_trajectories" migrations/kernel >/dev/null; then
  fail "内核迁移出现了禁止的轨迹表"
fi
echo "agents 入口与探针绑定页面已就位"

echo
echo "SMOKE M13 PASS"
