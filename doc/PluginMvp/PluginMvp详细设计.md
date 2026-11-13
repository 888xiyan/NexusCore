# NexusCore Plugin MVP 详细设计

> 版本：v0.2（2026-11-12）  
> 上游设计：`../NexusCore架构设计总结.md`（§3.2 / 3.3.1 / 3.4 / 5.1 / 5.5 / 6.9 / 6.10 / 8.4）+ `../BotMvp/BotMvp详细设计.md`  
> 状态：设计定稿，待实现  
> v0.2 变更：收口架构总结 §12.3 的"后端非 Bot 插件进程隔离方式"待定项——确立两级进程模型（Bot 进程 + 插件进程），冻结启动链路、PID 登记与可信性校验、优雅关闭与强制终止三条链路（第 6 节）。

## 1. 目标与非目标

### 1.1 目标

1. **四类插件形态都可写**：入站通道（连接外网 / IM）、工作台视图（业务数据 UI）、执行器（本地 / 远程工具）、知识 / 记忆（如 `plugin_memory`），通过统一 Manifest 与控制面接入。
2. **命名与事件规范冻结**：`<plugin_prefix>.<domain>.<event>` 规则 + bot 名命名规范，MVP 编译期 enforce（G1 收口）。
3. **生命周期完整**：install → security_review → approve → activate → upgrade（drain + 切换）→ disable → uninstall，全链路状态机；disable / uninstall 的进程回收是"先插件进程、后 Bot 进程"两阶段。
4. **两级进程模型冻结**：Bot 进程由内核 supervisor 拉起，插件进程由其 owner bot 拉起并把 PID 登记给 `monitor_bot`；关闭分优雅（user_bot → 插件 bot → 插件进程）与强制（user_bot 越过插件 bot，supervisor 代执行）两条路径（第 6 节）。
5. **卸载清理规则冻结**：Bot 进程下线、Consumer Group 停订、事件类型反注册、类型化表处理（G4/G5 最小解）。
6. **安全闭环**：security_bot 审查 + `kernel_security_decisions` 记录，覆盖 install / upgrade / uninstall / force_kill 四类动作。
7. **冒烟链路端到端可验收**（第 11 节）。

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
| 知识 / 记忆插件 | `plugin_memory`（M3 首发示例，详见 [../MemoryPlugin/MemoryPlugin详细设计.md](../MemoryPlugin/MemoryPlugin详细设计.md)） |

每个插件 = `plugin_id` + `plugin_manifest` + 一个可执行 Go 二进制（optional）+ 类型化数据表（optional）+ 1..N 个 `bot_manifest`（必须有，规则 3.3.1 规则 5）。MVP 生态下"每个插件必须有一个 bot 才存活"是硬条件，杜绝"空插件"。

**二进制与 Bot 不是同一个进程**：`plugin_manifest.binary` 是**插件进程**（业务实现，非总线主体），`bot_templates[]` 声明的是**Bot 进程**（bot-runtime 承载，总线主体）。前者由后者拉起，两级进程模型见第 6 节。`binary` 为空的插件（如纯工作台视图）只有 Bot 进程，没有插件进程。

知识 / 记忆插件与其它类型在接入方式上完全同级：独立二进制、自有 schema、自有账号、自有 manifest 与生命周期；内核不为 `plugin_memory` 保留任何特殊通道或凭据。记忆业务（图谱、金库、晋升、上下文装配）全部是插件内部实现，不属于内核，也不由内核做任何授权或装配。

## 3. 插件 Manifest Schema（v1 冻结）

```text
plugin_manifest
├── plugin_id           # 规范：^plugin_([a-z0-9_]{3,24})$，示例 plugin_architect
├── display_name        # 可读名，仅 UI
├── plugin_type         # ingress / workspace / executor / knowledge
├── version             # semver v1.0.0
├── binary              # optional：插件进程（L2）二进制 file name；纯工作台视图可空
├── binary_digest       # sha256，插件进程 exec 前与热更新时校验（区别于 bot 的 binary_digest）
├── namespace           # VARCHAR，默认与 plugin_id 去前缀同、可覆写
├── subscribing[]       # 事件订阅申请：{event_expr, scope:{namespace,project,bot}}
├── publishing[]        # 事件发布申请：{event_type}（由此登记到 event registry）
├── providing_tools[]   # 提供的工具（走 传输路由 5.1，`tools` 形式）
├── requiring_tools[]   # 期望使用的工具（他人插件提供的，安全审查需通过来源 plugin_id）
├── bot_templates[]     # 至少一个：{bot_id, activation_mode, agent_connection, binary_digest, ...}
│                       # [0] 即 owner bot：唯一有权 exec / 回收插件进程的 Bot（见 6.1）
├── ui_mounts[]         # optional：{slot: activitybar|maintab|sidepanel, title, entry}
├── resource_limits     # `{cpu_ms: int, memory_mb: int}` 默认 `{100ms,128MB}`
│                       # 按 owner bot 进程组（Bot 进程 + 插件进程 + 其子树）合计，见第 8 节
├── required_tables[]   # optional：插件自有类型化表声明（与 M3 依赖）
├── deps                # [] 对其他插件的 tool / bot 依赖声明
└── schema_version      # manifest 自身 schema 版本
```

