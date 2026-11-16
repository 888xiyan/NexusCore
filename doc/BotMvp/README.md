# Bot MVP 设计文档

本目录是 NexusCore Bot 体系（架构总结 3.3.1 定义的内核侧四角色与插件侧业务 Bot）MVP 的实现级详细设计。

## 文件索引

| 文件 | 内容 |
| --- | --- |
| [BotMvp详细设计.md](BotMvp详细设计.md) | 角色清单与部署形态、四角色详设（含配置中心职责）、插件侧 Bot（含 owner bot 职责）、数据模型、指标 / 决策命令、插件进程优雅关闭与强制终止、冒烟链路、验收标准 |
| [Bot职责与Prompt生成规范.md](Bot职责与Prompt生成规范.md) | Bot 核心定位（简单 Agent、脑手分离、单向触发）、四角色与用户 Bot 职责、单体记忆注入规范、以及供外部 AI 生成提示词的元规范模板 |
| [BotSkill设计所需上下文规范与模板.md](BotSkill设计所需上下文规范与模板.md) | 为 Bot 批量设计工具 Skill 时的 5 大上下文要素维度（场景、执行宿主、入参、噪音蒸馏、安全授权）及即用型填空模板 |

## 上游设计

- 架构总设计：[../NexusCore架构设计总结.md](../NexusCore架构设计总结.md)（第 3.3 / 3.3.1 / 3.4 / 5.4 / 5.5 / 6.9 节为本文档的实现依据）
- 内核 MVP：[../内核mvp/内核MVP详细设计.md](../内核mvp/内核MVP详细设计.md)（§3.5 scheduler / §3.9 supervisor / §5.0 BotRuntime 协议）
- 开发规划：[../开发规划.md](../开发规划.md)（M2 = 本 Bot MVP 落地里程碑）

## 相关设计

- 配置中心（D-23）：[../ConfigCenter/ConfigCenter详细设计.md](../ConfigCenter/ConfigCenter详细设计.md)。`user_bot` 是唯一批准人，`security_bot` 只做可执行类预审，`create_bot` / `monitor_bot` 只读 active 投影与版本。
- 插件进程两级模型：[../PluginMvp/PluginMvp详细设计.md](../PluginMvp/PluginMvp详细设计.md) §6。Bot 进程（L1）由 `create_bot` 决策、supervisor 执行；插件进程（L2）由 owner bot（`bot_templates[0]`）拉起与回收，PID 登记给 `monitor_bot` 只读巡检，强制终止与孤儿级联回收仍由 supervisor 执行。
- 用户级 Bot 默认个人记忆（`personal_stack_v1`）：[../MemoryPlugin/用户级Bot默认个人记忆架构.md](../MemoryPlugin/用户级Bot默认个人记忆架构.md)。该 profile 由记忆插件 `plugin_memory` 实现，内核只存储 `memory_profile` 声明、不解释；内核侧四角色不使用记忆。

## MVP 交付形态（一句话）

hub 启动时拉起 `user_bot / monitor_bot / security_bot` 三个 resident 进程与一个懒加载的 `create_bot` worker；内核侧四角色与插件注册的 `xxx_bot` 均通过同一个 bot-runtime 二进制承载，共享 gRPC 控制面。首次即可在 CLI 里完成"插件注册 → security_bot 审查 → xxx_bot 注册 → create_bot 拉起 → monitor_bot 监督"全链路。插件侧再往下延伸一级：owner bot 拉起插件进程并把 PID 登记给 `monitor_bot`，关闭走 `plugin stop`（优雅，经 owner bot）或 `plugin kill --confirm`（强制，supervisor 代执行）。
