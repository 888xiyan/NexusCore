# NexusCore 已修正问题归档

> 评审时间：2026-11-05  
> 评审对象：`doc/NexusCore架构设计总结.md`（当前仓库尚无实现代码，本评审针对设计文档）  
> 评审视角：消息队列 / 中间件工程语义（投递保证、幂等、租约、因果序、崩溃恢复）  
> 状态：归档于 2026-11-13，共 21 项已修正（D-01 至 D-10、D-13 至 D-23）  
> 未解决的问题见 `NexusCore待修正问题清单.md`

## 1. 已修正总览

| 编号 | 等级 | 问题 | 对应章节 | 修正日期 |
| --- | --- | --- | --- | --- |
| D-01 | P0 | 微内核声明与第 4 节架构图矛盾，Arbiter/Assembler 属强业务组件 | 3.1 / 4 / 7.1 / 7.2 | 2026-11-07 |
| D-02 | P0 | 事件总线投递语义未定义，失效事件丢失会静默破坏一致性 | 3.1 / 6.1 / 7.5 | 2026-11-07 |
| D-03 | P0 | 失效事件被当成正确性机制，但运行中任务无法可靠消费它 | 6.2 / 7.5 | 2026-11-07 |
| D-04 | P0 | 队列缺少幂等键、租约续期、挂起状态与安全的 DLQ replay | 5.2 / 6.4 / 6.5 | 2026-11-13 |
| D-05 | P0 | 跨队列因果链无 parent 关联与 happens-before 定义 | 6.1 / 6.6 | 2026-11-07 |
| D-06 | P1 | kernel_entities 是 EAV god-table，PM 任务状态与知识资产共表 | 8.1 / 8.2 | 2026-11-07 |
| D-07 | P1 | NULL 编码记忆象限，数据库无法守护公共记忆写路径 | 7.3 / 8.3 | 2026-11-07 |
| D-08 | P1 | kernel_relations 无并发控制，多 Bot 并发改边会交错 | 7.4 / 8.2 | 2026-11-07 |
| D-09 | P1 | Obsidian 双向同步排在 Step 2，冲突策略却仍是待定问题 | 11 / 12 | 2026-11-07 |
| D-10 | P1 | 插件信任边界缺失：无插件认证、无事件载荷 ACL、Agent 静态 token | 3.2 / 12 | 2026-11-07 |
| D-13 | P1 | 金库缺物理隔离，存储分层与网关契约未定义 | 8.1 / 8.3 / 8.4 | 2026-11-07 |
| D-14 | P1 | Bot-Agent 传输层与应用层耦合，内核强制 JSON-RPC/WebSocket | 5.1 / 5.2 / 11 | 2026-11-07 |
| D-15 | P1 | Bot↔Kernel 控制面与健康报告链路未定义 | 5.4 / 6.9 / 11 | 2026-11-07 |
| D-16 | P0 | 内核入口缺少统一因果链与上下文日志 | 6.8 / 6.10 / 11 | 2026-11-07 |
| D-17 | P1 | Bot 运行形态与懒加载未定义（内嵌/常驻/独立二进制） | 3.3 / 5.5 / 11 | 2026-11-07 |
| D-18 | P1 | Bot 类型与长连接策略未区分（lazy / resident） | 5.5 / 6.2 / 6.9 | 2026-11-07 |
| D-19 | P1 | 信封混装业务语义；用户未入总线，非 bot 主体可直接投递 | 3.3 / 6.1 / 6.10 | 2026-11-08 |
| D-20 | P1 | Bot 未分内核侧 / 插件侧角色体系，工厂 / 监督 / 安全角色缺失 | 3.3.1 / 5.5 / 11 | 2026-11-10 |
| D-21 | P0 | 知识访问与图谱存储仍被归入 hub 原语，进程与数据库账号边界未拆分 | 3.1 / 4 / 8.4 / 11 | 2026-11-11 |
| D-22 | P0 | 记忆仍以内核数据面形态存在，内核与内核侧 Bot 被迫携带记忆职责 | 1 / 3.1 / 7 / 8 / 13 | 2026-11-12 |
| D-23 | P0 | 状态底座与配置分散：MySQL 队列、manifest/环境变量散落，缺少统一配置来源与审批 | 3.1 / 6.5 / 6.11 / 8 | 2026-11-13 |

