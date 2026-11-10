# NexusCore 设计缺陷评审

> 评审时间：2026-11-05  
> 评审对象：`doc/NexusCore架构设计总结.md`（当前仓库尚无实现代码，本评审针对设计文档）  
> 评审视角：消息队列 / 中间件工程语义（投递保证、幂等、租约、因果序、崩溃恢复）  
> 状态：已修正 D-01 至 D-03、D-05 至 D-10、D-13 至 D-20（2026-11-10）；待修正 D-04、D-11、D-12

## 1. 评审结论

概念层设计基本合格：脑手分离、输出双层蒸馏、四象限记忆、晋升队列、提交时乐观锁，这些边界划分清晰。主要问题集中在中间件工程语义层：

1. **微内核声明与总体结构自相矛盾**，强业务组件被画进内核。
2. **事件总线投递语义未定义**，而记忆一致性模型却依赖事件广播。
3. **队列缺少幂等键、租约续期和挂起状态**，长任务与崩溃恢复场景会双重执行副作用。
4. **跨队列因果链无定义**，多级队列接力后顺序与归因断裂。
5. **统一图谱表退化成 EAV god-table**，任务状态机与知识资产共表。
6. **个人知识与项目数据共库**，金库缺少物理隔离与统一访问网关。
7. **Bot-Agent 协议绑定 JSON-RPC/WebSocket**，插件无法定义自己的应用层协议。
8. **Bot↔Kernel 控制面与故障报告链路未定义**，Agent 故障处理散落在各 Bot。
9. **内核入口缺少统一因果链与上下文日志**，跨入口链路无法完整还原。
10. **Bot 运行形态与懒加载未定义**，无法确定崩溃隔离、资源占用与启动时机。
11. **Bot 类型与长连接策略未区分**，lazy 与 resident 被同一策略处理。
12. **信封混装业务语义，主体模型缺统一席位**，插件 / 用户 / bot 三种投递主题并存。

## 2. 问题总览

| 编号 | 等级 | 问题 | 对应章节 | 状态 |
| --- | --- | --- | --- | --- |
| D-01 | P0 | 微内核声明与第 4 节架构图矛盾，Arbiter/Assembler 属强业务组件 | 3.1 / 4 / 7.1 / 7.2 | 已修正（2026-11-07） |
| D-02 | P0 | 事件总线投递语义未定义，失效事件丢失会静默破坏一致性 | 3.1 / 6.1 / 7.5 | 已修正（2026-11-07） |
| D-03 | P0 | 失效事件被当成正确性机制，但运行中任务无法可靠消费它 | 6.2 / 7.5 | 已修正（2026-11-07） |
| D-04 | P0 | 队列缺少幂等键、租约续期、挂起状态与安全的 DLQ replay | 5.2 / 6.4 / 6.5 | 待修正 |
| D-05 | P0 | 跨队列因果链无 parent 关联与 happens-before 定义 | 6.1 / 6.6 | 已修正（2026-11-07） |
| D-06 | P1 | kernel_entities 是 EAV god-table，PM 任务状态与知识资产共表 | 8.1 / 8.2 | 已修正（2026-11-07） |
| D-07 | P1 | NULL 编码记忆象限，数据库无法守护公共记忆写路径 | 7.3 / 8.3 | 已修正（2026-11-07） |
| D-08 | P1 | kernel_relations 无并发控制，多 Bot 并发改边会交错 | 7.4 / 8.2 | 已修正（2026-11-07） |
| D-09 | P1 | Obsidian 双向同步排在 Step 2，冲突策略却仍是待定问题 | 11 / 12 | 已修正（2026-11-07） |
| D-10 | P1 | 插件信任边界缺失：无插件认证、无事件载荷 ACL、Agent 静态 token | 3.2 / 12 | 已修正（2026-11-07） |
| D-11 | P2 | snapshot_json 冗余且无 GC；MySQL VECTOR 无 ANN 索引方案 | 7.1 / 7.3 / 8.1 | 待修正 |
| D-12 | P2 | 单人工作台按五个部署单元的分布式系统落地；信封契约未定稿 | 4 / 6.1 | 待修正 |
| D-13 | P1 | 金库缺物理隔离，存储分层与网关契约未定义 | 8.1 / 8.3 / 8.4 | 已修正（2026-11-07） |
| D-14 | P1 | Bot-Agent 传输层与应用层耦合，内核强制 JSON-RPC/WebSocket | 5.1 / 5.2 / 11 | 已修正（2026-11-07） |
| D-15 | P1 | Bot↔Kernel 控制面与健康报告链路未定义 | 5.4 / 6.9 / 11 | 已修正（2026-11-07） |
| D-16 | P0 | 内核入口缺少统一因果链与上下文日志 | 6.8 / 6.10 / 11 | 已修正（2026-11-07） |
| D-17 | P1 | Bot 运行形态与懒加载未定义（内嵌/常驻/独立二进制） | 3.3 / 5.5 / 11 | 已修正（2026-11-07） |
| D-18 | P1 | Bot 类型与长连接策略未区分（lazy / resident） | 5.5 / 6.2 / 6.9 | 已修正（2026-11-07） |
| D-19 | P1 | 信封混装业务语义；用户未入总线，非 bot 主体可直接投递 | 3.3 / 6.1 / 6.10 | 已修正（2026-11-08） |
| D-20 | P1 | Bot 未分内核侧 / 插件侧角色体系，工厂 / 监督 / 安全角色缺失 | 3.3.1 / 5.5 / 11 | 已修正（2026-11-10） |

