# M6 阶段详细设计定版：Bot 工厂与 Agent 绑定插件

> **阶段代号**：M6（Bot Factory & Agent Binding Plugin）  
> **文档状态**：正式定版（Approved & Finalized）  
> **编制日期**：2026-11-15  
> **核心战略**：降低多 Bot 扩展与定制门槛，提供轻量便捷的 Bot 创建与自由画像定制能力，支持将 M5 搜寻到的本地 Agent（Claude Code / Codex / Pi-Agent / Aider / Mock Agent 等）无缝绑定赋予 Bot。**统一采用配置中心全局单 API Key 统筹所有 Bot**，暂不引入复杂多模型路由。**恪守微内核硬约束**：Bot 为简单 Agent（禁 Shell、禁长思考），其单体记忆严格遵循 Claude Code 四层栈（`MEMORY.md` ≤ 200 行），画像与配置变更通过**重启策略（Restart Policy）**毫秒级生效。

---

## 1. 业务背景与设计理念

### 1.1 为什么需要 M6？
在 M1~M5 阶段，NexusCore 顺利完成了微内核底座（M1）、配置中心与两级进程生命周期（M2）、内置四层单体记忆与群聊插件（M3）、Vue 3 前端工作台（M4）以及宿主机本地 Agent 资产探测与防噪音蒸馏调度闭环（M5）。

然而，当前系统中可用的 Bot 角色主要依赖预置种子（如 `ops_bot`、`code_bot` 等），普通用户或上层业务无法低成本地按需派生专职智能体：
1. **创建门槛高**：此前需要手动通过 SQL 或底层 CLI 注册 `bot_manifest` 并手工初始化记忆目录；
2. **画像定制不便**：缺少针对特定职责（如代码审查员、运维巡检员、文档整理员）的标准化 Persona / Prompt 蓝图模板；
3. **Agent 绑定脱节**：M5 虽然探测到了本地已安装的各种 Agent CLI（`claude`、`codex`、`pi` 等），但缺乏直观的绑定机制将特定 Agent 赋予指定 Bot；
4. **API Key 配置繁琐**：若每个 Bot 都需要单独配置大模型 API Key 和 Endpoint，运维负担沉重且极易泄露密钥。

**M6 的核心使命就是提供一个标准化的 Bot 工厂插件（`plugin_bot_creator`），以全局单 API Key 为底座，实现“一键创建专职 Bot、自由画像定制、严格 200 行单体记忆保障、勾选绑定本地 Agent、Supervisor 毫秒级重启生效”的完整闭环。**

### 1.2 核心设计理念与铁律边界

```
[用户 / 前端工作台] ──填写蓝图/选模板/勾选Agent──> [plugin_bot_creator 独立插件]
                                                         │
                        ┌────────────────────────────────┴────────────────────────────────┐
                        ▼                                                                 ▼
             [1. 静态安全与蓝图校验]                                             [2. 单体记忆四层栈初始化]
             - 正则: ^[a-z0-9_]{5,64}$                                            - data/bots/<bot_id>/memory/
             - 黑名单拦截: user_bot/create_bot/                                  - transcripts/ (Append-Only)
               monitor_bot/security_bot                                          - notes/ & representations/
             - 严格行数限制: MEMORY.md ≤ 200 行                                   - MEMORY.md 初始模板 (≤200行)
                        │                                                                 │
                        └────────────────────────────────┬────────────────────────────────┘
                                                         ▼
                                       [3. 配置中心与 bot_manifest]
                                       - 全局继承 global.llm 凭证
                                       - 绑定 allowed_agents / agent-cli
                                       - 写入 bot_manifest 与 bots 期望表
                                                         │
                                                         ▼
                                       [4. Supervisor 优雅重启生效]
                                       - POST /v1/bots/{bot_id}/restart
                                       - 毫秒级拉起独立 bot-runtime
                                       - 挂载四层单体记忆与绑定 Agent
                                                         │
                                                         ▼
[多 Bot 群聊视区] <──即刻在线可 @ 协同──────────────────┘
```

1. **脑手分离（Brain-Hand Separation）**：
   - 新建的 Bot 依然是纯粹的**简单 Agent（Brain）**，负责理解用户意图、读取自身单体记忆、并将需求结构化调度给绑定的物理 Agent。
   - Bot 自身绝不持有 Shell 权限，所有环境与物理操作统一经 Hub Transport 反向调度到目标探针执行。
