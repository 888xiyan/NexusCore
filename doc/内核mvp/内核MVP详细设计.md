# NexusCore 内核 MVP 详细设计

> 版本：v0.3（2026-11-12，记忆移入插件后重构）  
> 上游设计：`../NexusCore架构设计总结.md`（本文件是 Step 0 / Step 1 必须项的实现级详细设计）  
> 范围说明：本 MVP **只包含内核**（队列与事件、Bot 注册与运行时、传输路由），不含任何记忆与知识能力；记忆由 knowledge 类插件 `plugin_memory` 承接（M3/M4），见 `../MemoryPlugin/MemoryPlugin详细设计.md`。  
> 状态：设计定稿，待实现

## 1. 目标与非目标

### 1.1 目标

1. hub 单二进制实现内核三原语：持久化队列与事件、插件注册与 Bot 运行时、传输路由（架构总结 3.1）。
2. bot-runtime 独立二进制实现 Bot 运行时，由内核按 lazy 模式拉起与回收（5.5）。
3. 单 MySQL 实例的 `kernel` schema 承载内核运行表；hub 只持有该 schema 的运行账号，不持有、不访问任何插件存储（记忆 / 金库 / 插件类型化表）。
4. 所有消息带完整因果链并通过统一入口校验；接受 / 拒绝 / 重复三类结果全部落审计日志（6.10）。
5. 队列语义完整：幂等键、租约续期、`suspended` 状态、DLQ replay 人工确认幂等（D-04 全部语义并入本设计）。
6. 冒烟链路端到端可验收（见 6.1）。

### 1.2 非目标（本 MVP 明确不做）

| 延后项 | 触发条件 |
| --- | --- |
| 记忆与知识能力（图谱、金库、晋升、上下文装配） | 全部属于 `plugin_memory`，M3 起落地；本 MVP 内核无任何相关代码与表 |
| 插件生命周期（install / security / upgrade / uninstall） | M2；本 MVP 只做 registry 的身份、授权与订阅基础 |
| Health Service 聚合评分 | 最小版仅心跳超时 + job 租约检测（monitor_bot 自 M2 起承担聚合） |
| resident Bot、`warm_pool` | 出现持续订阅型 Bot 时 |
| `promote`、跨 schema saga | M4，随记忆插件 |
| memory-worker（Context Assembler / Memory Arbiter） | M4，位于 `plugin_memory` 进程内 |
| UDP 传输、多 Bot 并发扩缩 | 需要遥测 / 并发时 |
| PM、Obsidian、IM、浏览器插件 | Step 2+ |
| 多节点部署、自动化证书管理 | 产品验证后 |

## 2. 部署形态

```text
┌──────────────────────┐        ┌────────────────────┐
│ hub（单 Go 二进制）   │        │ MySQL 单实例        │
│  ├─ store            │        │ kernel schema       │
│  ├─ registry + ACL   │◀──────▶│  （hub 运行账号）    │
│  ├─ grpcd            │        └────────────────────┘
│  ├─ ingress          │
│  ├─ scheduler        │        （memory / vault schema 属于
│  ├─ outbox/eventbus  │          plugin_memory，M3 起部署，
│  ├─ transport        │          内核无任何账号）
│  └─ supervisor       │
└──────────────────────┘
        │ supervisor ──exec──▶ bot-runtime（lazy 按需）
        │        ▲
        │        │ gRPC（Register / Heartbeat / StreamTasks / AckTask / SubscribeEvents）
        │        │
        │  agent-daemon（loopback / 远程，反向 TCP 接入 transport）
```

进程与端口：hub 监听 gRPC（BotRuntime / 管理接口）与 TCP 传输端口；bot-runtime 只主动出站连接 hub 与（按任务）Agent，不监听端口；agent-daemon 反向连接 hub 传输端口。memory / vault schema 不在本 MVP 部署范围内。

## 3. 模块详细设计

各模块按"职责 / 接口 / 规则"描述。hub 内部模块走 hub 内部接口。

### 3.1 store（hub 运行存储）

**职责**：管理 `kernel` schema 中内核运行表的连接池与事务。

**接口**：`WithKernelRuntimeTx(fn)`。

**规则**：