## 2. 修正要点速览

1. **内核边界（D-01 / D-14 / D-15 / D-21 / D-22）**：Arbiter/Assembler/深度审核外置；记忆与知识存储归 knowledge 类插件 `plugin_memory`（D-22）；hub 收敛为队列、Bot 注册与运行时、传输路由三类原语；Bot↔Agent 只传 TCP/UDP 帧，Bot↔Kernel 走 gRPC 控制面。
2. **事件语义（D-02 / D-03）**：持久事件与尽力而为事件二分，Outbox + Consumer Group at-least-once；invalidation 只是提示，正确性由提交时版本检查唯一承担。
3. **因果与追溯（D-05 / D-16）**：`correlation_id` / `causation_id` / `root_envelope_id` / `causal_path` / `traceparent` 全链路强制；所有入口统一 `IngressEnvelope` 校验，拒绝也记录完整上下文与加密快照。
4. **存储模型（D-06 / D-07 / D-08 / D-13 / D-21 / D-22）**：图谱只存知识域；象限用显式 `visibility` + 双 schema CHECK + 账号隔离；边集走 `edge_epoch` 同事务提交；金库与记忆 schema 统一由 `plugin_memory` 的工具面访问，内核只持 `kernel` schema 运行表账号。
5. **同步策略（D-09）**：Obsidian 放弃双向同步，改为单向发布 + File Watcher 变更摄取，冲突走三方合并与人工审查。
6. **Bot 运行（D-17 / D-18）**：Bot 是内核创建、监督、回收的独立二进制；分 lazy（按需、空闲回收、`agent_connection=per_task`）与 resident（常驻、持久长连接）两类，内核按 `activation_mode` reconcile。
7. **角色体系（D-20）**：Bot 分内核侧四角色（user_bot 预置 / create_bot 工厂 / monitor_bot 监督 / security_bot 安全与热更新治理）与插件侧 xxx_bot / user_xxx_bot；命名即角色，保留名禁用；拉起决策在 create_bot、执行在内核 supervisor。
8. **记忆插件化（D-22）**：记忆与知识能力完整归 knowledge 类插件 `plugin_memory`；内核与内核侧四角色不感知记忆；内核 schema 与插件 schema 分离；Health 聚合改由 monitor_bot；插件事件经 `memory_bot` 投递委托回投；图谱表更名 `memory_*`；M1 内核 MVP 变为纯内核。
9. **PostgreSQL + 配置中心（D-23）**：PostgreSQL 成为内核唯一状态底座（单 database + 三 schema；SKIP LOCKED + LISTEN/NOTIFY + 兜底轮询）；配置中心作为 hub 控制面模块统一保存 plugin / bot / agent / global 四类期望配置，proposal → security → user_bot 批准 → active；只存 secret_ref；MCP / Skill 明确不在范围。
10. **队列四语义（D-04）**：`idempotency_key` 唯一约束 + 租约续期 + 持久化 `suspended` + 人工确认幂等 replay（continue / replay / discard）；副作用先落 `kernel_job_intents` 再执行，租约过期时按"有无已执行意图"决定自动重试还是挂起；M1 已在真库落地并验收。

## 3. 详细修正记录

### D-01（P0）微内核声明与总体结构矛盾

**问题**：架构总结把“审核管道”和“记忆仲裁与上下文装配”画在 Kernel 子图内，但二者是强业务感知组件，与 3.1“内核不理解任何具体业务”直接冲突。

**影响**：hub 会逐步吸收记忆与审核的业务逻辑，退回巨石单体；插件边界失去公信力。

**修正（2026-11-07）**：按方案 1 执行——Context Assembler 与 Memory Arbiter 归入 memory-worker 认知服务，深度审核与执行前审核策略归入安全审核插件；hub 收敛为存储、队列、路由、注册四类原语，只保留“晋升队列入队 / 版本检查 / 失效事件投递”等原语。定位声明、内核排除清单、架构图、monorepo 注释、5.2 上下文水合归属、6.1/6.3 审核职责与第 13 节结论同步。