### 3.1 manifest 与配置中心的边界（D-23）

- **manifest 是不可变安装制品**：`plugin_id / binary / binary_digest / bot_templates / 权限声明` 等字段在安装与升级时由 `security_bot` 审查，变更走 install / upgrade 流程。
- **plugin 配置是可变的运行配置**：工具开关、资源限制、日志级别、插件运行参数等放在配置中心 `scope_type=plugin, scope_id=plugin_id, config_key=default` 的文档里，版本化、可回滚。
- 初始配置（v1）由 install 流程生成：`plugin install` 是 user_bot 发起的指令，安装时的配置随 `security-approve` 与安装动作一并批准并激活（install 流程内部以 user_bot 身份调用 `ConfigControl.Approve`，绑定 checksum，`approved_by=user_bot`）；后续任何变更必须走 `config propose → (security review) → user_bot approve → activate`。
- 配置文档不复制 manifest 字段；需要引用时用 `manifest_version + binary_digest`。

### 3.2 独立插件目录与打包标准规范（以 GroupChatPlugin 为例）

为支持外部业务插件实现“独立目录开发、开箱即装、隔离运行”，明确独立插件的标准目录结构与分发规范：

```text
GroupChatPlugin/                 # 插件根目录（独立开发、独立编译）
├── manifest.json                # 插件清单（声明 plugin_id, binary, tools, templates 等）
├── main.go                      # 插件 L2 独立进程入口
├── bin/                         # 编译产物目录（如 bin/groupchat_plugin）
├── migrations/                  # 插件自有数据表 DDL（按需）
│   ├── 0001_chat_rooms.up.sql
│   └── 0001_chat_rooms.down.sql
├── config_schema.json           # 插件自身运行配置的 JSON Schema
└── README.md                    # 插件能力与协议说明文档
```

**安装与校验前置要求**：
1. **源码或二进制就绪**：插件安装前，独立进程制品必须已被编译至指定路径，并通过 `sha256sum` 计算填入 `manifest.json` 的 `binary_digest`；
2. **所有权声明**：`bot_templates[0]` 声明为该插件的 `owner_bot`，作为唯一合法持有并管理插件进程的 Bot；
3. **安全审查通过**：`security_bot` 审查其二进制摘要、声明的发布/订阅事件与依赖工具，禁止声明内核保留前缀（`bot.* / kernel.*` 等）；
4. **用户批准后激活**：在交互终端由 `user_bot` 批准后，自动写入 `kernel_plugins`、登记事件与工具权限，并由 supervisor 拉起 owner bot 驱动 L2 插件进程自治启动。

## 4. 命名与事件规范（G1 收口）

### 4.1 bot_id 命名

```text
kernel-side roles   : user_bot / create_bot / monitor_bot / security_bot   # reserved
plugin-side default : [a-z0-9_]+_bot，由 plugin_id 去前缀 + '_bot'（如 plugin_architect → architect_bot）
plugin-side custom  : 任意合规命名（见命名规则 4.5）
user-created        : user_ 前缀 + plugin 侧提供的 bot 简称 + _bot   # user_writer_bot
                      默认 memory_profile=personal_stack_v1；宿插件不实现记忆引擎
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

active ─disable→ disabled （插件进程 → Bot 进程依次下线，订阅反注册，事件类型保留）
       └─uninstall→ uninstalled（插件进程 → Bot 进程依次下线 + 订阅反注册 + 类型化表数据需 CLI 确认后清理）

active / disabled ─upgrade→ security_review 新版本 → approved → 执行 drain/restart
```

进程回收顺序在所有下线动作中一致：**先插件进程（L2），后 Bot 进程（L1）**，见 6.4。

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
3. bot_templates 转入 bot_manifest 记录；`bot_templates[0]` 即 owner bot。
4. create_bot 读清单决定拉起对应 bot 进程（supervisor exec）。
5. owner bot ready 后 exec 插件进程（`binary_digest` 校验），经 health 通道上报 PID → hub 写 `kernel_plugin_processes`（见 6.2 / 6.3）。
6. kernel_plugins.status = active；monitor_bot 将该插件进程纳入存活巡检。
7. 配置中心：为 `plugin_id` 生成 `plugin` 配置文档 v1（`active`，由 install 动作批准），`kernel_plugins.config_version=1`；owner bot exec 插件进程时携带该版本。

### 5.4 upgrade