2. **全局单 API Key 统筹（Global Single API Key）**：
   - 在内核配置中心部署全局 LLM 配置（`scope_type='global'`, `scope_id='global'`, `config_key='llm'`），新建 Bot 默认继承，避免每个 Bot 单独配置密钥的复杂性。
3. **单体记忆 200 行硬约束（Claude Code MEMORY.md ≤ 200 行）**：
   - 新建 Bot 必须自动初始化四层单体记忆目录（`transcripts/`, `notes/`, `representations/`, `MEMORY.md`）；
   - `MEMORY.md` 初始模板必须在插件层和内核层接受双重强校验：超过 200 行直接抛出校验失败并拒绝创建。
4. **两级进程规范（L1 Bot + L2 插件服务）**：
   - Bot 工厂独立封装为 `plugin_bot_creator`（L2 二进制），由专职 Owner Bot `bot_creator_bot`（L1 进程）自治管理生命周期，绝不破坏微内核边界。
5. **重启策略生效（Restart Policy）**：
   - Bot 创建或画像修改后，不搞复杂的运行时动态热重载状态机，直接通过 Supervisor 触发独立 Bot 进程毫秒级冷重启生效。

---

## 2. 总体架构与模块划分

| 模块名 | 源码位置 | 运行形态 | 核心职责 |
| --- | --- | --- | --- |
| **Bot 工厂插件**<br/>(`plugin_bot_creator`) | `plugins/plugin_bot_creator/` | 独立 L2 插件二进制 | 提供标准插件清单（Manifest）；暴露蓝图合规校验、模板推荐、Bot 创建、Agent 绑定工具接口；由 `bot_creator_bot` 管理。 |
| **蓝图编排与记忆初始化器**<br/>(`Blueprint & Memory Initializer`) | `plugins/plugin_bot_creator/creator/` | 插件内部核心包 | 实现 `BotBlueprint` 结构体验证、保留名过滤、MEMORY.md 行数校验、四层单体记忆目录与模板自动装配。 |
| **预设角色模板库**<br/>(`Role Templates`) | `plugins/plugin_bot_creator/templates/` | 静态模板定义 | 内置常用专职智能体模板（如 `reviewer` 代码审查员、`coder` 编程辅助、`ops` 运维排错专家）。 |
| **内核 Supervisor 参数装配**<br/>(`Supervisor Agent Binding`) | `hub/supervisor/` | 内核核心运行时 | 从 `bot_manifest` 的 capabilities 中识别绑定的 Agent CLI（如 `agent-cli:<cli>`），在拉起 `bot-runtime` 时动态注入 `-agent-cli` 参数。 |
| **前端创建向导**<br/>(`Frontend Bot Wizard`) | `Frontend/src/` | Vue 3 UI 视区扩展 | 提供抽屉或弹窗向导，可视化填写画像、实时统计 MEMORY.md 行数、勾选 M5 探测到的可用本地 Agent，一键保存并自动上线。 |

---

## 3. Bot 工厂插件详细设计 (`plugin_bot_creator`)

### 3.1 清单契约 (`manifest.json`)

遵循 `nexus-plugin-dev` 技能规范与 Manifest Schema 强校验规则：

```json
{
  "plugin_id": "plugin_bot_creator",
  "name": "Bot Factory & Agent Binding Plugin",
  "version": "1.0.0",
  "plugin_type": "executor",
  "description": "专职 Bot 实例蓝图定义、合规校验、四层单体记忆初始化、本地 Agent 绑定与生命周期协同插件",
  "binary": "/home/yhy/Projects/NexusCore/bin/plugins/plugin_bot_creator",
  "publishing": [
    "plugin_bot_creator.bot.created",
    "plugin_bot_creator.bot.updated"
  ],
  "subscribing": [
    "bot.command.create_bot"
  ],
  "providing_tools": [
    "bot_creator.validate_blueprint",
    "bot_creator.create_bot",
    "bot_creator.update_bot",
    "bot_creator.list_templates",
    "bot_creator.list_available_agents"
  ],
  "requiring_tools": [],
  "bot_templates": [
    {
      "bot_id": "bot_creator_bot",
      "role": "plugin",
      "display_name": "Bot Creator Bot",
      "activation_mode": "resident",
      "capabilities": [
        "bot_creation",
        "blueprint_management"
      ]
    }
  ],
  "schema_version": 1,
  "binary_digest": ""
}
```

