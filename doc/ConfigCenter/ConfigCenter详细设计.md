# 配置中心（ConfigCenter）详细设计

> 版本：v0.1（2026-11-13，D-23）
> 上游设计：`../NexusCore架构设计总结.md`（§3.1 / §3.3.1 / §6.11 / §8 / §12.18）与 `../内核mvp/内核MVP详细设计.md`（§3.10）
> 定位：hub 内控制面模块，属于「Bot 注册、运行时与配置」原语；**不是第四原语、不是独立服务、不是总线主体**
> 状态：设计定稿；M1 落最小读集与激活事务，M2 落完整审批与 CLI

## 1. 定位与边界

配置中心解决一件事：插件、Bot、Agent 与内核的**期望配置**有唯一、版本化、可审批、可回滚的来源。

它存什么：`plugin / bot / agent / global` 四个配置域的期望状态（spec + 版本 + 审批记录）。

它不存什么：

1. **不存密钥值**：只存 `secret_ref`；hub 不解析、不下发密钥，执行者本地解析。
2. **不存业务数据**：PM 任务、笔记、记忆仍由各插件 schema 持有。
3. **不存运行时观测状态**：`bot_instances`、`kernel_plugin_processes`、心跳、Consumer Group 位点是运行表，不是配置。
4. **不存 manifest 正文**：manifest 是不可变安装制品（含 `binary_digest`，由 `security_bot` 审查）；配置中心只保存"当前激活的是哪个 manifest 版本"的引用与可变的运行配置。
5. **不包含 MCP / Skill**：当前没有对应运行时，不属于本设计；未来如引入，必须另立设计，不得把配置中心扩成"配置即运行"。

边界判定：如果一条数据会随进程运行实时变化（心跳、PID、任务状态），它属于运行表；如果它描述"下次启动 / 重载时应该怎样"，它属于配置中心。

## 2. 配置域

| scope_type | scope_id | 典型内容 | 读取方 | 预审 |
| --- | --- | --- | --- | --- |
| `plugin` | `plugin_id` | 启用的工具 / 权限、资源限制、日志级别、插件运行参数 | 插件进程（经 owner bot）/ create_bot | 工具或权限变化时 `security_bot` |
| `bot` | `bot_id` | prompt 引用、模型、`activation_mode`、`memory_profile` 声明、工具授权、限额 | bot-runtime / create_bot | 一般无需 |
| `agent` | Agent 节点 ID | 工具白名单、资源限制、project 绑定与策略 | agent-daemon | 工具白名单变化时 `security_bot` |
| `global` | 固定 `kernel` | 租约 / 重试 / 限流 / 保留期 / outbox 批大小等运行参数 | hub 各模块 | 一般无需 |

`config_key` 预留给同一 scope 的多份配置（默认 `default`）；MVP 每个 scope 只用 `default` 一份文档。

## 3. 数据模型（PostgreSQL，`kernel` schema）

### 3.1 配置文档

```sql
CREATE TABLE kernel.kernel_config_documents (
  doc_id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  scope_type          text NOT NULL CHECK (scope_type IN ('plugin','bot','agent','global')),
  scope_id            text NOT NULL,
  config_key          text NOT NULL DEFAULT 'default',
  version             bigint NOT NULL,
  status              text NOT NULL CHECK (status IN
                      ('draft','proposed','security_review','approved','active','retired','rejected')),
  spec                jsonb NOT NULL,
  schema_version      int NOT NULL DEFAULT 1,
  checksum            char(64) NOT NULL,          -- canonical JSON 的 sha256
  base_version        bigint,                     -- 提案基于的 active 版本（乐观并发）
  created_by          text NOT NULL,              -- bot_id / plugin_id / user_bot
  approved_by         text,                       -- 仅允许 user_bot
  approved_checksum   char(64),                   -- 审批时绑定的 checksum
  security_decision_id bigint,                    -- 可执行类须指向 approved 决定
  created_at          timestamptz NOT NULL DEFAULT now(),
  approved_at         timestamptz,
  activated_at        timestamptz,
  retired_at          timestamptz,
  UNIQUE (scope_type, scope_id, config_key, version)
);

-- 每个 scope+config_key 同时只有一个 active
CREATE UNIQUE INDEX kernel_config_one_active
  ON kernel.kernel_config_documents (scope_type, scope_id, config_key)
  WHERE status = 'active';

CREATE INDEX kernel_config_pending
  ON kernel.kernel_config_documents (status, created_at);
```

### 3.2 审计（append-only）

```text
kernel_config_audit
├── audit_id      bigint identity PK
├── doc_id        uuid
├── action        propose / security_review / approve / reject / activate / retire / rollback
├── actor         bot_id / plugin_id / user_bot / security_bot
├── from_status / to_status
├── checksum
├── reason        text
├── correlation_id
└── created_at    timestamptz
```

### 3.3 密钥引用（不存值）

```text
kernel_secret_refs
├── ref_id       text PK          # 稳定 ID，配置内以 {"$secret_ref":"..."} 引用
├── provider     text CHECK (provider IN ('env','file','keyring'))
├── locator      text NOT NULL    # 环境变量名 / 文件路径 / keyring key
├── description  text
├── created_by   text
└── created_at   timestamptz
```