与 BotMvp §8.4 热更新一致：安全审查 → drain 旧进程 → 切 binary_digest → 新进程 ready。观察期异常回滚。

涉及两个 digest，顺序不可颠倒：`plugin_manifest.binary_digest`（插件进程）与 `bot_manifest.binary_digest`（Bot 进程）。切换链路 = 优雅关闭旧插件进程（6.4）→ drain 旧 Bot 进程 → 切两个 digest → 新 Bot ready → exec 新插件进程 → 上报新 PID。回滚时同样先收插件进程再回退 Bot。

配置版本：升级 manifest 不等于升级 plugin 配置；若升级同时需要调整运行参数，必须另提 `config propose`（可执行类先审），与 manifest 升级分开审批与激活，避免"一次批准覆盖两类变更"。

### 5.5 disable

1. security_bot 收到 disable 决议，把 plugin.status 改 disabled。
2. **不清理**：数据库表、Bot manifest、事件 registry 均保留原样；`kernel_plugin_processes` 登记行保留但迁到终态。
3. 第一阶段：user_bot 向 owner bot 下发优雅关闭指令 → 插件进程退出（6.4）。
4. 第二阶段：create_bot `SetDesiredState(stop)` → supervisor 下线该插件全部 Bot 进程。
5. 插件进程卡死时以 6.5 强制终止兜底；不得跳过第一阶段直接杀 Bot 进程（会留孤儿，须由 supervisor 级联回收，见 6.6）。

### 5.6 uninstall

1. 交互式：用户列 Bot、插件进程与类型化表清点 → CLI 展示将删除内容 → 用户手动 `confirm` 或 `keep`。
2. 两种模式都先走 6.4 的两阶段进程回收：插件进程优雅关闭 → 全部 Bot 进程下线；`kernel_plugin_processes` 登记行随卸载删除（它是运行数据，不是业务数据）。
3. 配置中心：该插件的 `plugin` 配置文档标记 `retired`（不物理删除，保留审计）；keep 模式保留类型化表，confirm 模式按确认清理。
3. confirm 模式：bot_manifest 行删除、event_registry 反注册、类型化表（若有）整行 DROP、`kernel_plugins` 状态 uninstalled。
4. keep 模式：registry 反注册，但类型化表保留（数据可用 CLI 读取，或重建插件后恢复）。
5. `install / upgrade` 不支持从 uninstalled 状态直接激活，须重走 install。
6. 知识 / 记忆插件（`plugin_memory`）的卸载额外受保护：vault 数据默认强制保留，confirm 删除必须二次确认；卸载后依赖记忆的 Bot 降级为无上下文执行，内核行为不变。
7. 强制终止（6.5）只是卡死兜底，不得作为卸载的默认路径：插件进程可能持有未落盘状态。

## 6. 插件进程模型（两级进程：Bot 进程 + 插件进程）

本节收口架构总结 §12.3 的"后端非 Bot 插件的进程隔离方式"待定项。核心结论：**插件进程不是 Bot 进程**，二者分属两级，各有各的决策者、执行者与监督者。

### 6.1 两级进程模型

```text
hub（内核 supervisor）
└─ L1 Bot 进程 = bot-runtime 二进制          [总线主体：bot_id / Consumer Group / gRPC 控制面]
   └─ L2 插件进程 = plugin_manifest.binary   [非总线主体：只有 process_id + pid，无 bot_id]
      └─ L3 插件内部 worker（如 memory-worker，M4）  [内核不登记，属插件内部实现]
```

| 维度 | L1 Bot 进程 | L2 插件进程 |
| --- | --- | --- |
| 决策者 | `create_bot` | owner bot（插件侧自治） |
| 执行者 | 内核 supervisor | owner bot（exec / SIGTERM / 崩溃重启）；强制终止与孤儿级联回到 supervisor |
| 总线身份 | `bot_id`，可投递与消费 | 无；一切对外投递经 owner bot 的单次投递委托 |
| 制品校验 | `bot_manifest.binary_digest` | `plugin_manifest.binary_digest` |
| 状态机 | 架构总结 5.5 lazy / resident（内核权威） | 6.3 `state`（owner bot 执行，内核登记） |
| 监督 | supervisor 进程检测 + monitor_bot 心跳巡检 | owner bot `waitpid` + monitor_bot PID 存活巡检 |
| 数量（MVP） | 1..N（`bot_templates[]`） | 0 或 1（`binary` 为空则无） |

规则：

