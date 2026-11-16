# AGENTS.md

AI 助手（含 Codex / Copilot / Claude 等）在本仓库内工作时，请遵守以下约定。

## 项目概述

NexusCore 是以微内核为基础的个人智能工作台。系统结构解耦为三大板块：**Kernel**（微内核底座与原生单体记忆）、**GroupChatPlugin**（独立群聊插件）、**Frontend**（Vue 3 前端工作台）。

- **Kernel（微内核）**：负责持久化事件与任务队列、Bot 注册与运行时（supervisor）、跨设备传输路由三类原语；同时作为通用底座**内置 Bot 原生单体记忆功能**（基于 Claude Code 四层栈：Transcripts / Memory Notes / Memory Representations / Injected Context，严格限制 `MEMORY.md` ≤ 200 行，以 Auto Dream 离线整合削峰填谷；高缓存命中、零二次方膨胀）。
- **Bot 定位（简单 Agent）**：每一个 Bot 都是一个**简单 Agent**，只能使用系统提供的各种功能与工具，**禁止长时间思考，禁止直接执行 Shell**（所有环境与物理操作统一经 Hub 传输路由反向 TCP 调度到目标 `agent-daemon` 执行）。Bot 的人格、系统提示词、画像与单体记忆采用**重启策略（Restart Policy）**生效，暂不考虑配置中心运行时动态热重载。
- **GroupChatPlugin（群聊插件）**：独立目录与独立开发，实现多 Bot 群聊会话流。**会话严格由“仅仅用户@触发”**，禁止 Bot 间自发级联互 @ 唤醒，杜绝死循环与发散 Token 膨胀。
- **Frontend（前端工作台）**：基于 Vue 3 + Vite 搭建在 `Frontend/` 目录下，提供多 Bot 群聊窗口、@Mention 交互、Bot 画像/记忆可视化管理（一键保存重启）以及 Agent 探针执行可视化监控。
- 语言：Go 1.26（Kernel / bot-runtime / agent-daemon / cli / migrate / GroupChatPlugin），TypeScript + Vue 3（Frontend）
- 数据库：PostgreSQL 16+，单 database `nexus`，schema 分离：`kernel`（运行表 + 配置中心，hub 角色独占）
- 总线：只有 bot 能投递与消费消息；用户以 `user_bot` 身份入总线；插件经其注册 bot 的**单次投递委托**回投
- 配置：配置中心是 hub 模块；`plugin / bot / agent / global` 四类配置，写入经 `user_bot` 批准；Bot 级配置修改通过重启生效；**MCP / Skill 不纳入配置中心**

## 目录结构

```
Kernel/                 # 微内核底座与核心运行时
  ├─ hub/               # Go 微内核网关（队列/事件、注册与 ACL、config、ingress、scheduler、outbox、transport、supervisor）
  ├─ memory/            # Bot 原生单体记忆系统（Claude Code 四层栈：Transcripts / Notes / Representations / MEMORY.md ≤200行 / Auto Dream）
  ├─ bot-runtime/       # Bot 独立运行时（简单 Agent 实现：禁Shell、禁长思考、读单体记忆、操控 Agent）
  ├─ agent-daemon/      # 跨设备物理探针（受控工具执行器）
  ├─ cli/               # 用户(user_bot)的投递与管理代理
  ├─ cmd/migrate/       # 内核库迁移 CLI
  ├─ internal/          # 跨二进制共享内部包（osproc / envelope / version / migrate）
  ├─ migrations/        # 内核与内置表 DDL
  └─ proto/             # gRPC proto 契约
GroupChatPlugin/        # 独立群聊插件工程（独立开发）
  ├─ main.go            # 群聊独立服务二进制
  ├─ manifest.json      # 标准插件清单与能力声明
  ├─ room/              # 群聊房间与上下文管理
  └─ mention/           # @ 语法提取器（仅用户@触发路由）
Frontend/               # 独立前端工程（Vue 3 + Vite）
  ├─ src/views/         # 群聊会话主视区、Bot画像编辑抽屉、Agent实时追踪面板
  ├─ src/components/    # 聊天气泡、MentionInput、AgentActivityCard
  └─ package.json
doc/                    # 架构与实现设计文档
```