### 3.2 蓝图数据契约与校验规范

```go
package creator

// BotBlueprint Bot 蓝图配置结构
type BotBlueprint struct {
    BotID         string   `json:"bot_id"`                   // 唯一标识 (^[a-z0-9_]{5,64}$)
    DisplayName   string   `json:"display_name"`             // 显示名称 (如 "代码审查专家")
    Role          string   `json:"role"`                     // 角色分类 (如 "reviewer", "coder", "ops")
    SystemPrompt  string   `json:"system_prompt"`            // 核心系统指令
    Persona       string   `json:"persona"`                  // 人格与语气偏好
    InitialMemory string   `json:"initial_memory"`           // 初始 MEMORY.md 内容 (严格限制 ≤ 200 行)
    BoundAgentCLI string   `json:"bound_agent_cli"`          // 绑定的本地 Agent CLI (如 "claude", "codex", "pi")
    BoundAgentID  string   `json:"bound_agent_id"`           // 绑定的目标探针 ID (默认 "loopback_agent")
    ActivationMode string  `json:"activation_mode"`          // "lazy" 或 "resident" (默认 "lazy")
}

// ValidationResult 蓝图合规检查结果
type ValidationResult struct {
    Valid       bool     `json:"valid"`
    MemoryLines int      `json:"memory_lines"` // 行数统计
    Violations  []string `json:"violations"`   // 违规原因列表
}
```

#### 校验铁律（Fail-Fast）：
1. **标识符正则**：必须满足 `^[a-z0-9_]{5,64}$`；
2. **系统保留名**：严禁创建 `user_bot`、`create_bot`、`monitor_bot`、`security_bot`；
3. **单体记忆硬约束**：`InitialMemory` 行数必须满足 $\le 200$ 行；超过 200 行直接标记 `Valid = false`；
4. **显示名称与指令**：`DisplayName` 与 `SystemPrompt` 不能为空。

---

## 4. 全局单 API Key 统筹与 Agent 绑定机制

### 4.1 全局 LLM 凭据规范
在微内核配置中心内，统一部署全局 LLM 凭证文档：
- **Scope**: `scope_type = "global"`, `scope_id = "global"`, `config_key = "llm"`
- **Spec 结构**：
```json
{
  "provider": "openai_compatible",
  "endpoint": "https://api.openai.com/v1",
  "model": "gpt-4o",
  "secret_ref": "global_llm_api_key"
}
```
新建 Bot 统一继承该全局凭证，避免了每个 Bot 重复声明密钥带来的管理负担。

### 4.2 Agent 资产绑定与运行时映射
当在蓝图中指定 `BoundAgentCLI`（例如 `claude` 或 `nexus-mock-agent`）时：
1. **Manifest 存储**：插件向内核注册 `bot_manifest` 时，在 `capabilities` 数组中注入标签：
   `capabilities = ["chat", "agent-cli:<BoundAgentCLI>", "agent-id:<BoundAgentID>"]`
2. **Supervisor 自动提取**：
   内核 Supervisor 在 `loadManifest` / `startLocked` 启动 `bot-runtime` 进程时：
   - 扫描 `capabilities`，若发现 `agent-cli:<cli>`，提取并追加命令行启动参数：
     `-agent-cli <cli>`
   - 若发现 `agent-id:<id>`，提取并追加命令行启动参数：
     `-agent-id <id>`
3. **bot-runtime 直接生效**：
   `bot-runtime` 进程启动后，原生持有 `-agent-cli` 与 `-agent-id`，当收到群聊任务时，直接唤醒反向 TCP 路由，将用户需求结构化发往下层目标 Agent CLI 执行，并经防噪音蒸馏器精选回显。

---

## 5. 单体记忆四层栈自动初始化

遵循内置 Claude Code 单体记忆架构，新 Bot 创建时，插件自动执行目录与文件脚手架创建：