1. hub 账号只授权 `kernel` schema 的运行表（`kernel_projects / kernel_jobs / kernel_event_outbox / kernel_ingress_log / bot_manifest / bot_instances / kernel_plugins / kernel_security_decisions` 等），不持有其他 schema 的任何权限。
2. hub 只读取 `MYSQL_KERNEL_DSN`；不持有、不读取任何插件 DSN（含记忆插件与金库）。
3. 事务超时 5s，超时整体回滚。

### 3.2 registry（插件注册与 ACL）

**职责**：Manifest 校验、身份凭证签发 / 验证、授权表维护。

**接口**：

```text
IssueCredential(plugin_id)                     -> (token, expires_at)
VerifyCredential(token)                        -> (actor, allowed_scopes)
ApprovePermissions(plugin_id, perms, by_user)
Subscribe(plugin_id, event_types, scope)       -> consumer_group_id
Revoke(plugin_id)                               # 吊销即令牌失效 + 订阅停止
```

**规则**：

1. 凭证为 HMAC 签名的短期 token（默认 24h），`RenewCredential` 续期；吊销写入内存 + 数据库黑名单。
2. Manifest 未授权的声明（订阅 / 工具 / namespace）一律拒绝。namespace 注册仅在内核侧登记归属，不涉及任何知识数据。
3. 授权唯一批准人：MVP 为 CLI 管理命令（`approve-plugin`），工作台后置。
4. 插件的工具调用授权（如 `plugin_memory.*`）在 M2 随插件生命周期落地；M1 只需支持 Bot 身份与订阅授权的最小集。

### 3.3 grpcd（BotRuntime gRPC 服务）

**职责**：5.4 的 `BotRuntime` 服务；证书为 operator 签发的自签 mTLS（MVP 允许 `--insecure-local` 仅回环）。

**方法**：`Register / Heartbeat / StreamTasks(流) / AckTask / SubscribeEvents(流) / RenewCredential / Deregister`。

**规则**：

1. `StreamTasks` 每条任务带 `lease_token`；`AckTask(job_id, lease_token, result)` 校验 token 一致才 ack，否则拒绝（防双 worker）。
2. `Heartbeat(bot_id, job_lease_tokens)` 兼作长任务租约续期入口，间隔 10s；连续 3 次缺失即视同失联。
3. 所有 RPC 必须携带因果上下文元数据（6.8 字段），缺失返回 `CAUSAL_CONTEXT_REQUIRED`。
4. 请求 / 响应统一走 ingress 模块校验与日志。

### 3.4 ingress（入口校验与审计）

**职责**：所有入口消息过 `IngressEnvelope` 校验；写 `kernel_ingress_log` + 加密上下文快照与载荷存储。

**规则**：

1. 校验顺序：schema_version → 必填字段 → 凭证 → `causal_path` 连续性 / 深度 ≤ 32 → 幂等键去重。
2. 接受 / 拒绝 / 重复三种结果都在与接收同事务内写日志。
3. 快照加密：MVP 用单把用户密钥 AES-GCM，key 由 env 注入；字段级脱敏规则内嵌（token / cookie / key 名单替换为 `<redacted:field>` 引用）。
4. 快照保留期默认 30 天，日志永久保留（MVP；正式策略可调）。
5. 信封只承载通讯语义（按 D-19 后的 6.10 字段集：`msg_type / from_bot_id / channel / channel_ref` + 因果 + 幂等 + `payload_hash / payload_ref`）。入口不解析业务载荷；接收 bot 拆封后按 `payload_ref` 从内核加密载荷存储自行拉取，读取授权由其已收到的信封绑定。
6. 健康报告以 `channel=health` 走同一入口；聚合属于 `monitor_bot`，不在 ingress 内实现（见 6.7）。

### 3.5 scheduler（Job 调度器）

**职责**：D-04 全部语义的载体。

**状态机**：

```text
queued -> leasing -> running -> done
                     ├-> retry_backoff（attempt_count++，next_retry_at）
                     ├-> suspended        # yield_confirm / 租约冲突 / replay 待确认
                     ├-> cancelled
                     └-> dead（进 DLQ 语义标记）
leasing 冲突路径：lease 过期 → running 检测 → suspended + 人工确认
```

**接口**：