规则：

1. `spec` 中禁止出现疑似密钥的明文字段（`password / token / secret / api_key / private_key` 等）；校验器命中即拒绝。
2. 激活中的配置引用的 `secret_ref` 不得删除；删除需先解除引用（或仅标记 deprecated）。
3. 审计与事件只记录 `ref_id`，不记录 `locator` 的敏感部分（可脱敏展示）。

### 3.4 版本与 checksum

- 版本按 `(scope_type, scope_id, config_key)` 单调递增；激活新版本时旧 active 转 `retired`。
- `checksum` 对规范化 JSON（键排序、无空白差异）计算 sha256；任何字节变化都改变 checksum。
- 审批绑定 `approved_checksum = checksum`；激活前再次比对，防止"批准后篡改"。

## 4. 状态机与审批

```text
draft ─propose→ proposed ─(可执行类)→ security_review ─approved?→ approved ─activate→ active ─retire→ retired
                    │                         │
                    └──────────────reject─────┴→ rejected
```

### 4.1 提案（Propose）

- 允许主体：CLI / `user_bot`、已认证的插件、Bot、Agent（经各自凭证）；提案必须携带 `base_version`（等于当前 active 版本，或 0 表示首次）。
- 服务端做 JSON Schema 校验（按 `scope_type + config_key` 选 schema）、大小上限、密钥字段扫描。
- 提案不生效，不进入运行投影。

### 4.2 安全预审（Review，security_bot）

- 触发：`plugin` 配置涉及工具 / 权限；`agent` 配置涉及工具白名单；以及任何被策略标记为可执行面的变更。
- `security_bot` 读取 proposal diff，写 `kernel_security_decisions(subject_type=config_proposal, decision=approved|rejected, reason, checksum)`。
- 无 `approved` 决定的可执行类提案不得进入 `approved`。

### 4.3 批准（Approve，仅 user_bot）

- 调用方必须 `from_bot_id=user_bot`；请求携带 `proposal_id + checksum`。
- 服务端校验 `checksum` 与文档一致，写入 `approved_by='user_bot'`、`approved_checksum`、`approved_at`。
- CLI 必须交互确认并回显 checksum 前 12 位与 diff 摘要；`--yes` 不允许跳过。
- 任何 Bot（含 security_bot / create_bot / monitor_bot）不得批准。

### 4.4 激活（Activate）

单事务完成：

```text
1. 校验：状态 approved、approved_checksum == checksum、可执行类 security_decision=approved、
         base_version == 当前 active 版本（否则 409 并提示重新提案）
2. 旧 active → retired；新文档 → active；写 activated_at
3. 更新运行投影（bot_manifest / kernel_plugins / kernel_agents 的 config_version 与必要字段）
4. 写 kernel_event_outbox(kernel.config.changed)
5. 写 kernel_config_audit
```

激活失败整体回滚，不出现"半生效"。

### 4.5 回滚

`Rollback(scope, config_key, to_version)` 生成一个新的提案（spec 取旧版本、`base_version=当前 active`），仍走 review/approve/activate；不直接把旧行改回 active，保证审计线性。

## 5. 读取、ACL 与分发

### 5.1 API

```text
Propose(scope_type, scope_id, config_key, spec, base_version) -> proposal_id
Review(proposal_id)                        # security_bot
Approve(proposal_id, checksum)             # user_bot
Activate(proposal_id)
Reject(proposal_id, reason)
Get(scope_type, scope_id, config_key[, version]) -> {spec, version, checksum}
ListVersions(scope_type, scope_id, config_key) -> []VersionSummary
ListPending([scope_type])                  # user_bot / CLI
```

### 5.2 读取 ACL

| 读取方 | 可读范围 |
| --- | --- |
| `user_bot` | 全部（含 pending diff） |
| `create_bot` | 全部 active 的 `bot / plugin`（生命周期决策所需），不含 pending |
| `monitor_bot` | 仅版本号 / checksum / 状态，不读 spec 正文 |
| `security_bot` | 待审 proposal 的 spec + diff，以及 active 摘要 |
| 插件 / Bot 进程 | 自己的 `plugin` / `bot` 配置 + `global` 的公开子集 |
| Agent | 自己的 `agent` 配置 + `global` 的公开子集 |

每次读取写审计（至少记录 actor / scope / version）；`secret_ref` 只返回引用。

### 5.3 分发与版本固定

1. 激活写 `kernel.config.changed` 持久事件，payload：`{scope_type, scope_id, config_key, version, checksum}`。
2. 订阅方收到事件后调用 `Get` 拉取；事件只是提示，正确性依赖版本号与 checksum。
3. Bot 实例在启动 / 重载时固定 `config_version`，写入 `bot_instances`；任务领取时继承到 `kernel_jobs.config_version`（审计用）。
4. Agent 在注册 / 心跳时上报 `config_version`；插件进程在 PID 登记与状态上报中携带。
5. `monitor_bot` 对比 active 与上报版本，发现 `config_version_drift` 报警；不自动重载。
6. 热更新策略：bot / agent 配置可在下一租约边界重载；manifest / 二进制级别变更走 drain + restart（由 create_bot / owner bot 执行）。插件配置变更由 owner bot 决定重载或重启 L2。

