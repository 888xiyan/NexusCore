# NexusCore Bot MVP 详细设计

> 版本：v0.2（2026-11-12）  
> 上游设计：`../NexusCore架构设计总结.md`（§3.3 / 3.3.1 / 3.4 / 5.4 / 5.5 / 6.9）与 `../内核mvp/内核MVP详细设计.md`（§3.5 scheduler / §3.9 supervisor / §5.0 BotRuntime）  
> 状态：设计定稿，待实现  
> v0.2 变更：配合 PluginMvp v0.2 的两级进程模型——插件侧 owner bot 负责拉起 / 回收插件进程，`monitor_bot` 巡检插件进程 PID，`user_bot` 掌握优雅关闭与强制终止两条路径（§4 / §8.6 / §8.7）。

## 1. 目标与非目标

### 1.1 目标

1. 内核启动即预置 `user_bot / monitor_bot / security_bot` 三个 resident 进程与一个懒加载的 `create_bot` worker，均通过 bot-runtime 独立二进制承载（D-17 / D-18 保持）；四角色不持有、不读取任何记忆，其能力不依赖记忆插件是否安装。四角色在配置中心的职责见 §4 与 ConfigCenter 详设。
2. `create_bot` 读配置中心的 active 投影（Bot / 插件 / Agent），按需触发 supervisor 拉起业务 Bot 进程（决策与执行分离，D-20）；**插件进程**不由 create_bot / supervisor 常规拉起，而由该插件的 owner bot 拉起（PluginMvp §6）。
3. `monitor_bot` 周期扫描 `bot_instances / kernel_jobs / kernel_plugin_processes`：进程消失、心跳超时、租约过期、Consumer Group 位点停滞、插件进程失联、配置版本漂移六类异常走 6.9 Health 链路与事件总线。
4. `security_bot` 承担插件注册 / 注册 Bot 审查 / 热更新治理 / **配置提案安全预审**四件事，MVP 通过 CLI 审批（`security-approve / security-list / security-revoke`）；配置批准权属于 `user_bot`，security_bot 只给意见。
5. 插件清单最少须声明一个 `xxx_bot`（业务 Bot），用户通过插件 / CLI 注册的 Bot 前缀 `user_`。保留名禁用（3.3.1 规则 2）。
6. 四角色与插件侧 Bot 共用同一份 `BotRuntime` gRPC 控制面与 bot-runtime 二进制，仅 manifest 不同。
7. 插件进程关闭双路径：优雅路径 `user_bot → owner bot → 插件进程`；强制路径 `user_bot → create_bot → supervisor` 越过 owner bot（§8.6 / §8.7）。
8. 冒烟链路端到端可验收（见第 8 节）。

### 1.2 非目标（明确延后）

| 延后项 | 触发条件 |
| --- | --- |
| Bot 二进制自动拉取 / 签名校验自动化 | CI 有制品签名后（先用 `binary_digest` 手工对账） |
| 插件市场 / 远程安装 | 用户需要在线安装插件时 |
| `monitor_bot` 健康评分、告警策略、自动熔断 | 真实负载产生评分信号后 |
| Bot 角色间消息（role-to-role RPC） | 跨 role 有协作需求时 |
| Tauri 工作台 UI | M7 |
| 插件热更新 UI 入口 | CLI 已可运行（走 `security-review` + `create-bot-pull`） |

## 2. 角色清单与部署形态

| Bot | 托管侧 | 角色 | activation_mode | agent_connection | MVP 创建方式 |
| --- | --- | --- | --- | --- | --- |
| `user_bot` | 内核 | 用户席位 | resident | 不适用（不执行工具） | hub 启动时预置 |
| `create_bot` | 内核 | Bot 工厂 / 决策者 | lazy（worker 模式） | 不适用 | 首次管理指令触发 |
| `monitor_bot` | 内核 | 监督者 | resident | 不适用 | hub 启动时预置 |
| `security_bot` | 内核 | 安全审查 / 热更新治理 | resident | 不适用 | hub 启动时预置 |
| `xxx_bot` | 插件 | 业务 Bot | lazy（默认）`//` resident（可指定） | per_task / persistent（可选） | 插件清单声明 + `create_bot` reconcile |
| `user_xxx_bot` | 插件 | 用户自建 | lazy（默认）| per_task（默认）| 用户经 CLI/插件指定 + `security_bot` 审查走 `create_bot` |