## 3. 详细问题

### D-01（P0）微内核声明与总体结构矛盾

**问题**：架构总结第 4 节（约 74-82 行）把“审核管道（AU）”和“记忆仲裁与上下文装配（M）”画在 `NexusCore Kernel / Hub` 子图内。但 Memory Arbiter 需要判断“什么是项目事实、什么是 Bot 经验、什么直接清理”（7.2），Context Assembler 需要理解项目图谱、任务状态与近期跨端输入（7.1），二者都是强业务感知组件，与 3.1“内核不理解任何具体业务”的声明直接冲突。

**影响**：如果不解决，hub 会逐步吸收记忆与审核的业务逻辑，最终退回它声称要避免的巨石单体；插件边界也会失去公信力。

**建议**：二选一并写入文档：

1. 把 Context Assembler、Memory Arbiter、深度审核策略移出内核，归入 memory-worker 或独立插件；hub 只保留存储、队列、路由、注册四类原语。
2. 修改微内核声明，明确承认“认知协调”属于内核职责，并说明为什么它不会引发巨石化。

推荐方案 1：hub 保留“晋升队列入队 / 版本检查 / 失效事件投递”等原语，判断逻辑全部外置。

**修正记录（2026-11-07）**：已按方案 1 更新《NexusCore架构设计总结》：第 1 节定位声明、3.1 节内核排除清单、第 4 节架构图与 monorepo 注释、5.2 节上下文水合归属、6.1/6.3 节审核职责、第 7 节组件运行位置、第 13 节结论均已同步；文档末尾新增第 14 节修订记录。

### D-02（P0）事件总线投递语义未定义

**问题**：3.1 把“事件、消息与持久化任务队列”合并为一个原语，但从未定义事件本身的投递保证：事件是否持久化？订阅者离线时事件是否保留？事件消费是否需要 ack？重复投递如何处理？7.5 的多 Bot 一致性依赖 `project.memory.updated / project.memory.invalidated` 广播，而第二份原始笔记中的事件总线是“基于 WebSocket / gRPC 的发布订阅中心”，属于内存投递。

**影响**：如果 hub 重启或插件进程断连时失效事件丢失，其他 Bot 会静默基于过期公共记忆继续决策，整个 7.3/7.5 的一致性模型在最常见的故障场景下失效。

**建议**：明确二选一，不允许默认：

1. **持久事件**：所有正确性相关事件全部通过 Transactional Outbox + 持久化 Job Table 投递，订阅按 Consumer Group 记录消费位点，支持重投与 ack。
2. **尽力而为事件**：明确声明事件仅为 UI 提示与优化信号，不参与任何正确性保证；一致性完全依赖提交时版本检查。

推荐方案 1 用于 `project.memory.*`、`task.state.*` 等正确性事件，UI 刷新类事件可用方案 2。

**修正记录（2026-11-07）**：已按方案 1 更新《NexusCore架构设计总结》：新增 6.7 节，定义事件二分模型（持久事件 / 尽力而为事件）、同事务 Outbox 写入、按 Consumer Group 生成 `event.deliver` job、显式 ack、`event_id` 幂等去重，以及 `kernel_event_outbox / kernel_event_subscriptions / kernel_event_dedup` 表设计；第 3.1 节原语定义、第 4 节架构图（新增 Transactional Outbox 节点）、6.1 接口、6.5 复用说明、7.3 / 7.5 / 8.3 / 9.2 的事件发布措辞均已同步为持久事件。

### D-03（P0）失效事件被赋予它做不到的正确性职责

**问题**：7.5 要求“进行中任务标记上下文已过期，任务结束时由 Memory Arbiter 做冲突合并”。但运行中的 LLM 调用持续数分钟且不可中断；各 Bot 使用独立 Consumer Group（6.2），同一失效事件在不同组内的消费顺序也不一致。失效事件在物理上无法成为正确性机制。

**影响**：实现者若把 invalidation 当作一致性防线，会漏掉“事件迟到、乱序、丢失”三类场景，产生无法复现的记忆冲突。

**建议**：在文档中明确降级语义：

1. 正确性唯一来源 = 提交时乐观锁版本检查（7.4）+ 冲突合并流程。
2. invalidation 事件仅用于“新任务拉取最新记忆”和 UI 提示，属于尽力而为。
3. 进行中任务不承诺感知失效，只承诺提交时被版本检查拦截。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：7.5 节删除“进行中任务标记上下文已过期”的正确性承诺，改为“不承诺感知失效，最多 UI 提示”，并新增正确性分工表（版本检查 = 唯一正确性来源；invalidation = 投递持久、语义尽力而为；进行中任务 = 无一致性承诺）；7.4 节声明版本检查为唯一正确性来源；7.3 规则 5 与 6.7 边界说明同步标注语义分工。

### D-04（P0）队列缺少幂等、租约续期与挂起状态

**问题**：6.5 的 `kernel_jobs` 定义了 `lease_until / attempt_count`，但缺少中间件队列的关键语义：

1. 无幂等键：ack 丢失重投递时，Bot 是否重复回复用户？Agent 是否重复执行？
2. 无租约续期：Agent 执行 30 分钟构建时 lease 过期，另一 worker 抢走任务，会在远程机器上双重执行非幂等副作用（git push、文件修改）。
3. 无 `suspended_awaiting_confirm` 状态：5.2 的 `agent.yield_confirm` 挂起只存在于传输会话，hub 重启即丢失。
4. DLQ replay 无幂等保障：6.4 支持“人工修复后重新入队”，但未说明部分副作用已执行的任务如何安全重放。
5. 延迟任务调度未设计：`next_retry_at` 到期后由谁唤醒（轮询间隔、时间轮、wake-up 机制）未定义。