```text
Enqueue(job)                                # 生成 correlation / causation / idempotency
Lease(queue_name, worker_id)                # SKIP LOCKED 抢占
RenewLease(job_id, lease_token)
ResumeConfirmed(job_id, decision, by_user)  # continue / replay / discard
DeadLetter / Replay(job_id)                 # replay 必经 ResumeConfirmed
```

**规则**：

1. `idempotency_key` 唯一约束；`parent_job_id` 填充 causation 对应 job。
2. lease 基础 60s；`Heartbeat` 每 10s 续；过期 → 非幂等任务转 `suspended` 等人工确认（`lease_expired` 仅为检测信号，不单独落库为状态）。
3. 延迟调度：MVP 用 250ms 轮询 `queued / retry_backoff AND next_retry_at <= NOW()`；进程内有新任务时经 channel 立即唤醒。
4. 人工确认幂等（用户决策）：任何暂停任务先组装副作用清单（`kernel_job_intents` + job 日志），通过 HITL 渠道请用户在 `continue / replay / discard` 三选一；决定与确认人写入 job 审计字段。已声明幂等（有 `idempotency_key` 且消费者声明幂等）的操作除外，仍自动重试。
5. 毒丸防护：同一 job 连续 N=5 次 `retry_backoff` 后直接 `dead`。
6. 分区有序：`conversation_id` 分区严格串行（不越过前序未终态任务）；其余分区并行。分区键来自入口方在 job 创建时携带的路由提示（6.6），内核不解析载荷。

### 3.6 outbox（Outbox Dispatcher）

**规则**：

1. 状态 `pending -> dispatched -> archived`；全部分发完成后归档，不物理删除。
2. 每个授权 Consumer Group 生成一条 `event.deliver` job 复用 `kernel_jobs`。
3. 扫描间隔 250ms，批 100 条。
4. 插件事件（如 `plugin_memory.*`）由插件本地 Outbox 经其 bot 投递委托回投 ingress 后进入本 Outbox，dispatcher 不直接读取插件库。

### 3.7 eventbus（事件总线）

**规则**：

1. MVP 只实现持久事件投递（经 outbox + event.deliver job），内存尽力而为通道可整体延后。
2. 消费侧幂等：先查 `kernel_event_dedup(consumer_group_id, event_id)`，命中则只更新状态。
3. 订阅在 Bot dormant 期间由内核代持，激活后续投。

### 3.8 transport（传输路由）

**规则**：

1. MVP：反向 TCP。Agent 主动连 hub 传输端口，第一帧为鉴权帧（Agent 凭证）。
2. `transport_frame` 校验：`length ≤ 4MB`、`protocol_id` 已授权、`stream_id` 已注册；违规关闭会话并写审计。
3. 心跳 15s；丢 2 次判连接失联，本连接所有 stream 按协议错误关闭。
4. UDP 完全延后。

### 3.9 supervisor + bot-runtime（Bot 二进制）

**职责**：内核按 manifest 拉起 / 监督 / 回收 Bot 进程。

**规则**：

1. 制品校验：`binary_digest`（sha256）必须与 manifest 一致，不匹配拒绝执行。
2. MVP 只支持 `activation_mode=lazy`：首个 queued 任务或事件触发 `exec`，`idle_ttl`（默认 5 分钟）无任务则进程自退出；`desired_state / observed_state` reconcile 周期 2s。
3. `start_timeout` 默认 30s；超时任务回队列，连续 3 次启动失败熔断该 bot_id 并上报 `bot_stalled`。
4. bot-runtime 启动后立即 `Register`，随后 `SubscribeEvents` + `StreamTasks` 循环；不持有任何本地长期状态。
5. 崩溃重启指数退避 1s / 2s / 4s…封顶 60s。
6. 决策 / 执行分离（D-20）：`desired_state` 的修改者**只有** `create_bot`（读 bot 清单 + 插件清单），`supervisor` 是执行器；`user_bot / create_bot / monitor_bot / security_bot` 四个内核角色为预置进程，M1 阶段 `user_bot` 常驻，其余三个自 M2 起常驻；插件侧 `xxx_bot / user_xxx_bot` 的 `desired_state` 来源是其注册的插件清单条目。
7. 内核侧四角色与 Bot 运行时均不涉及记忆：`memory_profile` 只是插件声明的字段，内核原样存储、不解释（架构总结 3.3.1 规则 6）。

## 4. 数据模型

### 4.1 表清单与归属