部署形态：全部为 bot-runtime 二进制的独立进程；`user_bot / monitor_bot / security_bot` 由 hub 启动时将其 `desired_state=start` 且 `min_instances>=1`，`create_bot` `desired_state=start` 但实际 `exec` 由首次 `user_bot` CLI 触发。`xxx_bot / user_xxx_bot` 由插件清单与用户指令驱动。

本表只列 **Bot 进程（L1）**。**插件进程（L2）**不是 Bot、不在本表：它由该插件的 owner bot（`bot_templates[0]`）exec 与回收，无 `bot_id`、不入总线，PID 登记在 `kernel_plugin_processes` 供 `monitor_bot` 巡检（见 PluginMvp §6.1）。

## 3. 生命周期与启动链路

```text
hub 启动
  ├─ 预置 resident：内核 supervisor 直接 exec user_bot / monitor_bot / security_bot
  └─ 懒加载：create_bot 由首次管理指令触发

user_bot (resident) 收到 CLI `plugin install xxx` / `bot create xxx_user` 指令
  └─ user_bot 控制面（5.4 BotRuntime）追加 desired_state 项，标记给 kernel
       └─ kernel 不直接 exec：它只是将该 bot 的 desired_state 写入 `bot_manifest`
       └─ `create_bot` 被首次触发（lazy）后：
            └─ 读 `bot_manifest` 当前的 desired_state 队列
            └─ 决定 start / stop / drain / restart 请求
            └─ 通过 `CreateBotControl.SetDesiredState` 通知内核（supervisor 执行 exec）
                 └─ supervisor exec 一个新的 bot-runtime 进程（binary_digest 校验）
                      └─ 该 bot 进程 `Register` 后 ready
                           └─ 若它是 owner bot（bot_templates[0]）且 plugin_manifest.binary 非空：
                                └─ owner bot exec 插件进程（L2，setsid 独立进程组，binary_digest 校验）
                                     └─ 经 channel=health 上报 pid / pgid / pid_start_time
                                          └─ hub 校验归属 → 写 kernel_plugin_processes
                                               └─ monitor_bot 纳入 10s 存活巡检

插件的 / 用户创建的 Bot 交付的后续任务使用架构总结 5.5 的生命周期状态机，完全一致（D-17/D-18）。
插件进程（L2）不走 5.5 的 Bot 状态机，走 PluginMvp §6.3 的进程状态机；supervisor 对它只做强制终止与孤儿级联回收。
```

## 4. 内核侧四角色

### 4.1 user_bot（用户席位，resident）

**职责**：承接所有来自 CLI / 工作台的输入并转投总线；拥有对其它 Bot 的管理权限（经 3.4 凭证 + ACL）。

**MVP 能力**：

1. 消息投递代理：CLI / 工作台的输入直接作为 user_bot 的 `IngressEnvelope`（from_bot_id=user_bot, channel=cli）。
2. Bot 管理指令代理：插件安装、Bot 注册、DesiredState 变化均可经 user_bot 发出。
3. 响应 Sink：所有工作台等待的 `bot.reply` 事件由 user_bot 的 Consumer Group 表消费（MVP 由 CLI 做订阅式渲染）。
4. 插件进程关闭指令下发（两条路径，见 §8.6 / §8.7）：
   - 优雅：向 owner bot 投递 `msg_type=control` 信封，由 owner bot 通知插件进程退出；
   - 强制：`CreateBotControl.SetDesiredState(action=kill_process)`，由 create_bot 决策、supervisor 执行 SIGKILL。
   - **user_bot 自身不发任何 OS 信号**：它只下指令，执行者是 owner bot（优雅）或 supervisor（强制）。