## 常用命令

```bash
make build          # 编译 5 个内核二进制到 bin/（hub / bot-runtime / agent-daemon / cli / migrate）
make build-plugins  # 编译插件二进制到 bin/plugins/（当前仅 plugin_memory 骨架）
make test           # go test ./...
make vet            # go vet ./...
make fmt            # go fmt ./...
make lint           # vet + golangci-lint（可选）
make proto          # buf generate（buf 未装时提示）
make migrate-probe  # 检查内核库 DSN 可连通性（需 env）
make migrate-up     # 执行内核库 up 迁移
make migrate-down   # 执行内核库 down 迁移
make smoke-empty    # 组合冒烟（M0 为探活模式）
make test-pg        # 真库全量测试（先 scripts/dev-pg.sh 起夹具；未设 DSN 时集成用例跳过）
make smoke-m1       # M1 九步冒烟 + 边界验收 + 兜底轮询
make smoke-m2       # M2 配置审批全链路、Bot 管理与两级进程生命周期冒烟
make smoke-m3       # M3 单体记忆、简单 Agent 操控与群聊插件闭环冒烟
make smoke-m4       # M4 11 步端到端全链路冒烟验收（前端构建+网关+群聊+记忆抽屉+Agent监控）
make launch-m4      # M4 开发者全栈一键启动（Hub:7200 + Agent + GroupChat:7300 + Frontend:5173）
make drill-crash-m1 # M1 崩溃恢复演练（真实 SIGKILL：hub / Bot / LISTEN）
make clean          # 清 bin/
```

PostgreSQL 连接用环境变量。内核只使用 `NEXUS_PG_KERNEL_DSN`（运行角色）与按需的 `NEXUS_PG_MIGRATE_DSN`（迁移角色，未设置时回退运行 DSN）；插件自有 DSN（`NEXUS_PG_MEMORY_DSN`、`NEXUS_PG_VAULT_DSN`）只在 M3 起由 `plugin_memory` 使用，**内核任何代码不得读取**：

```bash
export NEXUS_PG_KERNEL_DSN="postgres://nexus_kernel:pass@host:5432/nexus"
export NEXUS_PG_MIGRATE_DSN="postgres://nexus_migrator:pass@host:5432/nexus"   # 可选
# M3 起，仅 plugin_memory 使用：
export NEXUS_PG_MEMORY_DSN="postgres://nexus_memory:pass@host:5432/nexus"
export NEXUS_PG_VAULT_DSN="postgres://nexus_vault:pass@host:5432/nexus"
```

hub 的 `LISTEN` 使用专用直连，不能经过 PgBouncer 的 transaction pooling；租约与重试比较一律使用数据库时钟 `now()`。各角色通过 `ALTER ROLE ... SET search_path = <自己的 schema>` 定位表空间（kernel / memory / vault），DSN 本身不携带 schema。

## 设计文档索引（必读）

写代码前，先按需读对应章节：