1. **owner bot = `bot_templates[0]`**（manifest 顺序即约定，与第 8 节规则 4 一致）。MVP 不新增 manifest 字段；将来需要显式指定 owner 时再走 manifest 契约确认。
2. **一插件一插件进程**：同插件其余 Bot 经本地 IPC 复用该进程，不各自 exec。进程与 Bot 之间的 IPC 是插件内部协议，内核不解析（硬约束 17、内核 MVP §5.3）。
3. 插件进程**永远不是 Bot**：不得 `Register`、不得持有 Consumer Group、不得直连 ingress、不得出现在 `bot_instances`。
4. L3 子进程属插件内部，内核不登记；owner bot 以**独立进程组**（`pgid` = 插件进程 pid）承载整棵子树，回收按进程组级联，避免留孙进程。
5. 硬约束 9 的"supervisor 是唯一执行器"作用于 **Bot 进程**。插件进程的常规启动 / 优雅关闭 / 崩溃重启由 owner bot 执行——这是 owner bot 唯一的 OS 级例外，且严格限定在自己的子树内；强制终止（6.5）与孤儿级联（6.6）仍由 supervisor 执行。
6. **MVP 限单机、同 OS 用户**：PID 归属校验与信号发送依赖 `/proc` 与同用户权限；跨设备插件进程（M5 agent-daemon）延后。
7. 插件进程无长期状态权威：状态落在 DB / 插件自有 schema，进程可随时被杀并重启（与"Bot 不持有本地长期状态"同源）。

### 6.2 启动链路（create_bot → 插件 bot → 插件进程 → monitor_bot）

```text
1. plugin install → security-approve → kernel_plugins.status=active
2. create_bot 读清单 → SetDesiredState(start, bot_id=owner_bot)
3. supervisor exec bot-runtime（bot_manifest.binary_digest 校验）
4. owner_bot Register → ready
5. owner_bot exec plugin_manifest.binary（binary_digest 校验；setsid 建独立进程组）
6. owner_bot 取 pid / pgid / pid_start_time，本地 IPC 握手确认插件进程 ready（插件协议，内核不解析）
7. owner_bot 经 channel=health 信封上报 health_report(target_type=plugin_process,
   evidence_json={plugin_id, owner_bot_id, pid, pgid, pid_start_time, binary_digest, config_version, state})
8. hub ingress 校验因果链 + PID 归属（6.3）→ 写 kernel_plugin_processes(state=running)
9. monitor_bot 巡检查读该行，纳入 10s 存活扫描
10. 同插件其余 Bot 由 create_bot 按 manifest 顺序拉起，经本地 IPC 连同一插件进程
```

规则：

1. 顺序不可颠倒：**先 Bot 进程 ready，再插件进程**；owner bot 未 ready 时不得 exec 插件进程。
2. `process_start_timeout` 默认 30s（与 Bot 的 `start_timeout` 同级但独立计时）；超时 owner bot 自行 SIGKILL 子树并上报 `state=failed`。
3. 崩溃重启由 owner bot 执行，退避 1s / 2s / 4s…封顶 60s；连续 3 次失败即停止重启，上报 `plugin_process_start_failed`，monitor_bot 聚合广播，create_bot 将 owner bot `desired_state=stop`（`kernel_plugins.status` 保持 active，进程状态 failed，CLI 可见）。
4. lazy owner bot 的 `idle_ttl` 到期退出前，必须先走 6.4 优雅关闭插件进程（先子后父）。
5. 上报是**事件驱动**（状态变化才报），不做高频心跳；存活由 monitor_bot 巡检刷新，避免污染 `kernel_health_reports`。

### 6.3 PID 登记与可信性校验

新表 `kernel_plugin_processes`（`kernel` schema，hub 运行账号写，monitor_bot 只读；M2 落地）：

```text
process_id        VARCHAR(128) PK      # <plugin_id>:<owner_bot_id>（31+1+64），MVP 一插件一行
plugin_id         VARCHAR(32) NOT NULL
owner_bot_id      VARCHAR(64) NOT NULL
owner_instance_id VARCHAR(64) NOT NULL # 持有它的 bot_instances 实例
pid               INT NOT NULL
pgid              INT NOT NULL         # = 插件进程 pid（独立进程组）
pid_start_time    BIGINT NOT NULL      # /proc/<pid>/stat starttime，防 PID 复用
binary_digest     CHAR(64) NOT NULL
config_version    BIGINT NOT NULL DEFAULT 0   # 插件进程当前加载的 plugin 配置版本（漂移观测）
state             pending / starting / running / stopping / stopped / killed / crashed / failed / orphaned
stop_mode         NULL / graceful / force
stop_requested_at TIMESTAMP NULL
started_at / stopped_at
last_seen_at      TIMESTAMP            # monitor_bot 巡检刷新
last_error        VARCHAR(512)
UNIQUE(plugin_id)                      # MVP 一插件一进程
INDEX(state, last_seen_at)
```

状态机（单向，终态不可被迟到上报覆盖）：

```text
pending → starting → running → stopping → stopped
             │          │          └→ killed   # 关闭超时 owner bot 自行升级
             │          ├→ killed              # 6.5 强制终止（supervisor）
             │          ├→ crashed → starting  # owner bot 退避重启，开新一轮 pid_start_time
             │          └→ orphaned → killed   # owner bot 先死，supervisor 级联回收
             └→ failed                         # 启动超时 / 连续 3 次启动失败
```