```
data/bots/<bot_id>/memory/
├── transcripts/              # Append-Only 原始流水层 (自动建目录)
├── notes/                    # 离散显著性经验笔记 (自动建目录)
├── representations/          # 结构化主题 Wiki 目录 (自动建目录)
└── MEMORY.md                 # 常驻注入切片 (严格限制 ≤ 200 行)
```

初始化模板格式：
```markdown
# <display_name> 核心单体记忆

## 1. 核心定位与 Persona
- **身份**: <role>
- **风格**: <persona>

## 2. 调度执行能力
- **绑定 Agent**: <bound_agent_cli>
- **执行探针**: <bound_agent_id>

## 3. 初始规则与注意事项
- 遵循脑手分离铁律，严禁直接执行 Shell。
- 接收模糊需求后主动提炼为结构化参数。
- 严格遵循精简 Markdown 格式回复用户。
```

---

## 6. 预设角色模板库 (`templates/`)

为了方便用户开箱即用，插件预置三类典型专职 Bot 模板：

| 模板标识 | 角色名称 | 建议绑定 Agent | 适用场景 |
| --- | --- | --- | --- |
| `reviewer` | 代码审查专家 | `claude` / `nexus-mock-agent` | 专职 Code Review，重点检查代码规范、并发安全与设计偏离 |
| `coder` | 架构编程助手 | `codex` / `claude` | 专职算法实现、重构与复杂代码编写 |
| `ops` | 运维排错专家 | `pi` / `aider` | 专职环境检查、容器日志排查与构建脚本调试 |

---

## 7. 前端工作台界面交互设计

在 Vue 3 前端工作台增加 **Bot 工厂向导抽屉（`BotCreateDialog.vue`）**：
1. **基础信息区**：输入 Bot ID、显示名称、选择分类角色；
2. **模板选择器**：一键套用 Reviewer / Coder / Ops 预设；
3. **Persona 与 System Prompt 编辑区**；
4. **单体记忆实时行数指示器**：
   - 实时监听 `InitialMemory` 文本框输入；
   - 行数 $\le 200$ 时：显示绿色徽章 `行数: X / 200`，允许保存；
   - 行数 $> 200$ 时：显示红色警示徽章 `超限: X / 200 (超出限制)`，禁用“创建并生效”按钮；
5. **本地可用 Agent 勾选列表**：
   - 从后端拉取 M5 探测到的所有本地已安装 Agent（Claude Code / Codex / Pi-Agent / Mock 等）；
   - 单选或下拉指定该 Bot 绑定的默认 Agent CLI；
6. **一键保存并自动重启生效**：
   - 点击“创建并上线”，后端完成蓝图校验、四层记忆初始化与注册；
   - 联动 Supervisor 毫秒级拉起进程；
   - 前端群聊视区实时刷新，输入框 `@Mention` 候选列表中即刻出现新 Bot。

---

## 8. 自动化验收标准 (M6 Exit Criteria)

1. **蓝图与安全拦截**：
   - 尝试创建系统保留名（`user_bot` / `create_bot` 等）被拦截；
   - 尝试注入 > 200 行的 `MEMORY.md` 被严格拦截；
2. **新 Bot 顺利创建与单体记忆落盘**：
   - 成功创建专职 Bot `user_code_reviewer`；
   - 自动生成 `data/bots/user_code_reviewer/memory/` 目录；
   - `MEMORY.md` 存在且行数 $\le 200$ 行；
3. **Agent 绑定与 Supervisor 重启生效**：
   - Bot 成功绑定目标 Agent CLI（如 `nexus-mock-agent` 或宿主机外部 CLI）；
   - 触发 Supervisor 重启后，进程就绪并向 Hub 发起 Heartbeat；
4. **端到端群聊调度闭环**：
   - 模拟用户在群聊发消息 `@user_code_reviewer 请审查当前函数性能`；
   - `user_code_reviewer` 正确响应，挂载自身单体记忆，并成功调度其绑定的目标 Agent 执行任务；
   - 终端输出经过防噪音蒸馏后，以结构化 Markdown 卡片回复群聊；
5. **冒烟脚本验收**：
   - 运行 `bash scripts/smoke-m6.sh`（或 `make smoke-m6`），全部链路自动化通过。
