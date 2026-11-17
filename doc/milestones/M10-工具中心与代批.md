# M10 详细设计定版：工具中心、MCP/Skill 插件化治理与中台 Bot 审批闭环

> **文档状态**：正式定版归档（Approved & Finalized）  
> **版本标识**：NexusCore v0.2.0 生态核心  
> **定版日期**：2026-11-16  
> **核心主题**：确立 NexusCore v0.2.0 工具生态与多层治理防线。落地独立工具插件（`plugin_tool_center`，统一治理不可编辑系统工具、可编辑 Skill 与外部 MCP）、配置中心 `bot_tool_grants` 授权矩阵、三层 Bot 体系与中台 Bot 分级代批 RBAC（Tier 1 受控代批 vs Tier 2 高危强制 HITL 一票否决）、插件商场（`plugin_marketplace`）三阶段安全审批闭环，以及受控沙盒网络外联审计。

---

## 1. 业务背景与设计目标

在完成 M9 全局工作台原型（Atelier Code）与内置沙盒工具轨迹审计闭环后，NexusCore 已经具备完备的人机交互界面、群聊协同引擎与任务溯源底座。然而，系统在工具管理、外部协议扩展及自动化授权上亟需规范化体系：
1. **工具分类杂乱缺乏治理**：系统底层物理命令、用户编写的提示词/脚本 Skill、外部基于 Model Context Protocol (MCP) 的三方服务混杂在一起，缺乏统一的增删改查（CRUD）与生命周期管理；
2. **Bot 越权滥用风险**：缺乏标准化的授权矩阵，所有 Bot 理论上均能获知全局工具列表，缺乏“最小特权原则（Principle of Least Privilege）”约束；
3. **微内核纯净化铁律受到挑战**：外部生态协议（如 MCP、社区 Skill）若直接侵入内核，将导致微内核膨胀与崩溃扩散；
4. **自动化审批与安全防线的矛盾**：用户希望中台助手 Bot 能自然语言代办事务，但若完全信任中台 Bot，一旦遭遇 Prompt 注入（Indirect Prompt Injection），黑客或恶意指令即可伪造用户身份安装恶意插件或销毁数据。

为此，M10 战略聚焦于：**工具生态插件化、权限授权矩阵化、中台代批分级化（Tier 1 受控 vs Tier 2 刚性阻断）、插件审批三阶段闭环**。

---

## 2. 总体架构拓扑

```mermaid
flowchart TD
    subgraph UI_Layer["前端工作台 (Frontend v0.2.0)"]
        NAV["左侧导航：工具 (Tools) / +插件 (Marketplace) / 设置"]
        TOOL_UI["工具中心视区 (系统工具/Skill编辑/MCP管理/授权矩阵)"]
        MARKET_UI["插件商场视区 (自然语言检索/依赖树/三阶段审批)"]
        TRAJ_DRAWER["沙盒工具轨迹抽屉 (实时审计与网络外联通知)"]
    end

    subgraph Bot_Hierarchy["三层 Bot 体系与中台角色"]
        SYS_BOTS["系统 Bot (create_bot / monitor_bot / security_bot / 插件Owner)"]
        HUB_BOT["中台 Bot (assistant_bot / hub_bot)<br/>持有 user_bot 委派凭证"]
        USER_BOTS["用户自建 Bot (user_xxx_bot)"]
        PLUGIN_BOTS["业务插件 Bot (xxx_bot)"]
    end

    subgraph Governance_Gate["中台代批 RBAC 分级网关 (Delegation RBAC)"]
        TIER1["Tier 1: 允许代批 (只读/主题切换/低危通知/白名单调用)"]
        TIER2["Tier 2: 强制拦截 (插件安装卸载/进程Exec/密钥/网络放行)<br/>⚠️ 必须 Human-In-The-Loop 交互确认"]
    end

    subgraph Plugin_Layer["L2 独立插件层 (两级进程模型自治)"]
        TC["工具中心插件 (plugin_tool_center :7400)<br/>System Tools / Editable Skills / MCP Stdio Gateway"]
        MP["插件商场插件 (plugin_marketplace :7500)<br/>3-Stage Approval: Download -> Install -> Uninstall"]
    end

    subgraph Kernel_Config["微内核与配置中心 (Kernel Hub :7200)"]
        ACL["3.4 消息 ACL 与 因果链校验"]
        GRANTS[("bot_tool_grants (多对多授权矩阵)")]
        AUDIT[("kernel_plugin_audit_logs (全生命周期留痕)")]
        TRAJ[("kernel_tool_trajectories (沙盒轨迹与网络告警)")]
    end

    subgraph Sandbox_Probe["跨设备物理探针沙盒 (agent-daemon)"]
        DRV["LocalCLIDriver (系统指令)"]
        MCP_RUN["MCP Container/Process (隔离运行)"]
        NET_FILTER["网络沙盒过滤器 (离线 / 443白名单外联通知)"]
    end

    NAV --> TOOL_UI
    NAV --> MARKET_UI
    HUB_BOT --> Governance_Gate
    Governance_Gate -->|Tier 1 自动批复| Kernel_Config
    Governance_Gate -->|Tier 2 拦截弹窗| UI_Layer
    TC --> GRANTS
    TC --> Sandbox_Probe
    MP --> Kernel_Config
```

