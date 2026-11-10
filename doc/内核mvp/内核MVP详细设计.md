# NexusCore 内核 MVP 详细设计

> 版本：v0.1（2026-11-08）  
> 上游设计：`../NexusCore架构设计总结.md`（本文件是 Step 0 / Step 1 必须项的实现级详细设计）  
> 状态：设计定稿，待实现

## 1. 目标与非目标

### 1.1 目标

1. hub 单二进制实现内核四原语：知识访问、持久化队列与事件、插件注册、传输路由（架构总结 3.1）。
2. bot-runtime 独立二进制实现 Bot 运行时，由内核按 lazy 模式拉起与回收（5.5）。
3. 单 MySQL 实例承载金库库 / 记忆库双 schema、双凭据（8.4）。
4. 所有消息带完整因果链并通过统一入口校验；接受 / 拒绝 / 重复三类结果全部落审计日志（6.10）。
5. 队列语义完整：幂等键、租约续期、`suspended` 状态、DLQ replay 人工确认幂等（D-04 全部语义并入本设计）。
6. 冒烟链路端到端可验收（见 6.1）。

### 1.2 非目标（本 MVP 明确不做）

| 延后项 | 触发条件 |
| --- | --- |
| Health Service 聚合评分 | Step 1（最小版仅心跳超时 + job 租约检测） |
| resident Bot、`warm_pool` | 出现持续订阅型 Bot 时 |
| 跨库 saga、`promote`、`kernel_external_refs` | 记忆晋升启用时（Step 5） |
| memory-worker（Context Assembler / Memory Arbiter）外部进程 | Step 3 后；MVP 允许以库内空实现占位 |
| UDP 传输、多 Bot 并发扩缩 | 需要遥测 / 并发时 |
| embedding ANN（D-11）、向量检索 | 语义检索启用前，`search` 先退化为标题 / 关键词匹配 |
| PM、Obsidian、IM、浏览器插件 | Step 2+ |
| 多节点部署、自动化证书管理 | 产品验证后 |

## 2. 部署形态

```text
┌────────────────────────────┐
│ hub（单 Go 二进制）         │
│  ├─ store                  │
│  ├─ gateway(MemoryGateway) │
│  ├─ registry + ACL         │
│  ├─ grpcd(BotRuntime)      │
│  ├─ ingress                │
│  ├─ scheduler + outbox     │
│  ├─ eventbus               │
│  ├─ transport              │
│  └─ supervisor             │──exec──▶ bot-runtime（lazy 按需）
└────────────┬───────────────┘
             │ 双 schema / 双凭据
      ┌──────▼──────┐
      │ MySQL 单实例 │
      │  vault 库    │
      │  memory 库   │
      └─────────────┘
```

进程与端口：hub 监听 gRPC（BotRuntime / 管理接口）与 TCP 传输端口；bot-runtime 只主动出站连接 hub，不监听端口。

## 3. 模块详细设计

各模块按"职责 / 接口 / 规则"描述；所有跨模块调用走 hub 内部接口，模块间不直接共享数据库连接对象。

### 3.1 store（存储接入）

**职责**：管理 `vault` / `memory` 两个 schema 的连接池与事务；禁止跨 schema 同事务。

**接口**：`WithVaultTx(fn)`、`WithMemoryTx(fn)`。

**规则**：

1. 两套 DSN 凭据独立；进程崩溃后凭据不落盘明文（env 注入）。
2. 金库连接池仅供 gateway 使用，其他模块拿不到金库句柄（编译期隔离：金库 Pool 类型不导出）。
3. 事务超时 5s，超时整体回滚。

### 3.2 gateway（Memory Gateway 模块）

**职责**：知识读写唯一入口；按调用方 ACL 过滤；按 `visibility` 路由。

**接口**（MVP 子集）：

```text
GatewayRead(entity_id)                    -> Entity
GatewaySearch(query, scope, limit)        -> []EntityRef   # MVP 关键词 / 标题匹配
GatewayCreate(source_actor, entity_draft) -> Entity        # 仅 web_clip 等允许写路径
```

**规则**：