**影响**：数据库队列最经典的翻车点：重复回复、双重执行、挂起任务丢失、毒丸任务卡死队列。

**建议**：

1. `kernel_jobs` 增加 `idempotency_key`（唯一约束）与 `parent_job_id`。
2. 所有消费者必须声明幂等语义；非幂等 Agent 工具在执行前落盘“执行意图”，重投递时先查意图。
3. 长任务引入 lease renewal（心跳续租），或对 Agent 任务改用“单次租约 + 幂等执行令牌”。
4. job 状态机增加 `suspended`，yield_confirm 持久化为 job 状态而非连接状态。
5. DLQ replay 前强制展示已执行副作用清单，由人工确认幂等策略。

### D-05（P0）跨队列因果链无定义

**问题**：6.1 的链路是 Ingress Queue → Bot Runtime Queue → Agent 任务队列，至少三级队列接力，但设计没有 parent_job_id、因果令牌或 happens-before 记录。6.6 声称“同一 conversation_id 内保持顺序”，而分区键是 `conversation_id 或 project_id`；“聊天命令触发项目任务”这类跨 key 的因果顺序完全未定义。多 Bot fan-out 后，conversation 有序也只在单个 Bot 的 Consumer Group 内成立。

**影响**：无法回答“这个 Agent 任务由哪条用户消息触发”，也无法在跨队列场景下保证因果顺序，调试与审计断裂。

**建议**：

1. 信封与 job 均携带 `causation_id`（直接原因）与 `correlation_id`（业务根因）。
2. 明确顺序承诺的精确范围：仅“同一 partition_key + 同一 Consumer Group”内有序。
3. 跨队列只承诺因果可追溯（通过 causation 链），不承诺全局有序。
4. fan-out 场景显式声明：各副本独立有序、互不有序。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：新增 6.8 节定义因果链三字段（correlation_id / causation_id / traceparent）、传播规则与 IM → Bot → Agent → 晋升示例；6.6 改写为最小可序承诺（仅同 partition_key + 同 Consumer Group 内有序，附严格 / 宽松重试模式与 SKIP LOCKED 边界）；6.2 补 fan-out 副本互不有序声明；`kernel_jobs` 与 `kernel_event_outbox` 表增加 correlation_id / causation_id 字段；6.4 DLQ 追踪键改为 correlation_id。完整信封 schema 定稿仍归 D-12。

### D-06（P1）统一图谱表退化为 EAV god-table

**问题**：8.1 用 `kernel_entities + metadata JSON` 同时承载 PM 任务状态机、Obsidian 笔记、Bot 记忆、网页摘录。这导致：无法按 namespace 施加 schema 约束；无法为不同业务建独立索引；锁竞争与表迁移互相拖累；PM 任务这种需要强 ACID 类型化转移的数据与知识资产耦合。原始笔记中的 `namespace ENUM(...)` 更是扩展性反模式（新增插件需要 ALTER TABLE）。

**影响**：一个插件的查询模式拖垮全部插件；一次表结构变更影响所有业务；任务状态机无法获得数据库级约束保护。

**建议**：

1. `kernel_entities / kernel_relations` 只保留给知识域（笔记、记忆、摘录及其关系）。
2. PM 任务、队列、项目等强结构数据使用独立类型化表，通过 entity_id 引用图谱节点。
3. `namespace` 改为普通 VARCHAR + 注册表校验，禁止 ENUM。
4. `metadata JSON` 只允许承载“检索不需要、约束不依赖”的展示性扩展字段。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：8.1 改写为“图谱边界：只存知识域”，新增数据归属表、PM 类型化表（`pm_tasks` / `pm_task_dependencies`，含 `source_entity_id` 引用）与注册扩展规则（namespace VARCHAR + 注册表校验、metadata JSON 仅展示性字段、只读接口与 namespace 写隔离、引用不复制内容）；8.2 移除 `blocks` 关系并注明任务依赖归 PM 自表；3.1 / 3.2 / 9.1 / 13 节同步收敛“万物皆节点”表述。

### D-07（P1）NULL 编码记忆象限，写路径不变量无法由存储层守护

**问题**：8.3 用 `owner_bot_id / project_id` 的 NULL 组合编码四个象限，并自己承认“语义必须由应用层严格约束”。这意味着 7.3 的核心规则“Bot 不能绕过晋升队列直接写公共记忆”完全靠应用层自觉。同时 SQL 中 NULL 比较语义（`NULL != NULL`、索引对 NULL 的处理）会持续制造查询与约束 bug。

**影响**：系统最重要的资产（项目公共记忆）缺少强制写路径，任何绕过服务的写入都会静默污染公共记忆。

**建议**：

1. 增加显式 `visibility`（global / bot_private / project_public / ephemeral）列，语义与 NULL 解耦。
2. 公共记忆写路径收敛到唯一服务（晋升队列消费者），数据库账号层面禁止其他组件直写 `visibility=project_public`。
3. 用 CHECK 约束或写路径校验保证 visibility 与 owner/project 组合自洽。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：`kernel_entities` 的 `scope` 列由显式 `visibility`（global / bot_private / project_public / ephemeral）取代；8.3 改写为“记忆象限与数据层校验”，给出 CHECK 约束 SQL（visibility 与 owner/project 组合强制自洽，MySQL 8.0.16+ 生效）、visibility 优先检索索引，以及公共记忆唯一写路径（project_public 的 INSERT/UPDATE 权限仅授予晋升队列消费者专用账号，其余账号在数据库层被拒绝）；7.3 规则、7.4 乐观锁 SQL（补 `AND visibility='project_public'`）与第 7 节象限表同步去除 NULL 语义推断。

