# NexusCore 内核 MVP 详细设计

> 版本：v0.4（2026-11-13，PostgreSQL 内核化 + 配置中心，D-23）  
> 上游设计：`../NexusCore架构设计总结.md`（本文件是 Step 0 / Step 1 必须项的实现级详细设计）  
> 范围说明：本 MVP **只包含内核**（队列与事件、Bot 注册、运行时与配置、传输路由），不含任何记忆与知识能力；记忆由 knowledge 类插件 `plugin_memory` 承接（M3/M4），见 `../MemoryPlugin/MemoryPlugin详细设计.md`。  
> 状态：设计定稿，待实现

## 1. 目标与非目标

### 1.1 目标

1. hub 单二进制实现内核三原语：持久化队列与事件、插件注册与 Bot 运行时（含配置中心）、传输路由（架构总结 3.1）。
2. **PostgreSQL 作为唯一内核状态底座**：单 database `nexus` 的 `kernel` schema 承载运行表与配置中心；hub 只持有该 schema 的运行角色，不持有、不访问任何插件存储（记忆 / 金库 / 插件类型化表）。
3. bot-runtime 独立二进制实现 Bot 运行时，由内核按 lazy 模式拉起与回收（5.5）。
4. 所有消息带完整因果链并通过统一入口校验；接受 / 拒绝 / 重复三类结果全部落审计日志（6.10）。
5. 队列语义完整：幂等键、租约续期、`suspended` 状态、DLQ replay 人工确认幂等（D-04 全部语义并入本设计），以 PG `SKIP LOCKED` + `LISTEN/NOTIFY` 实现。
6. **配置中心最小闭环**：配置文档 / 版本 / 审批 / active 投影；所有配置写入必须经 `user_bot` 批准（可执行类先经 `security_bot` 预审）。
7. 冒烟链路端到端可验收（见 6.1）。

### 1.2 非目标（本 MVP 明确不做）

| 延后项 | 触发条件 |
| --- | --- |
| 记忆与知识能力（图谱、金库、晋升、上下文装配） | 全部属于 `plugin_memory`，M3 起落地；本 MVP 内核无任何相关代码与表 |
| 配置中心完整 CLI 与插件/Agent 配置管理 | M2；M1 只做文档/版本/active 投影与只读 API |
| 插件生命周期（install / security / upgrade / uninstall） | M2；本 MVP 只做 registry 的身份、授权与订阅基础 |
| **MCP / Skill** | 当前无运行时，不进入配置中心；后续另立设计 |
| Health Service 聚合评分 | 最小版仅心跳超时 + job 租约检测（monitor_bot 自 M2 起承担聚合） |
| resident Bot、`warm_pool` | 出现持续订阅型 Bot 时 |
| `promote`、跨 schema saga | M4，随记忆插件 |
| memory-worker（Context Assembler / Memory Arbiter） | M4，位于 `plugin_memory` 进程内 |
| UDP 传输、多 Bot 并发扩缩 | 需要遥测 / 并发时 |
| PM、Obsidian、IM、浏览器插件 | Step 2+ |
| 多节点部署、自动化证书管理 | 产品验证后 |

## 2. 部署形态

```text
┌──────────────────────┐        ┌────────────────────────────┐
│ hub（单 Go 二进制）   │        │ PostgreSQL 16+              │
│  ├─ store            │        │ database: nexus             │
│  ├─ registry + ACL   │◀──────▶│  ├─ schema kernel（hub）     │
│  ├─ config           │        │  ├─ schema memory（插件）    │
│  ├─ ingress          │        │  └─ schema vault（插件）     │
│  ├─ scheduler        │        └────────────────────────────┘
│  ├─ outbox/eventbus  │
│  ├─ transport        │        （memory / vault schema 属于
│  └─ supervisor       │          plugin_memory，M3 起部署，
└──────────────────────┘          内核角色无任何权限）
        │ supervisor ──exec──▶ bot-runtime（lazy 按需）
        │        ▲
        │        │ gRPC（Register / Heartbeat / StreamTasks / AckTask / SubscribeEvents）
        │        │
        │  agent-daemon（loopback / 远程，反向 TCP 接入 transport）
```

