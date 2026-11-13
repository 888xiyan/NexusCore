---
name: nexus-plugin-dev
description: >-
  NexusCore 独立插件（L2 二进制）开发、构建、注册与生命周期管理标准指南。
  当用户需要开发新插件、编写或校验插件清单 manifest.json、排查两级进程生命周期、实现总线交互或群聊路由时使用此技能。
---

# NexusCore 独立插件开发与生命周期标准指南

本技能为 NexusCore 系统中独立插件（如 `GroupChatPlugin`、工作区插件、外部连接器插件）的开发、构建、安全审查、生命周期运营及防死循环事件路由提供完整研发规范与排错指引。

---

## 1. 核心架构边界：两级进程模型 (Two-Level Process Model)

在 NexusCore 中，插件系统严格解耦为两级进程模型（PluginMvp §6）：

```text
[内核 Supervisor] ──决策拉起──> [L1: Owner Bot 进程 (bot-runtime)]
                                         │
                                      exec setsid
                                         ▼
                               [L2: 插件独立服务进程 (binary)]
```

1. **L1 级（Bot 进程）**：
   - 由内核 `supervisor` 统一决策、拉起并注入凭证（`NEXUS_BOT_CREDENTIAL`）；
   - 拥有总线席位（`bot_id`），负责拉取并确认内核任务队列，是总线合法的通讯主体。
2. **L2 级（插件独立服务进程）**：
   - 即 `plugin_manifest.binary` 指定的独立二进制程序；
   - **由该插件的 Owner Bot（`bot_templates[0]`）以独立进程组（`Setpgid: true`）自治 exec 拉起与监控**；
   - 插件服务通过 HTTP 端口或反向 IPC 对外暴露领域功能；
   - **非总线主体**：无 `bot_id`、不注册独立 Consumer Group，业务消息经其 Owner Bot 的委托入总线；
   - PID 与状态经 `channel=health` 登记到 `kernel_plugin_processes` 表供只读巡检。
3. **下线铁律**：
   - 任何下线动作顺序**恒为先插件进程（L2）、后 Bot 进程（L1）**。

---

## 2. 独立插件标准目录结构

独立插件以顶级目录形式开发（如 `GroupChatPlugin/`），或置于 `plugins/<plugin_id>/`：

```text
<PluginFolder>/
├── manifest.json              # 插件核心清单与能力声明（强制要求）
├── main.go                    # 插件 L2 独立二进制服务入口
├── room/                      # 业务领域模块（如群聊会话与时序管理）
├── mention/                   # 辅助解析模块（如 @ 语法识别提取器）
└── config_schema.json         # 可选：插件专属配置 Schema
```

参考模板：[manifest_template.json](./references/manifest_template.json)

---

## 3. Manifest 清单强约束规范

