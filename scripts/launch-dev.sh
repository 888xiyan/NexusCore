#!/usr/bin/env bash
# ==============================================================================
# scripts/launch-dev.sh: NexusCore M4 开发者全栈启动脚本 (All-in-One Dev Launcher)
#
# 编排组件：
# 1. PostgreSQL 16+ 探测与数据库迁移
# 2. Kernel Hub 微内核网关 (HTTP: 7200, gRPC: 7201, Transport: 7202)
# 3. Agent Daemon (受控 loopback_agent 探针反向接入 Hub)
# 4. GroupChatPlugin 独立插件服务 (Port: 7300, 两级进程管理)
# 5. Frontend Vue 3 + Vite 前端工作台开发服务 (Port: 5173)
#
# 具备预检诊断、PID 追踪、进程组隔离、反向优雅退出与交互式监控大屏。
# ==============================================================================
set -meuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# --- ANSI 颜色代码 ---
C_RESET="\033[0m"
C_BOLD="\033[1m"
C_GREEN="\033[32m"
C_CYAN="\033[36m"
C_YELLOW="\033[33m"
C_RED="\033[31m"
C_BLUE="\033[34m"

# --- 配置与环境参数 ---
PORT="${NEXUS_DEV_PG_PORT:-55432}"
export NEXUS_PG_MIGRATE_DSN="${NEXUS_PG_MIGRATE_DSN:-postgres://nexus_migrator:nexus_dev_migrate@127.0.0.1:${PORT}/nexus}"
export NEXUS_PG_KERNEL_DSN="${NEXUS_PG_KERNEL_DSN:-postgres://nexus_kernel:nexus_dev_kernel@127.0.0.1:${PORT}/nexus}"
export NEXUS_KERNEL_CREDENTIAL_KEY="${NEXUS_KERNEL_CREDENTIAL_KEY:-0123456789abcdef0123456789abcdef}"
export NEXUS_KERNEL_SNAPSHOT_KEY="${NEXUS_KERNEL_SNAPSHOT_KEY:-abcdef0123456789abcdef0123456789}"
export NEXUS_KERNEL_PAYLOAD_DIR="${NEXUS_KERNEL_PAYLOAD_DIR:-$ROOT/data/payloads}"

HTTP_HOST="127.0.0.1"
HUB_HTTP_PORT="${HUB_HTTP_PORT:-7200}"
HUB_GRPC_PORT="${HUB_GRPC_PORT:-7201}"
HUB_TRANSPORT_PORT="${HUB_TRANSPORT_PORT:-7202}"
CHAT_PORT="${CHAT_PORT:-7300}"
TOOL_CENTER_PORT="${TOOL_CENTER_PORT:-7400}"
FRONTEND_PORT="${FRONTEND_PORT:-5173}"

DEV_DIR="$ROOT/data/dev_run"
LOG_DIR="$DEV_DIR/logs"
PID_DIR="$DEV_DIR/pids"
MEMORY_BASE_DIR="$ROOT/data/bots"

mkdir -p "$LOG_DIR" "$PID_DIR" "$NEXUS_KERNEL_PAYLOAD_DIR" "$MEMORY_BASE_DIR"

# --- 动态选择 PostgreSQL 容器 ---
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
  docker exec -i "$PG_CONTAINER" psql -U postgres -d nexus -tAc "$1" 2>/dev/null || true
}

# --- 进程追踪字典 ---
declare -A SERVICE_PIDS
declare -A SERVICE_PGIDS

# --- 预检清理占用端口 ---
cleanup_stale_ports() {
  local ports=("$HUB_HTTP_PORT" "$HUB_GRPC_PORT" "$HUB_TRANSPORT_PORT" "$CHAT_PORT" "$TOOL_CENTER_PORT" "$FRONTEND_PORT")
  for p in "${ports[@]}"; do
    if command -v fuser >/dev/null 2>&1; then
      fuser -k "${p}/tcp" 2>/dev/null || true
    fi
  done
}