1. `promote / audit / health` 为 Step 5 / Step 1 占位接口，MVP 返回 NotImplemented。
2. 每次调用校验 actor 的 `project_id` / `namespace` 授权；无授权拒绝并写 `kernel_ingress_log`。
3. 金库库只读（MVP 无用户直写 UI；写入等 Step 1 工作台）。
4. 结果条数上限 100，禁止全量扫描。
5. 读走快照读（MVCC），写走乐观锁（实体 `version`）。

### 3.3 registry（插件注册与 ACL）

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
2. Manifest 未授权的声明（订阅 / 工具 / namespace）一律拒绝。
3. 授权唯一批准人：MVP 为 CLI 管理命令（`approve-plugin`），工作台后置。

### 3.4 grpcd（BotRuntime gRPC 服务）

**职责**：5.4 的 `BotRuntime` 服务；证书为 operator 签发的自签 mTLS（MVP 允许 `--insecure-local` 仅回环）。

**方法**：`Register / Heartbeat / StreamTasks(流) / AckTask / SubscribeEvents(流) / RenewCredential / Deregister`。

**规则**：

1. `StreamTasks` 每条任务带 `lease_token`；`AckTask(job_id, lease_token, result)` 校验 token 一致才 ack，否则拒绝（防双 worker）。
2. `Heartbeat(bot_id, job_lease_tokens)` 兼作长任务租约续期入口，间隔 10s；连续 3 次缺失即视同失联。
3. 所有 RPC 必须携带因果上下文元数据（6.8 字段），缺失返回 `CAUSAL_CONTEXT_REQUIRED`。
4. 请求 / 响应统一走 ingress 模块校验与日志。

### 3.5 ingress（入口校验与审计）

**职责**：所有入口消息过 `IngressEnvelope` 校验；写 `kernel_ingress_log` + 加密上下文快照。

**规则**：

1. 校验顺序：schema_version → 必填字段 → 凭证 → `causal_path` 连续性 / 深度 ≤ 32 → 幂等键去重。
2. 接受 / 拒绝 / 重复三种结果都在与接收同事务内写日志。
3. 快照加密：MVP 用单把用户密钥 AES-GCM，key 由 env 注入；字段级脱敏规则内嵌（token / cookie / key 名单替换为 `<redacted:field>` 引用）。
4. 快照保留期默认 30 天，日志永久保留（MVP；正式策略可调）。
5. 信封只承载通讯语义（按 D-19 后的 6.10 字段集：`msg_type / from_bot_id / channel / channel_ref` + 因果 + 幂等 + `payload_hash / payload_ref`）。入口不解析业务载荷；接收 bot 拆封后按 `payload_ref` 自行从 Gateway / 快照存储拉取，读取授权由其已收到的信封绑定。

### 3.6 scheduler（Job 调度器）

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
2. lease 基础 60s；`Heartbeat` 每 10s 续；过期 → 状态置 `lease_expired`，非幂等任务转 `suspended` 等人工确认。
3. 延迟调度：MVP 用 250ms 轮询 `queued / retry_backoff AND next_retry_at <= NOW()`；进程内有新任务时经 channel 立即唤醒。
4. 人工确认幂等（用户决策）：任何暂停任务先组装副作用清单（`kernel_job_intents` + job 日志），通过 HITL 渠道请用户在 `continue / replay / discard` 三选一；决定与确认人写入 job 审计字段。已声明幂等（有 `idempotency_key` 且消费者声明幂等）的操作除外，仍自动重试。
5. 毒丸防护：同一 job 连续 N=5 次 `retry_backoff` 后直接 `dead`。
6. 分区有序：`conversation_id` 分区严格串行（不越过前序未终态任务）；其余分区并行。

### 3.7 outbox（Outbox Dispatcher）

**规则**：

1. 状态 `pending -> dispatched -> archived`；全部分发完成后归档，不物理删除。
2. 每个授权 Consumer Group 生成一条 `event.deliver` job 复用 `kernel_jobs`。
3. 扫描间隔 250ms，批 100 条。

### 3.8 eventbus（事件总线）

**规则**：

1. MVP 只实现持久事件投递（经 outbox + event.deliver job），内存尽力而为通道可整体延后。
2. 消费侧幂等：先查 `kernel_event_dedup(consumer_group_id, event_id)`，命中则只更新状态。
3. 订阅在 Bot dormant 期间由内核代持，激活后续投。

