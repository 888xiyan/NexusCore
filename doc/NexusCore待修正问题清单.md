# NexusCore 待修正问题清单

> 更新时间：2026-11-13（M1 内核 MVP 完成后）  
> 评审对象：`doc/NexusCore架构设计总结.md`  
> 状态：2 项待修正（D-11 / D-12），均属插件内部或业务契约，不阻塞内核  
> 已解决的问题见 `NexusCore已修正问题归档.md`（含 D-04 队列四语义、D-22 记忆即插件、D-23 PostgreSQL 内核化与配置中心）

## 1. 待修正问题总览

| 编号 | 等级 | 问题 | 对应章节 | 状态 |
| --- | --- | --- | --- | --- |
| D-11 | P2 | snapshot_json 冗余且无 GC；向量索引方案（PostgreSQL/pgvector）待验证（现归 `plugin_memory` 内部） | 7.1 / 7.3 / 8.4 | 待修正（M4） |
| D-12 | P2 | 业务载荷 schema 未定稿（通讯信封已随 D-19 定稿；部署形态已随 D-23 收敛为 PostgreSQL 单实例 + 三 schema） | 4 / 6.1 | 待修正 |

## 2. 修正顺序

1. **D-11**：快照冗余与向量索引方案缺失；D-22 后属于 `plugin_memory` 的内部设计（M4），不再影响内核 MVP。
2. **D-12**：完成 `ContextEnvelope` 业务 schema 定稿（通讯信封 `IngressEnvelope` 已随 D-19 定稿为纯通讯职责；追踪头已随 D-05 冻结；部署收敛已随 D-23 完成）。

> D-04（队列幂等 / 租约续期 / 挂起 / 安全 replay）曾是 Step 0 的开工前置条件，已于 2026-11-13 随 M1 实现并验收，移入已修正归档。

## 3. 详细问题

### D-11（P2）快照冗余与向量索引方案缺失

> D-22 后本项属于 `plugin_memory` 的内部设计（见 `MemoryPlugin/MemoryPlugin详细设计.md` §3/§5），不影响内核 MVP 与内核验收。D-23 后 PostgreSQL 可用 pgvector 侧表（`memory_embeddings`），不可用时插件内降级。

**问题**：7.3 建议任务上下文记录全量 `snapshot_json`，但既然已有 `memory_entity_versions`，版本向量 + involved_entity_ids 即可重建快照；全量 JSON 每任务可达数百 KB 且无清理策略。7.1 的“50ms 联合图谱+向量检索”目标缺少依据：原生 VECTOR 类型缺乏 ANN 索引，暴力余弦无法支撑；1536 维向量写死在共享实体表，更换 embedding 模型需要重写所有实体。

**影响**：上下文快照造成存储浪费与无界增长；语义检索在数据量上来后会不可用；换 embedding 模型的迁移成本失控。（历史背景为 MySQL；D-23 后由 PostgreSQL/pgvector 侧表替代，方案待 M4 验证。）

**建议**：

1. 任务上下文只存 `context_version + involved_entity_ids`，快照内容按需从版本历史重建。
2. 向量拆到侧表 `(entity_id, model_name, dim, vector)`，支持多模型共存与灰度切换。
3. 50ms 目标降级为“待压测验证的假设”，并给出降级路径（预检索、缓存、外置 ANN 索引）。

### D-12（P2）部署形态与契约节奏错配

**问题**：系统定位是“个人智能工作台”，却按 hub + Python worker + agent-daemon + Tauri + VS Code 扩展 + 数据库的多单元分布式系统落地。同时，系统最核心的消息契约 `ContextEnvelope` 从未给出完整定稿（缺 conversation_id、project_id、幂等键、优先级等字段）。

**影响**：部署运维负担前置到产品验证之前；信封不定稿，所有适配器各自实现，字段漂移无法避免。

**建议**：