| 文档 | 何时读 |
| --- | --- |
| [doc/NexusCore架构设计总结.md](doc/NexusCore架构设计总结.md) | 上游总设计；本文档"架构边界"部分引用它 |
| [doc/内核mvp/内核MVP详细设计.md](doc/内核mvp/内核MVP详细设计.md) | hub 10 模块 / kernel schema 数据模型 / 接口契约 / 验收标准（纯内核） |
| [doc/ConfigCenter/ConfigCenter详细设计.md](doc/ConfigCenter/ConfigCenter详细设计.md) | 配置中心：四域配置 / PG 表 / proposal → security → user_bot → active / CLI |
| [doc/BotMvp/BotMvp详细设计.md](doc/BotMvp/BotMvp详细设计.md) | Bot 角色体系 / 生命周期 / gRPC 控制面 / 插件侧 Bot / owner bot 与插件进程关闭（§8.6-8.7） |
| [doc/MemoryPlugin/MemoryPlugin详细设计.md](doc/MemoryPlugin/MemoryPlugin详细设计.md) | 记忆插件：双 schema / 四象限 / 晋升 / 工具 API / 事件回投（M3/M4） |
| [doc/MemoryPlugin/用户级Bot默认个人记忆架构.md](doc/MemoryPlugin/用户级Bot默认个人记忆架构.md) | user_xxx_bot 默认个人记忆四层栈 / Auto Dream / 注入约束 |
| [doc/PluginMvp/PluginMvp详细设计.md](doc/PluginMvp/PluginMvp详细设计.md) | 四类插件 / manifest schema / 命名规范 / 生命周期 / 卸载 / **两级进程模型与插件进程关闭链路（§6）** |
| [doc/M5-本地Agent搜寻与调度闭环详细设计.md](doc/M5-本地Agent搜寻与调度闭环详细设计.md) | 本地 Agent 资产探测（Claude Code/Codex/Pi-Agent 等）、探针 LocalCLIDriver、Bot 结构化调度与防噪音蒸馏闭环 |
| [doc/M8-基础模块沉淀与接口设计.md](doc/M8-基础模块沉淀与接口设计.md) | 基础模块下沉无头化、契约接口设计、DAG 拓扑加载与依赖生命周期治理 |
| [doc/M9-标准群聊插件与全局UI注册详细设计.md](doc/M9-标准群聊插件与全局UI注册详细设计.md) | v0.1.0 核心：群聊标准插件自治 UI、注册进全局 UI、聚合五大零散功能与独立 Schema 时序存储 |
| [doc/系统Update-全局UI原型与沙盒工具轨迹审计详细设计.md](doc/系统Update-全局UI原型与沙盒工具轨迹审计详细设计.md) | 全局 UI 页面原型规范（左右两段式分栏 + @xxx 输入框）、内置沙盒记录工具轨迹、三层 Bot 与安全审批流 |
| [doc/开发规划.md](doc/开发规划.md) | M0-M9 里程碑 / 估计 / 停线点 |
| [doc/NexusCore待修正问题清单.md](doc/NexusCore待修正问题清单.md) | D-11 / D-12（未解决问题，均属插件内部或业务契约） |
| [doc/NexusCore已修正问题归档.md](doc/NexusCore已修正问题归档.md) | D-01 至 D-10 / D-13 至 D-23（已解决） |

## 架构硬约束（不可违背）

以下边界是本项目的核心价值。任何 PR / commit 若破坏这些约束，视为错误提交。

### 总线与信封

1. **只有 bot 能投递与消费消息**。用户以 `user_bot` 身份入总线（常驻席位）；客户端插件只是 user_bot 的投递 / 展示代理；业务插件经其注册 bot 的单次投递委托入总线。
2. **信封只承载通讯语义**：身份（`from_bot_id / channel / channel_ref / msg_type`）、因果链（`correlation_id / causation_id / root_envelope_id / causal_path / traceparent`）、幂等（`idempotency_key`）与审计挂点。
3. **信封不含业务维度**：`project_id / conversation_id` 等不进信封；业务维度由来源插件装入业务载荷（`ContextEnvelope`），经 `payload_ref` 由接收 bot 自行拉取。
4. **内核不做上下文水合推送**：接收 bot 拉取，读取授权来自其收到的信封 + 3.4 ACL。

### 内核边界

5. **hub 收敛为三类原语**：持久化事件与任务队列（PostgreSQL）、Bot 注册运行时与配置中心、跨设备传输路由。业务语义（笔记渲染、看板拖拽、IM SDK、LLM 推理、记忆存储、上下文装配）绝不出现在 hub 源码里。
   **配置中心是该原语下的 hub 模块，不是第四原语**：只存 `plugin / bot / agent / global` 四类期望配置；配置写入只经 config 模块，必须由 `user_bot` 批准（绑定 checksum），可执行类先经 `security_bot` 预审；任何 Bot 不得自批，不得直接写 `kernel_config_*` 表；只存 `secret_ref`，不存密钥值。**MCP / Skill 不得进入配置中心**。
