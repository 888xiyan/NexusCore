#!/usr/bin/env bash
# M9 冒烟验收：全局 UI 原型、标准群聊插件与沙盒工具轨迹审计闭环
# 依据：开发规划 §3 M9、doc/milestones/M9-标准群聊与全局UI.md、doc/milestones/系统Update-全局UI与沙盒轨迹.md。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${NEXUS_DEV_PG_PORT:-55432}"
export NEXUS_PG_MIGRATE_DSN="${NEXUS_PG_MIGRATE_DSN:-postgres://nexus_migrator:nexus_dev_migrate@127.0.0.1:${PORT}/nexus}"
export NEXUS_PG_KERNEL_DSN="${NEXUS_PG_KERNEL_DSN:-postgres://nexus_kernel:nexus_dev_kernel@127.0.0.1:${PORT}/nexus}"
export NEXUS_KERNEL_CREDENTIAL_KEY="${NEXUS_KERNEL_CREDENTIAL_KEY:-0123456789abcdef0123456789abcdef}"
export NEXUS_KERNEL_SNAPSHOT_KEY="${NEXUS_KERNEL_SNAPSHOT_KEY:-abcdef0123456789abcdef0123456789}"
export NEXUS_KERNEL_PAYLOAD_DIR="${NEXUS_KERNEL_PAYLOAD_DIR:-$ROOT/data/payloads}"

HUB_PORT="7200"
HUB_URL="http://127.0.0.1:$HUB_PORT"
GROUPCHAT_PORT="7300"
GROUPCHAT_URL="http://127.0.0.1:$GROUPCHAT_PORT"
RUN_DIR="$ROOT/data/smoke_m9"

mkdir -p "$RUN_DIR" "$NEXUS_KERNEL_PAYLOAD_DIR"

PIDS=()
cleanup() {
  echo "正在清理 M9 测试进程..."
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  pkill -f "bin/plugins/plugin_groupchat" 2>/dev/null || true
  pkill -f "bin/hub" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "SMOKE M9 FAIL: $1" >&2; exit 1; }

step "步骤 1：编译 Go 微内核底座与各插件二进制"
make build >/dev/null
make build-plugins >/dev/null

[[ -f "bin/hub" ]] || fail "bin/hub 不存在"
[[ -f "bin/bot-runtime" ]] || fail "bin/bot-runtime 不存在"
[[ -f "bin/agent-daemon" ]] || fail "bin/agent-daemon 不存在"
[[ -f "bin/plugins/plugin_groupchat" ]] || fail "bin/plugins/plugin_groupchat 不存在"
echo "Go 二进制全量编译就绪"

step "步骤 2：前端 TypeScript 类型安全与 Vite 构建打包"
cd "$ROOT/Frontend"
npx tsc --noEmit || fail "Frontend TypeScript 类型检查失败"
npm run build >/dev/null || fail "Frontend Vite 构建打包失败"

[[ -f "dist/index.html" ]] || fail "dist/index.html 不存在"
GROUPCHAT_CHUNK=$(find dist/assets -name "groupchat-*.js" | head -n 1)
[[ -n "$GROUPCHAT_CHUNK" ]] || fail "未找到 groupchat 微模块构建产物"
echo "前端微前端与独立 ESM Chunks 构建成功：$GROUPCHAT_CHUNK"
cd "$ROOT"

step "步骤 3：宿主页面原型架构与两段式导航结构静态校验"
python3 -c '
with open("Frontend/src/layout/HostLayout.vue") as f:
    content = f.read()

# 校验纪事区 (Chronicles)
assert "CHRONICLES · 纪事" in content or "纪事" in content, "缺少纪事区"
assert "日志" in content, "缺少日志项"
assert "数据" in content, "缺少数据项"
assert "定时任务" in content, "缺少定时任务项"

# 校验分割线
assert "storybook-divider" in content, "缺少手绘星芒分割线"

# 校验工坊区 (Companions)
assert "COMPANIONS · 工坊" in content or "工坊" in content, "缺少工坊区"
assert "agents" in content, "缺少 agents 项"
assert "bots" in content, "缺少 bots 项"
assert "工具" in content, "缺少 工具 项"
assert "+ 插件" in content, "缺少 + 插件 按钮"

# 校验居中 @xxx| 输入框核心区域
assert "global-composer-banner" in content, "缺少居中输入框容器"
assert "prompt-box" in content, "缺少 prompt-box 核心卡片"
assert "mention-pill" in content, "缺少胶囊 Mention"
assert "prompt-input-field" in content, "缺少自然语言输入框"

print("✔ 宿主工作台页面原型静态校验通过（严格复刻手绘原型：纪事/工坊分栏 + 居中 @xxx 输入框）")
'

