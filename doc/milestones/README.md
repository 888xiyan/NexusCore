# 里程碑详设（M4–M10）

M4 起的实现专篇集中在本目录。M0–M3 的底座契约仍在 `doc/内核mvp/`、`doc/ConfigCenter/`、`doc/BotMvp/`、`doc/PluginMvp/`、`doc/MemoryPlugin/`。排期与验收记录在 [开发规划](../开发规划.md)。

原先散落在 `doc/` 根目录的 `M*.md` 已收入此处。M8 的两篇（基础模块、依赖治理）合并为一篇。系统 Update 是 M9 / M10 的产品规范，随里程碑一起归档。

| 里程碑 | 文档 | 内容 |
| --- | --- | --- |
| M4 | [M4-前端工作台.md](M4-前端工作台.md) | Vue 3 工作台、网关 SSE、群聊视区、记忆抽屉、Agent 监控。原 `M4开发总结.md` |
| M5 | [M5-本地Agent搜寻.md](M5-本地Agent搜寻.md) | 本地 Agent 探测、`LocalCLIDriver`、防噪音蒸馏 |
| M6 | [M6-Bot工厂.md](M6-Bot工厂.md) | `plugin_bot_creator`、角色模板、200 行记忆校验、Agent 绑定 |
| M7 | [M7-前端微模块.md](M7-前端微模块.md) | Vite ESM 动态挂载、纯净 Shell、PluginRegistry |
| M8 | [M8-基础模块与依赖治理.md](M8-基础模块与依赖治理.md) | 无头模块（第 1–6 节）与接口注册、DAG、环路检测、热更新（第 7 节起） |
| M9 | [M9-标准群聊与全局UI.md](M9-标准群聊与全局UI.md) | 标准群聊插件、全局 UI 注册、`groupchat` schema |
| 产品规范 | [系统Update-全局UI与沙盒轨迹.md](系统Update-全局UI与沙盒轨迹.md) | 左右两段导航、`@` 输入框、沙盒工具轨迹、三层 Bot。原型见 [index.html](../index.html) |
| 需求定稿 | [系统Update-需求规格.md](系统Update-需求规格.md) | 审计后的系统 Update 需求。与界面专篇冲突时以需求规格为准 |
| M10 | [M10-工具中心与代批.md](M10-工具中心与代批.md) | 工具插件、MCP/Skill、`bot_tool_grants`、中台 Bot 代批、插件商场 |

阅读顺序：M4 → M7 看前端怎么从单体页面拆成微模块；M5 → M6 看探针与 Bot 工厂；M8 看这些能力如何下沉成无头库；系统 Update 需求规格 → 界面专篇 → M9 → M10 看 v0.1.0 / v0.2.0。需求规格与界面专篇冲突时，以需求规格为准。
