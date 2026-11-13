# 记忆插件（plugin_memory）设计文档

本目录是 NexusCore 记忆与知识能力（knowledge 类插件 `plugin_memory`）的实现级详细设计。记忆不再是内核数据面：内核与内核侧四角色（`user_bot / create_bot / monitor_bot / security_bot`）不感知、不持有、不装配任何记忆；`plugin_memory` 以普通插件身份注册、运行与升级，只是它承接了全部记忆业务。

## 文件索引

| 文件 | 内容 |
| --- | --- |
| [MemoryPlugin详细设计.md](MemoryPlugin详细设计.md) | 定位与边界、manifest 草案、双 schema 存储模型与 CHECK 约束、四象限与一致性、读写路径、工具 API、事件回投、里程碑与验收 |
| [用户级Bot默认个人记忆架构.md](用户级Bot默认个人记忆架构.md) | `user_xxx_bot` 默认启用的四层个人记忆 profile（`personal_stack_v1`）与 Auto Dream 整合流程 |

## 上游设计

- 架构总设计：[../NexusCore架构设计总结.md](../NexusCore架构设计总结.md)（§3.1 / 3.2 / 3.3.1 / 3.4 / 6.7 / 6.11 / 7 / 8）
- 插件 MVP：[../PluginMvp/PluginMvp详细设计.md](../PluginMvp/PluginMvp详细设计.md)（manifest / 生命周期 / 命名 / 卸载清理 / 两级进程）
- 配置中心：[../ConfigCenter/ConfigCenter详细设计.md](../ConfigCenter/ConfigCenter详细设计.md)（plugin_memory 的 `plugin` 配置）
- 内核 MVP：[../内核mvp/内核MVP详细设计.md](../内核mvp/内核MVP详细设计.md)（只提供队列、Bot 运行时、配置与传输；无记忆模块）
- 开发规划：[../开发规划.md](../开发规划.md)（M3 = 记忆插件 v1，M4 = 认知闭环）

## MVP 交付形态（一句话）

`plugin_memory` = `plugin_manifest` + 知识服务（Go，L2）+ `memory_bot`（L1）+ 若干类型化表（`memory` / `vault` 两个 PostgreSQL schema，pgvector 侧表 M4）+ 工具面（read / search / create / promote / assemble_context / audit / health）+ `plugin` 配置文档；M3 以 Obsidian 单向发布与变更摄取首发，M4 接入 memory-worker 完成上下文装配与记忆晋升闭环。