| schema | 表 | 用途 | 服务账号 |
| --- | --- | --- | --- |
| kernel | kernel_projects | 项目与设备绑定 | hub 运行账号 |
| kernel | bot_manifest / bots / bot_instances | Bot 制品、配置、运行实例 | hub 运行账号 |
| kernel | kernel_jobs / kernel_job_intents | 任务队列与副作用清单 | hub 运行账号 |
| kernel | kernel_event_outbox / kernel_event_subscriptions / kernel_event_dedup | 持久事件 | hub 运行账号 |
| kernel | kernel_plugins / kernel_plugin_grants / kernel_plugin_subs / kernel_plugin_event_registry | 插件身份 / 授权 / 订阅 / 事件注册表 | hub 运行账号 |
| kernel | kernel_ingress_log / kernel_context_snapshots / kernel_payloads | 入口审计、上下文快照与载荷引用 | hub 运行账号 |
| kernel | kernel_security_decisions | 插件 / Bot 安全审查（M2） | hub 运行账号 |
| kernel | kernel_health_reports | 健康报告原始证据（M2 由 monitor_bot 聚合） | hub 运行账号 |

memory / vault schema（`memory_entities / memory_relations / memory_entity_versions / memory_audit_log / memory_outbox / memory_external_refs`）属于 `plugin_memory`，**不在本 MVP 范围**，hub 账号无任何权限。

### 4.2 关键表 DDL 摘要

**kernel_jobs（kernel 库）**

```text
job_id            BIGINT PK
queue_name        VARCHAR(128)      # bot_id 或 consumer_group_id
job_type          VARCHAR(64)
status            queued / leasing / running / retry_backoff / suspended / done / dead / cancelled
envelope_id       VARCHAR(64)
bot_id            VARCHAR(64)
project_id        VARCHAR(64)       # 入口路由提示，非业务解析
partition_key     VARCHAR(128)
correlation_id    VARCHAR(64)       # 整链不变
causation_id      VARCHAR(64)
root_envelope_id  VARCHAR(64)
causal_path_json  JSON
traceparent       VARCHAR(128)
idempotency_key   VARCHAR(128) UNIQUE
parent_job_id     BIGINT
lease_token       VARCHAR(64)
lease_until       TIMESTAMP
attempt_count     INT
next_retry_at     TIMESTAMP
priority          INT
payload_hash      CHAR(64)
payload_ref       VARCHAR(512)
result_json       JSON
resume_decision   continue / replay / discard，默认 NULL   # 人工确认幂等
resume_by         VARCHAR(64) NULL
resumed_at        TIMESTAMP NULL
created_at / updated_at
INDEX(queue_name, status, next_retry_at, priority)
INDEX(correlation_id)
```

**kernel_job_intents（副作用清单，D-04 配套）**

```text
intent_id       BIGINT PK
job_id          BIGINT
seq             INT
tool            VARCHAR(64)          # bash_exec / git_push ...
request_digest  CHAR(64)
executed_at     TIMESTAMP NULL
status          planned / executing / done / failed / rolled_back
detail_json     JSON
```

**kernel_event_outbox / kernel_event_subscriptions / kernel_event_dedup / kernel_ingress_log / kernel_context_snapshots**：字段与架构总结 6.7 / 6.10 一致，不重复展开。

**kernel_payloads（载荷引用）**

```text
payload_id        VARCHAR(64) PK
envelope_id       VARCHAR(64)
storage_uri       VARCHAR(512)       # M1 用加密本地文件，后续对象存储
encryption_key_id VARCHAR(64)
content_hash      CHAR(64)
size_bytes        BIGINT
created_at / expires_at
```

### 4.3 账号边界

1. hub 运行账号只对 `kernel` schema 拥有 DML 权限，无 DDL 权限（DDL 由迁移账号执行）。
2. hub 无 `memory` / `vault` / 任何插件类型化表的任何权限；越权访问在数据库层被拒绝。
3. 迁移 CLI 与 hub 运行账号分离（`MYSQL_KERNEL_DSN` 与迁移 DSN 可不同）。

## 5. 接口契约

### 5.0 BotRuntime proto（骨架）