### D-02（P0）事件总线投递语义未定义

**问题**：事件是否持久化、订阅者离线时事件去向、消费 ack、重复投递均未定义；记忆一致性却依赖事件广播。

**影响**：hub 重启或插件断连时失效事件丢失，其他 Bot 静默基于过期公共记忆决策。

**修正（2026-11-07）**：新增 6.7 定义事件二分模型：正确性事件全部经 Transactional Outbox + 持久化 Job Table 按 Consumer Group 投递（at-least-once、显式 ack、`event_id` 幂等去重），UI 事件走内存通道。`kernel_event_outbox / kernel_event_subscriptions / kernel_event_dedup` 表随此冻结；7.3/7.5/8.3/9.2 的发布措辞同步为持久事件。

### D-03（P0）失效事件被赋予做不到的正确性职责

**问题**：运行中的 LLM 任务无法中断消费 invalidation，各 Consumer Group 消费顺序也不一致，失效事件物理上无法成为正确性机制。

**影响**：事件迟到、乱序、丢失三类场景导致无法复现的记忆冲突。

**修正（2026-11-07）**：7.5 明确降级语义——正确性唯一来源是提交时乐观锁版本检查（7.4）+ 冲突合并；invalidation 仅用于新任务拉最新记忆与 UI 提示；进行中任务不承诺感知失效。正确性分工表已入文档。

### D-04（P0）队列缺少幂等、租约续期与挂起状态

**问题**：`kernel_jobs` 只有 `lease_until / attempt_count`，缺幂等键、租约续期、持久化挂起状态与安全 replay；ack 丢失会重复回复、长任务租约过期会被第二 worker 抢走并双重执行非幂等副作用、yield_confirm 挂起随 hub 重启丢失、毒丸任务卡死队列。

**影响**：数据库队列最经典的翻车点：重复回复、双重执行、挂起任务丢失、毒丸卡死。

**修正（2026-11-13，M1 落地）**：语义先并入内核 MVP 详设 §3.5 / §5.2，M1 完成实现与真库验收：

1. `kernel_jobs.idempotency_key` UNIQUE；`EnqueueTxIfAbsent` 用 `ON CONFLICT DO NOTHING` 避免重复入队把事务打成 aborted。
2. 租约续期 `RenewLease`；`lease_token` 与 `AckTask` 绑定防双 worker；所有租约比较用数据库时钟 `now()`。
3. `suspended` 是持久化 job 状态（不是连接状态），跨 hub 重启保留，`resume-list` 仍可见。
4. 副作用先落 `kernel_job_intents`（`UNIQUE(job_id, seq)`）再执行；租约过期时**有 `idempotency_key` 且无 `executed` 意图**才自动 `retry_backoff`，否则 `suspended / lease_expired` 等人工确认，绝不自动重放已执行副作用。
5. 人工确认三选一 `continue / replay / discard`，必须携带确认人与因果上下文；`replay` 生成新幂等键重新入队。DDL 以 CHECK 强制 `resume_decision` 非空时必须有 `resume_by / resumed_at`，`suspended` 必须有 `suspend_reason`。
6. 毒丸 N=5 熔断进 `dead`；严格分区用 `pg_try_advisory_xact_lock` + 队头检查保证不越位；`LISTEN/NOTIFY` 唤醒 + 5-10s 兜底轮询（通知丢失不阻塞）。

**证据**：`hub/scheduler/scheduler_test.go`（四语义 + 分区有序 + 并发认领 + 兜底轮询 + 重启保留）、`hub/recovery_test.go`（跨重启租约过期判定）、`make smoke-m1`、`make drill-crash-m1`（真实 SIGKILL 下无重复副作用）。详见内核 MVP 详设 §10.2。

### D-05（P0）跨队列因果链无定义

**问题**：Ingress → Bot → Agent 三级接力没有 parent 关联与 happens-before；跨 key 因果顺序未定义；fan-out 后有序范围不清。