### 3.9 transport（传输路由）

**规则**：

1. MVP：反向 TCP。Agent 主动连 hub 传输端口，第一帧为鉴权帧（Agent 凭证）。
2. `transport_frame` 校验：`length ≤ 4MB`、`protocol_id` 已授权、`stream_id` 已注册；违规关闭会话并写审计。
3. 心跳 15s；丢 2 次判连接失联，本连接所有 stream 按协议错误关闭。
4. UDP 完全延后。

### 3.10 supervisor + bot-runtime（Bot 二进制）

**职责**：内核按 manifest 拉起 / 监督 / 回收 Bot 进程。

**规则**：

1. 制品校验：`binary_digest`（sha256）必须与 manifest 一致，不匹配拒绝执行。
2. MVP 只支持 `activation_mode=lazy`：首个 queued 任务或事件触发 `exec`，`idle_ttl`（默认 5 分钟）无任务则进程自退出；`desired_state / observed_state` reconcile 周期 2s。
3. `start_timeout` 默认 30s；超时任务回队列，连续 3 次启动失败熔断该 bot_id 并上报 `bot_stalled`。
4. bot-runtime 启动后立即 `Register`，随后 `SubscribeEvents` + `StreamTasks` 循环；不持有任何本地长期状态。
5. 崩溃重启指数退避 1s / 2s / 4s…封顶 60s。
6. 决策 / 执行分离（D-20）：`desired_state` 的修改者**只有** `create_bot`（读 bot 清单 + 插件清单），`supervisor` 是执行器；`user_bot` / `create_bot` / `monitor_bot` / `security_bot` 四个内核角色为预置进程，MVP 阶段 `user_bot` 常驻，其余三个在 Step 1 起常驻；插件侧 `xxx_bot / user_xxx_bot` 的 `desired_state` 来源是其注册的插件清单条目。

## 4. 数据模型

### 4.1 表清单与归属

| schema | 表 | 用途 |
| --- | --- | --- |
| vault | kernel_entities / kernel_relations | 金库图谱（仅 `visibility=global`） |
| memory | kernel_entities / kernel_relations | 记忆图谱 |
| memory | kernel_projects | 项目与设备绑定 |
| memory | bot_manifest / bots / bot_instances | Bot 制品、配置、运行实例 |
| memory | kernel_jobs / kernel_job_intents | 任务队列与副作用清单 |
| memory | kernel_event_outbox / kernel_event_dedup | 持久事件 |
| memory | kernel_plugins / kernel_plugin_grants / kernel_plugin_subs | 插件身份 / 授权 / 订阅 |
| memory | kernel_ingress_log / kernel_context_snapshots | 入口审计与上下文快照 |

### 4.2 关键表 DDL 摘要

**kernel_jobs（memory 库）**

```text
job_id            BIGINT PK
queue_name        VARCHAR(128)      # bot_id 或 consumer_group_id
job_type          VARCHAR(64)
status            queued / leasing / running / retry_backoff / suspended / done / dead / cancelled
envelope_id       VARCHAR(64)
bot_id            VARCHAR(64)
project_id        VARCHAR(64)
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

**kernel_event_outbox / kernel_event_dedup / kernel_ingress_log / kernel_context_snapshots**：字段与架构总结 6.7 / 6.10 一致，不重复展开。

**kernel_entities（通用列，双 schema 同构）**

```text
entity_id       VARCHAR(36) PK        # ULID
namespace       VARCHAR(32)           # 注册表校验
owner_bot_id    VARCHAR(64)
project_id      VARCHAR(64)
visibility      VARCHAR(16) NOT NULL  # CHECK 双库不同（见 4.3）
title, content, metadata_json, version, edge_epoch, updated_by_bot_id, updated_at
embedding       BLOB                  # MVP 不启用（D-11），仅保留列
```

### 4.3 CHECK 约束

```sql
-- vault.kernel_entities
CHECK (visibility = 'global' AND owner_bot_id IS NULL AND project_id IS NULL)