```text
service BotRuntime {
  rpc Register(RegisterRequest{bot_id, manifest_digest, capabilities[]}) returns (RegisterResponse{token, ttl});
  rpc Heartbeat(HeartbeatRequest{bot_id, job_lease_tokens[]}) returns (HeartbeatResponse);
  rpc StreamTasks(StreamTasksRequest{consumer_group}) returns (stream TaskAssignment);
  rpc AckTask(AckTaskRequest{job_id, lease_token, result_ref}) returns (AckTaskResponse);
  rpc SubscribeEvents(SubscribeRequest{event_types, scope}) returns (stream Event);
  rpc RenewCredential(RenewRequest{token}) returns (RenewResponse{token, ttl});
  rpc Deregister(DeregisterRequest{bot_id}) returns (DeregisterResponse);
}
```

`proto/botruntime.proto` 的字段以设计为准对齐（订阅请求补 `scope` 与 `request_id`，M1 实现前完成）。

### 5.1 replay 人工确认契约（用户决策）

```text
触发条件（任一）：
  a. lease 过期且任务含非幂等意图（kernel_job_intents.status != planned）
  b. DLQ replay 请求
  c. Agent yield-confirm 失败遗留

暂停为 status=suspended 加 reason，同时通过 HITL 渠道（MVP：CLI 命令 resume-list）展示：
  - job 元数据 + 因果链
  - 已执行副作用清单（intents 快照）
  - 三选一：continue / replay / discard

确认后由用户执行 resume-confirm <job_id> <decision>，
job 的 (resume_decision, resume_by, resumed_at) 落库并以 IngressEnvelope(actor=user) 记录审计。

例外：任务幂等（有 idempotency_key 且消费者声明幂等）时跳过人工步骤，自动 retry。
```

### 5.2 transport_frame / IngressEnvelope

字段与架构总结 5.1 / D-19 后的 6.10 完全一致。`IngressEnvelope` v1 为通讯头（`msg_type / from_bot_id / channel / channel_ref` + 因果链 + 幂等 + `payload_hash / payload_ref`）；业务维度（如 `project_id / conversation_id`）不再在信封内，随载荷走，接收 bot 拆封后自行拉取；实现以 Go struct + JSON（v1）承载，升级 protobuf 时字段名不变、类型对齐。

### 5.3 插件工具调用的内核视角

内核不代理任何插件工具（含 `plugin_memory.*`），只为插件提供总线身份与投递通道：

1. 插件进程与其 Bot 进程之间的工具调用是插件内部 / 进程间协议，内核不参与。
2. 插件事件必须由注册 bot 以“单次投递委托”形式回投 ingress：委托绑定 `from_bot_id / event_type / idempotency_key / correlation_id`，一次性使用。
3. 内核校验委托身份与因果链，不校验事件业务语义。

## 6. 关键流程

### 6.1 冒烟主链路（验收路径，纯内核）

```text
1. CLI（user_bot 的投递代理）发送用户信封（from_bot_id=user_bot, channel=cli, msg_type=request）
2. ingress 校验 → kernel_ingress_log(decision=accepted) → 入队 job[bot.echo]（queue_name=echo_bot）
3. scheduler 分发 → supervisor 发现无 ready 实例 → 拉 bot-runtime（echo_bot fixture，binary_digest 校验）
4. bot-runtime Register → StreamTasks → 收到后继任务（lease_token）
5. echo_bot 按任务类型连 loopback agent（TCP，transport_frame），执行 echo 工具
6. AckTask(job_id, lease_token, result_ref) → job done → Outbox 写 bot.reply 事件（from_bot_id=echo_bot）
7. Outbox dispatcher → event.deliver job → user_bot Consumer Group
8. CLI 订阅端收到 bot.reply；核对 kernel_ingress_log / kernel_jobs / kernel_job_intents /
   kernel_event_outbox / kernel_event_dedup 同 correlation_id 可查
```

说明：`echo_bot` 是本 MVP 的测试夹具（bot-runtime 内置 echo 模式 + 种子 manifest），不引入插件生命周期，不属于业务插件；M2 起由真实插件侧 Bot 替代。

### 6.2 懒加载启动流程

```text
queued 任务 → reconcile 检查 bot_instances(observed=dormant)
            → exec bot-runtime（binary_digest 校验）
            → start_timeout 内收到 Register 即 ready
            → 失败：任务回队列，退避计数自增
```

### 6.3 持久事件分发

