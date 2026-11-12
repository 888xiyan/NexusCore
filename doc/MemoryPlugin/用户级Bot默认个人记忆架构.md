# 用户级 Bot 默认个人记忆架构

> 版本：v0.2（2026-11-12，归属记忆插件）  
> 适用范围：`role=plugin` 且 `owner=user_bot` 的 `user_xxx_bot`  
> 归属：由 `plugin_memory` 的 memory-worker 实现；内核与内核侧四角色不感知  
> 状态：设计定稿，M2 只落配置与默认值，运行时在 M4 随 memory-worker 落地  
> 参考模型：Claude Code Auto Memory / Auto Dream 的四层记忆栈与离线整合

## 1. 定位

用户级 Bot 由用户通过宿插件的配置入口或 CLI 创建，宿插件只提供业务模板、领域工具和创建界面；Bot 进程仍由 `create_bot` 统一拉起。除用户显式关闭外，每个 `user_xxx_bot` 默认启用 `personal_stack_v1` 记忆 profile。

该 profile 不是插件记忆引擎，也不是新插件仓库。它的写入与整合都归记忆插件 `plugin_memory` 内的 memory-worker；`user_xxx_bot` 只产生可追溯的会话记录和任务产物，插件按 `owner_bot_id` 隔离读取。宿插件不得绕过 `plugin_memory` 工具直接读写另一 Bot 的记忆；内核不参与。

这个默认栈与既有正交记忆模型兼容：

| 层 | 内容 | 存储 | 是否进入图谱 |
| --- | --- | --- | --- |
| Transcripts | 原始会话、工具调用与结果引用 | memory-worker 冷存储，按 `bot_id / session_id / correlation_id` 分片，append-only | 否 |
| Memory Notes | 单条高价值经验、踩坑、偏好、已验证结论 | `memory` schema `memory_entities`，宿插件 namespace，`visibility=bot_private` | 是 |
| Memory Representations | 按主题合成的内部 Wiki / 参考文档 | `memory` schema `memory_entities`，宿插件 namespace，`visibility=bot_private` | 是 |
| Injected Context | 新会话前装配的短索引与规则切片 | memory-worker 生成并缓存的只读上下文工件 | 否，只记录 `context_version` 与 hash |

`personal_stack_v1` 的所有图谱实体都保持 `owner_bot_id=user_xxx_bot` 且 `visibility=bot_private`。它们不会自动进入金库 schema；只有用户批准的晋升才能写入 `visibility=global` 的个人公理或长期偏好。

Notes 与 Representations 共用宿插件 namespace，不新增图谱字段。原子经验通过 `evidence_of` 指向 Transcripts 证据；主题表征通过 `composed_of` 聚合多个 Notes，这一差别同时供 memory-worker 检索和人工审计使用。

## 2. 写入路径

```text
会话 / 工具执行完成
  -> user_xxx_bot 输出任务摘要与原始记录引用
  -> memory-worker 读取 Transcripts，判断未来复用价值
  -> 高价值片段写入 Memory Notes
  -> Notes 携带 evidence_ref、content_hash、confidence、valid_from
  -> 冲突或主题足够稳定时进入 Memory Representations
  -> 新会话装配时由 Injected Context 引用最新版本
```

显著性判断交给模型，而不是硬编码“只记录失败教训”一类规则。默认只向模型声明记忆用途：跨会话减少重复探索、保留已验证结论、暴露风险与例外。置信度由证据强度、用户纠正、可验证结果共同决定；低置信度内容不进入 Injected Context。

## 3. Auto Dream 整合

默认触发条件为“距上次整合超过 24 小时且新增至少 5 个完整会话”；满足资源空闲时进入后台任务，失败按指数退避重试。整合任务在 memory-worker 内执行，不占用 Bot 请求循环。

四个阶段：

1. **Orientation**：扫描 Transcripts 索引、Notes 和 Representations，建立当前主题清单与缺口。
2. **Signal Gathering**：检索用户纠正、重复失败、重复工具路径、跨会话目标和高频约束。
3. **Consolidation**：把相对时间归一为绝对日期，消除旧架构和已删除资源造成的矛盾，合并重复条目，并保留证据链。
4. **Pruning & Indexing**：失效 Notes 标记新版本，重建主题 Representations，压平 Injected Context 到 200 行以内。

发现矛盾时不静默删除旧事实。Memory-worker 按版本化写入生成新版本，旧版本继续作为审计证据；若旧结论被推翻，新结论必须在证据中说明原因。下一次注入使用新版本，旧上下文随会话结束自然淘汰。

## 4. 注入约束

Injected Context 的目标是稳定、短小、可缓存：

1. 单次注入不超过 200 行；估算长度按 500 至 2000 tokens 控制。
2. 会话内不重写上下文前缀，避免破坏 Prompt Cache。
3. 注入内容只包含索引、当前有效规则、关键例外和证据引用，不粘贴完整 Transcripts。
4. 超限时按主题优先级与置信度裁剪，不因单条长文本挤占索引位。
5. 每次装配记录 `context_version`、输入实体版本和内容 hash，供冲突归因。

## 5. 安全与隔离

1. 金库 schema 仍只供 `plugin_memory` 访问；`personal_stack_v1` 不能把用户笔记直接复制进 Bot 私有记忆。
2. `bot_private` 记忆默认只允许 owner Bot 与用户读，宿插件可以申请读取申请中的 namespace，但不能跨 Bot 读取。
3. 跨 Bot 共享必须走显式晋升与用户批准，禁止通过共享插件 namespace 变相广播。
4. Transcripts 只增不改；清理只影响索引与派生记忆，不物理改写原始事实层。
5. memory-worker 崩溃或不可用时，Bot 可以继续执行无历史依赖的任务；装配降级为无个人上下文，不得阻塞队列。

## 6. 里程碑落点

M2 在创建 `user_xxx_bot` 时落 `bot_manifest.memory_profile=personal_stack_v1`（内核原样存储、不解释），并在 CLI `bot list` 中可见；M2 不实现 Dreaming 与注入。M4 把 profile 接入 `plugin_memory` 的 memory-worker：Transcripts、Notes、Representations、Injected Context 与 Auto Dream 调度全部可验收，且包含矛盾修正和超限裁剪用例。

## 7. 非目标

1. 不在本 profile 内实现项目公共记忆；项目事实仍走 `project_public` 与晋升队列。
2. 不引入固定向量库或插件自管记忆存储；先按 `plugin_memory` 的既有工具面收敛。
3. 不允许插件根据业务事件自由改写 Bot 记忆。
4. 不把 Anthropic 实验数据当作 NexusCore 的性能承诺，只保留架构机制。