校验规则（上报由 hub ingress 侧校验，kill 由 supervisor 在执行前校验；任一不满足即 `decision=rejected` 并落审计）：

1. **归属校验**：上报时 `ppid` 必须等于 `owner_instance_id` 对应 Bot 进程的 pid，且 `pgid == pid`（owner bot 已 setsid）。进程随后可能被 reparent，因此**登记之后**的校验以登记行的 `pid + pid_start_time + pgid` 为准，不再依赖实时 `ppid`。插件 Bot 不得登记非自己子树的 PID。
2. **防 PID 复用**：`pid_start_time` 必填；此后任何状态迁移与任何 kill 都必须 `pid + pid_start_time` 双匹配。不匹配即视为原进程已消失，不得对复用该 PID 的新进程动手。
3. **保留 PID 黑名单**：hub / supervisor / 内核侧四角色 Bot 进程 / 其他插件的登记进程 / 数据库进程，一律不得成为 kill 目标；kill 只允许命中本插件的登记行。
4. **幂等与迟到**：终态（stopped / killed / failed）行不接受迟到的 running 覆盖；只有新的 `pid_start_time` 才开新一轮。重复上报按 `process_id + pid_start_time + state` 去重。
5. **monitor_bot 只读**：它只观察、聚合、告警，**不执行 kill**（与架构总结 6.9 规则 3 同源）；巡检结果只刷新 `last_seen_at` 与发健康报告。
6. 该表属运行数据，卸载时随 `kernel_plugins` 一起清理；不得把插件业务数据放进来（硬约束 14）。

### 6.4 优雅关闭（user_bot → 插件 bot → 插件进程）

```text
1. CLI：plugin stop <plugin_id>（或 disable / uninstall 的第一阶段）
2. user_bot 投递 msg_type=control 信封 → owner_bot（channel=cli，完整因果链 + idempotency_key）
   载荷 = 插件自定义控制协议，内核不解析（硬约束 17）
3. owner_bot 经 health 通道上报 state=stopping, stop_mode=graceful, stop_requested_at=now
4. owner_bot 通知插件进程：本地 IPC shutdown 请求 + SIGTERM（MVP 两者都发，插件协议可自选其一）
5. 插件进程 flush 落盘 → 退出；owner_bot waitpid 回收，不留僵尸
6. owner_bot 上报 state=stopped（evidence_json 含 exit_code）→ hub 更新登记行
7. 若指令是 disable / uninstall：create_bot 再 SetDesiredState(stop, owner_bot)
   → supervisor 回收该插件全部 Bot 进程
```

规则：

1. **先子后父**：插件进程未到终态前不得回收 owner bot 进程；disable / uninstall 是两阶段，不是一步。
2. `process_stop_timeout` 默认 15s；超时 owner bot **自行升级** SIGKILL 整个进程组 → 上报 `state=killed, last_error=stop_timeout`。该升级仍在自己子树内，属合法自治，不需要走 6.5。
3. **幂等**：重复 stop 对已终态进程返回成功（no-op），不重复发信号；控制指令按 `idempotency_key` 去重。
4. 关闭期间 owner bot 停止向插件进程派发新工作；在途任务按 job 状态机回队列或进 suspended，不吞任务。
5. 插件进程拒绝退出或卡死时，monitor_bot 报 `plugin_process_stop_stuck`，用户可走 6.5 强制路径。
6. 内核不参与协商：它只登记状态与审计，不解释控制载荷，也不替插件决定 flush 语义。

### 6.5 强制终止（user_bot 越过插件 bot）

```text
1. CLI：plugin kill <plugin_id> --confirm（跳过插件 flush，可能丢未落盘状态）
2. user_bot → CreateBotControl.SetDesiredState{bot_id=owner_bot, action=kill_process, reason}
   （仅扩展 action 取值域，字段集合与 RPC 签名不变）
3. create_bot 决策 → 写 kernel_security_decisions(subject_type=plugin_process, action=force_kill)
4. supervisor 校验登记行（pid + pid_start_time + pgid 归属 + 保留黑名单）→ SIGKILL 整个进程组
5. supervisor 更新登记行 state=killed, stop_mode=force
6. owner_bot waitpid 观察到 child exited → 补报 health（同 pid_start_time，hub 按 6.3 规则 4 去重）
7. monitor_bot 聚合广播；owner bot 自身保持运行
```

规则：