---

## 3. 详细设计要点

### 3.1 工具分类治理与独立工具插件（`plugin_tool_center`）

#### 3.1.1 工具三级分类与特征
NexusCore 工具生态严格划分为三类：
1. **不可编辑系统工具（System Native Tools）**：
   - 来源于系统驱动层或本地受控探针（`LocalCLIDriver`），如文件查看、目录树扫描、Git 状态探查；
   - 固化于底层代码，用户/Bot **只可调用、不可修改代码**；
2. **可编辑工具与技能（Editable Skills）**：
   - 允许开发者或用户直接在线创建、编辑与测试；
   - 包含自定义 Python 脚本、Shell 批处理、结构化 Prompt 模板；
   - 支持完整的增删改查（CRUD）与版本快照回退；
3. **外部 MCP 服务工具（External MCP Tools）**：
   - 兼容标准 Model Context Protocol（MCP）协议规范（通过 stdio 或 HTTP/SSE 管道）；
   - **微内核隔离铁律**：**微内核不包含任何 MCP 协议解析与运行时代码**；
   - MCP 服务的进程拉起、stdio 管道交互与生命周期由 `plugin_tool_center` 委托 `agent-daemon` 在沙盒环境中隔离运行，异常崩溃不扩散至内核。

#### 3.1.2 Bot-Tool 授权矩阵（`bot_tool_grants`）
在配置中心建立统一的工具授权表 `kernel_bot_tool_grants`，严格遵循最小特权原则：
- 表结构包含：`grant_id`, `bot_id`, `tool_id`, `tool_type`, `permission_level` (`read_only` / `execute` / `admin`), `granted_by`, `granted_at`, `status`；
- **强绑与拦截**：
  - 特殊特权工具（如网络搜索、生产代码提交、系统重启）仅授权给特定白名单 Bot；
  - Bot 发起工具调用时，微内核/沙盒统一进行二次校验，未授权直接抛出 `E_TOOL_ACCESS_DENIED` 并计入轨迹留痕。

---

### 3.2 三层 Bot 体系与中台 Bot 代批 RBAC 安全防线

#### 3.2.1 三层 Bot 角色界定
1. **系统 Bot（System Bots）**：
   - 专司系统调度运维（如 `create_bot`、`monitor_bot`、`security_bot` 以及各独立插件的 Owner Bot 如 `groupchat_bot`、`tool_center_bot`）；
2. **中台管家 Bot（Hub Bot / `assistant_bot`）**：
   - 系统的总管家，代表用户在总线调度协调各业务 Bot，用户可授予其代理审批权；
3. **用户自建与业务 Bot（User / Plugin Business Bots）**：
   - 专精特定业务领域的轻量 Agent（如 Reviewer、Coder、Ops、Obsidian 笔记助手），禁 Shell、禁长思考。

#### 3.2.2 分级代批 RBAC（Delegation RBAC）与防注入铁律
为杜绝恶意 Prompt 诱导中台 Bot 滥用权限，构建刚性分级门禁：