6. **记忆只属于 `plugin_memory`**：内核不持有记忆 / 金库 DSN、不建 `memory_entities / memory_relations` 等表、不定义任何记忆结构；图谱读写、版本化写入、晋升与上下文装配只能经插件的工具 API。内核侧四角色不使用 memory_profile。
7. **内核不做上下文装配 / 记忆仲裁**：Context Assembler 与 Memory Arbiter 是 `plugin_memory` 内的 memory-worker 职责（M4）。
8. **内核不为 `plugin_memory` 保留特殊通道**：记忆插件与 PM、Obsidian 等插件接入方式完全同级；事件经其 `memory_bot` 投递委托回投，内核只校验身份与因果链。

### Bot 边界

9. **Bot 是独立二进制进程**：内核 supervisor 是唯一执行器；`create_bot` 是决策者；业务插件不能自行拉起 **Bot** 进程。
   **插件进程（L2）是另一级**：`plugin_manifest.binary` 由该插件的 owner bot（`bot_templates[0]`）exec 与回收，PID 经 `channel=health` 登记到 `kernel_plugin_processes` 供 `monitor_bot` 只读巡检；它不是总线主体（无 `bot_id`、无 Consumer Group、不得 `Register`）。常规启动 / 优雅关闭 / 崩溃重启由 owner bot 自治；**强制终止与孤儿级联回收仍由 supervisor 执行**，`user_bot` 与 `monitor_bot` 自身不发 OS 信号。任何下线动作顺序恒为**先插件进程、后 Bot 进程**（见 PluginMvp §6）。
10. **保留名禁用**：`user_bot / create_bot / monitor_bot / security_bot`，任何插件不得注册同名 bot。
11. **lazy Bot 不持有 Bot-Agent 长连接**；resident Bot 持有长连接但必须经 Agent Mesh 路由（5.1），不绕过 ACL。
12. **所有消息必须携带完整因果链**：缺失 / 不合法入口直接拒绝并记录 `decision=rejected`；内核不替调用方补造历史。

### 存储边界

13. **知识存储只在 `plugin_memory`**：`memory` / `vault` schema 及其账号只授予插件；图谱只承载知识域，PM 任务、队列、项目等强结构数据不入图谱；插件自有类型化表通过 `entity_id` 引用图谱节点。
14. **kernel schema 只承载运行数据与配置中心**：hub 运行角色只授权 `kernel` schema；不得把知识 / 记忆表放进 kernel schema；配置文档只存期望状态与 `secret_ref`，不得混入运行时观测数据或密钥值。
15. **金库 schema 只允许 `visibility=global`**；记忆 schema 只允许其余三种；CHECK 约束 + `visibility NOT NULL` 双重锁定。
16. **`project_public` 写权限只授予 `plugin_memory` 晋升队列消费者的专用账号**：hub、其他插件、Bot 运行时账号均无此权限。
17. **内核不解析插件 payload**：5.1 的 `transport_frame` 只校验 length / protocol_id / stream_id；`payload` 是插件自定义的字节，由对应 Bot 协议解释。

## 命名规范

| 对象 | 规则 |
| --- | --- |
| `bot_id` | `^[a-z0-9_]{5,64}$`；保留名 `user_bot / create_bot / monitor_bot / security_bot` 禁用 |
| `plugin_id` | `^plugin_[a-z0-9_]{3,24}$`；命名 `plugin_memory`、`plugin_architect` 等 |
| `event_type` | `<plugin_id>.<domain>.<action>[.state]`；内核保留前缀 `bot.* agent.* project.* plugin.* kernel.*` 禁用 |
| `namespace` | 默认 = `plugin_id` 去前缀；VARCHAR + 注册表校验，禁止 ENUM |
| `user_xxx_bot` | 用户自建 Bot，前缀 `user_`，xxx 由宿插件模板提供 |
| `xxx_bot` | 插件侧业务 Bot，`xxx` 由插件 / 用户决定 |