1. **越过的是插件 bot 的协商环节，不是内核**：执行器仍是 supervisor，硬约束 9 与"Bot 不直接物理操作"都不破。
2. 必须显式二次确认（`--confirm`），与 uninstall confirm 同级；无确认一律拒绝。
3. 审计双写：`kernel_security_decisions` + `kernel_ingress_log`（actor=user_bot，完整因果链）。
4. `kill_process` 只作用于插件进程，**不改变 owner bot 的 desired_state**；要连 Bot 一起停用 `stop`。
5. 校验失败（PID 已复用 / 归属不符 / 命中黑名单）→ 拒绝执行并记 `decision=rejected`，不做"尽力杀"。
6. 强制终止后**不自动重启**：owner bot 见 `stop_mode=force` 即保持 stopped，等用户显式 `plugin start`。

### 6.6 崩溃、孤儿与 hub 重启回收

| 场景 | 检测者 | 处置 |
| --- | --- | --- |
| 插件进程自己崩溃 | owner bot（`waitpid`） | 上报 crashed → 退避重启（6.2 规则 3） |
| owner bot 崩溃 / 被回收 | supervisor（Bot 进程退出） | **级联 SIGKILL 其登记的插件进程组** → orphaned → killed；再按架构总结 5.5 退避重启 Bot |
| 进程已消失但登记仍 running | monitor_bot 巡检 `/proc` | 报 `plugin_process_vanished`，不 kill；由 owner bot 收敛状态 |
| starting 超过 `process_start_timeout` | monitor_bot | 报 `plugin_process_start_failed`，提示查 owner bot 日志 |
| stopping 超过 `process_stop_timeout + 5s` | monitor_bot | 报 `plugin_process_stop_stuck`，提示走 6.5 |
| hub 重启 | hub 启动扫描 | `state IN (running, stopping)` 的行：校验 pid + pid_start_time，存活即由 supervisor 级联 SIGKILL，随后 owner bot 重新 exec |
| owner bot 重启后 | owner bot | **不认领旧 PID**：先确认旧进程已终态，再 exec 新进程并上报新 `pid_start_time` |

规则：

1. 不允许"父死子活"的孤儿跨越 supervisor 的回收周期；级联回收是 supervisor 的职责，不是 monitor_bot 的。
2. 级联 kill 前同样跑 6.3 的全部校验（pid + pid_start_time + 归属 + 黑名单）。
3. hub 重启采取"杀干净再拉起"，不做 re-adopt：MVP 不引入跨重启的进程认领协议（依据 6.1 规则 7）。

### 6.7 语言与形态（G2 回收：MVP 单语言，语言无关延后）

MVP 只支持 Go 独立二进制。其他语言处理策略：

| 语言 | MVP 应对 |
| --- | --- |
| Go | L1 由 bot-runtime 承载；L2 是 `cmd/xxx_bot/main.go` 或插件自有 binary |
| Python | 作为 L3：由 `plugin_memory` 的插件进程（L2）拉起 memory-worker（M4；模型先用 stub），内核不登记 |
| TypeScript / Vue | 无后端，纯 UI（M7 工作台起），只有 L1 没有 L2 |

**规则**：MVP 内所有插件都须 Go 生态可直接编译；非 Go 插件 SDK 推至 M4+。L2 / L3 的进程隔离方式已由本节冻结，架构总结 §12.3 不再是待定项；前端插件的模块挂载方式仍待 M7。

## 7. 插件数据迁移（G4 最小解）

### 7.1 自带 schema 的插件

