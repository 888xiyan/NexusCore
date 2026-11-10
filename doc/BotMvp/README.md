# Bot MVP 设计文档

本目录是 NexusCore Bot 体系（架构总结 3.3.1 定义的内核侧四角色与插件侧业务 Bot）MVP 的实现级详细设计。

## 文件索引

| 文件 | 内容 |
| --- | --- |
| [BotMvp详细设计.md](BotMvp详细设计.md) | 角色清单与部署形态、四角色详设、插件侧 Bot、数据模型、指标 / 决策命令、冒烟链路、验收标准 |

## 上游设计

- 架构总设计：[../NexusCore架构设计总结.md](../NexusCore架构设计总结.md)（第 3.3 / 3.3.1 / 3.4 / 5.4 / 5.5 / 6.9 节为本文档的实现依据）
- 内核 MVP：[../内核mvp/内核MVP详细设计.md](../内核mvp/内核MVP详细设计.md)（§3.6 scheduler / §3.10 supervisor / §5.1 BotRuntime 协议）
- 开发规划：[../开发规划.md](../开发规划.md)（M2 = 本 Bot MVP 落地里程碑）

## MVP 交付形态（一句话）

hub 启动时拉起 `user_bot / monitor_bot / security_bot` 三个 resident 进程与一个懒加载的 `create_bot` worker；内核侧四角色与插件注册的 `xxx_bot` 均通过同一个 bot-runtime 二进制承载，共享 gRPC 控制面。首次即可在 CLI 里完成"插件注册 → security_bot 审查 → xxx_bot 注册 → create_bot 拉起 → monitor_bot 监督"全链路。