### D-08（P1）关系边表缺少并发控制

**问题**：7.4 只给 `kernel_entities` 增加乐观锁，`kernel_relations` 没有 version 或 epoch。两个 Bot 并发替换同一实体的边集（例如沙盘中“旧心跳边置为失效、新边激活”）会交错执行，产生重复或冲突边。

**影响**：图谱结构在多 Bot 并发下出现静默损坏，且实体乐观锁无法感知边冲突。

**建议**：

1. 边更新同样携带 read_version，或引入 per-entity `edge_epoch`：修改边集时递增 epoch，提交时校验。
2. 边的插入与失效在同一事务内完成，并以实体版本为前置条件。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：`kernel_entities` 新增 `edge_epoch` 列；7.4 新增边集并发控制——边集变更在同一事务内先乐观校验并递增源实体 `edge_epoch`（影响行数为 0 即放弃提交），再完成新边插入与旧边 `valid_to` 失效，任意一步失败整体回滚；双向语义的边同时校验并递增两端 epoch；并发写策略表新增“关系边”行；8.2 补充指引。

### D-09（P1）Obsidian 双向同步先落地、冲突策略后决策

**问题**：11 的 Step 2 要做“本地 .md 双向同步”，而 12.6 把“Obsidian 文件、数据库图谱、PM 状态之间的同步冲突”列为待定工程决策。文件-DB 双向合并是全设计最难的问题之一：外部编辑器并发修改、重命名破坏实体身份、删除传播、wikilink 反向维护。

**影响**：在策略未定时先建上层，整个记忆层会继承未定义的同步语义，后续返工成本极高。

**建议**：

1. 明确数据库为 source of truth；本地 `.md` 是 DB 的单向投影。
2. 外部修改通过文件 watcher 摄取为新的 envelope 入库，由冲突流程处理，而不是做双向 merge。
3. v1 明确放弃“双向同步”表述，改为“单向发布 + 变更摄取”。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：新增 10.3 节“Obsidian 文件同步：单向发布 + 变更摄取”——数据库为 source of truth，`.md` 是幂等单向投影；外部修改由 File Watcher 封装为 `ContextEnvelope` 走 Ingress 队列，以上次同步基线 hash 做三方合并，非重叠自动合并、冲突进人工审查；重命名保实体身份（file_path 可变、entity_id 稳定）、删除进入待确认状态、wikilink 反向边由库侧在 `edge_epoch` 保护下维护；Step 2 与工程决策 12.6 同步改写为已决策。

### D-10（P1）插件与 Agent 信任边界缺失

**问题**：3.2 的插件通过 Manifest 自报订阅事件与提供工具，但设计缺少：

1. 插件认证：本地任意进程是否都能注册为插件并订阅全量事件？
2. 事件载荷级 ACL：订阅 `note.updated` 是否意味着拿到所有项目的笔记内容？
3. Agent 认证：原始笔记使用静态 `auth_token` 注册且可执行 bash，无轮换与吊销机制。
4. 审核权威归属：hub 执行前审核与 Agent 侧高危拦截冲突时，谁是权威？

**影响**：一个恶意或被攻陷的插件/Agent 即可读取全部记忆并跨设备执行命令。安全模型在执行链路定型后补救的成本是初期的十倍以上。

**建议**：

1. 插件注册引入身份凭证 + 按 namespace/project/bot 维度的事件订阅授权。
2. 事件投递前按订阅方权限过滤载荷，而不是广播全量。
3. Agent 改用可轮换凭证（短期 token / mTLS），注册即绑定能力与项目范围。
4. 明确 Agent 侧安全护栏为最终权威，hub 审核为策略层；高危操作必须两端都放行。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：新增 3.4 节“插件与 Agent 信任边界”——Manifest 仅声明不构成授权，插件安装由用户签发可轮换、可吊销的身份凭证并绑定 `plugin_id`；订阅权限按 `namespace / project / bot` 三维授权，未批准的声明不生效；Dispatcher 投递前做载荷 ACL 过滤（失败即重试，绝不降级为全量投递），用户可随时吊销；`kernel_event_subscriptions` 增加 `plugin_id / scope_filter / payload_policy / granted_by_user`；Agent 放弃静态 `auth_token`，改可轮换凭证并绑定设备、能力与项目范围；高危权威定为 Agent 侧护栏最终放行 + hub 策略审核。3.2、6.7、12.4 同步改写。

### D-11（P2）快照冗余与向量索引方案缺失

**问题**：7.3 建议任务上下文记录全量 `snapshot_json`，但既然已有 `memory_entity_versions`，版本向量 + involved_entity_ids 即可重建快照；全量 JSON 每任务可达数百 KB 且无清理策略。7.1 的“50ms 联合图谱+向量检索”目标缺少依据：MySQL 社区版 VECTOR 无 ANN 索引，暴力余弦无法支撑；1536 维向量写死在共享实体表，更换 embedding 模型需要重写所有实体。

**建议**：