5. **配置中心唯一批准人**（D-23）：审阅 `config pending` 的提案与 diff，批准 / 驳回；批准绑定内容 `checksum`，可执行类提案必须先有 `security_bot` 的 approved 决定。user_bot 只发 `ConfigControl.Approve` 控制消息，由 hub 配置模块执行激活；不得直接写 `kernel_config_*` 表，不得自批安全预审。
6. 配置可视化：`config list / get / history / pending` 以 user_bot 身份读取（只读），审批在交互终端完成（回显 checksum 与 diff 摘要）。

### 4.2 create_bot（Bot 工厂，lazy / worker）

**职责**：读配置中心的 active 投影（Bot / 插件 / Agent）→ 决策 → 触发 supervisor。

**MVP 实现**：

1. 用户或 hub 触发的 CLI `create-bot pull <bot_id|plugin_id>` 使 create_bot 唤起。
2. 读到 `desired_state != observed_state` 的 Bot，为 Bot 的每一次活动允许以下四种动作：`StartBot / StopBot / DrainBot / RestartBot`。
3. 另有一种作用于**插件进程**的动作 `KillProcess`（`action=kill_process`）：只强制终止该 owner bot 持有的插件进程，不改变 Bot 自身的 desired_state；执行者是 supervisor，决策与审计留痕见 PluginMvp §6.5。
4. 不涉及真实业务（比如 LLM 推理），只做进程/事件级生命周期管理。
5. 完成后自身进入 `busy`，空闲 `idle_ttl`（默认 2 分钟）自退出。
6. **配置感知**：启动与 reconcile 时读取 active 配置投影（`config_version` + 必要字段）；收到 `kernel.config.changed` 后按策略处理——bot/agent 配置可在下一租约边界重载，manifest / 二进制变更走 drain + restart；插件配置变更通知 owner bot 决定 L2 重载或重启。create_bot 不写配置、不批准配置。

### 4.3 monitor_bot（监督者，resident）

**职责**：横向检查各 Bot 存续、插件进程存续与内核 Job 状态。

**MVP 实现**：

1. 每 10s 扫一次 `bot_instances`：`last_heartbeat_at > 30s` 即疑似失联，经 Health 链路报告。
2. 每 600ms 扫一次 `kernel_jobs`：`lease_until < NOW() AND status IN ('leasing','running')` 即异常。
3. 每 300s 扫一次 Consumer Group 位点：`consumer_group_id` 的位点最近 N 次（MVP N=3）未推进则告警。
4. 每 10s 扫一次 `kernel_plugin_processes`（只读，MVP 单机读 `/proc`）：
   - `state=running` 但 `pid + pid_start_time` 已不存在 → `plugin_process_vanished`；
   - `state=starting` 超过 `process_start_timeout` → `plugin_process_start_failed`；
   - `state=stopping` 超过 `process_stop_timeout + 5s` → `plugin_process_stop_stuck`；
   - 校验通过即刷新 `last_seen_at`（存活信号由巡检产生，不要求 owner bot 高频上报）。
5. 异常一律走 6.9 事件链路，**不直接重启 Bot、也不 kill 任何进程**（Bot 重启交给 supervisor，插件进程回收交给 owner bot 或 supervisor）。
6. **配置漂移**：对比 `bot_instances / kernel_plugin_processes / kernel_agents` 上报的 `config_version` 与配置中心 active 版本，不一致即报 `config_version_drift`（只提示，不自动重载）；同时统计 pending proposal 积压（可选）。

### 4.4 security_bot（安全审查 / 热更新治理，resident）

**职责**：

1. 插件 Manifest 审查（签名、权限、Bot 模板合法性、保留名禁用）。
2. 插件侧 Bot 的注册审查（3.3.1 规则 3 中 `xxx_bot` 注册 + 用户 Bot `user_xxx_bot` 注册）。
3. 热更新治理：新 `binary_digest` 上报时，先在 `security_decisions` 记入 decision=pending，再启动新进程灰度验证；观察期（默认 300s）异常即回滚至旧版本。
4. **配置提案预审**（D-23）：对 `plugin` 配置中涉及工具 / 权限的变更、`agent` 配置中涉及工具白名单的变更做静态审查（最小权限、注入面、敏感字段）；输出 `kernel_security_decisions(subject_type=config_proposal)`。security_bot 只给 approved / rejected 意见，**不能批准或激活配置**。

