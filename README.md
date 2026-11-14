# NexusCore

以微内核为基础的个人智能工作台（Personal Intelligent Workspace based on Microkernel Architecture）。

系统架构解耦为三大核心板块：**Kernel**（微内核底座与原生单体记忆）、**GroupChatPlugin**（独立多 Bot 群聊插件）、**Frontend**（Vue 3 前端工作台）。

---

## 核心设计理念

1. **微内核三原语**：内核只负责 **PostgreSQL 持久化事件与任务队列**、**Bot 注册运行时与配置中心**、**跨设备传输路由**三类原语。业务语义（笔记渲染、看板拖拽、IM SDK、LLM 推理、上下文装配等）绝不侵入微内核源码。
2. **脑手分离（Brain-Hand Separation）**：
   - **Bot（简单 Agent）**：运行在内核或插件侧的长期认知决策体。**禁止长时间思考，禁止直接执行 Shell**，只能调用系统提供的受控功能与工具。
   - **Agent（跨设备物理探针）**：无人格受控工具执行器，通过 Hub 传输路由（反向 TCP）接受调度，负责实际环境与物理操作。
3. **Bot 原生单体记忆（对齐 Claude Code 四层栈）**：
   - 深度对齐 Claude Code 文件系统记忆模型：**Transcripts**（原始会话追加审计）→ **Memory Notes**（离散笔记经验）→ **Memory Representations**（结构化主题 Wiki）→ **Injected Context**（常驻极小切片）。
   - **铁律约束**：`MEMORY.md` 严格限制 **≤ 200 行**（约 500~2000 Tokens），配合 Prompt Caching 确保 85%~93% 缓存命中，彻底消除上下文二次方复合膨胀。
   - **Auto Dream 离线整合**：异步四阶段（Orientation → Signal Gathering → Consolidation → Pruning & Indexing）削峰填谷。
4. **独立群聊插件与严格防死循环**：
   - 业务多 Bot 群聊作为独立插件（L2 独立进程）解耦开发。
   - **严格限制“仅仅用户@触发”**：只有人类用户消息显式 `@<bot_id>` 时激活对应 Bot；Bot 回复中的 @ 仅作纯文本展示，**禁止 Bot 间自发级联互 @ 唤醒**，杜绝发散与 Token 爆炸。
5. **重启策略（Restart Policy）**：
   - Bot 的人格设定（System Prompt）、画像（Profile）与单体记忆修改通过 Supervisor 毫秒级优雅重启生效，无需复杂动态热更状态机负担。

---

## 模块架构与目录结构

```
NexusCore/
├── hub/                         # 微内核网关（队列/事件、注册与 ACL、config、ingress、scheduler、outbox、transport、supervisor）
├── memory/                      # Bot 原生单体记忆系统（Claude Code 四层栈 + MEMORY.md ≤200行 + Auto Dream）
├── bot-runtime/                 # Bot 独立运行时（简单 Agent 实现：禁Shell、禁长思考、操控 Agent）
├── agent-daemon/                # 跨设备受控物理探针（反向 TCP 接入、工具执行器）
├── cli/                         # 用户(user_bot)投递与管理控制台
├── cmd/migrate/                 # 内核 PostgreSQL 迁移 CLI
├── internal/                    # 跨二进制内部公共库（osproc、envelope、version 等）
├── migrations/                  # 内核 schema DDL 脚本
├── proto/                       # gRPC 控制面契约
├── GroupChatPlugin/             # 独立群聊插件工程（独立开发、L2 独立二进制）
│   ├── main.go                  # 群聊插件主进程
│   ├── manifest.json            # 插件标准清单（契约声明、SHA256 强校验）
│   ├── room/                    # 房间与会话滑动窗口管理
│   └── mention/                 # @ 语法提取器（仅用户@触发路由）
├── Frontend/                    # 独立前端工程（Vue 3 + Vite + Pinia + Element Plus）
│   ├── src/views/               # 群聊视区、Bot 画像/记忆抽屉、Agent 实时监控面板
│   ├── src/components/          # 气泡消息、@Mention 输入补全、Agent 执行卡片
│   ├── src/stores/              # 响应式状态管理（Bot 列表、房间消息、SSE 流）
│   └── vite.config.ts           # 前端工程构建与代理配置
├── doc/                         # 架构与实现设计文档全集
└── scripts/                     # 自动化验收、冒烟与全栈启动脚本集
```

---

## 关键功能特性

### 1. 微内核队列与总线
- **PG 原生持久队列**：基于 PostgreSQL `FOR UPDATE SKIP LOCKED` 高并发抢占消费，租约锁定 60s，心跳 10s 自动续租。
- **D-04 四语义支持**：全链路因果链（`correlation_id / causation_id / causal_path`）、幂等键去重、`suspended` 挂起与人工确认幂等恢复、Transactional Outbox 事务外发。
- **两级进程管理**：Supervisor 负责管理 Bot 进程（L1）与插件宿主进程（L2），支持 PID + `pid_start_time` 防复用、孤儿级联回收与优雅停机。