| 授权分级 | 包含操作类型 | 处理方式 | 安全保障机制 |
| :--- | :--- | :--- | :--- |
| **Tier 1<br/>(受控代批)** | - UI 主题切换（白昼/午夜/跟随系统）<br/>- 系统只读参数查询<br/>- 低危消息中继与群聊拉人<br/>- 白名单内只读/低危工具调用 | **自动代批放行** | 记录代批因果链，审计追踪，触发轻量通知 |
| **Tier 2<br/>(高危一票否决)** | - 插件安装、升级、卸载<br/>- 二进制进程 Exec 与 OS 信号下发<br/>- 密钥/API Token 提取<br/>- 穿透沙盒的网络隔离放行规则 | **绝对禁止代批<br/>强制人类确认** | **Mandatory HITL**：无论中台 Bot 提示词或规则如何配置，直接阻断，必须在终端或前端交互弹出确认框由人类签名！ |

---

### 3.3 插件商场与三阶段安全审批流闭环（`plugin_marketplace`）

插件的全生命周期必须在内核与日志中 100% 留痕，经历三个严格隔离的阶段：

1. **阶段一：下载与静态审查（Download & Static Inspection）**
   - 插件商场助手 Bot（`marketplace_bot`）支持自然语言搜索与依赖解析；
   - 下载二进制与清单，强制校验 `binary_digest`（SHA-256 强一致）；
   - 基于 Kahn 算法构建依赖 DAG，实施依赖环路检测（三色标记 DFS）与依赖深度硬限制（`MaxDepth ≤ 2`）。
2. **阶段二：安装与特权审批（Install & Security Approval）**
   - 生成标准微内核配置提案（Config Proposal），包含权限要求与所申请工具清单；
   - `security_bot` 进行静态合规预审；
   - 触发 Tier 2 高危审批：必须经由真实 `user_bot`（人类用户交互确认）通过，方可写入激活并由 Supervisor 调度拉起。
3. **阶段三：卸载与级联排空（Uninstall & Cascade Teardown）**
   - 依赖反向保护：若当前插件正被上层业务插件依赖，默认阻断单点卸载；
   - 仅在指定 `--cascade` 时，依据逆拓扑序由上至下优雅下线（先停 L2 插件服务，后停 L1 Bot 进程）；
   - 全程操作写入 `kernel_plugin_audit_logs`，不可抹除。

---

### 3.4 沙盒网络策略与工具执行轨迹审计

1. **分级网络隔离策略**：
   - 普通计算/代码/分析工具：容器/进程沙盒默认配置 `network=none`，物理阻断一切 TCP/UDP 外联；
   - 搜索与联网工具（如 `search_web`）：仅放行 443/53 端口受限域名；
2. **主动通知告警**：
   - 任何工具触发网络外联时，沙盒探针实时产生 Ingress 审计事件；
   - 中台 Bot 与前端实时气泡通知：“[网络告警] Bot 'xxx' 正在通过受控沙盒访问外网: api.search.org”，用户可一键熔断。

---

## 4. 落地步骤与工程排期

1. **阶段 1：内核授权矩阵与工具模块沉淀（Day 1 - Day 3）**
   - 新增 `internal/modules/toolcenter/` 无头模块与授权矩阵校验逻辑；
   - 扩展配置中心与数据库 DDL（`bot_tool_grants` 与 `plugin_audit_logs`）；
   - 沉淀系统工具、可编辑 Skill 与外部 MCP 契约。
2. **阶段 2：中台代批 RBAC 网关与安全防线（Day 4 - Day 6）**
   - 实现 Tier 1 vs Tier 2 审批裁决引擎，注入 Prompt 越权渗透自动化测试；
   - 落地 `assistant_bot` / `hub_bot` 委派调度逻辑。
3. **阶段 3：独立插件实现（Day 7 - Day 9）**
   - 实现独立插件 `plugins/plugin_tool_center/`（提供 `:7400` API）；
   - 实现独立插件 `plugins/plugin_marketplace/`（提供 `:7500` API 与三阶段审批）。
4. **阶段 4：前端微前端视区与商场/工具交互集成（Day 10 - Day 11）**
   - 前端集成工具中心管理视图（系统工具/可编辑 Skill CRUD/授权矩阵）；
   - 前端集成插件商场与审批中心视图；
   - 左侧两段导航“工具”与“+ 插件”路由联调打通。
5. **阶段 5：全链路端到端自动化冒烟验收（Day 12 - Day 14）**
   - 编写 `scripts/smoke-m10.sh` 与 `make smoke-m10`，全绿通过并更新规划。