**MVP 实现**：

1. CLI：`plugin install` / `plugin upgrade` 必须显式通过 `security-approve`；`security-list` 显示 pending 状态。
2. 审查记录写入 `kernel_security_decisions`（见第 5 节）。
3. 观察期结束时无异常记录即 `decision=approved`；异常即 `decision=rejected + rollback_required`。

## 5. 插件侧业务 Bot

### 5.1 xxx_bot（业务 Bot）

1. 每插件**至少**注册一个 Bot（3.3.1 规则 3），命名 `xxx_bot`；命名即角色（3.3.1 规则 2）。
2. manifest 声明 `parent_plugin`（插件 ID）、`role=plugin_bot`、`managed_by=plugin_id`。
3. 插件安装后其首个 README 同步写入 Bot 描述；在 CLI `bot list` 中可见，由 create_bot 移入 reconcile 队列。
4. 插件混热更新时，同一 `bot_id` 的新 binary_digest 走 `security_bot` 审查再切换。
5. **owner bot 额外职责**（`bot_templates[0]`，详见 PluginMvp §6）：
   - exec 插件进程（`plugin_manifest.binary` + `binary_digest` 校验，setsid 独立进程组）；
   - 经 `channel=health` 上报 `pid / pgid / pid_start_time` 与状态变化，供 hub 登记、`monitor_bot` 巡检；
   - 收到 user_bot 的 control 指令后通知插件进程优雅退出（IPC shutdown + SIGTERM），超时自行升级 SIGKILL；
   - `waitpid` 回收子进程、崩溃退避重启、自身退出前先关插件进程（先子后父）；
   - **不得 exec 任何 Bot 进程**，也不得对自己子树之外的 PID 发信号。

### 5.2 user_xxx_bot（用户自建 Bot）

1. 命名必须 `user_` 前缀；若宿插件给出模板，默认名从模板生成（可改尾缀）。
2. `managed_by=plugin_id`，`owner=user_bot`。
3. 用户 / 插件指令驱动的安全审查流程与 4.4 相同。
4. 创建时默认写入 `memory_profile=personal_stack_v1`（内核只存储声明、不解释其含义）；宿插件负责提供创建入口和业务工具，不实现记忆引擎。该 profile 的运行时由记忆插件 `plugin_memory` 内的 memory-worker 于 M4 承载，详见[用户级Bot默认个人记忆架构](../MemoryPlugin/用户级Bot默认个人记忆架构.md)。内核侧四角色不使用任何 memory_profile。
5. `user_xxx_bot` **不是 owner bot**：不 exec 插件进程，经本地 IPC 复用宿插件那一个插件进程；宿插件进程未运行时按 lazy 语义先唤醒 owner bot（PluginMvp §6.1 规则 2）。

## 6. 数据模型

### 6.1 bot_manifest 扩展（其余字段沿用内核 MVP §4.2）

```text
bot_manifest
├── （已定义identity/protocol/actuation字段…）
├── role               # user / create / monitor / security / plugin
├── managed_by         # kernel（四角色） / plugin_id（ɵxxx_bot / user_xxx_bot）
├── parent_plugin      # 仅插件侧 Bot 有值
├── owner              # user_bot  / 空
├── memory_profile     # personal_stack_v1（默认） / none；插件侧用户自建 Bot 默认启用；
│                      # 由 plugin_memory 实现，内核仅存储声明、不解释
├── config_version     # 当前 active bot 配置版本（激活事务投影）
├── config_doc_id      # 对应 kernel_config_documents.doc_id
└── reserved           # 保留名禁用标记（由 registry 校验）
```

### 6.2 bot_instances 新增

