# 内核 MVP 设计文档

本目录是 NexusCore 内核 MVP（对应架构总结 Step 0 / Step 1 必须项）的实现级详细设计。范围**只含内核**：队列与事件、Bot 注册与运行时、传输路由；记忆与知识能力属于 knowledge 类插件 `plugin_memory`（M3/M4），见 `../MemoryPlugin/`。

## 文件索引

| 文件 | 内容 |
| --- | --- |
| [内核MVP详细设计.md](内核MVP详细设计.md) | 目标与非目标、部署形态、hub 9 模块的详细设计、kernel schema 数据模型、接口契约、关键流程、验收标准 |

## 上游设计

- 架构总设计：[../NexusCore架构设计总结.md](../NexusCore架构设计总结.md)（第 3 / 5 / 6 / 8 / 11 节为本文档的实现依据）
- 问题追踪：[../NexusCore待修正问题清单.md](../NexusCore待修正问题清单.md)（D-04 语义已并入本文档 Job 调度器设计；D-11 / D-12 不在本 MVP 范围内）
- 已修正归档：[../NexusCore已修正问题归档.md](../NexusCore已修正问题归档.md)（D-22 记忆插件化）

## MVP 交付形态（一句话）

一个 hub 单二进制（队列 / 事件、Bot 注册与运行时、传输路由 9 个模块）+ 一个 bot-runtime 二进制 + 一个 loopback agent 测试夹具 + 一个 MySQL 实例（kernel schema，单账号），跑通"信封入口 → 队列 → lazy Bot → Agent echo → 回包 → Outbox 事件 → user_bot 订阅"全链路。不包含任何记忆 / 知识代码与表。