进程与端口：hub 监听 gRPC（BotRuntime / 管理接口）与 TCP 传输端口；bot-runtime 只主动出站连接 hub 与（按任务）Agent，不监听端口；agent-daemon 反向连接 hub 传输端口。hub 使用专用连接做 `LISTEN`；不能经过 PgBouncer 的 transaction pooling。

## 3. 模块详细设计

### 3.1 store（hub 运行存储）

**职责**：管理 PostgreSQL `kernel` schema 的连接池与事务；`pgx` 连接池（`pgxpool`），事务超时 5s。

**接口**：`WithKernelRuntimeTx(fn)`、`Listen(channel, handler)`。

**规则**：

1. hub 角色只授权 `kernel` schema（`kernel_*` 表），不持有其他 schema 的任何权限。
2. hub 只读取 `NEXUS_PG_KERNEL_DSN`；不持有、不读取任何插件 DSN（含记忆插件与金库）。
3. 所有租约 / 重试比较使用数据库时钟 `now()`；应用时钟只用于展示。
4. `LISTEN` 使用独立专用连接；断线自动重连并在重连后执行一次全量扫描补偿。

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

**职责**：5.0 的 `BotRuntime` 服务；证书为 operator 签发的自签 mTLS（MVP 允许 `--insecure-local` 仅回环）。

**方法**：`Register / Heartbeat / StreamTasks(流) / AckTask / SubscribeEvents(流) / RenewCredential / Deregister`。

**规则**：

1. `StreamTasks` 每条任务带 `lease_token`；`AckTask(job_id, lease_token, result)` 校验 token 一致才 ack，否则拒绝（防双 worker）。
2. `Heartbeat(bot_id, job_lease_tokens, config_version)` 兼作长任务租约续期与配置版本上报入口，间隔 10s；连续 3 次缺失即视同失联。
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

**职责**：D-04 全部语义的载体；PostgreSQL 持久队列。

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
3. 延迟调度：入队事务 `pg_notify('kernel_jobs_wakeup', queue_name)`；hub 监听后立即唤醒。**保留 5-10s 兜底轮询**（`status IN ('queued','retry_backoff') AND next_retry_at <= now()`），通知丢失不影响正确性。
4. 严格分区有序：`pg_try_advisory_xact_lock(hashtext(queue_name || ':' || partition_key))`；拿不到锁跳过该分区，拿到后检查同分区更早的非终态任务；宽松分区直接 `SKIP LOCKED`。
5. 人工确认幂等（用户决策）：任何暂停任务先组装副作用清单（`kernel_job_intents` + job 日志），通过 HITL 渠道请用户在 `continue / replay / discard` 三选一；决定与确认人写入 job 审计字段。已声明幂等（有 `idempotency_key` 且消费者声明幂等）的操作除外，仍自动重试。
6. 毒丸防护：同一 job 连续 N=5 次 `retry_backoff` 后直接 `dead`。
7. 分区有序：`conversation_id` 分区严格串行；其余分区并行。分区键来自入口方在 job 创建时携带的路由提示（架构总结 6.6），内核不解析载荷；未定稿前退化为 `queue_name` 级并行。
8. 认领使用 `UPDATE ... SET status='leasing', lease_token=..., lease_until=now()+interval '60 seconds' ... FROM (SELECT ... FOR UPDATE SKIP LOCKED) RETURNING ...`，单事务完成。
9. 运维：`kernel_jobs` 设 `fillfactor=80`；监控表膨胀与 autovacuum；必要时按月分区。

### 3.6 outbox（Outbox Dispatcher）

**规则**：

1. 状态 `pending -> dispatched -> archived`；全部分发完成后归档，不物理删除。
2. 每个授权 Consumer Group 生成一条 `event.deliver` job 复用 `kernel_jobs`。
3. 扫描间隔由 `kernel_config` 的 global 配置决定（默认 250ms 或 NOTIFY 唤醒），批 100 条。
4. 插件事件（如 `plugin_memory.*`）由插件本地 Outbox 经其 bot 投递委托回投 ingress 后进入本 Outbox，dispatcher 不直接读取插件库。

### 3.7 eventbus（事件总线）

**规则**：