```text
bot_instances
├── instance_id
├── bot_id
├── observed_state         # dormant / starting / ready / busy / draining / stopped / crashed
├── pid
├── binary_digest
├── started_at / stopped_at
├── last_heartbeat_at       # 供 monitor_bot 巡检
├── config_version          # 实例当前加载的配置版本（漂移观测）
└── last_error
```

### 6.3 kernel_security_decisions（kernel 库）

```text
decision_id             BIGINT PK
subject_type            # plugin / bot / hot_update / config_proposal / plugin_process
subject_id
action                  # register / hot_update / approve / review_config / force_kill
decision                # pending / approved / rejected / rolled_back
requested_by
decided_by              # security_bot
reason
evidence_json
grace_period_expiry     # 热更新观察期
created_at / updated_at
```

### 6.4 保留名与命名规则

1. 保留 bot 名：`user_bot / create_bot / monitor_bot / security_bot`，插件与用户均不得注册。
2. 允许 bot 名字符：`[a-z0-9_]+`，长度 5-64。
3. `user_` 前缀仅允许出现在用户手动创建的 Bot；插件侧创建的 `xxx_bot` 不可以带 `user_` 前缀。
4. 命名冲突由 registry 校验；未批准的 manifest 走 `security_bot` 审查。

### 6.5 kernel_plugin_processes（新增，kernel 库）

插件进程（L2）的 PID 登记表，**不是 Bot 实例表**：插件进程不得写入 `bot_instances`。完整字段、状态机与可信性校验规则以 [PluginMvp §6.3](../PluginMvp/PluginMvp详细设计.md) 为唯一来源，本文只列监督侧关心的字段：

```text
process_id / plugin_id / owner_bot_id / owner_instance_id
pid / pgid / pid_start_time      # kill 前必须 pid + pid_start_time 双匹配，防 PID 复用
state                            # pending/starting/running/stopping/stopped/killed/crashed/failed/orphaned
stop_mode / stop_requested_at    # graceful（owner bot 执行） / force（supervisor 执行）
config_version                   # 插件进程当前加载的配置版本（漂移观测）
last_seen_at                     # monitor_bot 巡检刷新
```

写权限：hub 运行账号（owner bot 经 health 通道上报后由 hub 落库，supervisor 执行强制终止后直接更新）；`monitor_bot` 只读。

## 7. 接口契约

### 7.1 BotRuntime（复用，见内核 MVP 5.0）

`Register / Heartbeat / StreamTasks / AckTask / SubscribeEvents / RenewCredential / Deregister`，无变化。

### 7.2 CreateBotControl（内部管理控制，仅内核 / user_bot / create_bot 使用）

```text
service CreateBotControl {
  rpc SetDesiredState(SetDesiredStateRequest) returns Ack;
  rpc ListDesiredState(ListDesiredStateRequest) returns stream DesiredStateItem;
}
SetDesiredStateRequest { bot_id, action[start/stop/drain/restart/kill_process], reason }
DesiredStateItem { bot_id, role, managed_by, desired_state, observed_state }
```

调用路径：`user_bot / CLI` → CreateBotControl；create_bot 读 ListDesiredState 得出 reconcile 请求。

`kill_process` 是**取值域扩展，不是签名变更**：字段集合不变，`bot_id` 填 owner bot 即可唯一定位插件进程（一插件一进程，PluginMvp §6.1）。语义 = 强制终止该 owner bot 持有的插件进程，**不改变 Bot 自身 desired_state**；执行者是 supervisor，不是 user_bot。

### 7.3 security CLI 契约

```text
NexusCore security-approve <decision_id>
NexusCore security-list   [pending]
NexusCore security-revoke <decision_id> <reason>
```

所有调用走 6.10 审计日志（actor=user_bot）。

### 7.4 插件进程 CLI 契约

```text
NexusCore plugin stop <plugin_id>              # 优雅：control 信封 → owner bot → 插件进程
NexusCore plugin start <plugin_id>             # 重新拉起（owner bot 已在则只 exec 插件进程）
NexusCore plugin kill <plugin_id> --confirm    # 强制：SetDesiredState(kill_process) → supervisor
NexusCore plugin ps [plugin_id]                # 查 kernel_plugin_processes：pid/state/stop_mode/last_seen_at
```

