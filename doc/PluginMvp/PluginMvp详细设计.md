# NexusCore Plugin MVP 详细设计

> 版本：v0.1（2026-11-10）  
> 上游设计：`../NexusCore架构设计总结.md`（§3.2 / 3.4 / 5.1 / 6.10 / 8.4）+ `../BotMvp/BotMvp详细设计.md`  
> 状态：设计定稿，待实现

## 1. 目标与非目标

### 1.1 目标

1. **三类插件形态都可写**：入站通道（连接外网 / IM）、工作台视图（业务数据 UI）、执行器（本地 / 远程工具），通过统一 Manifest 与控制面接入。
2. **命名与事件规范冻结**：`<plugin_prefix>.<domain>.<event>` 规则 + bot 名命名规范，MVP 编译期 enforce（G1 收口）。
3. **生命周期完整**：install → security_review → approve → activate → upgrade（drain + 切换）→ disable → uninstall，全链路状态机。
4. **卸载清理规则冻结**：Bot 进程下线、Consumer Group 停订、事件类型反注册、类型化表处理（G4/G5 最小解）。
5. **安全闭环**：security_bot 审查 + `kernel_security_decisions` 记录，覆盖 install / upgrade / uninstall 三类动作。
6. **冒烟链路端到端可验收**（第 9 节）。

### 1.2 非目标

| 延后项 | 触发条件 |
| --- | --- |
| Python / TS 插件 SDK 官方支持 | M4 / M4-onward 业务需要 |
| Tauri / VS Code UI 插件插槽协议 | M7 工作台 |
| 插件市场 / 远程安装 | 生态起来后 |
| 多插件声明同一事件 type 的合并策略 | 生态规模上来后 |
| 插件数据迁移自动化 | 有插件带 schema 变更的 upgrade |
| 超过 20 插件时的 fair-share 资源调度 | 资源吃紧时 |

## 2. 插件类型与 MVP 具体示例

| 类型 | MVP 参考实例 |
| --- | --- |
| 入站通道插件 | `cli_ingress_bot`（MVP 最简单）、后续 Telegram / 飞书 / VS Code / 浏览器 |
| 工作台视图插件 | `obsidian_view_bot`（M3 首发示例）、`knowledge_graph_bot`（M7 前置） |
| 执行器插件 | `loopback_agent_bot`（M1 冒烟用）、`bash_executor_bot`（M5） |

每个插件 = `plugin_id` + `plugin_manifest` + 一个可执行 Go 二进制（optional）+ 类型化数据表（optional）+ 1..N 个 `bot_manifest`（必须有，规则 3.3.1 规则 5）。MVP 生态下"每个插件必须有一个 bot 才存活"是硬条件，杜绝"空插件"。

## 3. 插件 Manifest Schema（v1 冻结）

```text
plugin_manifest
├── plugin_id           # 规范：^plugin_([a-z0-9_]{3,24})$，示例 plugin_architect
├── display_name        # 可读名，仅 UI
├── plugin_type         # ingress / workspace / executor
├── version             # semver v1.0.0
├── binary              # optional：Go 二进制 file name；工作台视图可空
├── binary_digest       # sha256，热更新时校验
├── namespace           # VARCHAR，默认与 plugin_id 去前缀同、可覆写
├── subscribing[]       # 事件订阅申请：{event_expr, scope:{namespace,project,bot}}
├── publishing[]        # 事件发布申请：{event_type}（由此登记到 event registry）
├── providing_tools[]   # 提供的工具（走 传输路由 5.1，`tools` 形式）
├── requiring_tools[]   # 期望使用的工具（他人插件提供的，安全审查需通过来源 plugin_id）
├── bot_templates[]     # 至少一个：{bot_id, activation_mode, agent_connection, ...}
├── ui_mounts[]         # optional：{slot: activitybar|maintab|sidepanel, title, entry}
├── resource_limits     # `{cpu_ms: int, memory_mb: int}` 默认 `{100ms,128MB}`
├── required_tables[]   # optional：插件自有类型化表声明（与 M3 依赖）
├── deps                # [] 对其他插件的 tool / bot 依赖声明
└── schema_version      # manifest 自身 schema 版本
```

## 4. 命名与事件规范（G1 收口）

### 4.1 bot_id 命名

```text
kernel-side roles   : user_bot / create_bot / monitor_bot / security_bot   # reserved
plugin-side default : [a-z0-9_]+_bot，由 plugin_id 去前缀 + '_bot'（如 plugin_architect → architect_bot）
plugin-side custom  : 任意合规命名（见命名规则 4.5）
user-created        : user_ 前缀 + plugin 侧提供的 bot 简称 + _bot   # user_writer_bot
```

