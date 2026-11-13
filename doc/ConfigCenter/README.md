# 配置中心设计文档

本目录是 NexusCore 配置中心（D-23）的实现级详细设计。配置中心是 hub 内控制面模块，属于「Bot 注册、运行时与配置」原语；不是第四原语、不是独立服务。

## 文件索引

| 文件 | 内容 |
| --- | --- |
| [ConfigCenter详细设计.md](ConfigCenter详细设计.md) | 定位与边界、四类配置域、PG 数据模型、proposal → security → user_bot → active 审批流、读取 ACL、版本固定与漂移、CLI、里程碑与验收 |

## 上游设计

- 架构总设计：[../NexusCore架构设计总结.md](../NexusCore架构设计总结.md)（§3.1 / §3.3.1 / §6.11 / §8 / §12.18）
- 内核 MVP：[../内核mvp/内核MVP详细设计.md](../内核mvp/内核MVP详细设计.md)（§3.10 config 模块）
- Bot MVP：[../BotMvp/BotMvp详细设计.md](../BotMvp/BotMvp详细设计.md)（内核侧四角色的配置职责）
- 插件 MVP：[../PluginMvp/PluginMvp详细设计.md](../PluginMvp/PluginMvp详细设计.md)（manifest 与 config 的边界）

## 范围（一句话）

只存 `plugin / bot / agent / global` 四类**期望配置**：版本化、可审批、可回滚；写入必须经 `user_bot` 批准（可执行类先经 `security_bot` 预审）；只存 `secret_ref`，不存密钥值。**MCP / Skill 不在范围内**（无运行时，后续另立设计）。