`plugin kill` 缺 `--confirm` 一律拒绝；`stop` 幂等（已终态返回成功）。命令均经 user_bot 投递，审计 actor=user_bot。

### 7.5 ConfigControl（复用内核 MVP §5.1）

```text
user_bot：Approve(proposal_id, checksum) / Reject(proposal_id, reason) / Get / ListPending
security_bot：Review(proposal_id) → kernel_security_decisions(subject_type=config_proposal)
create_bot / monitor_bot：Get / ListVersions（只读，不读 pending spec）
```

所有调用走 6.10 审计日志；`Approve` 只接受 `from_bot_id=user_bot` 且必须带 checksum。CLI 命令见 ConfigCenter 详设 §7。

## 8. 关键流程

### 8.1 内核启动四角色

```text
hub exec 起动
  ├─ supervisor 按 bot_manifest role 的 resident 标记 exec user_bot / monitor_bot / security_bot
  └─ 三个 bot 执行 Register + SubscribeEvents 后 ready
    ├─ user_bot 的 Consumer Group 是 `user_bot.cg`
    ├─ monitor_bot 起循环扫描
    └─ security_bot 起 security queue 扫描
```

### 8.2 插件安装 → 注册 xxx_bot → 拉起

```text
1. user 通过 CLI / 工作台：plugin install <plugin_id>
2. ingress（channel=cli, from=user_bot）受理后存 manifest / `kernel_plugins`
3. security_bot 加入 pending，等 CLI `security-approve`
4. approve 通过 → create_bot 读 manifests → SetDesiredState(start xxx_bot)
5. supervisor exec xxx_bot（binary_digest 校验）
6. xxx_bot Register → ready
7. monitor_bot 观察新实例心跳
8. 若 xxx_bot 是 owner bot 且 plugin_manifest.binary 非空：xxx_bot exec 插件进程
   （binary_digest 校验，setsid 独立进程组）→ 本地 IPC 握手 ready
9. xxx_bot 经 channel=health 上报 pid / pgid / pid_start_time → hub 校验归属 → kernel_plugin_processes(running)
10. monitor_bot 把该插件进程纳入 10s 存活巡检；同插件其余 Bot 经本地 IPC 复用它
```

### 8.3 用户建 user_xxx_bot

```text
1. user CLI：bot create user_writer_bot --plugin <id>
2. registry 校验命名并写入 memory_profile=personal_stack_v1
3. security_bot 根据 managed_by=插件 / owner=user_bot 完成审查
4. create_bot SetDesiredState(start user_writer_bot)
5. supervisor exec → Register → ready
```

### 8.4 插件热更新

```text
1. plugin upgrade v2 → security_bot 记 pending（新 digest）
2. CLI security-approve → create_bot 对每个 Bot SetDesiredState(drain)
3. drain 完成 → 更新 bot_manifest.binary_digest → SetDesiredState(start)
4. 观察期 300s 内异常 → decision=rolled_back，回退旧 digest 重新 exec
```

### 8.5 monitor_bot 心跳 / 租约扫描

```text
1. 扫 bot_instances，last_heartbeat_at > 30s → Health 上报
2. 扫 kernel_jobs，lease_until 过期 → 报 kernel.lease_expired
3. 扫 consumer_group，位点连续 3 轮停滞 → 报 bot.stalled
4. 扫 kernel_plugin_processes（只读 /proc 校验 pid + pid_start_time）：
   进程消失 → plugin_process_vanished；starting 超时 → plugin_process_start_failed；
   stopping 超时 → plugin_process_stop_stuck；校验通过则刷新 last_seen_at
（全部进 kernel_event_outbox → subscription Consumer Group 投递）
```

monitor_bot 在第 4 步只报告，不 kill、不重启：插件进程的回收权在 owner bot（优雅）与 supervisor（强制 / 孤儿）。