1. 任务上下文只存 `context_version + involved_entity_ids`，快照内容按需从版本历史重建。
2. 向量拆到侧表 `(entity_id, model_name, dim, vector)`，支持多模型共存与灰度切换。
3. 50ms 目标降级为“待压测验证的假设”，并给出降级路径（预检索、缓存、外置 ANN 索引）。

### D-12（P2）部署形态与契约节奏错配

**问题**：系统定位是“个人智能工作台”，却按 hub + Python worker + agent-daemon + Tauri + VS Code 扩展 + MySQL 的多单元分布式系统落地。同时，系统最核心的消息契约 `ContextEnvelope` 从未给出完整定稿（缺 conversation_id、project_id、幂等键、优先级、trace 上下文）。

**建议**：

1. v1 收敛为单二进制 hub，内嵌 memory-worker 与队列，保留进程边界接口；产品验证后再拆分部署。
2. 优先冻结 ContextEnvelope 完整 schema（含 idempotency_key 等其余必填字段；追踪头已随 D-05 冻结）。
3. 可观测性（envelope_id 全链路追踪）应内建于 Step 0，而不是 12.9 事后补齐。

Bot 二进制已按 D-17 改为内核按需拉起，不再是常驻单元；memory-worker 与队列仍建议在 v1 内嵌 hub。

### D-13（P1）金库缺物理隔离，存储分层与网关契约未定义

**问题**：原设计用单一 `kernel_entities` 承载个人笔记、全局公理、项目记忆与 Bot 记忆，仅靠 `visibility` 与数据库账号隔离。个人笔记与项目数据处于同一 blast radius；Bot 访问知识库没有统一入口与 API 面；跨库引用、跨库一致性、网关可用性都未定义。

**影响**：一个插件的迁移或查询错误可能波及不可再生的个人知识；缺少网关契约会让每个 Bot 各自实现访问逻辑，ACL 无法统一执行；一旦拆分存储，跨库边与无跨库事务会成为实现期的隐性坑。

**建议**：

1. 个人笔记与全局公理迁移到独立金库库，凭据只授予 Memory Gateway。
2. 项目记忆与 Bot 记忆由 memory service 统一持有，业务插件只保存 `entity_id` 引用；不按插件分库。
3. 冻结网关 API 面（read / search / promote / subscribe / audit / health），禁止 Bot 执行 SQL。
4. 明确跨库规则：ULID/UUID 全局唯一、只持久化库内边、跨库引用带版本与 hash、跨库写入走 saga + Outbox。
5. 定义降级：金库不可用降级为无个人上下文；记忆库不可用进入等待或 DLQ。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：新增 8.4 节“存储分层与 Memory Gateway”——金库库独立承载个人笔记与全局公理，凭据只授予网关；记忆库由 memory service 统一持有项目与 Bot 记忆，业务插件只保存 `entity_id` 引用；8.1 改为同一逻辑 schema 双部署，8.3 拆成两份 CHECK（金库仅 `global`，记忆库仅其余三种），第 4 节架构图新增存储层与网关，Step 0 交付纳入双 schema 与网关最小接口，第 12 节记为已决策。

### D-14（P1）Bot-Agent 传输层与应用层耦合

**问题**：原设计把 JSON-RPC 2.0 over WebSocket 写成内核契约，所有 Agent 插件被迫使用同一种应用层协议；内核需要理解 method、params、stream 等语义，插件无法自定义帧格式、序列化和任务状态机，UDP 场景无法支持。

**影响**：新增二进制协议、UDP 遥测或自定义流控都要改内核；应用语义持续沉入内核，回到巨石化；插件生态被单一协议锁死。

**建议**：

1. 内核只提供 TCP/UDP 会话、反向连接、鉴权、多路复用、心跳、背压、重连与限流。
2. 应用层协议由插件 Manifest 声明，内核按 `protocol_id` 路由，不解析 `payload`。
3. 内核保留最小传输信封（`protocol_id / version / stream_id / frame_type / length`），payload 不透明。
4. UDP 插件自行承担可靠性、分片与去重；工具调用默认 TCP。
5. 一个插件的协议错误只关闭该插件的 stream 或会话。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：第 5 节改为“内核只提供传输层，应用层协议由插件定义”，新增 5.1 传输层与应用层边界（职责表、`transport_frame`、TCP/UDP 语义差异、反向连接、协议注册与错误隔离），任务四阶段调整为 5.2、输出双层调整为 5.3；3.1 原语 4、3.2 执行器职责、monorepo 注释、Step 0 / Step 4、第 12 节与第 13 节同步改写。

### D-15（P1）Bot↔Kernel 控制面与健康报告链路未定义

**问题**：3.3 把 Bot 放在内核侧，但文档没有定义 Bot 如何注册、心跳、拉取任务、订阅事件、上报故障；Bot↔Agent 的 TCP/UDP 插件协议不能复用到 Bot↔Kernel。Agent 故障时 Bot 可能停摆；如果每个 Bot 自行处理故障，会出现状态分裂、重复上报和无法全局熔断。

**影响**：Bot 与内核之间只能靠隐式约定接入，故障处理散落在各 Bot；Agent 故障无法形成统一的健康视图，专门 Bot 或用户也收不到一致通知。

**建议**：