-- memory.kernel_entities
CHECK (visibility IN ('bot_private','project_public','ephemeral')
       AND NOT (visibility='bot_private'    AND (owner_bot_id IS NULL     OR project_id IS NOT NULL))
       AND NOT (visibility='project_public' AND (owner_bot_id IS NOT NULL OR project_id IS NULL))
       AND NOT (visibility='ephemeral'      AND (owner_bot_id IS NULL     OR project_id IS NULL)))
```

## 5. 接口契约

### 5.1 BotRuntime proto（骨架）

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

### 5.2 replay 人工确认契约（用户决策）

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

### 5.3 transport_frame / IngressEnvelope

字段与架构总结 5.1 / D-19 后的 6.10 完全一致。`IngressEnvelope` v1 为通讯头（`msg_type / from_bot_id / channel / channel_ref` + 因果链 + 幂等 + `payload_hash / payload_ref`）；业务维度（如 `project_id / conversation_id`）不再在信封内，随载荷走，接收 bot 拆封后自行拉取；实现以 Go struct + JSON（v1）承载，升级 protobuf 时字段名不变、类型对齐。

## 6. 关键流程

### 6.1 冒烟主链路（验收路径）

```text
1. CLI（user_bot 的投递代理）发送用户信封（from_bot_id=user_bot, channel=cli）
2. ingress 校验 → kernel_ingress_log(decision=accepted) → 入队 job[ingress]
3. scheduler 分发 → BotRuntime.StreamTasks 推送
4. supervisor 发现无 ready 实例 → 拉 bot-runtime → Register → lease
5. Bot 按任务类型连 loopback agent（TCP），执行 echo 工具
6. AckTask → job done → Outbox 写 bot.reply 事件
7. event.deliver job → CLI 订阅端收 bot.reply
8. 核对：kernel_ingress_log / jobs / intents / outbox 四条链同 correlation_id
```

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

## 7. 非功能边界

1. **可观测**：所有日志带 `correlation_id`；hub 暴露 `/metrics`（队列深度、重试率、DLQ 率、Bot 启动延迟、租约过期率）。
2. **安全**：两库凭据分离、金库句柄仅 gateway 可用；传输层与 gRPC 凭证独立；入口校验失败也全量记录；快照加密 MVP 固定单密钥（可升级）。
3. **韧性**：hub 崩溃无损（同事务日志 + Outbox）；Bot 崩溃退避重启；Agent 失联由传输心跳与任务租约双通道检测。
4. **性能底线**：单实例单 dispatcher 吞吐目标 ≥200 jobs/s（SKIP LOCKED 实测基准）；50ms 检索目标降级为待压测假设（D-11）。

## 8. 验收标准

1. 冒烟链路 6.1 八步全部通过，四条链同 `correlation_id` 可查。
2. 待修正清单验收问题 1-4 与第 6 条可在本设计直接回答：
   - 问题 1：hub 重启后 yield_confirm / lease 任务转 suspended，人工确认幂等后继续。
   - 问题 2：三 Bot fan-out 三副本独立 Consumer Group，副本互不有序；重复由 idempotency_key 抑制。
   - 问题 3：lease 过期且 intents 非空 → suspended 人工确认；续租成功即继续。
   - 问题 4：DLQ replay 强制人工确认，副作用清单来自 intents 与 job 日志。
   - 问题 6：ContextEnvelope v1 由 6.8 / 6.10 字段集构成，所有入口过同一校验器（多入口用例测试）。
3. CHECK / NOT NULL 约束、idempotency_key 唯一约束、日志同事务落库通过集成测试（含失败注入）。

## 9. 与上游文档的映射

| 本文档章节 | 架构总结对应 |
| --- | --- |
| 3.1 store / 4.1 表清单 | 8.1 / 8.3 / 8.4 |
| 3.2 gateway | 8.4 |
| 3.3 registry | 3.4 |
| 3.4 grpcd | 5.4 |
| 3.5 ingress | 6.10 |
| 3.6 scheduler | 6.4 / 6.5 / 6.6 与 D-04 |
| 3.7 outbox / 3.8 eventbus | 6.7 |
| 3.9 transport | 5.1 / D-14 |
| 3.10 supervisor + bot-runtime | 5.5 / D-17 / D-18 |