1. MVP 只实现持久事件投递（经 outbox + event.deliver job），内存尽力而为通道可整体延后。
2. 消费侧幂等：先查 `kernel_event_dedup(consumer_group_id, event_id)`，命中则只更新状态。
3. 订阅在 Bot dormant 期间由内核代持，激活后续投。
4. `kernel.config.changed` 是持久事件：`{scope_type, scope_id, config_key, version, checksum}`；订阅方收到后主动拉取新版本，事件不是正确性来源。

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
4. bot-runtime 启动后立即 `Register`（携带 `config_version`），随后 `SubscribeEvents` + `StreamTasks` 循环；不持有任何本地长期状态。
5. 崩溃重启指数退避 1s / 2s / 4s…封顶 60s。
6. 决策 / 执行分离（D-20）：`desired_state` 的修改者**只有** `create_bot`（读配置中心 active 投影），`supervisor` 是执行器；`user_bot / create_bot / monitor_bot / security_bot` 四个内核角色为预置进程，M1 阶段 `user_bot` 常驻，其余三个自 M2 起常驻。
7. 内核侧四角色与 Bot 运行时均不涉及记忆：`memory_profile` 只是插件声明的字段，内核原样存储、不解释（架构总结 3.3.1 规则 6）。
8. **supervisor 的执行范围（M2 起）**：常规只 exec / kill **Bot 进程（L1）**。对**插件进程（L2）**只做两件事——强制终止（`CreateBotControl` 的 `action=kill_process`，经 create_bot 决策）与孤儿级联回收；插件进程的常规启动、优雅关闭与崩溃重启由 owner bot 执行。
9. **kill 前置校验**：任何针对插件进程的信号都必须先校验 `kernel_plugin_processes` 的 `pid + pid_start_time` 双匹配与 `pgid` 归属，并排除保留 PID；校验失败即拒绝执行并落审计。
10. 配置变更（`kernel.config.changed`）到达后：lazy Bot 下次启动自然加载；resident Bot 在下一租约边界重载可热更新的配置，manifest / 二进制级变更走 drain + restart；重载后上报新的 `config_version`。

### 3.10 config（配置中心，控制面模块）

**职责**：插件 / Bot / Agent / 全局配置的期望状态；版本化、审批、激活、审计、分发。

**状态机**：

```text
draft → proposed → [security_review] → approved → active → retired
                       └→ rejected
```

**接口（内部 ConfigControl + CLI）**：

```text
Propose(scope_type, scope_id, config_key, spec, base_version) -> proposal_id
Review(proposal_id)                       # security_bot；只对可执行类
Approve(proposal_id, checksum)            # 仅 user_bot；绑定 checksum
Activate(proposal_id)                     # 配置模块执行；事务内切 active + outbox
Reject(proposal_id, reason)
Get(scope_type, scope_id, config_key[, version]) -> spec, version, checksum
ListVersions(scope_type, scope_id, config_key)  -> []VersionSummary
Rollback(scope_type, scope_id, config_key, to_version)   # 生成提案，仍需批准
```

**规则**：

1. `scope_type ∈ {plugin, bot, agent, global}`；`scope_id` 为 plugin_id / bot_id / agent 节点 ID（global 为空）。
2. **配置写入唯一路径**：所有 proposal / approve / activate 都经本模块；插件、Bot、Agent 不得直接读写 `kernel_config_*` 表。
3. **审批**：只有 `user_bot` 能 Approve；`approved_checksum` 必须等于文档 checksum；审批后内容再变则批准失效。可执行类（`plugin` 涉及权限/工具、`agent` 涉及工具白名单）必须有 `security_bot` 的 `approved` 决定。
4. **版本**：按 `(scope_type, scope_id, config_key)` 单调递增；`base_version` 做乐观并发；每个 scope+config_key 同时只有一个 `active`（部分唯一索引）。
5. **激活事务**：旧 active → retired、新版本 → active、写 `kernel.config.changed` 到 `kernel_event_outbox`、写 `kernel_config_audit`，同一事务。
6. **投影**：`kernel_plugins` / `bot_manifest` / `kernel_agents` 的 desired 字段由激活事务更新（或由投影任务异步收敛），运行方只读投影；投影与文档的 `config_version` 必须一致。
7. **密钥**：spec 只允许 `secret_ref`（provider + locator），禁止明文；hub 不解析、不下发值。
8. **分发**：`Get` 按 scope ACL 返回 spec + version + checksum；读取行为审计。版本固定在 Bot 实例 / Agent 连接 / 插件进程，任务继承实例版本。
9. **漂移**：`monitor_bot` 对比上报的 `config_version` 与 active 版本，报 `config_version_drift`；不自动重载。
10. **MCP / Skill 不在本模块范围内**；任何把它们塞进 config 的改动视为越界。

