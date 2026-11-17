# NexusCore

<div align="center">

**下一代个人智能多 Agent 微内核工作台**  
*Personal Intelligent Multi-Agent Workspace powered by Microkernel Architecture*

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://golang.org)
[![Vue 3](https://img.shields.io/badge/Frontend-Vue%203%20%2B%20Vite-4FC08D?logo=vue.js)](Frontend)
[![PostgreSQL](https://img.shields.io/badge/Database-PostgreSQL%2016+-4169E1?logo=postgresql)](https://www.postgresql.org)
[![Version](https://img.shields.io/badge/Release-v0.2.0-success)](#)

[产品特性](#核心亮点) • [使用场景](#典型应用场景) • [极速上手](#快速开始) • [架构理念](#架构设计理念) • [开源协议](#许可证)

</div>

---

## 什么是 NexusCore？

**NexusCore** 是一款专为开发者与深度 AI 用户打造的**个人多智能体（Multi-Agent）微内核工作台**。

传统 AI 助手往往面临三大困局：**“金鱼记忆”带来的高额重复上下文开销**、**多 Agent 协同中的无序死循环与 Token 爆炸**，以及**让大模型直接裸奔执行宿主命令的安全失控**。

NexusCore 通过**微内核架构（Microkernel）**与**脑手分离机制（Brain-Hand Separation）**，将 AI 的**认知决策**与**物理执行**彻底解耦：
- **认知层（Bot）**：专注领域思考、协同决策与长效知识沉淀，不直接接触宿主底层。
- **执行层（Agent Probe）**：跨设备的受控物理探针，通过加密反向通道接受调度，工具操作透明留痕。
- **微内核（Hub）**：提供高可靠持久消息总线、两级进程生命周期管理与安全审批中台。

无论是全栈研发协作、知识资产积累还是本地跨设备自动化，NexusCore 都能为你构建一个稳定、持久、自主掌控的智能专属团队。

---

## 核心亮点

### 💬 1. 秩序井然的多 Bot 协同群聊
- **人类主导的精准调度**：严格遵循“**仅用户显式 @ 唤醒**”机制，Bot 回复中的 @ 自动转化为展示标签，从根源上杜绝 Bot 之间互相无休止串联对话造成的死循环与 Token 消耗。
- **团队化分工协作**：自由创建架构师、研发专家、审查员等不同专属角色，在一个窗口内自如呼叫与切换。

### 🧠 2. 深度对齐长效单体记忆栈
- **四层记忆体系**：对齐现代文件系统级单体记忆设计：
  $$\text{Transcripts (原始会话事实)} \longrightarrow \text{Memory Notes (离散经验)} \longrightarrow \text{Representations (结构化认知)} \longrightarrow \text{Injected Context (常驻切片)}$$
- **200 行铁律（零膨胀）**：核心记忆文件 `MEMORY.md` 严格限定在 200 行以内，保证 **85%~93%** 的极高 Prompt Cache 命中率，彻底消灭二次方级 Token 膨胀。
- **Auto Dream 离线整合**：在系统空闲期自动进行记忆提取、去重、归纳与剪枝，实现认知沉淀的削峰填谷。
- **一键重载生效**：Bot 画像、人格指令与核心记忆随改随用，通过 Supervisor 毫秒级优雅重启立即生效。

### 🦾 3. 脑手分离与安全探针
- **受控跨设备物理执行**：Bot 严禁直接在主机运行未经审核的 Shell 命令，所有环境操作与代码运行统一经微内核路由至远端或本机的受控物理探针（Agent Daemon）执行。
- **全链路操作轨迹审计**：内置沙盒工具执行审计，命令输入、执行毫秒耗时、终端输出全部留痕，一览无余。

### 🛡️ 4. 严谨的安全审批与代批中台
- **高危操作阶梯式审批**：敏感系统配置与高风险工具调用遵循 `提议 (Propose) → 安全预审 (Security Review) → 人工批准 (User Approve)` 严谨链路。
- **中台 Bot 受控代批（RBAC）**：支持授权合规的中台 Bot 代行日常安全操作，严禁越权越级，平衡效率与极致安全。

### 🧩 5. 现代化微前端与插件生态
- **双段式工作台（纪事与工坊）**：左侧聚合历史上下文与知识纪事，右侧居中极简输入框，兼顾专注与全景浏览。
- **插件即应用**：基于微前端（Vite ESM 动态挂载）与两级进程模型，群聊、Bot 工厂、工具中心等核心功能均为可插拔组件，生态扩展随心所欲。
- **丰富工具生态集成**：原生兼容系统内置工具、自定义可编辑 Skill 以及外部标准化 MCP（Model Context Protocol）协议。

---

## 典型应用场景

| 场景 | 赋能方式 | 体验提升 |
| :--- | :--- | :--- |
| **全栈敏捷开发团队** | 创建专门的 `Architect`、`Coder` 与 `Reviewer` Bot，在群聊中按步骤 @ 召唤，联动本地代码探针执行编译、测试与重构。 | 多智能体各司其职，无相互干扰与上下文污染。 |
| **个人知识与项目管家** | 借助 Bot 单体长效记忆与 Auto Dream，持续记录个人偏好、业务上下文与技术沉淀。 | 对话越久越默契，打破每次对话都要重新交代背景的痛点。 |
| **跨设备自动化运维** | 将受控探针部署在多台服务器、边缘设备或开发机上，统一在 Web 端通过中枢 Bot 调度与监管。 | 反向连接穿透内网，命令执行可视透明，免去繁琐 SSH 切换。 |
| **AI 工具与 Skill 实验场** | 统一挂载多种 MCP 服务、本地 CLI 工具和定制脚本，通过权限矩阵动态授权给指定的 Bot。 | 即插即用，安全隔离，轻松测试前沿 Agent 工具生态。 |

---

## 快速开始

### 环境依赖
- **Go**: 1.26+
- **Node.js**: 18+ (推荐 20 或 22)
- **PostgreSQL**: 16+（内置 Docker Compose 配置）

### 1. 克隆代码与编译
```bash
git clone https://github.com/888xiyan/NexusCore.git
cd NexusCore

# 编译微内核组件（hub, bot-runtime, agent-daemon, cli, migrate）
make build

# 编译独立业务插件
make build-plugins
```

### 2. 初始化数据库
```bash
# 启动本地 PostgreSQL 容器（默认端口 55432）
make compose-up

# 执行数据库初始化迁移：运行角色 nexus_kernel 无 DDL 权限，迁移必须使用 nexus_migrator
export NEXUS_PG_KERNEL_DSN="postgres://nexus_kernel:nexus_dev_kernel@127.0.0.1:55432/nexus"
export NEXUS_PG_MIGRATE_DSN="postgres://nexus_migrator:nexus_dev_migrate@127.0.0.1:55432/nexus"
make migrate-up
```

### 3. 一键启动全栈工作台
```bash
# 自动拉起 Hub (网关:7200) + 物理探针 + 群聊插件 + Web 前端 (:5173)
make launch-m4
```

启动完成后，打开浏览器访问：
👉 **`http://localhost:5173`** 即可开启你的 NexusCore 智能工作台之旅。

---

## 架构设计理念

```
 ┌─────────────────────────────────────────────────────────────┐
 │               Frontend (Vue 3 + Vite 微前端)                 │
 │     [纪事与工坊视区]   [@Mention 沉浸输入]   [沙盒轨迹审计]    │
 └──────────────────────────────┬──────────────────────────────┘
                                │ HTTP / SSE 流式中继 (:7200)
 ┌──────────────────────────────▼──────────────────────────────┐
 │                      Hub 微内核底座                          │
 │  ┌───────────────────────┐   ┌───────────────────────────┐  │
 │  │ PG 原生持久事件与队列  │   │ Bot 运行时 (Supervisor)   │  │
 │  └───────────────────────┘   └───────────────────────────┘  │
 │  ┌───────────────────────┐   ┌───────────────────────────┐  │
 │  │ 安全配置中心与审批流  │   │ 跨设备反向 TCP 传输路由   │  │
 │  └───────────────────────┘   └───────────────────────────┘  │
 └──────────────┬───────────────────────────────┬──────────────┘
                │ 控制面与总线调度              │ 反向 TCP 探针通信
 ┌──────────────▼─────────────┐   ┌─────────────▼──────────────┐
 │     Bot 认知决策层         │   │      Agent 物理执行层       │
 │   - 单体记忆 (Claude Code) │   │   - 受控 Shell / CLI       │
 │   - MEMORY.md ≤ 200 行     │   │   - 跨设备物理操作探针     │
 │   - Auto Dream 离线整合    │   │   - 工具执行轨迹与沙盒     │
 └────────────────────────────┘   └────────────────────────────┘
```

NexusCore 严守以下核心工程规范：
1. **微内核最小原语化**：内核仅保留持久队列、Bot 运行时与跨设备传输路由，业务语义百分之百下沉至插件层。
2. **纯粹信封通讯**：总线消息信封仅承载身份与因果链，杜绝业务耦合。
3. **两级进程隔离**：Bot 进程与插件宿主进程生命周期严格隔离，具备防 PID 复用与安全级联回收机制。

---

## 常用操作与调试命令

| 常用命令 | 作用说明 |
| :--- | :--- |
| `make launch-m4` | 开发者一键启动全套服务栈（微内核 + 探针 + 插件 + 前端） |
| `make build` | 编译微内核全部核心二进制到 `bin/` |
| `make build-plugins` | 编译独立业务插件到 `bin/plugins/` |
| `make test` | 快速运行单元测试 |
| `make test-pg` | 运行真实 PostgreSQL 集成测试 |
| `make smoke-m9` | 运行 M9 全局 UI 原型、标准群聊与沙盒工具轨迹全链路冒烟验收 |
| `make smoke-m10` | 运行 M10 工具中心、MCP/Skill 插件化治理与中台审批闭环验收 |
| `make clean` | 清除编译输出产物 |

---

## 许可证

本项目基于 [MIT 许可证](LICENSE) 开源。欢迎自由使用、修改与分发。
