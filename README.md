# NexusCore

以微内核为基础的个人智能工作台：内核只负责持久化事件与任务队列、Bot 注册与运行时、跨设备传输路由三类原语；记忆与知识能力（图谱、金库、晋升、上下文装配）完整属于 knowledge 类插件 `plugin_memory`；所有具体业务以插件形式接入。内核与内核侧四角色不感知、不持有任何记忆。

## 设计文档索引

| 文档 | 内容 |
| --- | --- |
| [doc/NexusCore架构设计总结.md](doc/NexusCore架构设计总结.md) | 上游总设计：原则、架构图、队列语义、记忆插件边界、数据模型、结论与修订记录 |
| [doc/内核mvp/](doc/内核mvp/内核MVP详细设计.md) | 内核 MVP 实现级详细设计（hub 9 模块、kernel schema、接口契约、验收标准；纯内核） |
| [doc/BotMvp/](doc/BotMvp/BotMvp详细设计.md) | Bot MVP 实现级详细设计（内核侧四角色、插件侧业务 Bot、监督与安全审查链路） |
| [doc/MemoryPlugin/](doc/MemoryPlugin/MemoryPlugin详细设计.md) | 记忆插件实现级详细设计（双 schema、四象限、晋升、工具 API、事件回投、用户级 Bot 记忆） |
| [doc/PluginMvp/](doc/PluginMvp/PluginMvp详细设计.md) | Plugin MVP 实现级详细设计（四类插件、manifest schema、生命周期六态、命名规范） |
| [doc/开发规划.md](doc/开发规划.md) | 单人开发整体规划（M0-M7 里程碑、估计、停线点、砍单策略） |
| [doc/NexusCore待修正问题清单.md](doc/NexusCore待修正问题清单.md) | 未解决问题追踪（D-04 / D-11 / D-12） |
| [doc/NexusCore已修正问题归档.md](doc/NexusCore已修正问题归档.md) | 已解决问题归档（D-01 至 D-03、D-05 至 D-10、D-13 至 D-22） |

## 当前状态

- M0 工程骨架已落地；2026-11-12 按 D-22 完成记忆插件化重构：内核 5 个二进制（hub / bot-runtime / agent-daemon / cli / migrate）+ `plugins/plugin_memory` 骨架 + `migrations/{kernel,plugins/plugin_memory}` 目录，设计文档全部同步。
- 下一步：M1 内核 MVP（D-04 队列四语义 + kernel schema DDL + 纯内核冒烟链路）；记忆插件从 M3 起落地。