实现级细节见 `../ConfigCenter/ConfigCenter详细设计.md`。

## 4. 数据模型

### 4.1 表清单与归属（PostgreSQL，database `nexus`）

| schema | 表 | 用途 | 服务角色 |
| --- | --- | --- | --- |
| kernel | kernel_projects | 项目与设备绑定 | hub 运行账号 |
| kernel | bot_manifest / bots / bot_instances | Bot 制品、配置投影、运行实例（含 config_version） | hub 运行账号 |
| kernel | kernel_jobs / kernel_job_intents | 任务队列与副作用清单 | hub 运行账号 |
| kernel | kernel_event_outbox / kernel_event_subscriptions / kernel_event_dedup | 持久事件 | hub 运行账号 |
| kernel | kernel_plugins / kernel_plugin_grants / kernel_plugin_subs / kernel_plugin_event_registry | 插件身份 / 授权 / 订阅 / 事件注册表（含 config_version 投影） | hub 运行账号 |
| kernel | kernel_ingress_log / kernel_context_snapshots / kernel_payloads | 入口审计、上下文快照与载荷引用 | hub 运行账号 |
| kernel | kernel_security_decisions | 插件 / Bot / 配置提案安全审查（M2） | hub 运行账号 |
| kernel | kernel_health_reports | 健康报告原始证据（M2 由 monitor_bot 聚合） | hub 运行账号 |
| kernel | kernel_plugin_processes | 插件进程 PID 登记与状态（M2；含 config_version） | hub 运行账号 |
| kernel | kernel_config_documents / kernel_config_audit / kernel_secret_refs | 配置中心：文档 / 版本 / 审批审计 / 密钥引用 | hub 运行账号 |

memory / vault schema（`memory_entities / memory_relations / memory_entity_versions / memory_audit_log / memory_outbox / memory_external_refs`）属于 `plugin_memory`，**不在本 MVP 范围**，hub 角色无任何权限。

### 4.2 关键表 DDL 摘要

**kernel_jobs**

```text
job_id            bigint generated always as identity PK
queue_name        text NOT NULL
job_type          text NOT NULL
status            text NOT NULL CHECK (status IN
                  ('queued','leasing','running','retry_backoff','suspended','done','dead','cancelled'))
envelope_id       text
bot_id            text
project_id        text
partition_key     text
correlation_id    text NOT NULL
causation_id      text
root_envelope_id  text NOT NULL
causal_path_json  jsonb
traceparent       text
idempotency_key   text UNIQUE
parent_job_id     bigint
lease_token       text
lease_until       timestamptz
attempt_count     int NOT NULL DEFAULT 0
next_retry_at     timestamptz
priority          int NOT NULL DEFAULT 0
payload_hash      char(64)
payload_ref       text
result_json       jsonb
config_version    bigint                # 领取时的 Bot 实例配置版本（审计）
resume_decision   text CHECK (resume_decision IN ('continue','replay','discard'))
resume_by         text
resumed_at        timestamptz
created_at / updated_at timestamptz NOT NULL DEFAULT now()
INDEX (queue_name, status, next_retry_at, priority)
INDEX (correlation_id)
```

**kernel_config_documents / kernel_config_audit / kernel_secret_refs** 详见 `../ConfigCenter/ConfigCenter详细设计.md` §3；关键约束：`UNIQUE(scope_type, scope_id, config_key, version)`、部分唯一索引保证每个 scope+config_key 仅一个 `active`、`approved_checksum` 非空才可激活。