### 8.6 插件进程优雅关闭（user_bot → 插件 bot → 插件进程）

```text
1. user CLI：plugin stop <plugin_id>（disable / uninstall 的第一阶段同此）
2. user_bot 投递 msg_type=control 信封 → owner bot（channel=cli，因果链 + idempotency_key）
   载荷是插件自定义控制协议，内核不解析
3. owner bot 上报 state=stopping, stop_mode=graceful → hub 更新 kernel_plugin_processes
4. owner bot 通知插件进程：本地 IPC shutdown + SIGTERM
5. 插件进程 flush 落盘 → 退出；owner bot waitpid 回收
6. owner bot 上报 state=stopped（含 exit_code）
7. 超时（process_stop_timeout 默认 15s）未退出 → owner bot 自行 SIGKILL 整个进程组
   → 上报 state=killed, last_error=stop_timeout
8. 若是 disable / uninstall：create_bot SetDesiredState(stop owner_bot)
   → supervisor 回收该插件全部 Bot 进程（先子后父，顺序不可颠倒）
```

要点：内核只登记与审计，不参与协商；owner bot 在关闭期间停止派发新工作，在途任务按 job 状态机回队列或 suspended。完整规则见 [PluginMvp §6.4](../PluginMvp/PluginMvp详细设计.md)。

### 8.7 插件进程强制终止与孤儿回收（supervisor 执行）

```text
强制终止（user_bot 越过 owner bot）
1. user CLI：plugin kill <plugin_id> --confirm（缺 --confirm 直接拒绝）
2. user_bot → CreateBotControl.SetDesiredState{bot_id=owner_bot, action=kill_process, reason}
3. create_bot 决策 → kernel_security_decisions(subject_type=plugin_process, action=force_kill)
4. supervisor 校验登记行（pid + pid_start_time + pgid 归属 + 保留 PID 黑名单）
5. 校验通过 → SIGKILL 整个进程组 → 登记行 state=killed, stop_mode=force
   校验失败 → decision=rejected，不执行任何信号
6. owner bot waitpid 观察到 child exited → 补报 health（hub 幂等去重）；owner bot 自身保持 ready
7. 不自动重启：等用户显式 plugin start

孤儿级联（owner bot 先死）
1. supervisor 检测到 owner bot 进程退出 / 被回收
2. 读该 bot 名下的 kernel_plugin_processes 登记行
3. 同样跑 pid + pid_start_time + 归属 + 黑名单校验 → SIGKILL 插件进程组（含 L3 孙进程）
4. 登记行 state=orphaned → killed
5. 再按架构总结 5.5 退避重启 owner bot；重启后不认领旧 PID，一律 exec 新进程

hub 重启
1. 扫 state IN (running, stopping) 的登记行
2. 校验后存活即由 supervisor 级联 SIGKILL（不做 re-adopt）
3. owner bot 重新拉起后 exec 新插件进程，以新 pid_start_time 开新一轮
```

要点：**"越过插件 bot" 越过的是协商环节，不是内核**——执行器始终是 supervisor，user_bot 与 monitor_bot 都不自己发信号。完整规则见 [PluginMvp §6.5 / §6.6](../PluginMvp/PluginMvp详细设计.md)。

### 8.8 配置提案与激活

```text
1. CLI / 已授权主体 config propose --file spec.json → 文档 status=proposed（不生效）
2. 可执行类（插件权限 / Agent 工具白名单）：security_bot config review
   → kernel_security_decisions(approved/rejected)
3. user_bot config approve <proposal_id> --checksum <sha256>（交互终端回显 diff）
   → approved（approved_checksum 绑定；内容再变即失效）
4. config activate <proposal_id> → 事务内旧 active→retired、新→active、
   写 kernel.config.changed + kernel_config_audit
5. create_bot / owner bot 收到事件：bot/agent 配置下一租约边界重载；
   manifest / 二进制变更 drain + restart；插件配置由 owner bot 决定 L2 重载或重启
6. Bot / Agent / 插件进程上报新 config_version；monitor_bot 对比 active 版本，
   不一致报 config_version_drift（只提示）
```