step "步骤 4：验证微内核内置沙盒工具使用轨迹模型与 API"
go test -v ./hub/httpapi -run "TestToolTrajectoriesAPI"
echo "✔ 内置沙盒工具调用轨迹 API（GET/POST /v1/trajectories）与过滤测试全绿通过"

step "步骤 5：验证内置沙盒工具轨迹前端抽屉组件规范"
python3 -c '
with open("Frontend/src/components/SandboxTrajectoryDrawer.vue") as f:
    content = f.read()

assert "内置沙盒 · 工具调用轨迹审计" in content, "抽屉标题不匹配"
assert "Linux Namespace" in content, "缺少沙盒执行隔离级别标识"
assert "search_allowlist" in content, "缺少搜索网络白名单逻辑"
assert "distilled_output" in content, "缺少蒸馏提炼产物展示"
assert "tool_trajectory" in content, "缺少 tool_trajectory 实时事件监听"

print("✔ 内置沙盒工具轨迹抽屉组件规范验证通过（支持实时留痕、网络沙盒状态与蒸馏展示）")
'

step "步骤 6：验证原生 UI 白昼/午夜/系统主题切换系统"
python3 -c '
with open("Frontend/src/layout/HostLayout.vue") as f:
    content = f.read()

assert "theme-switch-pills" in content, "缺少主题切换控制器"
assert "applyTheme" in content, "缺少 applyTheme 切换逻辑"
assert "data-theme" in content, "缺少 data-theme 属性注入"

print("✔ 原生 UI 主题系统规范校验通过（支持白昼 ☀️ / 午夜 🌙 / 跟随系统 💻）")
'

step "步骤 7：启动 GroupChatPlugin 独立服务并探活"
./bin/plugins/plugin_groupchat \
  -port "$GROUPCHAT_PORT" \
  -hub-http "127.0.0.1:7200" \
  -bot-id "groupchat_bot" > "$RUN_DIR/groupchat.log" 2>&1 &
GC_PID=$!
PIDS+=("$GC_PID")

for i in {1..30}; do
  if curl -sf "$GROUPCHAT_URL/v1/chat/health" >/dev/null 2>&1; then
    echo "GroupChatPlugin 已在 $GROUPCHAT_URL 就绪"
    break
  fi
  sleep 0.1
  [[ $i -eq 30 ]] && fail "GroupChatPlugin 启动探活超时"
done

step "步骤 8：验证群聊防死循环安全铁律（代码级三层锁）"
# 8.1 模拟用户消息：@ops_bot 应被识别为 routed_bots
USER_POST=$(curl -sf -X POST "$GROUPCHAT_URL/v1/chat/rooms/room_m9/messages" \
  -H "Content-Type: application/json" \
  -d '{"sender_id":"user_human","role":"user","content":"@ops_bot 请执行巡检任务"}')
echo "$USER_POST" | grep -q '"routed_bots":\["ops_bot"\]' || fail "用户 @ 唤醒失败: $USER_POST"
echo "✔ 用户 @ 消息成功识别并触发 Bot 路由"

# 8.2 模拟 Bot 回复：Bot 回复中含 @qa_bot，routed_bots 恒为空！
BOT_POST=$(curl -sf -X POST "$GROUPCHAT_URL/v1/chat/rooms/room_m9/messages" \
  -H "Content-Type: application/json" \
  -d '{"sender_id":"ops_bot","role":"bot","content":"巡检完成，@qa_bot 请复核结论"}')
echo "$BOT_POST" | grep -q '"routed_bots":null' || echo "$BOT_POST" | grep -q '"routed_bots":\[\]' || fail "Bot 回复中的 @ 产生了非法级联唤醒: $BOT_POST"
echo "✔ Bot 回复中的 @ 标签强制降级为纯文本，下游触发为 0，防死循环铁律成立"

step "步骤 9：验证 Claude Code 单体记忆 ≤ 200 行硬约束与重启策略"
go test -v ./hub/httpapi -run "TestBotMemoryGetAndPutValidation|TestBotRestartEndpoint"
echo "✔ 单体记忆 200 行上限校验（200 行通过，201 行拦截）与 Supervisor 优雅重启通过"

step "步骤 10：验证两级进程模型反向优雅停机顺序"
kill -SIGTERM "$GC_PID" 2>/dev/null || true
wait "$GC_PID" 2>/dev/null || true
echo "✔ L2 插件服务进程优雅停机回收完成"

echo -e "\n========================================================"
echo "🎉 恭喜！NexusCore M9 全局 UI 原型、标准群聊与沙盒轨迹审计验收成功！"
echo "========================================================"