**影响**：无法回答“Agent 任务由哪条用户消息触发”，跨队列归因断裂。

**修正（2026-11-07）**：新增 6.8 因果链——`correlation_id`（业务根因）/ `causation_id`（直接原因）/ `traceparent` 及传播规则与示例；6.6 收敛为最小可序承诺（仅同 partition_key + 同 Consumer Group 内有序，跨队列只可追溯不保序）；`kernel_jobs` 与 outbox 补追踪字段；DLQ 追踪键改为 `correlation_id`。

### D-06（P1）统一图谱表退化为 EAV god-table

**问题**：PM 任务状态机、笔记、记忆、摘录共用 `kernel_entities + metadata JSON`；namespace 用 ENUM。

**影响**：无法按 namespace 施加约束；一次表变更影响所有业务；任务状态机缺数据库级保护。

**修正（2026-11-07）**：图谱只存知识域；8.1 增加数据归属表与 PM 类型化表（`pm_tasks` / `pm_task_dependencies`，含 `source_entity_id` 引用）；namespace 改 VARCHAR + 注册表校验；metadata JSON 限定展示性字段；8.2 移除 `blocks` 关系，任务依赖归 PM 自表；“万物皆节点”表述同步收敛。

### D-07（P1）NULL 编码记忆象限，写路径不变量无存储层守护

**问题**：`owner_bot_id / project_id` 的 NULL 组合编码象限，“Bot 直写公共记忆”只能靠应用层自觉。

**影响**：核心资产缺少强制写路径，不属于公共记忆的写操作可静默污染。

**修正（2026-11-07）**：`scope` 列由显式 `visibility`（global / bot_private / project_public / ephemeral）取代；8.3 改为“记忆象限与数据层校验”，CHECK 约束强制组合自洽（并强制 `visibility NOT NULL`，避免 UNKNOWN 放行）；visibility 优先索引；`project_public` 写权限仅授予晋升队列消费者专用账号；7.3/7.4/第 7 节同步去除 NULL 推断。

### D-08（P1）关系边表缺少并发控制

**问题**：实体有乐观锁但 `kernel_relations` 没有 version/epoch，多 Bot 并发改边集会交错。

**影响**：图谱结构静默损坏，实体乐观锁无法感知边冲突。

**修正（2026-11-07）**：`kernel_entities` 新增 `edge_epoch`；7.4 边集变更先乐观递增源实体 `edge_epoch`（影响行数为 0 即放弃），再在同一事务完成新边插入与旧边 `valid_to` 失效；双向边两端同校验；并发写策略表新增“关系边”行。

### D-09（P1）Obsidian 双向同步先落地、冲突策略后决策

**问题**：Step 2 要做双向同步，12.6 的多端一致性却是待定项；重命名、删除、wikilink 反向维护均无策略。

**影响**：记忆层继承未定义的同步语义，返工成本极高。

**修正（2026-11-07）**：新增 10.3——DB 为 source of truth，`.md` 是幂等单向投影；外部修改由 File Watcher 摄取为 `ContextEnvelope`（含上次同步基线 hash）走 Ingress 队列，以基线三方合并；非重叠自动合并、冲突人工审查；重命名保实体身份、删除进入待确认；wikilink 反向边在 `edge_epoch` 保护下由库侧维护。Step 2 与 12.6 记为已决策。

### D-10（P1）插件与 Agent 信任边界缺失

**问题**：无插件认证、无事件载荷 ACL、Agent 静态 `auth_token`、审核权威归属不清。

**影响**：被攻陷插件可读取全部记忆；被攻陷 Agent 可跨设备执行命令。

**修正（2026-11-07）**：新增 3.4——Manifest 只声明不授权，插件安装由用户签发可轮换凭证；订阅按 `namespace / project / bot` 三维授权，未批准不生效；Dispatcher 投递前做载荷 ACL（失败重试，不降级全量）；`kernel_event_subscriptions` 增加 `plugin_id / scope_filter / payload_policy / granted_by_user`；Agent 改可轮换凭证（mTLS 或短期 token）并绑定能力与项目范围；高危权威定为 Agent 侧护栏最终放行 + hub 策略审核。