**kernel_job_intents**：`intent_id bigint identity`、`job_id`、`seq`、`tool`、`request_digest char(64)`、`executed_at timestamptz`、`status`、`detail_json jsonb`。

**kernel_event_outbox / kernel_event_subscriptions / kernel_event_dedup / kernel_ingress_log / kernel_context_snapshots**：字段与架构总结 6.7 / 6.10 一致，类型 `jsonb` / `timestamptz`。

**kernel_payloads**：`payload_id text PK`、`envelope_id`、`storage_uri`、`encryption_key_id`、`content_hash`、`size_bytes bigint`、`created_at / expires_at timestamptz`。

### 4.3 角色与 schema 边界

1. 角色：`nexus_kernel`（hub 运行）、`nexus_migrator`（DDL）、`nexus_memory`（记忆插件）、`nexus_promotion`（`project_public` 写）、`nexus_vault`（金库）。各角色通过 `ALTER ROLE ... SET search_path` 固定自己的 schema；DSN 只指向 database `nexus`。
2. `nexus_kernel` 只 `USAGE kernel`，对 `memory / vault` 无任何 `USAGE`；验收用 `has_schema_privilege('nexus_kernel','memory','USAGE') = false`。
3. 迁移 CLI 使用 `nexus_migrator`（`NEXUS_PG_MIGRATE_DSN`，未设置时回退 `NEXUS_PG_KERNEL_DSN`）；运行角色无 DDL 权限。
4. `_migrations` 每 schema 独立；内核迁移由 `cmd/migrate -dir migrations/kernel` 执行；插件迁移由插件 install 流程执行（M2/M3）。

## 5. 接口契约

### 5.0 BotRuntime proto（骨架）

```text
service BotRuntime {
  rpc Register(RegisterRequest{bot_id, manifest_digest, capabilities[], config_version}) returns (RegisterResponse{token, ttl});
  rpc Heartbeat(HeartbeatRequest{bot_id, job_lease_tokens[], config_version}) returns (HeartbeatResponse);
  rpc StreamTasks(StreamTasksRequest{consumer_group}) returns (stream TaskAssignment);
  rpc AckTask(AckTaskRequest{job_id, lease_token, result_ref}) returns (AckTaskResponse);
  rpc SubscribeEvents(SubscribeRequest{event_types, scope}) returns (stream Event);
  rpc RenewCredential(RenewRequest{token}) returns (RenewResponse{token, ttl});
  rpc Deregister(DeregisterRequest{bot_id}) returns (DeregisterResponse);
}
```

`proto/botruntime.proto` 的字段以设计为准对齐（订阅请求补 `scope` 与 `request_id`，`Register/Heartbeat` 增 `config_version`；proto 变更按 AGENTS 流程确认后落地）。

### 5.1 ConfigControl（内部管理面）

```text
service ConfigControl {
  rpc Propose(ProposeRequest) returns (ProposeResponse);
  rpc Review(ReviewRequest) returns (ReviewResponse);      # security_bot
  rpc Approve(ApproveRequest) returns (ApproveResponse);   # 仅 user_bot
  rpc Activate(ActivateRequest) returns (ActivateResponse);
  rpc Get(GetRequest) returns (ConfigDocument);
  rpc ListVersions(ListVersionsRequest) returns (stream VersionSummary);
  rpc Rollback(RollbackRequest) returns (ProposeResponse);
}
```

约束：`Approve` 必须携带 `checksum` 且调用方 `from_bot_id=user_bot`；`Review` 仅 security_bot；`Activate` 校验批准与安全决定，事务内切换 active 并写 outbox。

### 5.2 replay 人工确认契约

沿用 v0.3：触发条件（lease 过期且含非幂等意图 / DLQ replay / yield-confirm 遗留）→ `suspended` → CLI `resume-list` 展示元数据、因果链与 intents 快照 → `resume-confirm <job_id> <decision>`；幂等任务自动重试。

### 5.3 transport_frame / IngressEnvelope

字段与架构总结 5.1 / D-19 后的 6.10 一致。`IngressEnvelope` v1 为通讯头，业务维度随载荷走；实现以 Go struct + JSON（v1）承载。配置版本**不进信封**：它属于运行时审计字段，落在 job / bot_instances / 进程登记行。