[manifest.json](file:///home/yhy/Projects/NexusCore/GroupChatPlugin/manifest.json) 必须满足严格的校验规则（`hub/plugin/plugin.go`）：

```json
{
  "plugin_id": "plugin_groupchat",
  "name": "GroupChat Plugin",
  "version": "1.0.0",
  "plugin_type": "workspace",
  "description": "多 Bot 独立群聊协作插件",
  "binary": "/home/yhy/Projects/NexusCore/bin/plugins/plugin_groupchat",
  "publishing": [
    "plugin_groupchat.message.bot",
    "plugin_groupchat.room.event"
  ],
  "subscribing": [
    "chat.message.user",
    "bot.reply"
  ],
  "providing_tools": [
    "groupchat.send_message",
    "groupchat.get_history"
  ],
  "requiring_tools": [],
  "bot_templates": [
    {
      "bot_id": "groupchat_bot",
      "role": "plugin",
      "display_name": "GroupChat Bot",
      "activation_mode": "resident",
      "capabilities": ["groupchat", "room_management"]
    }
  ],
  "schema_version": 1,
  "binary_digest": "c0e92a49622d2c2c000e0636f808d32b9425b18d92b9136b98f77f5c908b2529"
}
```

### 必守校验规则清单：
1. **`plugin_id`**：必须匹配正则 `^plugin_[a-z0-9_]{3,24}$`。
2. **`plugin_type`**：只能为 `workspace`、`ingress`、`executor` 或 `knowledge`。
3. **`publishing`（事件前缀约束）**：**所有发布的事件名称必须严格以 `<plugin_id>.` 开头**（例如 `plugin_groupchat.message.bot`），严禁跨命名空间乱发。
4. **`binary` 与 `binary_digest`（制品强校验）**：
   - 必须提供可执行文件的绝对路径；
   - 必须提供 64 位小写 SHA256 十六进制摘要，内核在安装与启动前进行哈希强匹配。
5. **`bot_templates`**：数组不能为空；`bot_templates[0]` 约定为该插件的 **Owner Bot**。

---

## 4. 构建与制品摘要绑定流程

开发新插件时，必须在构建后重新计算 SHA256 并回写清单：

### 1. Makefile 集成
在 [Makefile](file:///home/yhy/Projects/NexusCore/Makefile) 的 `build-plugins` 目标中追加编译命令：
```makefile
build-plugins:
	@mkdir -p $(PLUGIN_BIN_DIR)
	@go build -o $(PLUGIN_BIN_DIR)/plugin_groupchat ./GroupChatPlugin
```

### 2. 自动化更新 Manifest 摘要
构建完成后，通过脚本自动注入最新的路径与摘要：
```bash
make build-plugins
BIN_PATH="$(pwd)/bin/plugins/plugin_groupchat"
DIGEST=$(sha256sum "$BIN_PATH" | awk '{print $1}')

python3 -c "
import json
with open('GroupChatPlugin/manifest.json', 'r') as f:
    mf = json.load(f)
mf['binary'] = '$BIN_PATH'
mf['binary_digest'] = '$DIGEST'
with open('GroupChatPlugin/manifest.json', 'w') as f:
    json.dump(mf, f, indent=2)
"
```

---

## 5. 插件安装与生命周期命令操作流

插件生命周期严格遵从 **安装 $\rightarrow$ 安全预审 $\rightarrow$ 用户批准 $\rightarrow$ 激活 $\rightarrow$ 调和拉起** 闭环：

```bash
# 环境变量设置
export NEXUS_HUB="http://127.0.0.1:7200"
export CRED_FILE="data/user_bot.cred"

# 1. 提交安装（生成待审安全决策）
INSTALL_RESP=$(./bin/cli plugin install GroupChatPlugin/manifest.json -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE")
DECISION_ID=$(echo "$INSTALL_RESP" | python3 -c 'import sys, json; print(json.load(sys.stdin)["decision_id"])')

# 2. 安全审查查询
./bin/cli security-list pending -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE"

# 3. 审查批准与激活插件
./bin/cli security-approve "$DECISION_ID" -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE"

# 4. 触发 Supervisor 调和以启动 Owner Bot 及 L2 插件进程
./bin/cli create-bot pull -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE"

# 5. 查看插件进程运行状态
./bin/cli plugin ps -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE"
```

---

## 6. L2 进程启停、强制终止与优雅停机

插件管理命令提供了完整的运维控制能力（通过用户根因果链信封授权）：

```bash
# 优雅停机（SIGTERM 15s 超时）
./bin/cli plugin stop <plugin_id> -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE"

# 重新启动插件进程
./bin/cli plugin start <plugin_id> -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE"

# 强制终止（必须携带 --confirm 参数，防止误杀）
./bin/cli plugin kill <plugin_id> --confirm -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE"

# 禁用与彻底卸载
./bin/cli plugin disable <plugin_id> -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE"
./bin/cli plugin uninstall <plugin_id> --confirm -hub-http "$NEXUS_HUB" -credential-file "$CRED_FILE"
```

---

## 7. 消息时序与群聊路由安全铁律（防死循环）

在实现包含多 Bot 交互的插件（如群聊、任务流水线）时，必须严格遵守以下路由约束：

```text
[用户发出消息: @ops_bot ...] ──> ExtractMentions ──> [routed_bots: ops_bot] ──> 投递唤醒目标 Bot
                                                                                  │
[Bot 输出回复: ... @qa_bot ...] ──> ExtractMentions ──> [routed_bots: []] (恒为空，仅作文本展示)
```

1. **仅人类用户触发**：
   - 只有消息发送者角色 `role == "user"`（或 `user_bot` 发送的消息）中提取的 `@bot_id` 才作为唤醒目标投递任务；
   - 未被显式 `@` 的 Bot 保持静默。
2. **禁止 Bot 间自激死循环**：
   - 当 `role == "bot"` 时，提取出的 `@` 仅能作为展示属性保留，**返回的待唤醒列表恒为空**；
   - 严禁下游 Bot 被自发唤醒，彻底根除 Bot 互相 @ 导致的死循环与 Token 发散。

---

## 8. 排错备忘录与高频踩坑指南

1. **`WithKernelRuntimeTx` 事务嵌套冲突**：
   - 内核 `st.WithKernelRuntimeTx(ctx, fn)` 会在闭包函数返回 `nil` 时自动 `tx.Commit()`。
   - **严禁在闭包逻辑中手动执行 `tx.Commit()`**，否则会导致外层报 `提交内核事务: tx is closed`。
2. **`ConfigureRuntime` 缺失导致无法安装插件**：
   - 内核启动时必须调用 `sup.ConfigureRuntime(botBinary)` 固化可信 Bot 制品；
   - 若未调用，审批插件时调用 `sup.RuntimeArtifact()` 会报 `可信 bot-runtime 尚未配置`。
3. **插件无法连通 Hub HTTP 端口**：
   - Owner Bot 拉起插件独立进程时，会自动向子进程注入环境变量：
     `NEXUS_HUB_HTTP=<hub_address>` 与 `NEXUS_BOT_CREDENTIAL=<token>`；
   - 插件初始化解析命令行参数与环境变量时，需优先读取 `NEXUS_HUB_HTTP` 并去除前缀 `http://`。
4. **CI 自动化脚本交互卡死**：
   - `cli config approve` 等关键命令受终端硬约束保护，禁止非终端 stdin 重定向；
   - 编写自动化冒烟测试时，必须通过 `python3 -c "import pty; ..."` 模拟真实 PTY 交互输入 `yes\n`。
