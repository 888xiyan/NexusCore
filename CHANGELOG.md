# Changelog

All notable changes to NexusCore will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [v0.1.0] - 2026-11-16 · 旗舰正式发行版 (Flagship Release)

NexusCore 是以微内核为基础的个人智能工作台系统，首创**“脑手分离（Brain-Hand Separation）”**与**“原生单体记忆（Native Monolithic Memory）”**架构，提供从微内核底座、简单 Agent 调度、群聊协同到可视化工作台的端到端完整闭环。

### 🌟 核心特性与架构突破

#### 1. 微内核底座与统一事件总线 (Microkernel Core)
- **三类原语收敛**：持久化任务/事件队列（PostgreSQL）、Bot 注册与 Supervisor 运行时、跨设备反向 TCP 传输路由；
- **纯净信封规范**：信封仅承载通讯语义与完整因果链（`correlation_id`, `causation_id`, `causal_path`, `traceparent`），严禁业务语义侵入内核；
- **配置中心完整审批流**：`kernel_config_*` 四域配置，严格由 `user_bot` 审批并校验 checksum 签名，阻断未授权写入；
- **两级进程模型 (Two-Level Process Model)**：L1 Bot 进程由 Supervisor 统一拉起，L2 插件独立服务进程由 Owner Bot 以独立进程组（`setsid`）自治管理，严格遵守“先下线 L2 插件服务、后下线 L1 Bot”的回收铁律。

#### 2. 原生单体记忆系统 (Native Monolithic Memory)
- **Claude Code 四层文件系统记忆栈**：
  - **Transcripts（会话层）**：Append-Only JSONL 原始事实溯源；
  - **Memory Notes（笔记层）**：任务中自主捕获显著性（Salience）经验与避坑记录；
  - **Memory Representations（表征层）**：离线合成的结构化参考文档；
  - **Injected Context（注入层）**：`MEMORY.md` **刚性限制严格 ≤ 200 行（约 500~2000 Tokens）**，配合 Prompt Caching 确保 85%~93% 缓存命中，彻底消除二次方膨胀；
- **Auto Dream 离线整合**：异步四阶段流转（Orientation → Signal Gathering → Consolidation → Pruning & Indexing）；
- **重启生效策略（Restart Policy）**：单体记忆与画像通过毫秒级冷重启生效，消除复杂运行时热更负担。

#### 3. 简单智能体定位与防死循环群聊 (Simple Agent & GroupChat)
- **轻量简单 Agent 铁律**：禁止长时间思考（单次推理超时硬封顶），**绝对禁止直接执行 Shell**；所有环境与物理操作统一反向调度至目标 `agent-daemon` 探针受控沙盒执行；
- **单向唤醒门与防死循环铁律**：群聊协同严格限制**“仅仅用户@触发”**，Bot 回复中的 `@` 强制降级为纯展示文本，绝对杜绝 Bot 间自发级联互 @ 唤醒，根除 Token 爆炸风险；
- **PostgreSQL 独立 Schema 存储**：独立 `groupchat` schema 全量持久化房间、消息与时序上下文。

#### 4. Atelier Code 全局工作台与微前端架构 (Global Workbench)
- **手绘原型 100% 落地**：
  - **左侧两段式导航**：上半区“纪事”（日志/数据/定时任务）+ 下半区“工坊”（agents/bots/工具/+插件）；
  - **右侧主舞台**：居中大 `@xxx|` 胶囊输入框与快捷指令气泡；
  - **原生主题系统**：白昼羊皮纸（Light ☀️）、午夜星空（Dark 🌙）、跟随系统（Auto 💻）；
- **方案 A 原生 ESM 动态微模块化**：宿主纯净 Shell（`HostLayout.vue`）剥离业务视图硬编码，通过 `PluginRegistry` 运行时共享（`window.__NEXUS_CORE__`）动态加载独立打包的 ESM Chunks（`groupchat`、`agent-discovery`、`bot-creator`、`tool-center`、`marketplace`）。

#### 5. 无头基础模块与 DAG 拓扑治理 (Headless Engine)
- **三大无头引擎**：`groupchat`、`agentmesh`、`botfactory` 100% 剥离 UI 逻辑（单测覆盖率全量 ≥ 85%）；
- **依赖 DAG 治理**：基于 Kahn 算法的拓扑排序加载、三色标记 DFS 循环依赖阻断、依赖深度硬限制（`MaxDepth ≤ 2`）、逆拓扑序级联卸载与两阶段平滑热更新。

#### 6. 内置沙盒工具轨迹审计与中台代批安全防线 (Tool Trajectory & RBAC)
- **工具分类治理与可编辑 Skill**：内置不可编辑系统原生工具（禁止篡改源码）+ 在线可编辑 Skill 增删改查；
- **Bot-Tool 授权矩阵（`bot_tool_grants`）**：细粒度鉴权，未授权调用返回 `403 Forbidden` 并留痕；
- **沙盒工具调用轨迹全量落盘（`kernel_tool_trajectories`）**：实时捕获入参、终端回显、退出码与耗时，前端滑出抽屉实时可视；
- **分级网络隔离与主动外联通知**：普通工具沙箱彻底离线（`network=none`）；搜索工具（`sys.search.web`）受控放行并产生 `network_alert` 广播；
- **中台 Bot 代批 RBAC（Delegation RBAC）**：
  - **Tier 1（受控代批）**：UI 主题切换、只读查询自动代批；
  - **Tier 2（刚性 HITL 阻断）**：插件安装卸载、进程 Exec、密钥提取、穿透沙箱规则**一票否决**，无论如何配置必须人类交互确认（Mandatory HITL）；
- **插件商场三阶段审批**：下载（完整性与 DAG 深度检查）→ 安装（特权提案与人类签名）→ 卸载（逆拓扑级联下线）。

---

### 📦 交付制品列表

- 内核核心二进制：
  - `bin/hub`：Go 微内核网关与调度中心
  - `bin/bot-runtime`：简单 Agent 独立运行时
  - `bin/agent-daemon`：跨设备物理探针与受控工具执行器
  - `bin/cli`：命令行管理与客户端代理
  - `bin/migrate`：数据库模式迁移工具
- 独立 L2 插件服务：
  - `bin/plugins/plugin_groupchat`（:7300）
  - `bin/plugins/plugin_bot_creator`（:7500）
  - `bin/plugins/plugin_tool_center`（:7400）
  - `bin/plugins/plugin_marketplace`（:7600）
- 前端生产制品：
  - `Frontend/dist/index.html`
  - `Frontend/dist/assets/index-*.js`、`groupchat-*.js`、`tool-center-*.js`、`marketplace-*.js`
- 自动化端到端冒烟测试套件：
  - `make smoke-m1` 至 `make smoke-m10` 全部 100% 验证通过。