1. Bot↔Kernel 使用 gRPC 控制面，定义 `BotRuntime` 服务（Register / Heartbeat / StreamTasks / AckTask / SubscribeEvents / RenewCredential / Deregister）。
2. Gateway Health Service 负责健康报告的去重、聚合与评分；聚合报告经内核策略校验后广播。
3. 报告携带 `correlation_id` / `causation_id` / `idempotency_key`；内核是状态迁移权威，Bot 报告只是证据。
4. 单条 Bot 报告不能直接驱逐 Agent；需要阈值、多源佐证或人工确认。
5. Bot 自身停摆由内核心跳与 job 租约检测，不依赖自报。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：新增 5.4 Bot↔Kernel gRPC 控制面与 6.9 健康报告与故障广播；架构图增加 Health Service 与上报/广播边；8.4 声明 Gateway 由 Memory Gateway 与 Health Service 组成；Step 0 / Step 1 / Step 4、第 12 节与第 13 节同步改写。

### D-16（P0）内核入口缺少统一因果链与上下文日志

**问题**：D-05 只定义了 job/event 的 `correlation_id` / `causation_id` / `traceparent`，但不同入口（用户 `@bot`、IM、VS Code、浏览器、Bot gRPC、Agent 健康报告）没有统一的强制信封，缺失字段不会被拒绝。日志也只记录事件与状态，未要求保存完整决策上下文，事故后无法还原“谁在什么上下文下触发了什么”。

**影响**：跨入口链路断裂，无法做端到端归因；异常或恶意消息缺少取证依据；若简单全量记录原文，又可能把个人知识和密钥写进明文日志。

**建议**：

1. 所有入口统一使用 `IngressEnvelope`，强制 `correlation_id` / `causation_id` / `root_envelope_id` / `causal_path` / `traceparent` / actor / source / target / project / bot / conversation / schema_version。
2. 内核入口统一校验，缺失或不合法直接拒绝并记录 `decision=rejected`。
3. 日志使用 append-only `kernel_ingress_log`，接受、拒绝、重复三种结果都记录完整结构化上下文。
4. 完整原始上下文写入加密 `kernel_context_snapshots`，日志记录 `context_snapshot_id` + `content_hash`，保证可完整重建，同时避免明文泄漏。
5. 所有适配器共用同一校验库；`causal_path` 深度上限与错误码固定。
6. 访问与保留策略：审计角色可读全量，用户可查自己的 trace，Bot/插件只能看授权范围；查询行为本身也要审计。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：新增 6.10 全局可追溯与上下文日志——统一 `IngressEnvelope`、强制因果头、入口校验、append-only `kernel_ingress_log`、加密 `kernel_context_snapshots` 与脱敏/保留/访问规则；6.8 扩展 `root_envelope_id` / `causal_path`；5.4、6.9、6.1、Step 0/3/4、第 12 节与第 13 节同步要求所有入口携带完整因果链。

### D-17（P1）Bot 运行形态与懒加载未定义

**问题**：文档把 Bot 描述为“内核侧 / Hub”，但没有说明它是内嵌协程、常驻进程还是独立二进制；也没有定义谁创建、谁监督、何时启动与回收、冷启动与崩溃重启。Agent 故障或 Bot 崩溃时，资源隔离与恢复边界不清楚。

**影响**：如果 Bot 是内嵌协程，一个 Bot 崩溃可能带崩 hub；如果全部常驻，空闲 Bot 浪费资源；如果没有懒加载与 supervisor，崩溃重启、任务接管和版本升级都不可控。

**建议**：

1. Bot 采用独立二进制，由内核创建与监督，业务插件不能自行拉起。
2. 懒加载：首个任务或事件触发启动，空闲超过 `idle_ttl` 回收；支持 `max_instances` 与可选 `warm_pool`。
3. 生命周期固定为 registered → dormant → starting → ready → busy → draining → stopped / crashed，启动超时与崩溃退避有明确规则。
4. 启动期间任务保持 queued，不提前 lease；只有 ready / busy 能 lease。
5. dormant 时事件由内核持久化，唤醒后按 Consumer Group 消费。
6. 升级采用版本固定 + drain + 切换；Bot 不持有长期状态，重启后从 memory service 与 job 队列恢复。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：新增 5.5 Bot 进程形态与懒加载——独立二进制、内核 supervisor、`bot_manifest`、生命周期状态机、`start_timeout` / `idle_ttl` / `max_instances` / `warm_pool`、冷启动与 drain 规则；3.1、3.3、第 4 节架构图、monorepo、6.2、6.9、Step 1、第 12 节与第 13 节同步。

### D-18（P1）Bot 类型与长连接策略未区分

**问题**：D-17 把 Bot 统一按懒加载处理，未区分常驻 Bot；也没有定义哪些 Bot 持有 Bot-Agent 长连接、哪些按任务建立短连接，以及内核如何按类型激活。把 resident Bot 也按 `idle_ttl` 回收会导致交互式 Bot 频繁冷启动；让 lazy Bot 保持长连接则浪费连接资源。

**影响**：交互式和批处理 Bot 被同一策略处理，资源与延迟都次优；内核无法判断何时启动、保持或回收；Agent 故障检测对两类 Bot 不适用同一信号。

**建议**：

1. manifest 增加 `activation_mode: lazy|resident` 与 `agent_connection: per_task|persistent`，内核是激活权威。
2. Lazy：首个任务或事件触发，`idle_ttl` 回收，`min_instances=0`，不持有 Bot-Agent 长连接，任务短连接结束即关闭。
3. Resident：kernel 启动、项目激活或用户启用时创建，`min_instances>=1`，持有长连接，不因空闲回收。
4. 内核维护 `desired_state` / `observed_state`，通过 reconcile 执行 start / stop / drain / restart。
5. Resident 长连接断开作为 Agent 故障辅助信号；Lazy 依靠 Agent 心跳、任务租约与执行超时。
6. 长连接仍经 Agent Mesh，不绕过 ACL、因果链与审计。