### 5.4 缓存与失效

- hub 内存缓存 active 文档，键为 `(scope, key, version)`；激活事务提交后通过 `pg_notify('kernel_config_changed', ...)` 与本地事件双重失效。
- 缓存不可作为正确性来源：`Get` 命中缓存也要校验版本；hub 重启后从表重建。

## 6. 与运行表 / 模块的关系

| 对象 | 关系 |
| --- | --- |
| `bot_manifest` | 保存不可变制品字段 + `config_version`/`config_doc_id`；可变字段（prompt、model、activation_mode）由 active bot 配置投影而来，写入只发生在激活事务 |
| `kernel_plugins` | 保存 manifest 摘要 + `config_version`；插件运行参数从配置文档读取 |
| `kernel_agents` | 保存身份、证书、能力上报；desired policy（工具白名单等）来自 agent 配置，`config_version` 记录已加载版本 |
| `kernel_plugin_processes` | 登记行增加 `config_version`，用于漂移观测 |
| `create_bot` | 只读 active 投影与配置，做 L1/L2 生命周期决策；不写配置 |
| `monitor_bot` | 只读版本 / 状态，报告漂移；不写配置 |
| `security_bot` | 对可执行类 proposal 写 security decision；不批准 |
| `user_bot` | 唯一批准人；只发控制消息 |
| `plugin_memory` | 作为普通插件拥有 `plugin` scope 配置（如 Dream 阈值、装配上限）；不享受特殊通道 |

## 7. CLI 契约（M2）

```text
NexusCore config list [--scope plugin|bot|agent|global]
NexusCore config get <scope_type> <scope_id> [--version N]
NexusCore config history <scope_type> <scope_id>
NexusCore config propose --file spec.json --scope <type> --id <scope_id> [--base-version N]
NexusCore config diff <proposal_id>
NexusCore config approve <proposal_id> --checksum <sha256>     # user_bot，交互确认
NexusCore config reject <proposal_id> --reason "..."
NexusCore config activate <proposal_id>
NexusCore config rollback <scope_type> <scope_id> --to <version>
NexusCore config pending
NexusCore config secret add --provider env --locator NAME --description "..."
```

`config set`（propose + approve + activate 一次完成）只在交互终端可用，必须显示 diff 与 checksum 并要求确认；等价于三次独立调用。

## 8. 故障与恢复

1. **hub 重启**：无内存状态权威；pending / approved 文档持久；缓存重建；激活事务未提交则整体回滚。
2. **NOTIFY 丢失**：订阅方下次启动 / 心跳 / 租约边界重新拉取；兜底轮询 active 版本摘要（如 30s）。
3. **投影失败**：激活事务包含投影更新，失败即回滚；若采用异步投影，必须带 `config_version` 幂等重试并报警。
4. **审批后篡改**：激活校验 `approved_checksum`，不匹配即 409。
5. **错误配置上线**：回滚走新提案；`security_bot` 可对 active 版本发起 `config_version_drift` / 安全事件，但不自动改配置。
6. **密钥引用失效**：执行者本地解析失败即报错；hub 不代解析，不把失败降级为明文。

## 9. 里程碑与验收

### M1（最小读集）

1. `kernel_config_documents / kernel_config_audit / kernel_secret_refs` DDL；部分唯一 active 索引。
2. 种子：`echo_bot` 的 active bot 配置（v1）与 `global` 默认参数，由迁移脚本写入（`status=active`，`approved_by=user_bot`，注明 seed）。
3. `Get` 只读 API + 读取 ACL + `kernel.config.changed` 事件（激活路径可暂只由 seed 触发）。
4. 版本固定：`bot_instances.config_version` / `kernel_jobs.config_version`。
5. 验收：种子配置可读；非授权角色读不到；版本固定正确。

### M2（完整审批）

1. Propose / Review / Approve / Activate / Reject / Rollback 全链路；`kernel_security_decisions` 集成。
2. CLI 全套；user_bot 交互批准（checksum + diff）。
3. 投影：`bot_manifest / kernel_plugins / kernel_agents` 的 `config_version` 与必要字段。
4. 漂移观测：`monitor_bot` 报 `config_version_drift`。
5. 验收：提案不生效；无 user_bot 批准不能 active；checksum 变更批准失效；可执行类缺 security 决定被拒；回滚生成新版本；secret 明文被拒且审计无值；越权读取被拒。

## 10. 与上游文档的映射

| 本文档章节 | 架构总结对应 |
| --- | --- |
| 1 / 2 | 3.1 原语 2 / 3.4 / 6.11 |
| 3 / 4 | 6.11 / 8.1 / D-23 |
| 5 | 6.7 / 6.9 / 6.11 |
| 6 | 3.3.1 / 5.5 / PluginMvp §6 |
| 7 / 8 / 9 | 11 Step 0 / Step 1、开发规划 M1 / M2 |
