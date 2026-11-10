# NexusCore

以微内核为基础的个人智能工作台：内核只负责知识访问与图谱存储、持久化事件与任务队列、Bot 注册和跨设备传输路由，所有具体业务以插件形式接入。

## 设计文档索引

| 文档 | 内容 |
| --- | --- |
| [doc/NexusCore架构设计总结.md](doc/NexusCore架构设计总结.md) | 上游总设计：原则、架构图、队列语义、记忆模型、数据模型、结论与修订记录 |
| [doc/内核mvp/](doc/内核mvp/内核MVP详细设计.md) | 内核 MVP 实现级详细设计（10 模块、双库数据模型、接口契约、验收标准） |
| [doc/BotMvp/](doc/BotMvp/BotMvp详细设计.md) | Bot MVP 实现级详细设计（内核侧四角色、插件侧业务 Bot、监督与安全审查链路） |
| [doc/开发规划.md](doc/开发规划.md) | 单人开发整体规划（M0-M7 里程碑、估计、停线点、砍单策略） |
| [doc/NexusCore待修正问题清单.md](doc/NexusCore待修正问题清单.md) | 未解决问题追踪（D-04 / D-11 / D-12） |
| [doc/NexusCore已修正问题归档.md](doc/NexusCore已修正问题归档.md) | 已解决问题归档（D-01 至 D-03、D-05 至 D-10、D-13 至 D-19） |

## 当前状态

- M0 工程骨架已落地（git commit c657459），设计文档全部冻结。
- 下一步：M1 内核 MVP（D-04 队列语义 + 双 schema DDL + 冒烟链路）。