## 6. 关键流程

### 6.1 冒烟主链路（验收路径，纯内核）

```text
1. 迁移种子：kernel_config 写入 echo_bot 的 active bot 配置（v1）与 echo_bot manifest
2. CLI（user_bot 的投递代理）发送用户信封（from_bot_id=user_bot, channel=cli, msg_type=request）
3. ingress 校验 → kernel_ingress_log(decision=accepted) → 入队 job[bot.echo]（queue_name=echo_bot）
4. scheduler 认领 → supervisor 发现无 ready 实例 → 拉 bot-runtime（echo_bot fixture，binary_digest 校验）
5. bot-runtime Register（config_version=1）→ StreamTasks → 收到任务（lease_token）
6. echo_bot 按任务类型连 loopback agent（TCP，transport_frame），执行 echo 工具
7. AckTask(job_id, lease_token, result_ref) → job done → Outbox 写 bot.reply 事件（from_bot_id=echo_bot）
8. Outbox dispatcher → event.deliver job → user_bot Consumer Group
9. CLI 订阅端收到 bot.reply；核对 kernel_ingress_log / kernel_jobs / kernel_job_intents /
   kernel_event_outbox / kernel_event_dedup 同 correlation_id 可查
```

说明：`echo_bot` 是本 MVP 的测试夹具（bot-runtime 内置 echo 模式 + 种子 manifest + 种子配置），不引入插件生命周期。

### 6.2 配置提案与激活（M1 最小 / M2 完整）

```text
1. CLI 或已授权主体 Propose(scope_type, scope_id, config_key, spec, base_version) → 文档 status=proposed
2. 可执行类：security_bot Review → kernel_security_decisions(approved/rejected)
3. user_bot Approve(proposal_id, checksum)（CLI 交互确认；校验 approved_checksum）
4. Config 模块 Activate：旧 active→retired，新版本→active，写 kernel.config.changed + audit（同事务）
5. 订阅方（create_bot / owner bot / agent）收到事件 → 拉取新版本 → 按策略重载/重启
6. Bot/Agent/插件进程上报新 config_version；monitor_bot 对比 active 版本
```

### 6.3 配置变更分发与版本固定

```text
Bot 实例启动/重载 → 固定 config_version（bot_instances）
任务领取 → 继承实例版本 → job.config_version（审计）
配置热更新 → 下一租约边界重载；manifest/二进制变更 → drain + restart
Agent 连接 → Register 携带 config_version；漂移上报
```

### 6.4 懒加载启动流程

```text
queued 任务 → reconcile 检查 bot_instances(observed=dormant)
            → 读取 active bot 配置 / manifest 投影
            → exec bot-runtime（binary_digest 校验）
            → start_timeout 内收到 Register 即 ready
            → 失败：任务回队列，退避计数自增
```

### 6.5 持久事件分发

```text
业务事务内 outbox(pending) → dispatcher 扫描 → 按授权订阅者生成 event.deliver job
→ 消费者处理 → kernel_event_dedup 幂等 → ack → outbox dispatched
```

### 6.6 DLQ replay 人工确认

```text
dead 任务 → resume-list 展示 → 组装副作用清单（intents）
→ 用户选择 continue / replay（新 idempotency_key 重新入队）/ discard
→ status 迁移 + 审计落库
```

### 6.7 健康报告（最小版）

```text
Bot / Agent / supervisor → channel=health 信封 → ingress
→ kernel_health_reports（M2 起由 monitor_bot 聚合并发布 bot.health.changed / agent.health.changed）
M1：仅 supervisor 检测进程退出与心跳超时，落 kernel_ingress_log + 事件；不做评分。
M2：owner bot 上报插件进程状态；monitor_bot 观测 config_version_drift。
```

### 6.8 hub 崩溃恢复

```text
重启后：outbox pending 续扫；LISTEN 重连后全量扫描一次；
lease 过期任务 → suspended（人工确认）；bot 进程失联由 supervisor 重新拉起；
ingress 日志无损（同事务）。
```

## 7. 非功能边界