角色边界：user_bot 只批准，security_bot 只预审，create_bot / monitor_bot 只读；任何角色都不得直接改配置表。

## 9. 验收标准

1. 冷启动 smoke：hub 启动后 5s 内 3 个 resident Bot（user_bot / monitor_bot / security_bot）全部 Register 且 ready。
2. `create-bot pull <bot_id>` → 在 `start_timeout=30s` 内看到目标 Bot ready。
3. 插件链路：`plugin install` → `security-list` 显示 pending → `security-approve` → xxx_bot 以新身份 Register → bot list 可见。
4. 插件热更新：v1 → v2 走 drain + 切换，观察期无异常；异常注入回滚。
5. monitor_bot：人工 kill 某一个 bot-runtime 进程，30s 内收到 `bot_stalled` / `agent.health.changed` 事件。
6. 验收问题 19 的四问均可将本设计逐条回显。
7. 集成测试：状态机（Bot lifecycle）与保留名校验通过表驱动用例；审计日志写入覆盖 security 行为。
8. 用户建 `user_xxx_bot` 后，`bot list` 显示 `memory_profile=personal_stack_v1`（M2 仅声明，运行时归 `plugin_memory`）；普通插件侧 `xxx_bot` 不继承该默认值；内核侧四角色无该字段。

**插件进程（配合 PluginMvp §12 第 9-17 条）**

9. owner bot 链路：`xxx_bot` ready 后插件进程进入 running，`plugin ps` 可见 pid / state / stop_mode；monitor_bot 巡检刷新 `last_seen_at`。
10. 优雅关闭：`plugin stop` 后插件进程 stopped 而 owner bot 仍 ready；重复 stop 幂等 no-op。
11. 强制终止：`plugin kill --confirm` 后 `state=killed, stop_mode=force`，owner bot 的 desired_state 不变；缺 `--confirm` 被拒；`kernel_security_decisions` 与 `kernel_ingress_log` 双写可查。
12. 孤儿级联：人工 kill owner bot 进程 → supervisor 级联回收插件进程组（含孙进程）无残留，monitor_bot 30s 内报 `bot_stalled`。
13. 角色边界：`user_bot` 与 `monitor_bot` 全程不发 OS 信号（代码审查 + 失败注入验证）；插件进程不出现在 `bot_instances`，且尝试 `Register` 被拒。

**配置中心（配合 ConfigCenter 详设）**

14. 提案不生效：`config propose` 后 bot / 插件行为不变，直到 `activate`。
15. 审批权限：非 user_bot 调用 `Approve` 被拒；user_bot 缺 checksum 或 checksum 不匹配被拒；批准后修改 spec 再次激活返回 409。
16. 安全预审：可执行类（插件工具/权限、Agent 工具白名单）无 security_bot approved 决定时不能批准；security_bot 自己调用 Approve 被拒。
17. 激活与分发：`config activate` 后 `kernel.config.changed` 可订阅；create_bot 按 active 投影决策；bot 重载后上报新 `config_version`；篡改版本触发 `config_version_drift`。
18. 密钥：spec 中出现明文密钥字段被拒；审计与事件只出现 `secret_ref`，不出现值。

## 10. 与上游文档的映射

| 本文档章节 | 架构总结对应 |
| --- | --- |
| 2 / 3 / 4 | 3.3 / 3.3.1 / 5.5 |
| 4.4 / 5 / 7.2 / 8.4 | 3.4 / 6.9 |
| 6 / 7.1 | 5.4（BotRuntime）/ 内核 MVP §4.2 |
| 6.4 保留名 | 3.3.1 规则 2 |
| 8.2 / 8.4 | 3.3.1 规则 3 / 5.5 规则 8 |
| 8.5 | 6.9 |
| 4.1 / 4.2 / 8.6 / 8.7 | PluginMvp §6.4 / §6.5（插件进程关闭双路径） |
| 4.3 / 6.5 / 8.5 | 6.9 + PluginMvp §6.3（PID 登记与巡检） |