### D-13（P1）金库缺物理隔离，存储分层与网关契约未定义

**问题**：个人笔记、全局公理、项目记忆、Bot 记忆共库，仅靠 visibility 与账号隔离；跨库引用、一致性、网关可用性均未定义。

**影响**：个人知识处于项目数据同一 blast radius；ACL 无统一执行点。

**修正（2026-11-07）**：新增 8.4 存储分层与 Memory Gateway——金库库独立承载个人笔记与全局公理，凭据只授予网关；记忆库由 memory service 单点持有项目与 Bot 记忆，业务插件只保存 `entity_id` 引用；冻结 read / search / promote / subscribe / audit / health 六组 API（禁止 Bot 执行 SQL）；`entity_id` 全局 ULID/UUID；跨库写入走 saga + Outbox，`kernel_external_refs` 存单向引用；金库不可用降级为无个人上下文；8.1 双 schema 双部署，8.3 拆两份 CHECK（金库仅 `global`，记忆库仅其余三种）。MVP 用同一实例两 schema，后续迁独立实例不改 API。

### D-14（P1）Bot-Agent 传输层与应用层耦合

**问题**：JSON-RPC 2.0 over WebSocket 被写成内核契约，所有 Agent 插件被迫使用同一应用层协议，UDP 也无法支持。

**影响**：新协议与遥测都要改内核；插件被单一协议锁死。

**修正（2026-11-07）**：内核只提供传输层（TCP/UDP 会话、反向连接、鉴权、多路复用、心跳、背压、重连、限流）；应用层协议由插件 Manifest 声明（`transport / protocol_id / version / framing / capabilities`），内核按 `protocol_id` 路由，不解析 `payload`；最小 `transport_frame` 冻结；UDP 由插件自担可靠性与分片；协议错误只关闭该插件的 stream 或会话。JSON-RPC/WebSocket 降级为可选插件实现。

### D-15（P1）Bot↔Kernel 控制面与健康报告链路未定义

**问题**：Bot 如何注册、心跳、拉取任务、订阅事件、上报故障均未定义；Agent 故障处理散落各 Bot。

**影响**：接入只能靠隐式约定；故障视图碎片化，无法全局熔断。

**修正（2026-11-07）**：新增 5.4 Bot↔Kernel gRPC 控制面——`BotRuntime` 最小方法集（Register / Heartbeat / StreamTasks / AckTask / SubscribeEvents / RenewCredential / Deregister），任务与事件至少一次投递，`AckTask` + `request_id` 幂等；执行数据不进控制面。新增 6.9 健康报告链路——Bot/Agent/Supervisor → Gateway Health Service（去重、聚合、评分）→ 内核（策略校验、状态迁移、Outbox）→ 订阅授权的事件广播；报告携带 `correlation_id` / `causation_id` / `idempotency_key`；内核是状态迁移权威，单条报告不能直接驱逐 Agent；Bot 自身停摆由内核心跳与租约检测。

### D-16（P0）内核入口缺少统一因果链与上下文日志

**问题**：各入口（用户 `@bot`、IM、VS Code、浏览器、Bot gRPC、Agent 健康报告）没有统一强制信封；日志未要求保存完整决策上下文。

**影响**：跨入口链路断裂，事故后无法还原决策现场，也没有取证依据。

**修正（2026-11-07）**：新增 6.10——所有入口统一 `IngressEnvelope` 并强制 `correlation_id` / `causation_id` / `root_envelope_id` / `causal_path`（深度 ≤ 32）/ `traceparent` 等；缺失或不合法直接拒绝并记录 `decision=rejected`；append-only `kernel_ingress_log` 记录接受、拒绝、重复三种结果；完整原始上下文写入加密 `kernel_context_snapshots`（脱敏 token/Cookie，个人内容按密钥加密），日志记 `context_snapshot_id` + `content_hash`；保留与访问策略明确且查询行为本身审计。平台事件（Register / Heartbeat / RenewCredential）以 `actor_type=system` 自建根节点。