1. plugin manifest 中 `required_tables[]` 声明以避免缺表异常；但 DDL 由插件 install 时发至 security 审查（与 Bot 注册合并审核）。
2. DDL 语句格式：` migrations/plugins/<plugin_id>/NNNN_<name>.up.sql /`.down.sql`
3. DDL 用键：`<plugin_id>__<table_name>_<action>` 格式 prefix，避免与其他插件冲突。
4. security_bot不加判定；只是收集并在 approve 时入 DDL。

### 7.2 无 schema 的插件

使用记忆插件 `plugin_memory` 的 `memory_entities` 即可（namespace=plugin_id），不产生 `required_tables[]`。M3 Obsidian 先走这条路。

## 8. 资源限制（G6 最小解）

MVP 仅限单实例的 cpu / 内存（`resource_limits` 字段的 max）：

1. 建议默认 100ms CPU、128MB 内存；超过 3s 持续即 `bot.stalled` 上报。
2. 限额按 **owner bot 进程组**合计（L1 Bot 进程 + L2 插件进程 + L3 子树），不按单进程各给一份；MVP 不新增 manifest 字段。
3. 全局 fair-share / 20+ 插件调度延后。
4. MVP 无 min_instances 争抢假定，只有单进程组 max 上限；多 Bot 插件按 manifest 顺序调度。

## 9. 安全链路（见 BotMvp 4.4）

MVP 走 security_bot CLI：

```text
security-list
security-approve <decision_id>
security-revoke <decision_id> <reason>
```

所有 install / upgrade / uninstall / force_kill 记录都经过 `security_bot`，写入 `kernel_security_decisions`；每条记录含 `correlation_id`（按 6.10 追溯）。强制终止插件进程（6.5）额外要求 CLI `--confirm` 二次确认，`subject_type=plugin_process`。

## 10. 插件安装 / 拉起 / 触发 / 热更新 / 关闭（五条完整链路，均端到端）

注意链路基本都经过 user_bot / create_bot / supervisor，由 BotMvp 定义；本 MVP 只覆盖"插件清单是否合法 / 是否会拉起对应 bot 与插件进程的决策"。

安装链路 / 拉起链路 / 热更新链路见 BotMvp §8.2 / §8.4 与本文 §5.3-5.4；优雅关闭与强制终止链路见本文 §6.4 / §6.5 与 BotMvp §8.6 / §8.7；卸载链路见本文 §5.6。MVP 验收按两者组合验证。

## 11. 冒烟链路

```text
1. plugin install plugin_demo（Go 二进制 + manifest + 一个 bot_template）
2. security-list 命中 pending；security-approve 通过
3. plugin status = active；生成 `plugin` 配置 v1（active）
4. create_bot 读 active 投影 → supervisor exec demo_bot（binary_digest 校验）
5. demo_bot Register + ready（config_version=1）
6. demo_bot exec plugin_demo 插件进程（binary_digest 校验，setsid 独立进程组）→ IPC 握手 ready
7. demo_bot 经 channel=health 上报 PID + config_version → hub 校验归属 → kernel_plugin_processes(state=running)
8. monitor_bot 巡检命中该行，last_seen_at 持续刷新
9. user 通过 CLI 发条消息 → ingress 校验 → user_bot → router → demo_bot → 插件进程
10. demo_bot 回 bot.reply 事件 → user_bot 订阅端接收
11. 优雅关闭：plugin stop plugin_demo → control 信封到 demo_bot → SIGTERM 插件进程
    → state=stopped，demo_bot 仍 ready
12. 强制终止：plugin start 后 plugin kill plugin_demo --confirm
    → supervisor SIGKILL 进程组 → state=killed, stop_mode=force，demo_bot 仍 ready
13. 孤儿回收：人工 kill demo_bot 进程 → supervisor 级联 SIGKILL 插件进程组
    → state=orphaned→killed，无残留进程
14. cleanup 测试：plugin uninstall → 插件进程与 Bot 依次下线，kernel 表可见 status=uninstalled
15. disable 路径：plugin disable → 两阶段下线但 manifest/表/registry 保留
    → plugin activate → 重新 Register + exec 插件进程
16. upgrade 路径：plugin upgrade → 审查 + 关旧插件进程 + drain + 新 digest 进 ready + exec 新插件进程
17. 配置变更：config propose（plugin 运行参数）→ 无审批不生效 → security 预审（如涉及权限）→ user_bot approve → config activate
    → owner bot 重载/重启插件进程并上报新 config_version