### 4.2 event_type 命名

```text
event_type = <plugin_id>.<domain>.<action>[.state]
示例：plugin_architect.weekly.report
event_type 由 plugin manifest 的 publishing[] 登记，event_registry 表校验唯一性
内核保留 event_type 前缀：bot.* / agent.* / project.*  # 插件不得使用
```

### 4.3 namespace 命名

默认 `namespace = plugin_id 去前缀（plugin_architect → architect）`；若插件需多个 namespace，在 manifest 明确列出。namespace 由 registry 校验，与 plugin_id 一一映射。

### 4.4 工具命名

工具命名 `<plugin_id>.<tool>`，如 `plugin_architect.refactor_code`。工具间依赖走 `requiring_tools[]`；依赖的 source plugin_id 必须在 `requiring_tools` 里显式列出，不允许隐式引用。

### 4.5 完整命名规则

```text
bot_id      : ^[a-z0-9_]{5,64}$       # user_bot 等 reserved 名除外
plugin_id   : ^plugin_[a-z0-9_]{3,24}$
bot_id      = plugin 前缀 + '_bot'，由 registry 在插件首次 register 时生成
user_bot    : fixed reserved to user_bot itself
user_XXX_bot: 用户创建；XXX 由 plugin manifest 的 template 生成（可改尾缀）
event_type prefix 保留：bot.* / agent.* / project.* 示例内详意（如 bot.connected、project.memory.updated）

保留名列表核心:
  user_bot / create_bot / monitor_bot / security_bot
  event prefix: bot.* / agent.* / project.* / plugin.* / kernel.* / obsidian.* / pm.*
（后四者由对应插件/插件组持有，非当前插件映不得乱用）
```

## 5. 插件生命周期（六态）

```text
install → security_review → approved → active
                  │
                  └→ rejected → install（重试）

active ─disable→ disabled （bot 进程下线，订阅反注册，事件类型保留）
       └─uninstall→ uninstalled（bot 进程下线 + 订阅反注册 + 类型化表数据需 CLI 确认后清理）

active / disabled ─upgrade→ security_review 新版本 → approved → 涌现 drain/restart
```

### 5.1 install

1. 入口：CLI `plugin install /path/to/plugin.go` 或 manifest 相对路径插件（跳过二进制，只有 manifest）。
2. `kernel_plugins` 表由 scheduler 添加一行 status=pending。
3. `security_bot` 收到 pending 通知，按 3.4 审查。
4. audit_log 记录 install 动作 + 安全审查请求。

### 5.2 security_review

走 security_bot 路径，见 Bot MVP §4.4。MVP CLI：`security-list` / `security-approve <id>`。

### 5.3 activate

1. decrypted / binary+_manifest 按需落盘。
2. manifest 中 subscribing / publishing 登记 event_registry。
3. bot_templates 转入 bot_manifest 记录。
4. create_bot 读清单决定拉起对应 bot 进程。
5. kernel_plugins.status = active。

### 5.4 upgrade

与 BotMvp §8.4 热更新一致：安全审查 → drain 旧进程 → 切 binary_digest → 新进程 ready。观察期异常回滚。

### 5.5 disable

1. security_bot 收到 disable 决议，把 plugin.status 改 disabled。
2. **不清理**：数据库表、Bot manifest、事件 registry 均保留原样。
3. Bot 进程由 supervisor 下线（desired_state=stop）。

### 5.6 uninstall

1. 交互式：用户列 Bot 与类型化表清点 → CLI 展示将删除内容 → 用户手动 `confirm` 或 `keep`。
2. confirm 模式：bot_manifest 行删除、bot 进程下线、event_registry 反注册、类型化表（若有）整行 DROP、`kernel_plugins` 状态 uninstalled。
3. keep 模式：bot 进程下线 + registry 反注册，但类型化表保留（数据可用 CLI 读取，或重建插件后恢复）。
4. `install / upgrade` 不支持从 uninstalled 状态直接激活，须重走 install。

## 6. 插件进程模型（G2 回收：MVP 单语言，语言无关延后）

MVP 只支持 Go 独立二进制（bot-runtime 执行）。其他语言处理策略：

| 语言 | MVP 应对 |
| --- | --- |
| Go | 直接在 MVP 起飞：cmd/xxx_bot/main.go 或 plugin binary |
| Python | 临时方案：通过 Go hub 启一个 `gateway-mem-worker`（stub 替换 MLP 模型） |
| TypeScript / Vue | 无后端，纯 UI（M7 工作台起） |