### D-17（P1）Bot 运行形态与懒加载未定义

**问题**：Bot 是内嵌协程、常驻进程还是独立二进制未声明；谁创建、监督、启动、回收均未定义。

**影响**：内嵌 Bot 崩溃会带崩 hub；无懒加载与 supervisor 时崩溃重启、任务接管与版本升级不可控。

**修正（2026-11-07）**：Bot 是内核创建、监督的独立二进制（可签名校验 `bot_manifest`）；生命周期 registered → dormant → starting → ready → busy → draining → stopped / crashed；启动超时 `start_timeout`、崩溃退避与熔断规则明确；任务先入队再触发，starting 期间保持 queued，只有 ready/busy 能 lease；dormant 事件内核持久化；升级用版本固定 + drain + 切换；Bot 不持有长期状态，重启后从 memory service 与 job 队列恢复。

### D-18（P1）Bot 类型与长连接策略未区分

**问题**：懒加载与常驻 Bot 被同一策略处理，长连接归属未定义。

**影响**：交互式 Bot 频繁冷启动或常驻 Bot 被错误回收；Agent 故障检测信号对两类 Bot 都不适用。

**修正（2026-11-07）**：5.5 改为双类型——`activation_mode: lazy|resident`、`agent_connection: per_task|persistent`。Lazy：按需激活、`idle_ttl` 回收、不持 Bot-Agent 长连接（任务短连接结束即关闭）；Resident：内核启动或用户启用时创建、不因空闲回收、持有长连接（断线由 supervisor 重建）。内核维护 `desired_state / observed_state` 并 reconcile 调度。resident 长连接断开是 Agent 故障辅助信号；lazy 靠 Agent 心跳、任务租约与执行超时判定。长连接仍经 Agent Mesh，不绕过 ACL、因果链与审计。

### D-19（P1）信封混装业务语义，主体模型缺统一席位

**问题**：原 `IngressEnvelope` 同时承载业务维度（`project_id / bot_id / conversation_id / target_type / actor_type` 等）与通讯维度；用户未入总线（用户走插件通道、Agent 走插件协议），非 bot 主体可直接向内核投递。

**影响**：信封变相成为业务 schema，内核被迫理解业务字段，回退到旧模式"内核做上下文水合"；多插件各自塞业务字段导致无法归因，跨入口一致性被破坏。

**修正（2026-11-08）**：3.3 增加用户席位与总线统一规则——用户以 `user_bot`（常驻席位）入总线，客户端插件只是其投递 / 展示代理，**只有 bot 能投递与消费消息**，内核是各 bot 间的消息通道（每 bot 一个 Consumer Group 即邮箱）。6.10 信封收敛为通讯语义：`msg_type / from_bot_id / channel / channel_ref` + 因果链 + 幂等 + `payload_hash / payload_ref`；业务维度随载荷走，由来源插件定义业务信封；接收 bot 拆封后按 `payload_ref` 自行拉取（拉取授权来自其收到的信封 + 3.4 ACL）；内核不水合、不解析业务字段。kernel_ingress_log 记录字段同步为通讯语义；13 节补充信封边界不可侵蚀。

### D-20（P1）Bot 未分内核侧 / 插件侧角色体系

**问题**：D-17 / D-18 只定义了进程形态与类型，未回答谁创建业务 Bot、谁监督存续、插件如何注册自己的 Bot、热更新由谁把关。

**影响**：插件侧 Bot 成为绕过内核的旁路；多插件环境的注册与热更新无统一治理。

**修正（2026-11-10）**：新增架构总结 3.3.1——内核侧四角色（`user_bot` 用户席位预置常驻 / `create_bot` 读 Bot 与插件清单决定拉起 / `monitor_bot` 监督各 Bot 存续 / `security_bot` 负责插件注册与热更新治理）+ 插件侧业务 Bot（插件至少注册一个 `xxx_bot`，用户建设的命名 `user_xxx_bot`）；内核是唯一执行器（supervisor），`create_bot` 是决策者；保留名禁用；3.1 原语 3、Step 1、内核 MVP 详设 §3.10、开发规划 M2 同步。