### 2. 配置中心（Config Center）
- 统一管理 `plugin / bot / agent / global` 四类期望配置。
- **全链路审批机制**：`Propose → Security Review (预审) → User Approve (user_bot 交互终端审批) → Activate`，内置 checksum 防篡改与一键 Rollback。
- 敏感配置严格采用 `secret_ref` 凭据隔离，绝不明文存储密钥。

### 3. 原生单体记忆（Claude Code 栈）
- 每个 Bot 自带独立记忆根目录：`data/bots/<bot_id>/memory/`。
- **会话追加**：`transcripts/*.jsonl` 纯物理追加，记录每轮交互事实。
- **记忆提取与重构**：`MEMORY.md` 严格维持在 200 行以内，由 Auto Dream 离线周期提炼；超过 200 行更新直接在 API 层面 400 拦截。

### 4. 独立群聊插件（GroupChatPlugin）
- 声明式 `manifest.json`，静态绑定事件订阅与发布能力。
- 房间消息时序维护与滑动上下文窗口注入。
- 严格的用户唤醒防线：只有真实用户发出的消息中包含 `@<bot_id>` 时，插件才封装并分发调度任务；Bot 生成文本里的 `@` 自动转换为展示标签，绝不回投总线触发连环死锁。

### 5. 现代化 Web 工作台（Frontend）
- **实时通信网关**：基于 Hub HTTP/SSE 网关（`:7200`），实时中继推送 Bot 状态（空闲 / 思考 / 调度 Agent）、群聊消息流与 Agent 实时终端流帧。
- **多 Bot 群聊会话**：多 Bot 同台在线，支持 Markdown 渲染、代码高亮、输入框 `@` 自动提示补全。
- **Bot 画像与记忆抽屉**：可视化编辑 Bot 的 System Prompt、Persona、Profile 及 `MEMORY.md`，支持“保存并一键重启”立即生效。
- **Agent 执行可视化监视**：实时展示反向 TCP 传输帧、工具调用详情、终端输出与执行毫秒耗时。

---

## 快速开始

### 运行环境要求
- **Go**: 1.26+
- **Node.js**: 18+ (推荐 Node 20 / 22)
- **PostgreSQL**: 16+（自带 `docker-compose.yml` 或本地实例）

### 1. 编译构建
```bash
# 编译微内核核心二进制（bin/hub、bin/bot-runtime、bin/agent-daemon、bin/cli、bin/migrate）
make build

# 编译独立插件（bin/plugins/plugin_groupchat、bin/plugins/plugin_memory）
make build-plugins
```

### 2. 准备数据库
```bash
# 使用 Docker Compose 快速启动本地 PostgreSQL 16+ 实例（默认端口 55432）
make compose-up

# 执行内核 Schema 迁移脚本
export NEXUS_PG_KERNEL_DSN="postgres://nexus_kernel:nexus_dev_kernel@127.0.0.1:55432/nexus"
make migrate-up
```

### 3. 一键启动全栈开发环境（推荐）
```bash
# 一键自动检查依赖、清理旧进程，并同时拉起 Hub(7200) + Agent-Daemon + GroupChatPlugin + Frontend(5173)
make launch-m4
```
启动成功后，浏览器访问：**`http://localhost:5173`** 即可进入 NexusCore 智能工作台。

---

## 常用命令一览

| 命令 | 描述 |
| --- | --- |
| `make build` | 编译 5 个核心二进制到 `bin/` |
| `make build-plugins` | 编译独立插件到 `bin/plugins/` |
| `make test` | 运行单元测试（跳过真库测试） |
| `make test-pg` | 运行全量单元测试与 PostgreSQL 真库集成测试 |
| `make vet` | 执行静态代码检查 |
| `make proto` | 基于 buf 与 protoc 重新生成 gRPC 代码 |
| `make compose-up` | 启动 PostgreSQL 16+ Docker 容器 |
| `make compose-down` | 停止 PostgreSQL 16+ Docker 容器 |
| `make migrate-up` | 执行内核数据库向上迁移 |
| `make migrate-down` | 回滚内核数据库迁移 |
| `make smoke-m1` | 运行 M1 纯内核九步冒烟与边界验收 |
| `make smoke-m2` | 运行 M2 Bot 管理、配置审批全链路与进程生命周期冒烟 |
| `make smoke-m3` | 运行 M3 单体记忆、简单 Agent 操控与群聊插件闭环冒烟 |
| `make smoke-m4` | 运行 M4 11 步端到端全链路冒烟验收（前端+网关+群聊+记忆抽屉+监控） |
| `make launch-m4` | 开发者全栈一键启动（Hub + Agent + GroupChat + Frontend） |
| `make drill-crash-m1` | 运行 M1 崩溃恢复演练（注入 SIGKILL 测试恢复能力） |
| `make clean` | 清理编译产物 `bin/` |

