# Plugin MVP 设计文档

本目录是 NexusCore 插件生态（架构总结 3.2 / 3.3.1 / 3.4 定义的入站通道 / 工作台视图 / 执行器三类插件）MVP 的实现级详细设计。

## 文件索引

| 文件 | 内容 |
| --- | --- |
| [PluginMvp详细设计.md](PluginMvp详细设计.md) | 三类插件 / 生命周期 / manifest schema / 命名与事件规范 / 进程模型 / 安全 / 数据迁移 / 冒烟链路、验收标准 |

## 上游设计

- 架构总设计：[../NexusCore架构设计总结.md](../NexusCore架构设计总结.md)（§3.2 万物皆插件 / §3.4 插件信任边界 / §5.1 传输 / §6.10 入口 / §8.4 Memory Gateway）
- Bot MVP：[../BotMvp/BotMvp详细设计.md](../BotMvp/BotMvp详细设计.md)（§4.4 security_bot / §8.2 插件安装链路 / §8.4 热更新）
- 开发规划：[../开发规划.md](../开发规划.md)（M2 = 插件生态落地里程碑）

## MVP 交付形态（一句话）

一个插件 = `plugin_manifest` 清单 + 一个 Go 二进制（可选）+ 若干类型化数据表 + 若干注册 Bot；通过 `security_bot` 审查 → `create_bot` 拉起，支持生命周期六状态（install / approve / activate / upgrade / disable / uninstall），MVP 语言 = Go 单语言（Python / TS 資源 stub 延后 M4 / M6）。