**修正记录（2026-11-07）**：已更新《NexusCore架构设计总结》：5.5 改为 Bot 双类型——lazy 按需激活、空闲回收、不持有 Bot-Agent 长连接；resident 常驻、持有长连接、不因空闲回收；内核按 `activation_mode` 维护 `desired_state` / `observed_state` 并 reconcile；补充 `agent_connection` / `min_instances` / `warm_pool` 字段与两类生命周期；3.1、3.3、4、5.4、6.2、6.9、Step 1、12、13 节同步。

### D-19（P1）信封混装业务语义，主体模型缺统一席位

**问题**：原 `IngressEnvelope` 同时承载业务维度（`project_id / bot_id / conversation_id / target_type / actor_type` 等）与通讯维度，导致：任何插件或非 bot 主体都能直接往内核投递入口消息（用户走插件通道、Agent 走插件协议），投递主体没有统一约束；信封字段随业务膨胀，每个入口都要解释业务字段才能路由。

**影响**：信封变相成为业务 schema，内核被迫理解业务字段，回退到“内核做上下文水合”的旧模式；多插件各自塞业务字段导致无法归因，跨入口一致性被破坏。

**建议**：

1. 用户本身也是一个 bot：`user_bot` 作为常驻席位，用户通过客户端插件（CLI / Tauri / IM 适配器）代 user_bot 投递消息。
2. 只有 bot 能投递与消费消息，适配器只是 bot 的通道代理；`source_type=im|vscode|browser` 等改为信封的 `channel`（投递通道）属性。
3. 信封只承载通讯语义：`msg_type / from_bot_id / channel / channel_ref` + 因果链 + 幂等；业务维度全部移入载荷，由来源插件定义业务信封。
4. 发件 bot 把载荷写入加密存储 / 快照，信封携带 `payload_hash / payload_ref`；接收 bot 拆封后按引用自行拉取，读取授权来自其收到的信封（内核按投递关系授予）。
5. 内核不做上下文水合推送（对齐 D-01/ D-14 内核边界）。

**修正记录（2026-11-08）**：已更新《NexusCore架构设计总结》：3.3 增加用户席位与统一总线规则（user_bot 常驻、只有 bot 才能投递 / 消费、内核是 bot 之间消息通道、适配器仅代理用户与进站）；6.10 信封字段重写为通讯语义（`msg_type / from_bot_id / channel / channel_ref` + 因果 + 幂等 + `payload_hash/ref`），业务维度随载荷走，接收 bot 自行拉取，信封不解析、内核不水合；载荷拉取受投递关系+ ACL 双重约束；kernel_ingress_log 记录字段同步为通讯语义；第 13 节补充信封边界不可侵蚀。内核 MVP 详设：§3.5 ingress 同步通讯专属信封 + 拉取授权模型，§5.3 注明实现以 Go struct + JSON（v1）承载待 protobuf 化。

### D-20（P1）Bot 未分内核侧 / 插件侧角色体系

**问题**：D-17 / D-18 只定义了 Bot 的进程形态（独立二进制）与类型（lazy / resident），但未回答四个根本问题：谁负责创建业务 Bot？谁监督所有 Bot 的存续？插件如何接入 Bot 体系、注册自己的 Bot？插件安装后的热更新由谁把关？

**影响**：进内核的 Bot 无统一拉起与监督链路，插件与 Bot 的关系悬空；多插件环境没有统一的安全审查与热更新治理，插件侧 Bot 会成为绕过内核的旁路。

**建议**：

1. Bot 分内核侧（`user_bot / create_bot / monitor_bot / security_bot`）与插件侧（`xxx_bot / user_xxx_bot`）。
2. `create_bot` 读内核 Bot 清单 / 插件清单，决定拉起哪些相关进程；`monitor_bot` 横向监督各 Bot 存续（进程、心跳、租约、消费位点）；`security_bot` 负责插件注册与安全审查、插件侧 Bot 注册审查、安装后的热更新治理。
3. 插件最少注册一个 Bot，命名为 `xxx_bot`（如 `architect_bot`）；用户通过插件注册的 Bot 命名 `user_xxx_bot`。
4. 唯一创建者仍是内核 supervisor（对齐 D-17）；`create_bot` 是拉起决策者，经内核控制面执行。
5. 保留名禁用：任何插件不得注册 `user_bot / create_bot / monitor_bot / security_bot` 或保留前缀。

**修正记录（2026-11-10）**：已更新《NexusCore架构设计总结》：3.3.1 新增 Bot 角色体系（角色表：user_bot / create_bot / monitor_bot / security_bot / xxx_bot / user_xxx_bot，含拉起关系与五条规则）；3.1 原语 3、Step 1 同步；内核 MVP 详设 supervisor 规则补“create_bot 为决策者、supervisor 为执行器”；开发规划 M2 增加四角色骨架。

## 4. 值得保留的设计

以下机制方向正确，问题只在于被附加了做不到的保证，修正时不应推翻：

1. 脑手分离与输出双层蒸馏（raw_stream_uri / distilled_artifact）。
2. 四象限记忆模型与三层上下文装配读路径。
3. 晋升队列作为项目公共记忆的唯一写入口。
4. 提交时乐观锁 + `memory_entity_versions` 版本历史。
5. 审核记录、审计事件与队列状态由内核统一维护。

## 5. 修正优先级

建议按以下顺序修正，前五项（全部 P0）必须在 Step 0 开工前完成：

