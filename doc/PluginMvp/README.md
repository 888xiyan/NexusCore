# Plugin MVP 设计文档

本目录是 NexusCore 插件生态（架构总结 3.2 / 3.3.1 / 3.4 定义的入站通道 / 工作台视图 / 执行器 / 知识记忆四类插件）MVP 的实现级详细设计。

## 文件索引

| 文件 | 内容 |
| --- | --- |
| [PluginMvp详细设计.md](PluginMvp详细设计.md) | 四类插件 / 生命周期 / manifest schema / 命名与事件规范 / **两级进程模型（Bot 进程 + 插件进程）与启动、优雅关闭、强制终止链路** / 安全 / 数据迁移 / 冒烟链路、验收标准 |

## 上游设计

- 架构总设计：[../NexusCore架构设计总结.md](../NexusCore架构设计总结.md)（§3.2 万物皆插件 / §3.3.1 规则 1 两级进程 / §3.4 插件信任边界 / §5.1 传输 / §5.5 Bot 类型 / §6.9 健康报告 / §6.10 入口 / §6.11 配置中心 / §8.4 记忆插件边界）
- Bot MVP：[../BotMvp/BotMvp详细设计.md](../BotMvp/BotMvp详细设计.md)（§4.4 security_bot / §8.2 插件安装链路 / §8.4 热更新 / §8.6-8.7 插件进程关闭与孤儿回收）
- 配置中心：[../ConfigCenter/ConfigCenter详细设计.md](../ConfigCenter/ConfigCenter详细设计.md)（manifest 不可变 vs config 可变；plugin 配置审批）
- 记忆插件：[../MemoryPlugin/MemoryPlugin详细设计.md](../MemoryPlugin/MemoryPlugin详细设计.md)（knowledge 类插件首发实例）
- 开发规划：[../开发规划.md](../开发规划.md)（M2 = 插件生态落地里程碑，M3 = 记忆插件 v1）

## MVP 交付形态（一句话）

一个插件 = `plugin_manifest` 清单 + 一个 Go 二进制（可选，即**插件进程 L2**）+ 若干类型化数据表 + 若干注册 Bot（**Bot 进程 L1**）+ 一份 `plugin` 配置（配置中心，版本化审批）；通过 `security_bot` 审查 → `create_bot` 决策 → supervisor 拉起 Bot 进程 → owner bot（`bot_templates[0]`）拉起插件进程并把 PID 登记给 `monitor_bot`。支持生命周期六状态（install / approve / activate / upgrade / disable / uninstall），下线顺序恒为**先插件进程、后 Bot 进程**；关闭分优雅（`user_bot → owner bot → 插件进程`）与强制（`user_bot → create_bot → supervisor`，越过 owner bot）两条路径。MVP 语言 = Go 单语言（Python / TS 资源 stub 延后 M4 / M6）。