```text
业务事务内 outbox(pending) → dispatcher 扫描 → 按授权订阅者生成 event.deliver job
→ 消费者处理 → kernel_event_dedup 幂等 → ack → outbox dispatched
```

### 6.4 DLQ replay 人工确认

```text
dead 任务 → resume-list 展示
→ 组装副作用清单（intents）
→ 用户选择 continue / replay（新 idempotency_key 重新入队）/ discard
→ status 迁移 + 审计落库
```

### 6.5 hub 崩溃恢复

```text
重启后：outbox pending 续扫；lease 过期任务 → suspended（人工确认）；
bot 进程失联由 supervisor 重新拉起；ingress 日志无损（同事务）。
```

### 6.6 插件事件回投（与记忆插件联调时验证，M3）

```text
plugin_memory 图谱事务提交（同事务写 memory_outbox）
→ 插件 dispatcher 申请 memory_bot 单次投递委托
→ 以 memory_bot 身份投递 IngressEnvelope（channel=internal）
→ hub ingress 校验（身份 / 因果链 / 幂等）→ kernel_event_outbox
→ 按订阅投递；重复回投由 idempotency_key 去重
```

内核不感知事件来自记忆插件还是普通业务插件；插件崩溃恢复只影响其自身（Outbox 续扫）。

### 6.7 健康报告（最小版）

```text
Bot / Agent / supervisor → channel=health 信封 → ingress
→ kernel_health_reports（M2 起由 monitor_bot 聚合并发布 bot.health.changed / agent.health.changed）
M1：仅 supervisor 检测进程退出与心跳超时，落 kernel_ingress_log + 事件；不做评分。
```

## 7. 非功能边界

1. **可观测**：所有日志带 `correlation_id`；hub 暴露 `/metrics`（队列深度、重试率、DLQ 率、Bot 启动延迟、租约过期率、Outbox 分发延迟）。
2. **安全**：hub 只有 `kernel` schema 账号；不能访问任何插件存储；传输层与 gRPC 凭证独立；入口校验失败也全量记录；快照加密 MVP 固定单密钥（可升级）。
3. **韧性**：hub 崩溃无损（同事务日志 + Outbox）；Bot 崩溃退避重启；Agent 失联由传输心跳与任务租约双通道检测。
4. **性能底线**：单实例单 dispatcher 吞吐目标 ≥200 jobs/s（SKIP LOCKED 实测基准）。

## 8. 验收标准

1. 冒烟链路 6.1 八步全部通过，五条链同 `correlation_id` 可查。
2. 待修正清单验收问题 1-4 与第 6 条可在本设计直接回答：
   - 问题 1：hub 重启后 yield_confirm / lease 任务转 suspended，人工确认幂等后继续。
   - 问题 2：三 Bot fan-out 三副本独立 Consumer Group，副本互不有序；重复由 idempotency_key 抑制。
   - 问题 3：lease 过期且 intents 非空 → suspended 人工确认；续租成功即继续。
   - 问题 4：DLQ replay 强制人工确认，副作用清单来自 intents 与 job 日志。
   - 问题 6：IngressEnvelope v1 由 6.8 / 6.10 字段集构成，所有入口过同一校验器（多入口用例测试）。
3. CHECK / NOT NULL 约束（kernel 表）、idempotency_key 唯一约束、日志同事务落库通过集成测试（含失败注入）。
4. 边界验收：hub 进程的 DSN 无法连接 `memory` / `vault`（库不存在或账号被拒）；hub 运行账号在数据库层无任何插件表权限；迁移 CLI 与运行账号分离验证通过；hub 崩溃恢复测试通过。
5. 失败注入五例：缺因果头 / 路径断裂 / 深度 32 / 根不一致 / 重复幂等键。

## 9. 与上游文档的映射

| 本文档章节 | 架构总结对应 |
| --- | --- |
| 3.1 store / 4.1 表清单 | 8.1 / 8.4 |
| 3.2 registry | 3.4 |
| 3.3 grpcd | 5.4 |
| 3.4 ingress | 6.10 |
| 3.5 scheduler | 6.4 / 6.5 / 6.6 与 D-04 |
| 3.6 outbox / 3.7 eventbus | 6.7 |
| 3.8 transport | 5.1 / D-14 |
| 3.9 supervisor + bot-runtime | 5.5 / D-17 / D-18 |