## 代码风格

1. **Go**：`gofmt`；`go vet ./...` 必须无警告；导入按标准库 → 第三方 → 本项目分组。
2. **跨模块调用**：hub 与任何插件只走总线、gRPC 与显式接口，不直接共享数据库连接对象或 DSN。
3. **错误处理**：中文错误消息带上下文；不吞错；带 `fmt.Errorf("...: %w", err)`。
4. **DDL**：内核表写在 `migrations/kernel/NNNN_name.up.sql` 与 `.down.sql`；插件表写在 `migrations/plugins/<plugin_id>/<schema>/NNNN_name.{up,down}.sql`；成对文件；版本号递增；检查 CHECK / NOT NULL 约束。PostgreSQL 类型：`jsonb` / `timestamptz` / identity 主键；不用 `ENGINE` / `CHARSET` / `AUTO_INCREMENT`。
5. **测试**：单元表驱动（状态机 / 校验器）+ 契约测试（信封 / proto 兼容）+ 失败注入（崩溃 / 超时 / 双写）；`go test ./...` 必须绿。
6. **提交信息**：中文一行 + 详细 body；`docs:` / `feat:` / `fix:` 前缀。

## 当前状态

| 里程碑 | 状态 | 说明 |
| --- | --- | --- |
| M0 工程骨架 | 已完成（`c657459`） | D-22 插件化与 D-23 文档重构已落地 |
| M1 内核 MVP（纯内核） | 已完成 | 详设第 8 节六条验收全过；实现偏离与验收证据见详设 §10 |
| M2 Bot 管理与插件运营 | 已完成 | PostgreSQL 16+ docker-compose、配置中心完整审批流、Bot 管理、两级进程生命周期、DLQ 与 Health 全部验收通过（`d569a80`） |
| M3 群聊底座与单体记忆 | 已完成 | 内置 Claude Code 式四层单体记忆栈（MEMORY.md ≤200行约束 + Auto Dream 离线整合）；独立开发 `GroupChatPlugin`（仅用户@唤醒、防死循环）；简单 Agent 操控 Agent 闭环全链路验收通过 |
| M4 Vue 工作台与实机演示 | 已完成 | `Frontend/` Vue 3 前端工程独立搭建（Vite + Pinia + Element Plus）；Hub HTTP/SSE 网关打通；多 Bot 群聊（严格用户@触发）、Bot 画像与单体记忆抽屉（MEMORY.md ≤200行约束与一键重启）、Agent 实时调度执行监控；端到端自动化验收 `scripts/smoke-m4.sh` 与 `scripts/launch-dev.sh` 全链路通过 |
| M5 本地 Agent 搜寻与调度闭环 | 已完成 | 详设文档定版落地（`doc/M5-本地Agent搜寻与调度闭环详细设计.md`），LocalCLIDriver 本地 CLI 驱动、本地 Agent 搜寻插件 `plugin_agent_discovery` 与 Bot 结构化调度与防噪音蒸馏闭环全部完成（`scripts/smoke-m5.sh`） |
| M6 Bot 工厂与 Agent 绑定插件 | 已完成 | 详设定版（`doc/M6-Bot工厂与Agent绑定插件详细设计.md`），独立插件 `plugin_bot_creator`、预设角色模板库（Reviewer/Coder/Ops）、蓝图与保留名/200行记忆校验、Agent 绑定参数装配、前端创建向导（Vue 3）与端到端自动化冒烟（`make smoke-m6`）全链路通过 |
| M7 前端微模块化与插件注册表 | 已完成 | 方案 A（Vite ESM 动态导入）：宿主纯净 Shell 骨架（100% 剥离业务硬编码）+ 插件注册表（PluginRegistry）、UI 契约与运行时共享规范（`window.__NEXUS_CORE__`）、微模块独立代码分割（GroupChat / Agent Discovery / Bot Creator）与端到端自动化冒烟（`make smoke-m7`）全链路通过 |
| M8 基础模块沉淀与依赖治理 | 已完成 | 核心能力下沉无头化（100% 剥离 UI，涵盖 GroupChat、AgentMesh、BotFactory），单测覆盖率全量 ≥ 85%；能力契约入配置中心（SemVer），业务插件清单声明依赖；内核 DAG 拓扑排序加载（Kahn 算法）；环路静态检测（三色标记 DFS）+ 依赖深度硬限制（Max Depth ≤ 2）+ 逆序安全级联卸载 + 两阶段平滑滚动热更新；端到端自动化验收 `make smoke-m8` 10 步全链路通过 |
| M9 全局UI原型、标准群聊与沙盒轨迹 | 已完成（v0.1.0 旗舰） | 落地手绘原型左右两段导航（纪事/工坊）+ 右侧居中 @ 输入框；标准群聊微前端自治；**内置沙盒记录 Bots 使用工具轨迹**；独立 Schema 存储与原生 UI 主题；`make smoke-m9` 10 步全链路通过 |
| M10 工具中心、MCP/Skill与中台Bot审批 | 已定版（v0.2.0 生态） | 独立工具插件（系统工具/可编辑Skill/外部MCP）、`bot_tool_grants` 授权矩阵、插件商场与三阶段审批流、中台 Bot 受控代批 RBAC（严禁代批高危操作） |

