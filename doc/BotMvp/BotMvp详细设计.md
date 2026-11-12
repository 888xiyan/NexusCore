# NexusCore Bot MVP 详细设计

> 版本：v0.1（2026-11-10）  
> 上游设计：`../NexusCore架构设计总结.md`（§3.3 / 3.3.1 / 3.4 / 5.4 / 5.5 / 6.9）与 `../内核mvp/内核MVP详细设计.md`（§3.6 scheduler / §3.10 supervisor / §5.1 BotRuntime）  
> 状态：设计定稿，待实现

## 1. 目标与非目标

### 1.1 目标

1. 内核启动即预置 `user_bot / monitor_bot / security_bot` 三个 resident 进程与一个懒加载的 `create_bot` worker，均通过 bot-runtime 独立二进制承载（D-17 / D-18 保持）；四角色不持有、不读取任何记忆，其能力不依赖记忆插件是否安装。
2. `create_bot` 读内核的 Bot 清单与插件清单，按需触发 supervisor 拉起业务 Bot 进程（决策与执行分离，D-20）。
3. `monitor_bot` 周期扫描 `bot_instances / kernel_jobs`：进程消失、心跳超时、租约过期、Consumer Group 位点停滞四类异常走 6.9 Health 链路与事件总线。
4. `security_bot` 承担插件注册 / 注册 Bot 审查 / 热更新治理三件事，MVP 通过 CLI 审批（`security-approve / security-list / security-revoke`）。
5. 插件清单最少须声明一个 `xxx_bot`（业务 Bot），用户通过插件 / CLI 注册的 Bot 前缀 `user_`。保留名禁用（3.3.1 规则 2）。
6. 四角色与插件侧 Bot 共用同一份 `BotRuntime` gRPC 控制面与 bot-runtime 二进制，仅 manifest 不同。
7. 冒烟链路端到端可验收（见第 8 节）。

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

插件的 / 用户创建的 Bot 交付的后续任务使用架构总结 5.5 的生命周期状态机，完全一致（D-17/D-18）。
```

## 4. 内核侧四角色

### 4.1 user_bot（用户席位，resident）

**职责**：承接所有来自 CLI / 工作台的输入并转投总线；拥有对其它 Bot 的管理权限（经 3.4 凭证 + ACL）。

**MVP 能力**：

1. 消息投递代理：CLI / 工作台的输入直接作为 user_bot 的 `IngressEnvelope`（from_bot_id=user_bot, channel=cli）。
2. Bot 管理指令代理：插件安装、Bot 注册、DesiredState 变化均可经 user_bot 发出。
3. 响应 Sink：所有工作台等待的 `bot.reply` 事件由 user_bot 的 Consumer Group 表消费（MVP 由 CLI 做订阅式渲染）。

### 4.2 create_bot（Bot 工厂，lazy / worker）

**职责**：读 Bot / 插件清单 → 决策 → 触发 supervisor。

**MVP 实现**：

1. 用户或 hub 触发的 CLI `create-bot pull <bot_id|plugin_id>` 使 create_bot 唤起。
2. 读到 `desired_state != observed_state` 的 Bot，为 Bot 的每一次活动允许以下四种动作：`StartBot / StopBot / DrainBot / RestartBot`。
3. 不涉及真实业务（比如 LLM 推理），只做进程/事件级生命周期管理。
4. 完成后自身进入 `busy`，空闲 `idle_ttl`（默认 2 分钟）自退出。

### 4.3 monitor_bot（监督者，resident）

**职责**：横向检查各 Bot 存续与内核 Job 状态。

**MVP 实现**：

1. 每 10s 扫一次 `bot_instances`：`last_heartbeat_at > 30s` 即疑似失联，经 Health 链路报告。
2. 每 600ms 扫一次 `kernel_jobs`：`lease_until < NOW() AND status IN ('leasing','running')` 即异常。
3. 每 300s 扫一次 Consumer Group 位点：`consumer_group_id` 的位点最近 N 次（MVP N=3）未推进则告警。
4. 异常一律走 6.9 事件链路，不直接强制重启 Bot（重启交给 supervisor）。

### 4.4 security_bot（安全审查 / 热更新治理，resident）

**职责**：

1. 插件 Manifest 审查（签名、权限、Bot 模板合法性、保留名禁用）。
2. 插件侧 Bot 的注册审查（3.3.1 规则 3 中 `xxx_bot` 注册 + 用户 Bot `user_xxx_bot` 注册）。
3. 热更新治理：新 `binary_digest` 上报时，先在 `security_decisions` 记入 decision=pending，再启动新进程灰度验证；观察期（默认 300s）异常即回滚至旧版本。

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

### 5.2 user_xxx_bot（用户自建 Bot）

1. 命名必须 `user_` 前缀；若宿插件给出模板，默认名从模板生成（可改尾缀）。
2. `managed_by=plugin_id`，`owner=user_bot`。
3. 用户 / 插件指令驱动的安全审查流程与 4.4 相同。
4. 创建时默认写入 `memory_profile=personal_stack_v1`（内核只存储声明、不解释其含义）；宿插件负责提供创建入口和业务工具，不实现记忆引擎。该 profile 的运行时由记忆插件 `plugin_memory` 内的 memory-worker 于 M4 承载，详见[用户级Bot默认个人记忆架构](../MemoryPlugin/用户级Bot默认个人记忆架构.md)。内核侧四角色不使用任何 memory_profile。

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
└── last_error
```

