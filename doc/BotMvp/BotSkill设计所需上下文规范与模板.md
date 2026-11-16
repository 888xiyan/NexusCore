# Bot Skill 设计所需上下文要素规范与填空模板

> 本文档规范了在为 NexusCore 体系中的 Bot 设计或生成具体工具 Skill 时所需的上下文要素，并提供了可直接复制填充的结构化表单模板。

---

## 1. 为什么需要严谨的 Skill 上下文？

NexusCore 的系统架构采用**“脑手分离（Brain-Hand Separation）”**与**“两阶段噪音拦截（Distillation & Anti-Noise）”**：
- Bot 自身无权执行 Shell；
- 工具实际在物理探针（`agent-daemon`）或宿主环境执行；
- 执行产生的巨量日志必须被蒸馏为结构化摘要（Distilled Artifact）再送入 Bot 上下文。

因此，给 Bot 生成工具 Skill 不能只是简单的文字描述，必须明确**调用场景、执行宿主、输入 Schema、结果蒸馏规则与安全授权策略**。

---

## 2. 核心上下文 5 大维度

### 维度一：业务目标与触发场景（What & Why）
- **Skill 标识（工具名）**：如 `code_lint_checker`、`pg_slow_query_diagnoser`、`git_branch_cleaner`。
- **业务职责**：该 Skill 帮助 Bot 解决的具体领域问题。
- **正向使用场景**：用户发出何种意图或命令时，Bot 应该主动决策调用此 Skill。
- **负向排除规则**：哪些看似相似但绝对不应该调用此 Skill 的边界场景（防止 Tool 误调用）。

### 维度二：执行宿主与物理边界（Where & How）
明确工具底层执行代码的位置与调用协议：
- **执行宿主**：
  - 本地设备探针（开发者本机 `agent-daemon`）
  - 远程服务器探针（远程测试/生产环境 `agent-daemon`）
  - 外部/本地 Coding CLI（如 Claude Code CLI、Codex CLI、Docker 容器内部）
  - 微内核内部 HTTP/gRPC 服务接口（如查询内核任务队列、查询配置中心）
- **底层实际执行的物理动作**：
  - 执行特定 CLI 命令（如 `git status --porcelain`、`go test -v`、`pg_isready`）
  - 运行专用可执行程序或分析脚本
  - 发送 REST / gRPC 请求

### 维度三：输入参数规范（Inputs & Constraints）
- **必需参数（Required）**：调用此工具必传的字段、类型（string、number、boolean、array）及业务含义。
- **可选参数（Optional）**：选填字段与默认值。
- **输入约束**：路径格式（绝对路径/相对路径）、支持的文件后缀、数值安全区间等。

### 维度四：噪音拦截与结果蒸馏规则（Distillation & Anti-Noise）
防止大段日志撑爆 Bot 上下文与造成记忆库污染：
- **原始数据形态（Raw Stream）**：底层执行预计输出的大文本（如编译输出、全量 JSON、5000 行标准输出）。
- **Bot 关心的精炼摘要（Distilled Artifact）**：Bot 做出下一步判断必须依赖的核心字段。
  - 例如：*“只需提取：执行状态码、错误发生的文件路径与行号、最后 3 行核心异常信息”*。

### 维度五：安全等级与人工确认（Security & HITL）
- **操作属性**：
  - 只读安全（Read-Only）：如读取系统状态、代码只读检查（通常直接放行）。
  - 高危变更（Mutating / Destructive）：如写文件、修改配置、执行 DDL、删除分支。
- **人类确认机制（Yield Confirm）**：
  - 是否需要在实际执行前挂起任务，向用户/前端弹出确认卡片（Yes/No）以获取批准。

---

## 3. 即用型上下文填空模板

设计新 Skill 时，直接复制并填写以下模板：

```markdown
【Skill 基本信息】
- Skill 标识（工具名）：例如 git_repo_status
- 适用 Bot 角色：例如 user_git_assistant_bot
- 业务目标与场景：[描述这个工具做什么，什么时候触发]
- 严禁调用的场景：[描述不该调用的场景]

【执行实体与底层动作】
- 执行宿主：[例如：本地 agent-daemon / 远程 Linux 服务器 / 某个 CLI]
- 物理执行形式：[例如：执行 bash 命令 `git status --porcelain`，超时 5 秒]

【输入参数】
- 必填参数：
  - 参数名1: [类型, 格式约束, 说明]
- 可选参数：
  - 参数名2: [类型, 默认值, 说明]

【结果蒸馏规则（防 Token 膨胀）】
- 原始输出特征：[如：git status 的全量终端输出]
- 提炼给 Bot 的摘要内容：[例如：提取变更文件数、冲突文件列表、当前分支名]

【安全与授权】
- 操作属性：[只读 / 产生系统变更]
- 确认机制：[免确认直接执行 / 触发高危挂起等待用户批准]
```

---

## 4. 标准产出物规范

基于上述上下文，AI / 开发者应为该 Skill 生成以下 3 项规范交付物：

1. **`SKILL.md` 标准文档**：
   - 包含 YAML Frontmatter（`name`, `description`）以及使用指南与示例。
2. **结构化 Tool Schema (JSON Schema)**：
   - 标准 Function Calling / Tool Call 协议格式，声明 properties、required 与 type 约束。
3. **Bot 侧 System Prompt 提示词集成片段**：
   - 指导 Bot 在何种情况下触发该 Tool Call，以及收到蒸馏结果后如何解析并呈现给用户。