### D-21（P0）知识数据面未从内核拆出

**问题**：架构虽声明 Memory Gateway 是独立数据面，但 3.1 仍把“知识访问与图谱存储”列为 hub 原语，8.4 又允许 MVP 先作为 hub 内模块，导致进程边界、DSN 边界与事务边界可能被开发便利击穿。

**影响**：hub 继续持有图谱凭据并吸收知识业务；金库隔离退化为逻辑声明；图谱事务与内核 Outbox 强耦合，后续独立部署或换存储成本线性上升。

**修正（2026-11-11）**：新增独立 Go 二进制 `memory-gateway`；hub 收敛为队列 / 事件、Bot 注册与运行时、传输路由三类原语。vault DSN 与 memory 图谱表账号只授予 gateway，hub 仅持有内核运行表账号；gateway 维护 `gateway_audit_log` 与双 schema `gateway_outbox`，图谱事务与本地 Outbox 同事务提交，再通过调用方 bot 的单次投递委托把事件回投 hub 统一入口。架构总结、内核 MVP 详设、开发规划、README 与 AGENTS 同步。

### D-22（P0）记忆仍以内核数据面形态存在

**问题**：D-21 虽把知识访问拆出 hub，但记忆仍以“内存数据面（memory-gateway）”的特殊形态存在：内核文档、内核 MVP 范围、双 schema 数据模型、Health Service、Bot 清单字段都围绕记忆组织，内核与内核侧四角色实际携带了记忆职责与凭据边界，微内核“记忆可替换 / 可停用”的性质没有成立。

**影响**：任何记忆模型调整都会反向冲击内核文档与实现；内核侧与插件侧的边界模糊，删除记忆能力不再是纯粹卸载一个插件。

**修正（2026-11-12）**：记忆即插件（用户决策）——所有记忆与知识能力（四象限记忆、个人金库、知识图谱、版本与晋升、Context Assembler、Memory Arbiter、用户级 Bot 个人记忆）完整归 knowledge 类插件 `plugin_memory`；内核与内核侧四角色不感知、不持有、不装配任何记忆；内核 schema（`kernel`）与插件 schema（`memory` / `vault`）分离，hub 只持 `kernel` 账号；Health 聚合改由 `monitor_bot`；插件事件经其 `memory_bot` 的单次投递委托回投；图谱表更名 `memory_*`；新增 `doc/MemoryPlugin/` 详设；M1 内核 MVP 收敛为纯内核（冒烟改用 `echo_bot` 夹具）。架构总结、内核 / Bot / Plugin 详设、开发规划、AGENTS、README 与代码骨架（`plugins/plugin_memory`、`migrations` 目录）同步。

### D-23（P0）状态底座与配置来源分散

**问题**：内核队列建在 MySQL 上、运行时配置散落在 manifest / 环境变量 / 插件自管文件中；插件、Bot、Agent 的运行配置没有统一来源、版本与审批，改动不可追溯、不可回滚，也无法约束"谁能改配置"。

**影响**：配置漂移与双真相无法收敛；DB 队列缺少可靠的唤醒与顺序原语；单人环境下仍要维护多套配置入口。

**修正（2026-11-13）**：PostgreSQL 成为内核唯一状态底座——单 database `nexus`，`kernel / memory / vault` 三 schema 与角色隔离；队列使用 `SKIP LOCKED` 认领、`LISTEN/NOTIFY` 唤醒 + 5-10s 兜底轮询、严格分区 advisory lock。新增配置中心模块（不是第四原语）：`plugin / bot / agent / global` 四类期望配置，`proposal → security_bot 预审（可执行类）→ user_bot 批准（绑定 checksum）→ active`，版本化、可回滚，只存 `secret_ref`。内核侧四角色职责同步：user_bot 批准、create_bot 读 active 投影、monitor_bot 观测配置漂移、security_bot 预审。**MCP / Skill 不纳入配置中心**（无运行时，未来另立设计）。新增 `doc/ConfigCenter/` 详设；架构总结、内核 / Bot / Plugin / Memory 详设、开发规划、AGENTS、README 同步。