### 6.3 kernel_security_decisions（kernel 库）

```text
decision_id             BIGINT PK
subject_type            # plugin / bot / hot_update
subject_id
action                  # register / hot_update / approve
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

## 7. 接口契约

### 7.1 BotRuntime（复用，见内核 MVP 5.1）

`Register / Heartbeat / StreamTasks / AckTask / SubscribeEvents / RenewCredential / Deregister`，无变化。

### 7.2 CreateBotControl（内部管理控制，仅内核 / user_bot / create_bot 使用）

```text
service CreateBotControl {
  rpc SetDesiredState(SetDesiredStateRequest) returns Ack;
  rpc ListDesiredState(ListDesiredStateRequest) returns stream DesiredStateItem;
}
SetDesiredStateRequest { bot_id, action[start/stop/drain/restart], reason }
DesiredStateItem { bot_id, role, managed_by, desired_state, observed_state }
```

调用路径：`user_bot / CLI` → CreateBotControl；create_bot 读 ListDesiredState 得出 reconcile 请求。

### 7.3 security CLI 契约

```text
NexusCore security-approve <decision_id>
NexusCore security-list   [pending]
NexusCore security-revoke <decision_id> <reason>
```

所有调用走 6.10 审计日志（actor=user_bot）。

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
（全部进 kernel_event_outbox → subscription Consumer Group 投递）
```

## 9. 验收标准

1. 冷启动 smoke：hub 启动后 5s 内 3 个 resident Bot（user_bot / monitor_bot / security_bot）全部 Register 且 ready。
2. `create-bot pull <bot_id>` → 在 `start_timeout=30s` 内看到目标 Bot ready。
3. 插件链路：`plugin install` → `security-list` 显示 pending → `security-approve` → xxx_bot 以新身份 Register → bot list 可见。
4. 插件热更新：v1 → v2 走 drain + 切换，观察期无异常；异常注入回滚。
5. monitor_bot：人工 kill 某一个 bot-runtime 进程，30s 内收到 `bot_stalled` / `agent.health.changed` 事件。
6. 验收问题 19 的四问均可将本设计逐条回显。
7. 集成测试：状态机（Bot lifecycle）与保留名校验通过表驱动用例；审计日志写入覆盖 security 行为。
8. 用户建 `user_xxx_bot` 后，`bot list` 显示 `memory_profile=personal_stack_v1`（M2 仅声明，运行时归 `plugin_memory`）；普通插件侧 `xxx_bot` 不继承该默认值；内核侧四角色无该字段。

## 10. 与上游文档的映射

| 本文档章节 | 架构总结对应 |
| --- | --- |
| 2 / 3 / 4 | 3.3 / 3.3.1 / 5.5 |
| 4.4 / 5 / 7.2 / 8.4 | 3.4 / 6.9 |
| 6 / 7.1 | 5.4（BotRuntime）/ 内核 MVP §4.2 |
| 6.4 保留名 | 3.3.1 规则 2 |
| 8.2 / 8.4 | 3.3.1 规则 3 / 5.5 规则 8 |
| 8.5 | 6.9 |