1. v1 收敛为单二进制 hub，内嵌 memory-worker 与队列，保留进程边界接口；产品验证后再拆分部署。
2. 完成 ContextEnvelope 完整 schema 定稿，补 idempotency_key 等其余必填字段；追踪头已随 D-05 冻结，可直接并入（见架构总结 6.8 / 6.10 的字段定义）。
3. 可观测性（envelope_id 全链路追踪）已内建于 6.10；此项主要剩部署收敛。

## 4. 验收问题清单

修正后，设计文档应能逐条回答以下问题；任何一条答不上，视为未修完：

1. `plugin_memory` 的语义检索目标的降级路径是什么？换 embedding 模型时向量表如何迁移？（插件内部，M4 验收）
2. ContextEnvelope 完整 schema 是否冻结并通过所有入口校验测试？（通讯信封 `IngressEnvelope` 已冻结并有校验测试；业务载荷 schema 待定稿）

## 5. 历史验收问题（已由已修正项覆盖）

以下问题在原文档中提出，对应的缺失语义已随归档条目修正；验收时可对照归档复核：

1. hub 重启后，进行中的 yield_confirm 任务会怎样？事件订阅者的消费位点会怎样？（D-04，M1 已验收：`suspended` 持久化跨重启保留，消费位点由 `event.deliver` job 状态 + `kernel_event_dedup` 构成）
2. 一条消息 @ 三个 Bot，用户会收到几条回复？顺序由谁决定？重复回复如何抑制？（D-02 / D-04 / D-05：每授权 Consumer Group 一条投递，同分区有序，幂等键 + 去重表抑制重复）
3. Agent 任务租约过期但进程仍在执行时，系统如何阻止第二个 worker 抢占？（D-04，M1 已验收：有已执行意图即 `suspended`，不被抢占；`lease_token` 绑定 ack）
4. DLQ replay 一个已部分执行的任务时，哪些副作用会被重放？如何确认？（D-04，M1 已验收：`kernel_job_intents` 落盘副作用清单，人工确认 continue / replay / discard）
5. `project.memory.invalidated` 丢失、迟到、乱序三种情况下，一致性分别靠什么保证？（D-03）
6. 哪个数据库账号有权限写 `visibility=project_public`？绕过晋升队列的写操作会被什么拦截？（D-07）
7. PM 任务状态迁移的约束由哪张表的哪个约束保证？（D-06）
8. 从一条 IM 消息追溯到最终 Agent 任务与记忆晋升，靠哪些字段串联？（D-05 / D-16）
9. 一个未获用户批准的插件能否订阅 `note.updated`？拿到的事件里能否看到未授权项目的内容？用户吊销授权后是否立即停止投递？（D-10）
10. Agent 的凭证如何轮换与吊销？hub 与 Agent 对同一高危命令判定相反时，以哪一端为准？（D-10）
11. 哪个组件持有金库凭据与记忆库图谱表账号？Bot 能否绕过 `plugin_memory` 读取个人笔记？（D-13 / D-21 / D-22）
12. 跨库引用如何保持一致？金库不可用时项目任务如何降级？（D-13）
13. 内核能否在不修改代码的情况下接入一个新的二进制协议插件？UDP 丢包或乱序时由谁负责重传、去重与分片？（D-14）
14. Bot 如何注册、拉取任务、订阅事件？Agent 故障报告经过哪个服务聚合，谁有权把节点标记为不健康，最终如何广播给专门 Bot 或用户？（D-15）
15. 一条用户 `@bot` 消息能否在不查其他系统的前提下还原完整因果链？拒绝的消息是否也有完整上下文日志？敏感内容如何脱敏与授权访问？（D-16）
16. Bot 是独立进程还是 hub 内协程？谁负责创建、监督、重启和回收？首个任务到达时如何懒加载，启动超时或崩溃退避如何处理？（D-17）
17. 一个 Bot 是 lazy 还是 resident？谁决定激活与回收？lazy 是否持有 Bot-Agent 长连接，resident 断线后如何重连，Agent 故障如何分别检测？（D-18）