# --- 优雅停机处理器 (Reverse Teardown) ---
TEARDOWN_DONE=0
teardown() {
  if [[ "$TEARDOWN_DONE" -eq 1 ]]; then
    return
  fi
  TEARDOWN_DONE=1

  printf "\n${C_YELLOW}>>> 正在优雅停止 NexusCore M4 全栈服务...${C_RESET}\n"

  local script_pgid
  script_pgid="$(ps -o pgid= -p $$ 2>/dev/null | tr -d ' ' || echo "$$")"

  # 1. 停止前端 (Vite)
  if [[ -n "${SERVICE_PIDS[frontend]:-}" ]]; then
    printf "  停止 Frontend (PID %s)... " "${SERVICE_PIDS[frontend]}"
    kill -TERM "${SERVICE_PIDS[frontend]}" 2>/dev/null || true
    local f_pgid="${SERVICE_PGIDS[frontend]:-}"
    if [[ -n "$f_pgid" && "$f_pgid" != "$script_pgid" && "$f_pgid" != "$$" ]]; then
      kill -TERM -"$f_pgid" 2>/dev/null || true
    fi
    pkill -f "vite.*$FRONTEND_PORT" 2>/dev/null || true
    pkill -f "vite.*5173" 2>/dev/null || true
    printf "${C_GREEN}[OK]${C_RESET}\n"
  fi

  # 1b. 停止工具中心（先于 Hub，目录已落盘）
  if [[ -n "${SERVICE_PIDS[toolcenter]:-}" ]]; then
    printf "  停止 plugin_tool_center (PID %s)... " "${SERVICE_PIDS[toolcenter]}"
    kill -TERM "${SERVICE_PIDS[toolcenter]}" 2>/dev/null || true
    printf "${C_GREEN}[OK]${C_RESET}\n"
  fi

  # 2. 停止 GroupChat 插件
  if [[ -n "${SERVICE_PIDS[groupchat]:-}" ]]; then
    printf "  停止 GroupChatPlugin (PID %s)... " "${SERVICE_PIDS[groupchat]}"
    ./bin/cli plugin stop plugin_groupchat -hub-http "http://$HTTP_HOST:$HUB_HTTP_PORT" -credential-file "$DEV_DIR/user_bot.cred" >/dev/null 2>&1 || true
    kill -TERM "${SERVICE_PIDS[groupchat]}" 2>/dev/null || true
    pkill -f "bin/plugins/plugin_groupchat" 2>/dev/null || true
    printf "${C_GREEN}[OK]${C_RESET}\n"
  fi

  # 3. 停止 Agent Daemon
  if [[ -n "${SERVICE_PIDS[agent]:-}" ]]; then
    printf "  停止 Agent Daemon (PID %s)... " "${SERVICE_PIDS[agent]}"
    kill -TERM "${SERVICE_PIDS[agent]}" 2>/dev/null || true
    printf "${C_GREEN}[OK]${C_RESET}\n"
  fi

  # 4. 停止 Hub Gateway
  if [[ -n "${SERVICE_PIDS[hub]:-}" ]]; then
    printf "  停止 Kernel Hub (PID %s)... " "${SERVICE_PIDS[hub]}"
    kill -TERM "${SERVICE_PIDS[hub]}" 2>/dev/null || true
    printf "${C_GREEN}[OK]${C_RESET}\n"
  fi

  # 5. 兜底回收任何残留的 Bot 运行时进程与插件进程
  pkill -f "$ROOT/bin/bot-runtime" 2>/dev/null || true
  pkill -f "bin/plugins/plugin_groupchat" 2>/dev/null || true

  # 6. 等待并升级为 SIGKILL（若 4 秒内未退出，每个服务独立计时）
  for pid in "${SERVICE_PIDS[@]}"; do
    local wait_cycles=0
    while kill -0 "$pid" 2>/dev/null; do
      sleep 0.2
      wait_cycles=$((wait_cycles + 1))
      if [[ "$wait_cycles" -ge 20 ]]; then
        kill -9 "$pid" 2>/dev/null || true
        break
      fi
    done
  done

  # 清理 PID 文件
  rm -rf "$PID_DIR"/*

  printf "${C_GREEN}>>> NexusCore 全栈服务已安全退出并释放所有端口。${C_RESET}\n"
}

trap teardown INT TERM HUP EXIT

# --- 日志工具函数 ---
log_info() { printf "${C_CYAN}[INFO]${C_RESET} %s\n" "$1"; }
log_ok()   { printf "${C_GREEN}[OK]${C_RESET} %s\n" "$1"; }
log_warn() { printf "${C_YELLOW}[WARN]${C_RESET} %s\n" "$1"; }
log_err()  { printf "${C_RED}[ERROR]${C_RESET} %s\n" "$1" >&2; }

dump_log_and_exit() {
  local service="$1"
  local log_file="$2"
  log_err "服务 $service 启动异常！最近 25 行日志 ($log_file):"
  printf "${C_RED}------------------------------------------------------------${C_RESET}\n"
  tail -n 25 "$log_file" 2>/dev/null || true
  printf "${C_RED}------------------------------------------------------------${C_RESET}\n"
  exit 1
}

# ==============================================================================
# 步骤 1：预检构建与数据库依赖
# ==============================================================================
log_info "1. 检查构建产物与数据库依赖..."

# 检查可执行二进制
if [[ ! -f "bin/hub" || ! -f "bin/agent-daemon" || ! -f "bin/bot-runtime" || ! -f "bin/cli" || ! -f "bin/plugins/plugin_groupchat" ]]; then
  log_info "正在编译内核制品与插件..."
  make build >/dev/null
  make build-plugins >/dev/null
fi
log_ok "二进制构建完毕"

# 数据库迁移
log_info "检查数据库迁移..."
go run ./cmd/migrate -cmd up -dsn "$NEXUS_PG_MIGRATE_DSN" -dir migrations/kernel >/dev/null 2>&1 || {
  log_warn "数据库迁移执行受阻（请确保已运行 make compose-up 或 scripts/dev-pg.sh）"
}

# 同步二进制摘要
BOT_RUNTIME_DIGEST=$(sha256sum "$ROOT/bin/bot-runtime" | awk '{print $1}')
psql_exec "UPDATE kernel.bot_manifest SET binary_path='$ROOT/bin/bot-runtime', binary_digest='$BOT_RUNTIME_DIGEST', manifest_digest='$BOT_RUNTIME_DIGEST', updated_at=now() WHERE bot_id IN ('echo_bot','user_bot','create_bot','monitor_bot','security_bot');"
psql_exec "UPDATE kernel.bot_manifest SET binary_path='$ROOT/bin/bot-runtime', binary_digest='$BOT_RUNTIME_DIGEST', manifest_digest='$BOT_RUNTIME_DIGEST', updated_at=now() WHERE bot_id='groupchat_bot';"

GROUPCHAT_DIGEST=$(sha256sum "$ROOT/bin/plugins/plugin_groupchat" | awk '{print $1}')
python3 -c "
import json
with open('GroupChatPlugin/manifest.json', 'r') as f:
    mf = json.load(f)
mf['binary'] = '$ROOT/bin/plugins/plugin_groupchat'
mf['binary_digest'] = '$GROUPCHAT_DIGEST'
with open('GroupChatPlugin/manifest.json', 'w') as f:
    json.dump(mf, f, indent=2)
" 2>/dev/null || true

# 注册默认开发 Bot: ops_bot 与 qa_bot
psql_exec "
INSERT INTO kernel.bot_manifest (bot_id, role, managed_by, display_name, capabilities, binary_path, binary_digest, manifest_digest, activation_mode, model, prompt_ref)
VALUES 
  ('ops_bot', 'plugin', 'user_bot', 'Ops Bot', '[\"ops\", \"agent_control\"]'::jsonb, '$ROOT/bin/bot-runtime', '$BOT_RUNTIME_DIGEST', '$BOT_RUNTIME_DIGEST', 'lazy', 'claude-3-5-sonnet', 'prompts/ops_bot.txt'),
  ('qa_bot', 'plugin', 'user_bot', 'QA Bot', '[\"qa\", \"test_runner\"]'::jsonb, '$ROOT/bin/bot-runtime', '$BOT_RUNTIME_DIGEST', '$BOT_RUNTIME_DIGEST', 'lazy', 'claude-3-5-sonnet', 'prompts/qa_bot.txt')
ON CONFLICT (bot_id) DO UPDATE SET binary_path=EXCLUDED.binary_path, binary_digest=EXCLUDED.binary_digest, manifest_digest=EXCLUDED.manifest_digest;

INSERT INTO kernel.bots (bot_id, desired_state, desired_by, observed_state, enabled)
VALUES 
  ('ops_bot', 'start', 'create_bot', 'dormant', true),
  ('qa_bot', 'start', 'create_bot', 'dormant', true)
ON CONFLICT (bot_id) DO UPDATE SET enabled=true, desired_state='start';
"

# 初始化 ops_bot 单体记忆栈 (Claude Code MEMORY.md ≤ 200 行)
OPS_MEM_DIR="$MEMORY_BASE_DIR/ops_bot/memory"
mkdir -p "$OPS_MEM_DIR/transcripts" "$OPS_MEM_DIR/notes" "$OPS_MEM_DIR/representations"
if [[ ! -f "$OPS_MEM_DIR/MEMORY.md" ]]; then
  cat <<'EOF' >"$OPS_MEM_DIR/MEMORY.md"
# ops_bot 长期核心记忆（Injected Context）

> 铁律约束: 本文件行数必须严格 ≤ 200 行，零二次方膨胀，高 Prompt Caching 命中
- 角色定位: 简单轻量 Agent，只调用系统提供的功能与工具
- 禁执行 Shell: 禁止自身执行任何 bash/sh 命令；所有受控物理操作必须调度 agent-daemon
- 禁长思考: 单次推理超时硬封顶 15s，杜绝递归发散
- 关键约束: 遇到构建环境检查请求，调用 loopback_agent 验证环境并输出结构化卡片
EOF
fi

# 前端依赖检查
if [[ -d "Frontend" && ! -d "Frontend/node_modules" && -f "Frontend/package.json" ]]; then
  log_info "Frontend/node_modules 不存在，正在安装前端依赖..."
  (cd Frontend && npm install) || log_warn "npm install 失败，请检查网络"
fi

# 释放残留端口
cleanup_stale_ports

# ==============================================================================
# 步骤 2：启动 Kernel Hub Gateway (:7200, :7201, :7202)
# ==============================================================================
log_info "2. 启动 Kernel Hub Gateway (:7200, :7201, :7202)..."
./bin/hub \
  -listen-http "$HTTP_HOST:$HUB_HTTP_PORT" \
  -listen-grpc "$HTTP_HOST:$HUB_GRPC_PORT" \
  -listen-transport "$HTTP_HOST:$HUB_TRANSPORT_PORT" \
  -user-credential-file "$DEV_DIR/user_bot.cred" \
  -bot-binary "$ROOT/bin/bot-runtime" \
  >"$LOG_DIR/hub.log" 2>&1 &
HUB_PID=$!
SERVICE_PIDS[hub]=$HUB_PID
echo "$HUB_PID" > "$PID_DIR/hub.pid"

for _ in $(seq 1 40); do
  if ! kill -0 "$HUB_PID" 2>/dev/null; then
    dump_log_and_exit "Kernel Hub" "$LOG_DIR/hub.log"
  fi
  curl -sf "http://$HTTP_HOST:$HUB_HTTP_PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "http://$HTTP_HOST:$HUB_HTTP_PORT/healthz" >/dev/null || dump_log_and_exit "Kernel Hub" "$LOG_DIR/hub.log"
log_ok "Kernel Hub Gateway 已就绪 (PID: $HUB_PID)"

# 签发 loopback_agent 凭证
./bin/cli fixture -hub-http "http://$HTTP_HOST:$HUB_HTTP_PORT" -credential-file "$DEV_DIR/user_bot.cred" >/dev/null 2>&1 || true
AGENT_CRED=$(./bin/cli credential -hub-http "http://$HTTP_HOST:$HUB_HTTP_PORT" -credential-file "$DEV_DIR/user_bot.cred" \
  -kind agent -subject loopback_agent 2>/dev/null | python3 -c 'import sys, json; print(json.load(sys.stdin).get("token", ""))' || true)

# ==============================================================================
# 步骤 3：启动 Agent Daemon (loopback_agent)
# ==============================================================================
log_info "3. 启动 Agent Daemon (loopback_agent)..."
if [[ -n "$AGENT_CRED" ]]; then
  NEXUS_AGENT_CREDENTIAL="$AGENT_CRED" ./bin/agent-daemon \
    -hub-transport "$HTTP_HOST:$HUB_TRANSPORT_PORT" \
    >"$LOG_DIR/agent.log" 2>&1 &
  AGENT_PID=$!
  SERVICE_PIDS[agent]=$AGENT_PID
  echo "$AGENT_PID" > "$PID_DIR/agent.pid"

  for _ in $(seq 1 20); do
    if ! kill -0 "$AGENT_PID" 2>/dev/null; then
      dump_log_and_exit "Agent Daemon" "$LOG_DIR/agent.log"
    fi
    grep -q "已接入 hub 传输路由" "$LOG_DIR/agent.log" 2>/dev/null && break
    sleep 0.2
  done
  grep -q "已接入 hub 传输路由" "$LOG_DIR/agent.log" 2>/dev/null || dump_log_and_exit "Agent Daemon" "$LOG_DIR/agent.log"
  log_ok "Agent Daemon 已接入传输路由 (PID: $AGENT_PID)"
else
  log_warn "未能签发 loopback_agent 凭证，跳过 Agent Daemon 启动"
fi

# ==============================================================================
# 步骤 4：安装并激活 GroupChatPlugin 独立插件 (:7300)
# ==============================================================================
log_info "4. 启动 GroupChatPlugin 独立插件 (:7300)..."
GROUPCHAT_INSTALLED_BIN=$(psql_exec "SELECT COALESCE(manifest_json->>'binary','') FROM kernel.kernel_plugins WHERE plugin_id='plugin_groupchat' AND status IN ('enabled','installed','disabled');")
GROUPCHAT_INSTALLED_DIGEST=$(psql_exec "SELECT COALESCE(manifest_json->>'binary_digest','') FROM kernel.kernel_plugins WHERE plugin_id='plugin_groupchat' AND status IN ('enabled','installed','disabled');")
if [[ -n "$GROUPCHAT_INSTALLED_BIN" && ( "$GROUPCHAT_INSTALLED_BIN" != "$ROOT/bin/plugins/plugin_groupchat" || "$GROUPCHAT_INSTALLED_DIGEST" != "$GROUPCHAT_DIGEST" ) ]]; then
  log_info "GroupChatPlugin 制品与当前 checkout 不一致，执行开发态重装..."
  ./bin/cli plugin uninstall plugin_groupchat --confirm -hub-http "http://$HTTP_HOST:$HUB_HTTP_PORT" -credential-file "$DEV_DIR/user_bot.cred" >/dev/null 2>&1 || log_warn "GroupChatPlugin 旧制品清理失败，继续尝试安装"
fi
INSTALL_RESP=$(./bin/cli plugin install GroupChatPlugin/manifest.json -hub-http "http://$HTTP_HOST:$HUB_HTTP_PORT" -credential-file "$DEV_DIR/user_bot.cred" 2>/dev/null || true)
DECISION_ID=$(echo "$INSTALL_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin).get("decision_id", ""))' 2>/dev/null || true)
if [[ -n "$DECISION_ID" ]]; then
  ./bin/cli security-approve "$DECISION_ID" -hub-http "http://$HTTP_HOST:$HUB_HTTP_PORT" -credential-file "$DEV_DIR/user_bot.cred" >/dev/null 2>&1 || true
fi
./bin/cli create-bot pull -hub-http "http://$HTTP_HOST:$HUB_HTTP_PORT" -credential-file "$DEV_DIR/user_bot.cred" >/dev/null 2>&1 || true

for _ in $(seq 1 40); do
  if ! kill -0 "$HUB_PID" 2>/dev/null; then
    dump_log_and_exit "Kernel Hub" "$LOG_DIR/hub.log"
  fi
  curl -sf "http://$HTTP_HOST:$CHAT_PORT/v1/chat/health" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "http://$HTTP_HOST:$CHAT_PORT/v1/chat/health" >/dev/null || dump_log_and_exit "GroupChatPlugin" "$LOG_DIR/hub.log"

# 获取 GroupChat L2 进程 PID
GROUPCHAT_PID=$(pgrep -f "bin/plugins/plugin_groupchat" | head -n 1 || true)
if [[ -n "$GROUPCHAT_PID" ]]; then
  SERVICE_PIDS[groupchat]=$GROUPCHAT_PID
  echo "$GROUPCHAT_PID" > "$PID_DIR/groupchat.pid"
fi
log_ok "GroupChatPlugin 服务已就绪 (PID: ${GROUPCHAT_PID:-managed})"

# ==============================================================================
# 步骤 4b：启动工具中心（自有目录，重启后授权仍在）
# ==============================================================================
log_info "4b. 启动 plugin_tool_center (:${TOOL_CENTER_PORT})..."
TOOL_DATA_DIR="$ROOT/data/plugins/plugin_tool_center"
mkdir -p "$TOOL_DATA_DIR"
if [[ -x "$ROOT/bin/plugins/plugin_tool_center" ]]; then
  ./bin/plugins/plugin_tool_center -port "$TOOL_CENTER_PORT" -data "$TOOL_DATA_DIR" \
    >"$LOG_DIR/tool_center.log" 2>&1 &
  TOOL_PID=$!
  SERVICE_PIDS[toolcenter]=$TOOL_PID
  echo "$TOOL_PID" > "$PID_DIR/tool_center.pid"
  for _ in $(seq 1 30); do
    curl -sf "http://$HTTP_HOST:$TOOL_CENTER_PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.2
  done
  curl -sf "http://$HTTP_HOST:$TOOL_CENTER_PORT/healthz" >/dev/null || dump_log_and_exit "plugin_tool_center" "$LOG_DIR/tool_center.log"
  log_ok "plugin_tool_center 已就绪 (PID: $TOOL_PID)"
else
  log_warn "bin/plugins/plugin_tool_center 不存在，跳过工具中心"
fi

# ==============================================================================
# 步骤 5：启动 Frontend Vue 3 开发服务器 (:5173)
# ==============================================================================
if [[ -d "Frontend" && -f "Frontend/package.json" ]]; then
  log_info "5. 启动 Frontend Workbench (Vite :5173)..."
  (
    cd Frontend
    exec npx vite --host "$HTTP_HOST" --port "$FRONTEND_PORT" --strictPort
  ) >"$LOG_DIR/frontend.log" 2>&1 &
  FRONTEND_PID=$!
  SERVICE_PIDS[frontend]=$FRONTEND_PID
  SERVICE_PGIDS[frontend]=$(ps -o pgid= -p "$FRONTEND_PID" 2>/dev/null | tr -d ' ' || echo "$FRONTEND_PID")
  echo "$FRONTEND_PID" > "$PID_DIR/frontend.pid"

  for _ in $(seq 1 30); do
    if ! kill -0 "$FRONTEND_PID" 2>/dev/null; then
      dump_log_and_exit "Frontend" "$LOG_DIR/frontend.log"
    fi
    curl -sf "http://$HTTP_HOST:$FRONTEND_PORT/" >/dev/null 2>&1 && break
    sleep 0.5
  done
  curl -sf "http://$HTTP_HOST:$FRONTEND_PORT/" >/dev/null || dump_log_and_exit "Frontend" "$LOG_DIR/frontend.log"
  log_ok "Frontend Workbench 已就绪 (PID: $FRONTEND_PID)"
else
  log_warn "5. Frontend 目录或 package.json 尚未就绪，跳过前端启动"
fi

# ==============================================================================
# 步骤 6：终端展示大横幅 (Terminal Banner)
# ==============================================================================
MEM_LINES=$(wc -l <"$OPS_MEM_DIR/MEMORY.md" 2>/dev/null || echo "0")

printf "\n"
printf "${C_CYAN}================================================================================${C_RESET}\n"
printf "${C_BOLD}                    NexusCore M4 智能工作台开发环境                             ${C_RESET}\n"
printf "${C_CYAN}================================================================================${C_RESET}\n"
printf " ${C_GREEN}[✓]${C_RESET} Kernel Hub Gateway   ${C_BOLD}http://%s:%d${C_RESET}    PID: %-7s 日志: %s\n" "$HTTP_HOST" "$HUB_HTTP_PORT" "${SERVICE_PIDS[hub]:-N/A}" "$LOG_DIR/hub.log"
printf " ${C_GREEN}[✓]${C_RESET} GroupChatPlugin      ${C_BOLD}http://%s:%d${C_RESET}    PID: %-7s 日志: %s\n" "$HTTP_HOST" "$CHAT_PORT" "${SERVICE_PIDS[groupchat]:-N/A}" "$LOG_DIR/hub.log (supervisor)"
if [[ -n "${SERVICE_PIDS[toolcenter]:-}" ]]; then
printf " ${C_GREEN}[✓]${C_RESET} Tool Center          ${C_BOLD}http://%s:%d${C_RESET}    PID: %-7s 日志: %s\n" "$HTTP_HOST" "$TOOL_CENTER_PORT" "${SERVICE_PIDS[toolcenter]}" "$LOG_DIR/tool_center.log"
fi
printf " ${C_GREEN}[✓]${C_RESET} Agent Daemon         ${C_BOLD}%s:%d (TCP)${C_RESET}     PID: %-7s 日志: %s\n" "$HTTP_HOST" "$HUB_TRANSPORT_PORT" "${SERVICE_PIDS[agent]:-N/A}" "$LOG_DIR/agent.log"
if [[ -n "${SERVICE_PIDS[frontend]:-}" ]]; then
printf " ${C_GREEN}[✓]${C_RESET} Frontend Workbench   ${C_BOLD}http://%s:%d${C_RESET}    PID: %-7s 日志: %s\n" "$HTTP_HOST" "$FRONTEND_PORT" "${SERVICE_PIDS[frontend]}" "$LOG_DIR/frontend.log"
fi
printf "\n"
printf "${C_BOLD}活跃 Bot 席位列表:${C_RESET}\n"
printf "  • ${C_CYAN}ops_bot${C_RESET}         [lazy]      能力: [ops, agent_control] (MEMORY.md: %d 行 ≤ 200)\n" "$MEM_LINES"
printf "  • ${C_CYAN}qa_bot${C_RESET}          [lazy]      能力: [qa, test_runner]    (防死循环保护)\n"
printf "  • ${C_CYAN}groupchat_bot${C_RESET}   [resident]  能力: [groupchat, room_management]\n"
printf "\n"
printf "${C_BOLD}快速访问链接:${C_RESET}\n"
if [[ -n "${SERVICE_PIDS[frontend]:-}" ]]; then
printf "  🌐 前端工作台:               ${C_CYAN}http://%s:%d${C_RESET}\n" "$HTTP_HOST" "$FRONTEND_PORT"
fi
printf "  📡 Hub SSE 实时事件流:       ${C_CYAN}http://%s:%d/v1/events/stream${C_RESET}\n" "$HTTP_HOST" "$HUB_HTTP_PORT"
printf "  💬 群聊房间消息 API:         ${C_CYAN}http://%s:%d/v1/chat/rooms/dev_room/messages${C_RESET}\n" "$HTTP_HOST" "$CHAT_PORT"
printf "  📋 实时监控日志:             ${C_BOLD}tail -f %s/*.log${C_RESET}\n" "$LOG_DIR"
printf "\n"
printf "${C_YELLOW}按 [Ctrl+C] 优雅停止所有微服务并回收资源...${C_RESET}\n"
printf "${C_CYAN}================================================================================${C_RESET}\n"

# ==============================================================================
# 步骤 7：进程监控等待循环 (Active Supervision Loop)
# ==============================================================================
while true; do
  (sleep 2) &
  wait -n 2>/dev/null || true
  for sname in "${!SERVICE_PIDS[@]}"; do
    spid="${SERVICE_PIDS[$sname]}"
    if [[ -n "$spid" ]] && ! kill -0 "$spid" 2>/dev/null; then
      log_warn "服务 $sname (PID $spid) 意外退出！"
      log_target="$LOG_DIR/${sname}.log"
      if [[ "$sname" == "groupchat" && ! -f "$log_target" ]]; then
        log_target="$LOG_DIR/hub.log"
      fi
      dump_log_and_exit "$sname" "$log_target"
    fi
  done
done