1. **可观测**：所有日志带 `correlation_id`；hub 暴露 `/metrics`（队列深度、重试率、DLQ 率、Bot 启动延迟、租约过期率、Outbox 分发延迟、配置待审批数、配置漂移数）。
2. **安全**：hub 只有 `kernel` schema 角色；不能访问任何插件存储；配置写入必须经审批（user_bot）与预审（可执行类）；secret 只存引用；传输层与 gRPC 凭证独立；入口校验失败也全量记录；快照加密 MVP 固定单密钥（可升级）。
3. **韧性**：PG 单实例是当前单点（个人部署可接受）——MVP 以备份与快速恢复兜底；hub 崩溃无损（同事务日志 + Outbox）；NOTIFY 丢失由兜底轮询补偿；Bot 崩溃退避重启；Agent 失联由传输心跳与任务租约双通道检测。
4. **性能底线**：单实例单 dispatcher 吞吐目标 ≥200 jobs/s（PG SKIP LOCKED 实测基准）；`kernel_jobs` 需监控膨胀并调 autovacuum。

## 8. 验收标准

1. 冒烟链路 6.1 九步全部通过，五条链同 `correlation_id` 可查；队列在**无 NOTIFY**（人为丢弃通知）时仍能靠兜底轮询完成。
2. 待修正清单验收问题可在本设计直接回答：§5 历史验收问题 1-4（人工确认、fan-out、租约、replay，随 D-04 归档）与 §4 第 2 条的信封校验部分（`IngressEnvelope` 已冻结；业务载荷 schema 属 D-12）。
3. PG 约束与集成测试：`idempotency_key` 唯一、`status` CHECK、部分唯一 active 索引、`approved_checksum` 激活校验、日志同事务落库；失败注入五例。
4. 边界验收：`nexus_kernel` 角色对 `memory` / `vault` 无 USAGE；迁移角色与运行角色分离；hub 崩溃恢复测试通过。
5. 配置中心验收（M1 最小 / M2 完整）：提案不生效；无 user_bot 批准不能 active；checksum 变更后批准失效；可执行类缺 security_bot 决定不能批准；激活后 `kernel.config.changed` 可订阅；非授权主体读不到其他 scope 的配置；secret 值不出现在任何文档 / 审计中。
6. 明确排除项：仓库与运行表中不存在 MCP / Skill 配置类型或运行时。

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
| 3.10 config | 3.1 / 6.11 / D-23 |

## 10. 修订记录

### 10.1 M1 实现落地（2026-11-13）

M1 已实现并验收。以下为落地时相对本设计的偏离与细化，按「实现与设计偏离当天回写」原则记录。

**1. 入口适配器用 hub HTTP 网关，不新增 gRPC RPC。**

§5.0 的七个 RPC 是 **Bot↔Kernel 控制面**，不含 user_bot 侧入口（信封投递、载荷拉取、DLQ 人工确认、凭证签发）。新增 RPC 需按 AGENTS 流程确认，M1 不改 proto 契约，改由 hub HTTP 网关承载这些入口，内部复用与 gRPC 完全相同的模块与校验：

| 入口 | 承载 | 说明 |
| --- | --- | --- |
| `POST /v1/ingress` | `ingress.Submit` | 信封投递；路由提示（`bot_id / queue_name / job_type`）由适配器携带，**不进信封** |
| `GET /v1/payload/{ref}` | `ingress.FetchPayload` | 接收 bot 自行拉取；授权来自其收到的投递关系，不做上下文水合推送 |
| `GET /v1/jobs/suspended`、`POST /v1/jobs/{id}/resume`、`POST /v1/jobs/{id}/replay` | `scheduler` | §5.2 人工确认三选一 |
| `POST /v1/credentials` | `registry` | 凭证签发 / 续期 |
| `GET /metrics`、`GET /healthz` | hub | §7.1 可观测 |
| `POST /v1/dev/fixture` | hub | **仅 M1 夹具**：绑定 echo_bot 制品摘要、登记 loopback agent；M2 由插件安装 + 安全审查替代 |

§5.1 的 ConfigControl 在 M1 以 `hub/config` 模块的 Go 接口 + CLI 承载，未起独立 gRPC 服务；是否暴露为 RPC 留待 M2 决定。

**2. `bots` 与 `bot_manifest` 职责切分以 ConfigCenter §6 为准。**

