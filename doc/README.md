# NexusCore 设计与实现文档全集

本目录汇集了 NexusCore 微内核个人智能工作台的所有系统架构设计、里程碑实现方案、问题清单以及演进规划。

---

## 目录导航图谱

```mermaid
graph TD
    A[NexusCore架构设计总结.md<br/>顶层总设] --> B[内核底座 M0-M1<br/>内核mvp/]
    A --> C[控制面治理 M2<br/>ConfigCenter/ & BotMvp/ & PluginMvp/]
    A --> D[认知与群聊 M3-M4<br/>MemoryPlugin/ & M4开发总结.md]
    A --> E[探针与工厂 M5-M6<br/>M5-本地Agent & M6-Bot工厂]
    A --> F[解耦与治理 M7-M8<br/>M7-前端微模块 & M8-无头模块沉淀]
    A --> G[旗舰与生态 M9-M10<br/>M9-全局UI与沙盒轨迹 & M10-工具中心]
```

---

## 一、架构总览与基石规范

| 文档 | 说明 | 核心内容 |
| --- | --- | --- |
| [NexusCore架构设计总结.md](NexusCore架构设计总结.md) | **上游顶层总设** | 系统定位、微内核三原语、脑手分离、数据模型、因果链与信封规范 |
| [开发规划.md](开发规划.md) | **工程落地规划** | M0~M10 里程碑划分、人日估算、退出条件与停线点 |
| [NexusCore设计缺陷评审.md](NexusCore设计缺陷评审.md) | **早期架构缺陷评审** | 针对 Monolith 倾向与边界模糊的历史深度评审（D-01 ~ D-23 源头） |
| [NexusCore已修正问题归档.md](NexusCore已修正问题归档.md) | **已解决问题归档** | D-01 至 D-10、D-13 至 D-23 的具体修正方案与验收记录 |
| [NexusCore待修正问题清单.md](NexusCore待修正问题清单.md) | **未解决问题追踪** | D-11（向量侧表）、D-12（业务载荷 Schema）等持续跟踪项 |

---

## 二、基础底座模块详细设计（M0 - M3）

| 目录/模块 | 对应阶段 | 主要文档 | 核心职责与设计边界 |
| --- | --- | --- | --- |
| [内核mvp/](内核mvp/README.md) | M0 ~ M1 | [内核MVP详细设计.md](内核mvp/内核MVP详细设计.md) | 微内核网关 10 核心模块、PostgreSQL 原生持久队列（SKIP LOCKED）、D-04 四语义 |
| [ConfigCenter/](ConfigCenter/README.md) | M1 ~ M2 | [ConfigCenter详细设计.md](ConfigCenter/ConfigCenter详细设计.md) | 统一期望配置中心（plugin/bot/agent/global）、四阶段审批流、secret_ref 凭据隔离 |
| [BotMvp/](BotMvp/README.md) | M2 | [BotMvp详细设计.md](BotMvp/BotMvp详细设计.md)<br>[Bot职责与Prompt生成规范.md](BotMvp/Bot职责与Prompt生成规范.md)<br>[BotSkill设计所需上下文规范与模板.md](BotMvp/BotSkill设计所需上下文规范与模板.md) | 内核四角色与业务 Bot 规范、Supervisor 进程监管、优雅重启、禁 Shell/禁长思考 |
| [PluginMvp/](PluginMvp/README.md) | M2 | [PluginMvp详细设计.md](PluginMvp/PluginMvp详细设计.md) | 标准清单 manifest.json、两级进程模型（L1 Bot + L2 插件进程）、防 PID 复用 |
| [MemoryPlugin/](MemoryPlugin/README.md) | M3 | [MemoryPlugin详细设计.md](MemoryPlugin/MemoryPlugin详细设计.md)<br>[用户级Bot默认个人记忆架构.md](MemoryPlugin/用户级Bot默认个人记忆架构.md) | Claude Code 四层单体记忆栈、`MEMORY.md` ≤ 200 行铁律约束、Auto Dream 离线整合 |

---

## 三、演进里程碑详细设计（M4 - M10）

| 里程碑 | 设计文档 | 交付内容与关键机制 |
| --- | --- | --- |
| **M4** | [M4开发总结.md](M4开发总结.md) | Vue 3 独立前端工作台、网关 SSE 实时流、多 Bot 群聊视区、记忆抽屉一键重启 |
| **M5** | [M5-本地Agent搜寻与调度闭环详细设计.md](M5-本地Agent搜寻与调度闭环详细设计.md) | 本地 Agent 资产探测（Claude Code/Codex/Pi-Agent 等）、LocalCLIDriver、防噪音蒸馏闭环 |
| **M6** | [M6-Bot工厂与Agent绑定插件详细设计.md](M6-Bot工厂与Agent绑定插件详细设计.md) | 独立插件 `plugin_bot_creator`、预设角色库（Reviewer/Coder/Ops）、蓝图校验、Agent 绑定向导 |
| **M7** | [M7-前端微模块化与插件注册表详细设计.md](M7-前端微模块化与插件注册表详细设计.md) | Vite ESM 动态挂载方案、纯净 Shell、PluginRegistry、微模块代码分割 |
| **M8** | [M8-基础模块沉淀与接口设计.md](M8-基础模块沉淀与接口设计.md)<br>[M8-Update-插件接口注册与依赖治理方案.md](M8-Update-插件接口注册与依赖治理方案.md) | 基础模块 100% 剥离 UI 下沉无头化、DAG 拓扑加载、三色标记循环检测、滚动热更 |
| **M9** | [M9-标准群聊插件与全局UI注册详细设计.md](M9-标准群聊插件与全局UI注册详细设计.md)<br>[系统Update-全局UI原型与沙盒工具轨迹审计详细设计.md](系统Update-全局UI原型与沙盒工具轨迹审计详细设计.md) | **v0.1.0 旗舰版**：左右两段导航（纪事/工坊）+ 右侧居中 `@` 原型、内置沙盒记录工具轨迹、独立 Schema 存储 |
| **M10** | [M10-工具中心与中台Bot代批审批详细设计.md](M10-工具中心与中台Bot代批审批详细设计.md) | **v0.2.0 生态版**：独立工具插件（系统工具/可编辑Skill/外部MCP）、`bot_tool_grants` 授权矩阵、中台 Bot 受控代批 RBAC |

---

## 四、UI 原型与历史归档

- **[index.html](index.html)**：M9 全局 UI 原型（Nexus Storybook Console），包含白昼童话羊皮纸与午夜古籍主题、双段式分栏与交互演示。
- **[archive/](archive/README.md)**：早期原型立项草稿与原始构想记录文本归档。

---

## 五、设计文档阅读指南

1. **新加入开发者 / 贡献者**：请优先通读根目录 `AGENTS.md`、`README.md`，随后阅读 [NexusCore架构设计总结.md](NexusCore架构设计总结.md)。
2. **开发新插件**：请阅读 [PluginMvp/PluginMvp详细设计.md](PluginMvp/PluginMvp详细设计.md) 与 [M8-基础模块沉淀与接口设计.md](M8-基础模块沉淀与接口设计.md)。
3. **开发或修改 Bot**：请阅读 [BotMvp/BotMvp详细设计.md](BotMvp/BotMvp详细设计.md) 与 [M6-Bot工厂与Agent绑定插件详细设计.md](M6-Bot工厂与Agent绑定插件详细设计.md)。
4. **集成工具或 MCP**：请阅读 [M10-工具中心与中台Bot代批审批详细设计.md](M10-工具中心与中台Bot代批审批详细设计.md)。