**规则**：MVP 内所有插件都须 Go 生态可直接编译；非 Go 插件 SDK 推至 M4+，在 SDK 就绪前由"插件进程模型（12.3）"继续开放。

## 7. 插件数据迁移（G4 最小解）

### 7.1 自带 schema 的插件

1. plugin manifest 中 `required_tables[]` 声明以避免缺表异常；但 DDL 由插件 install 时发至 security 审查（与 Bot 注册合并审核）。
2. DDL 语句格式：` migrations/plugins/<plugin_id>/NNNN_<name>.up.sql /`.down.sql`
3. DDL 用键：`<plugin_id>__<table_name>_<action>` 格式 prefix，避免与其他插件冲突。
4. security_bot不加判定；只是收集并在 approve 时入 DDL。

### 7.2 无 schema 的插件

使用 Memory Gateway 的 kernel_entities 即可（namespace=plugin_id），不产生 `required_tables[]`。M3 Obsidian 先走这条路。

## 8. 资源限制（G6 最小解）

MVP 仅限单实例的 cpu / 内存（`resource_limits` 字段的 max 每 bot）：

1. 建议默认 100ms CPU、128MB 内存；超过 3s 持续即 `bot.stalled` 上报。
2. 全局 fair-share / 20+ 插件调度延后。
3. MVP 无 min_instances 争抢假定，只有单进程 max 上限；多 Bot 插件按 manifest 顺序调度。

## 9. 安全链路（见 BotMvp 4.4）

MVP 走 security_bot CLI：

```text
security-list
security-approve <decision_id>
security-revoke <decision_id> <reason>
```

所有 install / upgrade / uninstall 记录都经过 `security_bot`，写入 `kernel_security_decisions`；每条记录含 `correlation_id`（按 6.10 追溯）。

## 10. 插件安装 / 拉起 / 触发 / 热更新（四条完整链路，均端到端）

注意链路基本都经过 user_bot / create_bot / supervisor，由 BotMvp 定义；本 MVP 只覆盖"插件清单是否合法 / 是否会拉起对应 bot 的决策"。

安装链路 / 拉起链路 / 热更新链路 / 卸载链路分别见 BotMvp §8.2 / §8.4 与本文 §5.5-5.6，MVP 验收也是按两者组合验证。

## 11. 冒烟链路

```text
1. plugin install plugin_demo（Go 二进制 + manifest + 一个 bot_template）
2. security-list 命中 pending；security-approve 通过
3. plugin status = active
4. create_bot 读 manifests → supervisor exec bot_demo_bot（binary_digest 校验）
5. bot_demo_bot Register + ready
6. user 通过 CLI 发条消息 → ingress 校验 → user_bot → router → bot_demo_bot
7. bot_demo_bot 回 bot.reply 事件 → user_bot 订阅端接收
8. cleanup 测试：plugin uninstall → bot 下线，kernel 表可见 status=uninstalled
9. disable 路径：plugin disable → bot 下线但 manifest/表/registry 保留 → plugin activate → 重新 Register
10. upgrade 路径：plugin upgrade → 审查 + drain + 新 digest 进 ready
```

## 12. 验收标准

1. install → security-list → approve → bot ready 走通。
2. 安装一个含 bot_templates=2 的插件，两个 Bot 都由 create_bot 成功拉起。
3. 向 `demo_bot` 发一条消息：user_bot 收到 bot.reply 事件，事件 `from_bot_id=demo_bot`，日志含 plugin_id。
4. uninstall keep 模式：Bot 下线，表仍在。
5. uninstall confirm 模式：Bot 下线 + 类型化表 DROP 确认后删除。
6. upgrade 流程：drain + 换 binary_digest + 新 ready；升级 diff 记录到 security_decisions。
7. header 校验（plugin_id 格式、保留名禁用、event prefix 保留）表驱动用例通过。
8. 需要事件类型注册的唯一性测试。
## 13. 与上游文档的映射

| 本文档章节 | 架构总结对应 |
| --- | --- |
| 2 / 3.4 | 3.2 万物皆插件 |
| 3.4 / 9 | 3.4 插件信任边界 + BotMvp §4.4 |
| 4 | 3.3.1 规则 2（保留名） + 新增 |
| 5 / 11 | BotMvp §8.2 / 8.4 |
| 7 | 8.4 Memory Gateway / 8.1 namespace 规范 |
| 10 | 5.1 传输路由 / D-14 插件自定义协议 |