`bot_manifest` = 不可变制品 + 配置投影（`binary_path / binary_digest / activation_mode / idle_ttl_seconds / start_timeout_seconds / model / prompt_ref / config_version / config_doc_id`）；`bots` = 生命周期（`desired_state / desired_by / observed_state / enabled`）。`desired_state` 只由 `create_bot` 写（D-20），supervisor 只读不改。

**3. `kernel_plugin_subs.plugin_id` 可空。**

内核侧 Consumer Group（`user_bot`）无插件归属，故 `plugin_id` 可空，并以 `CHECK (plugin_id IS NOT NULL OR bot_id IS NOT NULL)` 保证至少一个归属主体。

**4. 租约过期判定口径（§5.2 细化）。**

有 `idempotency_key` **且无 `executed` 意图** → `retry_backoff` 自动重试；否则 → `suspended / lease_expired` 等人工确认。已执行副作用绝不自动重放。判定全部用数据库时钟 `now()`。

**5. `event.deliver` 不补造 `causal_path`。**

outbox 只持久化 `correlation_id / causation_id / root_envelope_id`，祖先路径未随事件落库，故投递 job 的 `causal_path` 留空而非伪造节点（硬约束 12：内核不替调用方补造历史）。

**6. 流式订阅的 ack 必须脱离流上下文。**

`SubscribeEvents` 在 `stream.Send` 成功后，去重登记与任务 ack 使用 `context.WithoutCancel` + 超时执行。订阅端收到即断开是常态（CLI `-wait`），若沿用 `stream.Context()`，取消会让 ack 失败、任务泄漏在 `leasing` 并被反复重投。

**7. proto 生成路径。**

`buf.yaml` 的 `modules.path=proto`、`buf.gen.yaml` 的 `out=proto/gen/v1`，与 `go_package` 一致。生成物已提交，普通构建不需要 buf / protoc。

**8. 集成测试隔离。**

每个测试包使用独立 database（`nexus_test_<包名>`），因为 `go test ./...` 并行执行包，共用一个库会让各包清理互相抹掉在途数据。开发夹具的迁移角色因此需要 `CREATEDB`（**生产部署不应授予**）。

### 10.2 M1 验收结果（第 8 节六条）

| 验收条 | 结果 | 证据 |
| --- | --- | --- |
| 1 九步链路 + 无 NOTIFY 兜底 | 通过 | `make smoke-m1`（`scripts/smoke-m1.sh`）：五条链同 `correlation_id` 可查，第二条链路走兜底轮询 |
| 2 验收问题（人工确认 / fan-out / 租约 / replay / 信封校验） | 通过 | 人工确认 `TestResumeConfirmedDecisions`；fan-out `TestDispatchCreatesJobPerGroup`；租约 `TestRenewLease` / `TestConcurrentLeaseNoDoubleDelivery`；replay `TestReplayRequiresConfirmation`；信封校验 `internal/envelope` 全量 |
| 3 PG 约束 + 失败注入五例 | 通过 | 唯一幂等键 / status CHECK / 部分唯一 active 索引 / `approved_checksum` 校验 / 日志同事务；五例见 `hub/ingress/ingress_test.go`（缺因果头、路径断裂、深度 32、根不一致、重复幂等键） |
| 4 边界 + 崩溃恢复 | 通过 | `nexus_kernel` 对 `memory` / `vault` 无 USAGE；迁移与运行角色分离；`make drill-crash-m1`（`scripts/drill-crash-m1.sh`）真实 SIGKILL 注入 hub 崩溃 / Bot 失联 / LISTEN 断连，全部恢复且无重复副作用；模块级见 `hub/recovery_test.go` |
| 5 配置中心验收 | 通过 | `hub/config/config_test.go`：提案不生效、非 user_bot 不能批准、checksum 变更批准失效、可执行类缺安全决定不能批准、`kernel.config.changed` 可订阅、读取 ACL、secret 值不落文档与审计 |
| 6 排除 MCP / Skill | 通过 | 冒烟脚本静态扫描 + 运行表 `scope_type` 白名单校验 |

`go test ./...`（真库、`-count=1`）全绿；`go vet ./...` 无警告。
