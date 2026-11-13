# NexusCore

以微内核为基础的个人智能工作台：内核只负责 PostgreSQL 持久化事件与任务队列、Bot 注册运行时与配置中心、跨设备传输路由三类原语；记忆与知识能力（图谱、金库、晋升、上下文装配）完整属于 knowledge 类插件 `plugin_memory`；所有具体业务以插件形式接入。内核与内核侧四角色不感知、不持有任何记忆。

## 设计文档索引

| 文档 | 内容 |
| --- | --- |
| [doc/NexusCore架构设计总结.md](doc/NexusCore架构设计总结.md) | 上游总设计：原则、架构图、队列语义、配置中心、记忆插件边界、数据模型、结论与修订记录 |
| [doc/内核mvp/内核MVP详细设计.md](doc/内核mvp/内核MVP详细设计.md) | 内核 MVP 实现级详细设计（hub 10 模块、kernel schema、PG 队列、配置最小集、验收标准） |
| [doc/ConfigCenter/ConfigCenter详细设计.md](doc/ConfigCenter/ConfigCenter详细设计.md) | 配置中心实现级详细设计（四域配置、proposal → security → user_bot → active、CLI） |
| [doc/BotMvp/BotMvp详细设计.md](doc/BotMvp/BotMvp详细设计.md) | Bot MVP 实现级详细设计（内核侧四角色、插件侧业务 Bot、监督与安全审查链路） |
| [doc/MemoryPlugin/MemoryPlugin详细设计.md](doc/MemoryPlugin/MemoryPlugin详细设计.md) | 记忆插件实现级详细设计（双 schema、四象限、晋升、工具 API、事件回投、用户级 Bot 记忆） |
| [doc/PluginMvp/PluginMvp详细设计.md](doc/PluginMvp/PluginMvp详细设计.md) | Plugin MVP 实现级详细设计（四类插件、manifest schema、两级进程模型、生命周期六态、命名规范） |
| [doc/开发规划.md](doc/开发规划.md) | 单人开发整体规划（M0-M7 里程碑、估计、停线点、砍单策略） |
| [doc/NexusCore待修正问题清单.md](doc/NexusCore待修正问题清单.md) | 未解决问题追踪（D-11 / D-12） |
| [doc/NexusCore已修正问题归档.md](doc/NexusCore已修正问题归档.md) | 已解决问题归档（D-01 至 D-10、D-13 至 D-23） |

## 当前状态

- M0 工程骨架已落地；D-22 记忆插件化（`65f3901`）与 D-23 文档重构（PostgreSQL 内核化 + 配置中心）已完成，设计文档全部同步。
- M1 内核 MVP（纯内核）已完成：hub 10 模块 + bot-runtime + loopback agent + D-04 四语义 + 失败注入五例；内核 MVP 详设第 8 节六条验收全过，实现偏离与逐条证据见详设 §10。未写任何记忆代码、未建任何记忆表。
- 验收命令：`scripts/dev-pg.sh` 起本地 PostgreSQL 夹具后，`make test-pg`（真库全量测试）、`make smoke-m1`（九步冒烟 + 边界验收 + 兜底轮询）、`make drill-crash-m1`（真实 SIGKILL 崩溃恢复演练）。
- 下一步：M2 Bot 管理与插件运营（配置中心完整审批流、插件生命周期、权限授权、DLQ 运营、docker-compose）；记忆插件从 M3 起落地。MCP / Skill 不在当前设计范围。