## 常见禁忌（先问用户再做）

1. 把业务语义写进信封 / transport_frame。
2. 把记忆 / 知识表、记忆 DSN、上下文装配逻辑写进内核代码（含 hub 的内部包）。
3. 绕过 `plugin_memory` 工具直连 `memory` / `vault` schema。
4. 绕过 3.4 ACL 广播全量事件。
5. 在插件侧手动 `exec` **Bot** 进程（插件进程 L2 由 owner bot 拉起属合法自治，见 PluginMvp §6.1）。
6. 修改 `visibility` 列的 CHECK 约束使其变弱。
7. 新增插件时直接 ALTER TABLE 图谱表或 kernel 表。
8. 删除 `kernel_ingress_log` / `kernel_context_snapshots` / `memory_audit_log` 行（它们是 append-only / 审计墓碑）。
9. 绕过 7.4 乐观锁版本检查直接写公共记忆。
10. 让内核对 `memory_profile` 做解释或校验（它只是插件声明字段）。
11. 未经 `kernel_plugin_processes` 校验（`pid + pid_start_time` 双匹配 + `pgid` 归属 + 保留 PID 黑名单）就对任何进程发信号；或让 `user_bot` / `monitor_bot` 自己 kill 进程。
12. 绕过配置中心直接写 `kernel_config_*` / 直改 `bot_manifest`、`kernel_plugins` 的配置字段，或由 Bot 自批配置。
13. 在配置 spec 中存明文密钥 / token；配置中心只允许 `secret_ref`。
14. 把 MCP / Skill 配置或运行时塞进内核或配置中心。
15. 把 LISTEN/NOTIFY 当作可靠投递（必须保留兜底轮询与崩溃扫描）。
16. 绕过交互终端安全机制（如 stdin 重定向、脚本管道注入或跳过确认标志）批准配置；配置批准必须在真实交互终端由 `user_bot` 确认。

## 需要用户确认的场景

1. 需要新增 proto RPC 或改动已有 RPC 签名。
2. 需要改 `kernel_jobs` / `kernel_projects` / `memory_entities` 等表（kernel 表与插件表分属不同所有者）的字段集合。
3. 需要引入新的第三方库 / 依赖。
4. 需要改动架构总结的章节结构（不禁止，但需说明理由）。
5. 需要在 M2 之前引入 docker-compose / 多节点部署。
6. 需要调整插件清单契约（manifest schema）或 `plugin_memory` 工具面。
7. 需要新增配置域（`scope_type`）、修改配置文档 schema，或改变审批 / 预审规则。
8. 需要改 PostgreSQL 拓扑（database / schema / 角色）或引入扩展（如 pgvector）。