1. **D-01**（已于 2026-11-07 修正）：移动内核边界，或正式承认认知协调属于内核并说明理由。
2. **D-02**（已于 2026-11-07 修正）：冻结事件投递语义（持久事件 vs 尽力而为事件）。
3. **D-03**（已于 2026-11-07 修正）：明确 invalidation 仅为提示，正确性由提交时版本检查唯一承担。
4. **D-04**：job 状态机补齐 suspended / 幂等键 / 租约续期 / replay 安全。
5. **D-05**（已于 2026-11-07 修正）：冻结追踪头（causation_id / correlation_id / traceparent）与最小顺序承诺；完整信封 schema 定稿归入 D-12。
6. **D-06 / D-07**（均已于 2026-11-07 修正）：PM 拆独立表；象限改显式 visibility 并落地数据层校验。
7. **D-09**（已于 2026-11-07 修正）：Obsidian 改为单向发布 + 变更摄取。
8. **D-10**（已于 2026-11-07 修正）：插件与 Agent 信任边界随 Step 0 的注册协议一起落地——身份凭证、用户授权订阅、载荷 ACL、Agent 可轮换凭证均已定义。
9. **D-13**（已于 2026-11-07 修正）：金库独立分库 + Memory Gateway；项目与 Bot 记忆由 memory service 单点持有，业务插件只保存 `entity_id` 引用。
10. **D-14**（已于 2026-11-07 修正）：内核只做 TCP/UDP 传输，应用层协议由插件定义；JSON-RPC/WebSocket 降级为可选的插件实现。
11. **D-15**（已于 2026-11-07 修正）：Bot↔Kernel 走 gRPC 控制面；Health Service 聚合报告，内核策略校验并广播。
12. **D-16**（已于 2026-11-07 修正）：所有内核入口强制 `IngressEnvelope` 与完整因果链；日志 append-only，上下文用加密快照保存并可完整重建。
13. **D-17**（已于 2026-11-07 修正）：Bot 独立二进制，内核创建与监督；懒加载、空闲回收、`max_instances` 与 `warm_pool` 已定义。
14. **D-18**（已于 2026-11-07 修正）：Bot 分 lazy / resident；lazy 不持长连接、空闲回收，resident 常驻并持长连接，内核按类型激活。
15. **D-19**（已于 2026-11-08 修正）：user_bot 常驻席位 / 只有 bot 投递消费消息 / 信封只承载通讯语义，用户与 Agent 全部经由总线上的 bot 站点表现。
16. **D-20**（已于 2026-11-10 修正）：Bot 分内核侧四角色（user_bot / create_bot / monitor_bot / security_bot）与插件侧业务 Bot（xxx_bot / user_xxx_bot）；create_bot 只决策、supervisor 只执行。

## 6. 验收问题清单

修正后，设计文档应能逐条回答以下问题；任何一条答不上，视为未修完：

1. hub 重启后，进行中的 yield_confirm 任务会怎样？事件订阅者的消费位点会怎样？
2. 一条消息 @ 三个 Bot，用户会收到几条回复？顺序由谁决定？重复回复如何抑制？
3. Agent 任务租约过期但进程仍在执行时，系统如何阻止第二个 worker 抢占？
4. DLQ replay 一个已部分执行的任务时，哪些副作用会被重放？如何确认？
5. `project.memory.invalidated` 丢失、迟到、乱序三种情况下，一致性分别靠什么保证？
6. 哪个数据库账号有权限写 `visibility=project_public`？绕过晋升队列的写操作会被什么拦截？
7. PM 任务状态迁移的约束由哪张表的哪个约束保证？
8. 从一条 IM 消息追溯到最终 Agent 任务与记忆晋升，靠哪些字段串联？
9. 一个未获用户批准的插件能否订阅 `note.updated`？拿到的事件里能否看到未授权项目的内容？用户吊销授权后是否立即停止投递？
10. Agent 的凭证如何轮换与吊销？hub 与 Agent 对同一高危命令判定相反时，以哪一端为准？
11. 哪个组件持有金库库凭据？Bot 能否绕过 Memory Gateway 读取个人笔记？
12. 跨库引用如何保持一致？金库不可用时项目任务如何降级？
13. 内核能否在不修改代码的情况下接入一个新的二进制协议插件？UDP 丢包或乱序时由谁负责重传、去重与分片？
14. Bot 如何注册、拉取任务、订阅事件？Agent 故障报告经过哪个服务聚合，谁有权把节点标记为不健康，最终如何广播给专门 Bot 或用户？
15. 一条用户 `@bot` 消息能否在不查其他系统的前提下还原完整因果链？拒绝的消息是否也有完整上下文日志？敏感内容如何脱敏与授权访问？
16. Bot 是独立进程还是 hub 内协程？谁负责创建、监督、重启和回收？首个任务到达时如何懒加载，启动超时或崩溃退避如何处理？
17. 一个 Bot 是 lazy 还是 resident？谁决定激活与回收？lazy 是否持有 Bot-Agent 长连接，resident 断线后如何重连，Agent 故障如何分别检测？
18. 谁能在内核总线上投递与消费消息？用户消息是否经 user_bot 投递？信封里为什么不应该出现 `project_id / conversation_id`？业务数据如何由接收 bot 拉取？拉取授权与日志快照有什么约束？
19. 内核侧四角色是谁？插件注册 Bot 的命名规则是什么？create_bot / supervisor 的决策和执行如何分工？插件安装后的热更新安全由谁把关？