---

## 里程碑与当前状态

| 里程碑 | 内容概述 | 状态 | 验收凭证 |
| --- | --- | --- | --- |
| **M0 工程准备** | 仓库骨架、PostgreSQL 迁移 CLI（pgx）、proto 契约、Makefile、配置中心表结构 | **已完成** | `c657459`（D-22 / D-23 落地） |
| **M1 内核 MVP** | hub 10 模块、PostgreSQL 持久队列、D-04 四语义、失败注入、崩溃恢复 | **已完成** | `3a0e2a6`、详设 §8 六条验收全过 |
| **M2 Bot管理与插件运营** | 配置中心审批流、两级进程生命周期（防PID复用）、插件安装审计、DLQ运营 | **已完成** | `d569a80`、`make smoke-m2` 全绿 |
| **M3 群聊底座与单体记忆** | Claude Code 四层单体记忆（MEMORY.md ≤200行）、Auto Dream、GroupChatPlugin（仅用户@触发、防死循环） | **已完成** | `e2d72ee`、`make smoke-m3` 全绿 |
| **M4 Vue 工作台与实机演示** | `Frontend/` Vue 3 独立工程、微内核网关 SSE、多 Bot 群聊交互、单体记忆抽屉（Save & Restart）、Agent 实时监控 | **已完成** | `0f236c7`、`make smoke-m4` 全链路 11 步全过 |
| **M5 Agent Mesh 实真** | 跨设备 Agent 探针（反向 TCP、心跳 15s、工具清单上报）、Bash 工具白名单、HITL 人机协同审批确认 | **规划中（当前主线）** | 详见 `doc/开发规划.md` |
| **M6 生态与输入通道** | Telegram / 飞书连接器插件、外部 Webhook 接入、多端消息中继 | **规划中** | 计划中 |

---

## 设计文档索引

在进行任何功能调整与二次开发前，请务必阅读对应模块的设计文档：

| 文档 | 说明 |
| --- | --- |
| [doc/NexusCore架构设计总结.md](doc/NexusCore架构设计总结.md) | 上游总设计：微内核三原语、脑手分离、数据模型与修订记录 |
| [doc/内核mvp/内核MVP详细设计.md](doc/内核mvp/内核MVP详细设计.md) | 微内核实现级设计（hub 10 模块、kernel schema、PG 队列、配置中心底座） |
| [doc/ConfigCenter/ConfigCenter详细设计.md](doc/ConfigCenter/ConfigCenter详细设计.md) | 配置中心架构（四域配置、完整审批流、交互终端安全约束） |
| [doc/BotMvp/BotMvp详细设计.md](doc/BotMvp/BotMvp详细设计.md) | Bot 角色体系、生命周期、Supervisor 进程监督与优雅重启机制 |
| [doc/MemoryPlugin/用户级Bot默认个人记忆架构.md](doc/MemoryPlugin/用户级Bot默认个人记忆架构.md) | Bot 原生单体记忆架构（Claude Code 四层栈、Auto Dream 机制） |
| [doc/PluginMvp/PluginMvp详细设计.md](doc/PluginMvp/PluginMvp详细设计.md) | 插件模型规范、Manifest Schema、两级进程模型与优雅退出 |
| [doc/开发规划.md](doc/开发规划.md) | 完整开发规划（M0-M7 里程碑、估计、停线点与验收标准） |
| [doc/NexusCore已修正问题归档.md](doc/NexusCore已修正问题归档.md) | 已解决重大设计问题归档（D-01 至 D-10、D-13 至 D-23） |
| [doc/NexusCore待修正问题清单.md](doc/NexusCore待修正问题清单.md) | 未解决问题追踪（D-11 / D-12） |

---

## 架构硬约束（铁律）

1. **总线权限隔离**：只有 bot 能够投递和消费消息；人类用户以 `user_bot` 席位入总线；插件经注册 bot 的单次投递委托交互。
2. **纯粹信封通讯**：信封仅包含身份（`from_bot_id` 等）与因果链（`correlation_id / causation_id / causal_path`），严禁包含业务维度。
3. **内核三原语收敛**：业务语义（图谱算法、笔记渲染、IM 协议等）绝不进入内核源码；MCP / Skill 不进入内核与配置中心。
4. **单体记忆 200 行上限**：`MEMORY.md` 严格限定在 200 行以内，杜绝上下文二次方膨胀，保持极高缓存命中率。
5. **群聊严格用户 @ 触发**：仅人类用户 @ 能够唤醒 Bot；Bot 回复中的 @ 仅为展示，绝不允许 Bot 间自发互 @ 唤醒，坚决阻断递归死循环。
6. **两级进程安全**：插件进程（L2）由 owner bot 负责拉起与自治回收；强制终止前严格执行 PID + `pid_start_time` 双校验，杜绝误杀系统进程。