```

## 12. 验收标准

**插件与 Bot 生命周期**

1. install → security-list → approve → bot ready 走通。
2. 安装一个含 bot_templates=2 的插件，两个 Bot 都由 create_bot 成功拉起。
3. 向 `demo_bot` 发一条消息：user_bot 收到 bot.reply 事件，事件 `from_bot_id=demo_bot`，日志含 plugin_id。
4. uninstall keep 模式：Bot 下线，表仍在。
5. uninstall confirm 模式：Bot 下线 + 类型化表 DROP 确认后删除。
6. upgrade 流程：drain + 换 binary_digest + 新 ready；升级 diff 记录到 security_decisions。
7. header 校验（plugin_id 格式、保留名禁用、event prefix 保留）表驱动用例通过。
8. 事件类型注册的唯一性测试通过。

**插件进程（第 6 节）**

9. 启动链路：owner bot ready 后 exec 插件进程，`kernel_plugin_processes` 出现 running 行且 `pid / pgid / pid_start_time / binary_digest` 齐全；monitor_bot 巡检持续刷新 `last_seen_at`。
10. 一插件一进程：bot_templates=2 时只有一个插件进程、只有一行登记，第二个 Bot 经本地 IPC 复用。
11. 优雅关闭：`plugin stop` → 插件进程 stopped 且 owner bot 仍 ready；重复 stop 幂等 no-op，不重复发信号。
12. 关闭超时：注入忽略 SIGTERM 的插件进程 → `process_stop_timeout` 后 owner bot 自行 SIGKILL，`state=killed, last_error=stop_timeout`。
13. 强制终止：`plugin kill --confirm` → supervisor SIGKILL 进程组，`state=killed, stop_mode=force`，owner bot 存活且 desired_state 不变；缺 `--confirm` 一律拒绝。
14. PID 可信性失败注入三例（谎报非子树 PID / PID 复用致 start_time 不匹配 / 命中保留 PID 黑名单）全部 `decision=rejected`，且无任何进程被误杀。
15. 孤儿回收：kill owner bot 进程 → supervisor 级联 SIGKILL 插件进程组，含 L3 孙进程在内无残留。
16. hub 重启：running / stopping 行被级联清理，owner bot 重新 exec 并以新 `pid_start_time` 开新一轮；终态行不被迟到上报覆盖。
17. 两阶段顺序：disable / uninstall 时插件进程先于 Bot 进程到达终态，时序可从审计日志复核。

**配置中心（D-23）**

18. install 生成的 plugin 配置 v1 为 active 且 `approved_by=user_bot`；后续变更必须走 propose/approve/activate。
19. 未审批的 plugin 配置不改变插件进程行为；激活后 owner bot 按策略重载/重启 L2，`kernel_plugin_processes.config_version` 更新。
20. 卸载后 plugin 配置文档为 `retired`（保留审计）；重装生成新版本，不复用旧 active。

## 13. 与上游文档的映射

| 本文档章节 | 架构总结对应 |
| --- | --- |
| 2 / 3.4 | 3.2 万物皆插件 |
| 3.4 / 9 | 3.4 插件信任边界 + BotMvp §4.4 |
| 4 | 3.3.1 规则 2（保留名） + 新增 |
| 5 / 11 | BotMvp §8.2 / 8.4 |
| 6.1 / 6.2 | 3.3.1 规则 1 / 5（两级进程） + 5.5 + 12.3（已收口） |
| 6.3 | 6.9 健康报告（target_type=plugin_process） + 6.10 入口校验 |
| 6.4 / 6.5 / 6.6 | 5.5 规则 1 / 8 + 6.9 规则 3 + BotMvp §8.6 / §8.7 |
| 7 | 8.4 记忆插件存储边界 / 8.1 namespace 规范 |
| 10 | 5.1 传输路由 / D-14 插件自定义协议 |

## 14. 修订记录

### 14.1 M2 实现落地与工程细化（2026-11-13）

插件 MVP 与两级进程管理模型已在 M2 交付落地并通过全套自动化验证。结合工程落地与代码审查，以下设计细节与偏离做正式归档：

1. **防 PID 复用安全信号发送机制（SignalProcessGroup 与僵尸过滤）**：
   - 在 `internal/osproc` 实现 `SignalProcessGroup(pid, pgid, start, sig)`：
     - 校验组长身份完整性（`pid > 1 && pgid == pid && start > 0`）；
     - 读取 `/proc/<pid>/stat` 校验 start_time 与 PGRP 是否完全一致；
     - 显式过滤僵尸进程（`'Z'` 与 `'X'` 状态不视为活体目标）；
     - 原进程消失或 ESRCH 时转化为 `os.ErrNotExist` 并 fail closed，绝不向可能已被其他程序复用的 PID/PGID 发送误杀信号。
2. **生命周期并发互斥与行级排他锁（lifecycleMu + FOR UPDATE）**：
   - `hub/plugin.Manager` 引入 `lifecycleMu` 互斥锁，确保插件的安装、安全审批、启用、禁用、卸载操作严格串行化；
   - 数据库事务内部对 `kernel_plugins`、`kernel_security_decisions` 与 `kernel_plugin_processes` 加 `FOR UPDATE` 行锁，防范并发 TOCTOU 与竞态状态覆盖。
3. **强制终止全流程代际核验与进程组退出等待**：
   - supervisor 的 `KillPluginProcess` 强制核对代际元组（`owner_bot_id`、`owner_instance_id`、`pid`、`pgid`、`pid_start_time`）；
   - 发送 SIGKILL 后通过 `waitPluginGroupExit` 轮询确保整棵进程树（含所有子/孙进程）完全退出；双写 `kernel_security_decisions`（`decided_by='supervisor'`）。
4. **客户端控制命令全链路因果链闭环（硬约束 12 落实）**：
   - CLI 中的插件生命周期控制命令（`disable`, `uninstall`, `stop`, `start`, `kill`）统一在客户端构造根因果链信封（`postPluginControl`，`MsgControl` / `ChannelCLI`），并将信封注入请求体，使每一次管理动作在内核均有完整的因果链追踪。
5. **开发环境本地回环绑定（docker-compose 隔离）**：
   - `docker-compose.yml` 中的 PostgreSQL 开发端口严格绑定在 `127.0.0.1`，防止数据库监听暴露至外部网络。
6. **M2 验收结果（第 12 节全量项）**：
   - 验收通过：涵盖插件安装审查激活、多 Bot 模板、消息路由、优雅关闭、忽略 SIGTERM 超时杀进程组、`plugin kill --confirm` 强制终止、PID 复用注入防御、孤儿级联回收、配置版本关联与卸载审计。自动化验收见 `scripts/smoke-m2.sh`。

