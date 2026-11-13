# 记忆插件（plugin_memory）详细设计

> 版本：v0.1（2026-11-12，D-22 记忆插件化）  
> 上游设计：`../NexusCore架构设计总结.md`（§3.1 / 3.2 / 3.3.1 / 3.4 / 6.7 / 7 / 8）与 `../PluginMvp/PluginMvp详细设计.md`（manifest / 生命周期 / 命名 / 卸载清理）  
> 定位：knowledge 类插件，首个复杂插件实例；**内核与内核侧四角色不感知记忆**  
> 状态：设计定稿，M3 落 v1（存储 / 笔记 / 只读图），M4 落认知闭环

## 1. 定位与边界

`plugin_memory` 承接 NexusCore 的全部记忆与知识能力：四象限记忆模型、个人金库（Obsidian 通用笔记与全局公理）、知识图谱、版本历史与晋升、Context Assembler（上下文装配）、Memory Arbiter（记忆仲裁）、用户级 Bot 默认个人记忆（`personal_stack_v1`）与 Auto Dream 整合。

它首先是一个普通插件，其次才是“记忆的归属”：

1. 通过 `plugin_manifest` 注册，纳入插件生命周期（install → security_review → approve → active → upgrade / disable / uninstall）。
2. 独立二进制、独立 schema、独立数据库账号；内核不持有其 DSN、不建其表、不读其数据。
3. 不是总线主体：其注册的 `memory_bot` 是总线上的身份；事件经单次投递委托以 `memory_bot` 名义回投内核入口。
4. 删除本插件，内核与其它插件仍可运行；依赖记忆的 Bot 退化为“无个人上下文 / 无项目记忆”执行。

内核侧四角色（`user_bot / create_bot / monitor_bot / security_bot`）不使用、不读取、不写入任何记忆，其行为不因本插件存在与否而改变。

## 2. 插件形态

### 2.1 Manifest 草案（M3 冻结）

```text
plugin_manifest
├── plugin_id           # plugin_memory
├── display_name        # 记忆与知识
├── plugin_type         # knowledge
├── version             # semver
├── binary              # memory 知识服务（Go）
├── binary_digest       # sha256
├── namespace           # memory（默认；不新增额外 namespace）
├── subscribing[]       # 事件订阅：task.state.changed（scope=授权 project）
├── publishing[]        # plugin_memory.memory.updated / plugin_memory.memory.invalidated
├── providing_tools[]   # read / search / create / promote / assemble_context / audit / health
├── requiring_tools[]   # 无（不依赖他人工具）
├── bot_templates[]     # [memory_bot]：lazy，per_task；[0] 即 owner bot，持有知识服务进程
├── resource_limits     # {cpu_ms, memory_mb}
├── required_tables[]   # memory_entities 等（见第 3 节）
└── schema_version      # 1
```

### 2.2 组件

| 组件 | 语言 | 职责 | 里程碑 |
| --- | --- | --- | --- |
| 知识服务 | Go | 图谱事务、金库访问、版本与晋升、ACL、审计、本地 Outbox、工具服务端 | M3 |
| memory-worker | Python | Context Assembler、Memory Arbiter、抽取 / 蒸馏 / Embedding、Auto Dream | M4 |
| `memory_bot` | Go（bot-runtime） | 插件在总线上的身份；订阅事件、执行插件任务、回投事件 | M3 |
| 发布器 / 摄取服务 | Go（可并入知识服务） | Obsidian 单向发布 + File Watcher 变更摄取（与 `plugin_obsidian` 协作） | M3 |

进程关系（两级进程模型，见 [PluginMvp §6](../PluginMvp/PluginMvp详细设计.md)）：`memory_bot` 是 **Bot 进程（L1）**，由 `create_bot` 决策、内核 supervisor exec；知识服务是 **插件进程（L2）**，即 `plugin_manifest.binary`，由 `memory_bot`（owner bot = `bot_templates[0]`）exec 与回收，PID 经 `channel=health` 登记到 `kernel_plugin_processes` 供 `monitor_bot` 只读巡检。memory-worker 是 **L3**，由知识服务按需拉起（M4 允许库内占位），内核不登记，回收按进程组级联。全部属于插件自己的进程域：supervisor 对 L2 只做强制终止与孤儿级联回收，优雅关闭由 `memory_bot` 通知知识服务完成。

### 2.3 与内核的关系

```text
Bot（任意插件侧） ──工具调用 plugin_memory.*──▶ 知识服务（插件内，进程间/内部协议）
知识服务 ──事务写──▶ memory / vault schema
知识服务 ──memory_outbox──▶ memory_bot ──单次投递委托──▶ hub ingress ──▶ 总线
hub ──事件投递（task.state.changed 等）──▶ memory_bot ──▶ 知识服务
```

内核只承担三件事：为 `memory_bot` 提供注册 / 任务 / 事件通道；校验委托与因果链；持久化审计。所有记忆语义（授权、版本、晋升、装配）在插件内闭环。

## 3. 存储模型

### 3.1 schema 与账号

| schema | 内容 | 角色 |
| --- | --- | --- |
| `memory` | 项目公共记忆、Bot 角色经验、任务现场、版本历史、晋升队列、审计、Outbox、跨库引用 | 插件图谱角色（读写）+ 晋升消费者角色（`project_public` 写） |
| `vault` | 个人笔记、全局公理（仅 `visibility=global`） | 插件金库角色（唯一持有者） |

三个 schema 位于同一 PostgreSQL 实例（database `nexus`），与内核 `kernel` schema 做角色隔离；内核角色对以上 schema 无任何权限。迁移文件位于 `migrations/plugins/plugin_memory/{memory,vault}/NNNN_name.{up,down}.sql`，由插件 install / upgrade 时经 `security_bot` 审查后入库（PluginMvp §7）。

### 3.2 表清单

```text
memory schema
├── memory_entities
├── memory_relations
├── memory_entity_versions
├── memory_promotion_queue      # 晋升队列（唯一写 project_public 的入口）
├── memory_audit_log            # append-only 访问审计
├── memory_outbox               # 本地事务 Outbox
├── memory_external_refs        # 跨 schema 单向引用（vault）
└── memory_embeddings           # 向量侧表（M4，pgvector；MVP 不启用）

vault schema
├── memory_entities             # 同构，仅 visibility=global
├── memory_relations
├── memory_audit_log
└── memory_outbox
```

### 3.3 图谱表 DDL 摘要

**memory_entities（双 schema 同构）**

```text
entity_id       VARCHAR(36) PK        # ULID
namespace       VARCHAR(32)           # 注册表校验：obsidian_note / bot_memory / web_clip
owner_bot_id    VARCHAR(64) NULL
project_id      VARCHAR(64) NULL
visibility      VARCHAR(16) NOT NULL  # CHECK 双库不同（见 3.4）
title           VARCHAR(512)
content         text
metadata_json   jsonb                 # 仅展示性字段
version         BIGINT NOT NULL DEFAULT 1
edge_epoch      BIGINT NOT NULL DEFAULT 0
updated_by_bot_id VARCHAR(64)
updated_at      timestamptz
INDEX idx_memory_visibility (visibility, project_id, namespace, owner_bot_id)
INDEX idx_memory_namespace (namespace, updated_at)
```

**memory_relations**

```text
relation_id     BIGINT PK
source_id       VARCHAR(36)
target_id       VARCHAR(36)
relation_type   VARCHAR(64)   # wikilink / remembers / annotated_from / implemented_in
weight          DOUBLE PRECISION
valid_from / valid_to timestamptz NULL
INDEX idx_rel_source (source_id, relation_type, valid_to)
INDEX idx_rel_target (target_id, relation_type, valid_to)
```

**memory_embeddings（M4，pgvector 侧表）**

```text
entity_id       VARCHAR(36)
model_name      VARCHAR(64)      # 多模型共存 / 灰度切换
dim             INT
embedding       vector(1536)     # pgvector 扩展；不可用时插件降级为关键词检索
updated_at      timestamptz
PRIMARY KEY (entity_id, model_name)
```

**memory_entity_versions**

```text
entity_id       VARCHAR(36)
version         BIGINT
content         LONGTEXT
updated_by_bot_id VARCHAR(64)
source_task_id  VARCHAR(64)
created_at      TIMESTAMP
PRIMARY KEY (entity_id, version)
```

**memory_promotion_queue**

```text
promotion_id    BIGINT PK
source_entity_id VARCHAR(36)
target_visibility VARCHAR(16)   # project_public / bot_private / global(需用户批准)
target_owner_bot_id / target_project_id
status          pending / approved / writing / done / rejected / failed
approval_id     VARCHAR(64)     # 用户批准记录
read_version    BIGINT
source_task_id  VARCHAR(64)
correlation_id  VARCHAR(64)
attempt_count   INT
next_retry_at   TIMESTAMP
created_at / updated_at
```

**memory_audit_log（双 schema 同构，append-only）**

```text
audit_id        BIGINT PK
correlation_id  VARCHAR(64)
from_bot_id     VARCHAR(64)
action          read / search / create / promote / assemble
entity_id       VARCHAR(36) NULL
decision        allowed / denied
limit_hit       BOOLEAN
detail_json     JSON
created_at      TIMESTAMP
```

**memory_outbox（双 schema 同构）**

```text
event_id          VARCHAR(64) PK
event_type        VARCHAR(128)          # plugin_memory.memory.updated / .invalidated
from_bot_id       VARCHAR(64)           # memory_bot
correlation_id / causation_id / root_envelope_id
causal_path_json  JSON
traceparent       VARCHAR(128)
idempotency_key   VARCHAR(128) UNIQUE
payload_hash      CHAR(64)
payload_ref       VARCHAR(512)
delegate_token_hash CHAR(64)
status            pending / dispatched / failed
attempt_count     INT
next_retry_at     TIMESTAMP
created_at / updated_at
INDEX(status, next_retry_at)
```

**memory_external_refs（memory schema）**

```text
ref_id            BIGINT PK
source_entity_id  VARCHAR(36)      # memory schema 实体
target_store      VARCHAR(16)      # vault
target_entity_id  VARCHAR(36)
target_version    BIGINT
content_hash      CHAR(64)
created_at        TIMESTAMP
```

### 3.4 CHECK 约束

```sql
-- vault.memory_entities
CHECK (visibility = 'global' AND owner_bot_id IS NULL AND project_id IS NULL)

-- memory.memory_entities
CHECK (visibility IN ('bot_private','project_public','ephemeral')
       AND NOT (visibility='bot_private'    AND (owner_bot_id IS NULL     OR project_id IS NOT NULL))
       AND NOT (visibility='project_public' AND (owner_bot_id IS NOT NULL OR project_id IS NULL))
       AND NOT (visibility='ephemeral'      AND (owner_bot_id IS NULL     OR project_id IS NULL)))
```

`visibility` 必须 `NOT NULL`（PostgreSQL CHECK 对 UNKNOWN 结果放行，允许 NULL 会绕过约束）。`project_public` 的 INSERT / UPDATE 仅授予晋升消费者角色，其余角色在数据库层被拒绝。

## 4. 记忆模型与一致性

### 4.1 四象限

| 象限 | 归属 | 内容 | 生命周期 |
| --- | --- | --- | --- |
| Bot 角色经验 | `bot_private`（owner 有值、project 空） | 通用技能、偏好、跨项目踩坑 | 跟随 Bot |
| 项目公共记忆 | `project_public`（owner 空、project 有值） | 架构决策、表结构、目录说明、项目历史 | 跟随项目 |
| 任务工作现场 | `ephemeral`（owner、project 均有值） | 当前修改位置、临时报错、执行状态 | 任务结束蒸馏后清理 |
| 全局公理 | `global`（vault） | Obsidian 通用笔记、技术资料 | 跟随用户 |

### 4.2 并发写控制

实体更新使用乐观锁（`version`）：

```sql
UPDATE memory_entities
SET content = :new_content, version = version + 1, updated_by_bot_id = :bot_id
WHERE entity_id = :entity_id AND visibility = 'project_public' AND version = :read_version;
```

边集变更使用 per-entity `edge_epoch`：同一事务内先递增并校验源实体 epoch，再插入新边、写旧边 `valid_to`；影响行数为 0 即放弃提交。双向边（wikilink 反向）两端同校验。

内容类型与策略：普通摘要 / 标签冲突自动合并或重试；架构决策 / 核心约束冲突进人工审查；任务状态机由状态机 + 事务锁保证；原始日志 append-only。

### 4.3 晋升（项目公共记忆唯一写入口）

```text
Bot 任务产物 → plugin_memory.create(ephemeral / bot_private)
→ Memory Arbiter 分拣（M4）
→ 高价值内容入 memory_promotion_queue（待批准或直接升 bot_private）
→ 晋升事务：7.4 版本检查 → 写 memory_entities → 追加 memory_entity_versions
   → 同事务写 memory_outbox(plugin_memory.memory.updated / invalidated)
→ memory_bot 回投内核 → 订阅者获知
```

`project_public` 的写入只能由晋升消费者账号执行；任何绕过晋升队列的写入在数据库层失败。晋升重复请求按 `idempotency_key` / `promotion_id` 去重。

### 4.4 失效事件的语义

`plugin_memory.memory.invalidated` 持久投递但语义尽力而为：用于新任务重新装配与 UI 提示；进行中任务不承诺感知。正确性唯一来源是 4.2 的提交时版本检查。

## 5. 读写路径

### 5.1 读路径：上下文装配（M4）

```text
Bot 调用 plugin_memory.assemble_context(bot_id, project_id, task_ref)
→ 授权校验（bot 归属、project、namespace）
→ 三层装配：
   1. Bot 个人风格与技能偏好（bot_private）
   2. 当前项目图谱 / 架构决策 / 任务状态 / Repo Map（project_public）
   3. 近期跨端输入（浏览器批注、VS Code 评论、Obsidian 标签）
→ 生成只读上下文工件（≤200 行 / 500-2000 tokens），记录 context_version + 输入实体版本 + content_hash
```

装配结果是插件返回给 bot 的一次性工件；内核不缓存、不推送、不感知。

### 5.2 写路径：分拣与蒸馏

| 产物类型 | 去向 |
| --- | --- |
| 项目专属事实 | 晋升队列 → `project_public` |
| 可复用技术认知 | `bot_private` |
| 中间日志 / 失败堆栈 / 临时状态 | 原始输出归档，现场压缩或清理 |
| 用户笔记 / 通用资料 | vault（用户直写或批准晋升，`global`） |

### 5.3 用户级 Bot 默认个人记忆

`user_xxx_bot` 默认 `memory_profile=personal_stack_v1`（四层：Transcripts / Memory Notes / Memory Representations / Injected Context），写入与整合全部由本插件的 memory-worker 执行，详见 [用户级Bot默认个人记忆架构.md](用户级Bot默认个人记忆架构.md)。

## 6. 工具 API 与授权

```text
plugin_memory.read(entity_id)                        -> Entity
plugin_memory.search(query, scope, limit)            -> []EntityRef      # 上限 100，MVP 关键词
plugin_memory.create(source_actor, entity_draft)     -> Entity           # 允许的 namespace / visibility 白名单
plugin_memory.promote(source_entity_id, target_visibility, approval_id)  # M4
plugin_memory.assemble_context(bot_id, project_id, task_ref)             # M4
plugin_memory.audit(correlation_id)                  -> []AuditRecord
plugin_memory.health()                               -> HealthStatus
```

授权规则：

1. 调用方必须是被内核批准了该工具授权的 Bot（3.4 `requiring_tools`）；插件再次校验调用方身份与 `plugin_id` 归属。
2. 三维过滤：`namespace / project / bot`；`bot_private` 默认仅 owner Bot 与用户可读；宿插件不得跨 Bot 读取。
3. 每次调用写 `memory_audit_log`（允许与拒绝都写），带 `correlation_id`。
4. 结果条数上限 100，禁止全量导出；批量导出需二次确认。
5. 插件不解析总线载荷的语义；`payload_ref` 由调用方 bot 自行拉取后作为工具参数传入。

## 7. 事件与总线职责

1. 事件类型：`plugin_memory.memory.updated`、`plugin_memory.memory.invalidated`；由 manifest 的 `publishing[]` 登记，命名符合 `<plugin_id>.<domain>.<action>`。
2. 发布路径：插件本地 `memory_outbox`（与图谱事务同事务）→ dispatcher 申请 `memory_bot` 单次投递委托 → 以该 bot 身份投递 `IngressEnvelope`（`channel=internal`）→ hub ingress 校验 → 内核 Outbox。
3. 幂等：`memory_outbox.idempotency_key` 唯一；hub 侧重复投递记 `decision=duplicate`。
4. 订阅：本插件可订阅 `task.state.changed` 等事件（按授权 project 过滤），驱动现场清理 / 蒸馏任务；订阅经内核 Consumer Group 投递。
5. 插件崩溃恢复：重启后 `memory_outbox` pending 续扫；已提交未回投的事件按 `next_retry_at` 重试；重复回投由 hub 去重。

## 8. 部署、升级与卸载

1. 部署形态：`memory_bot`（L1）+ 知识服务（L2）两个进程（M3），memory-worker（L3）由知识服务按需拉起（M4）；与内核分进程部署、分开暴露健康检查。MVP 限单机同 OS 用户（PID 归属校验与信号发送依赖 `/proc`，见 PluginMvp §6.1 规则 6）。
2. 升级：走插件 upgrade 流程（security 审查 → 优雅关闭知识服务 → drain `memory_bot` → 切换两个 `binary_digest` → 新 Bot ready → exec 新知识服务 → 上报新 PID）；`memory_outbox` pending 续扫，事件不丢。
3. 卸载：
   - `keep` 模式：知识服务优雅关闭 → `memory_bot` 下线（先子后父）、订阅反注册，`memory` / `vault` 数据保留。
   - `confirm` 模式：用户确认后由插件 DDL 清理（涉及个人知识，必须二次确认；vault 默认强制保留）。
   - 知识服务卡死时才以 `plugin kill plugin_memory --confirm` 兜底（supervisor 强制终止）；不作为默认路径，避免丢未落盘的 Outbox 与晋升中间态。
4. 降级：vault 不可用 → 装配降级为“无个人上下文”，项目任务照常；memory 不可用 → 依赖记忆的任务进入等待或 DLQ，不静默丢上下文。
5. 配置中心（D-23）：本插件拥有 `scope_type=plugin, scope_id=plugin_memory` 的 `plugin` 配置（如 Dream 阈值、装配上限、检索条数、向量模型），变更走 `propose → user_bot approve → activate`；配置变更由 owner bot 决定 L2 重载或重启，并上报 `config_version`。

## 9. 里程碑与验收

### M3（图谱与笔记 v1）

1. manifest 入库 + security 审查通过；`memory_bot` 由 `create_bot` 拉起。
2. `memory` / `vault` DDL（CHECK / 索引 / 账号）落地；read / search / create 三工具可用。
3. `plugin_obsidian` 协作：单向发布（DB → `.md`）与 File Watcher 摄取（基线 hash 三方合并，冲突进人工审查）。
4. 图谱只读视图（静态 HTML + JSON）。
5. 验收：10.3 关键规则（重命名 / 删除 / 反向边）各有测试；`Create` 与 `memory_outbox` 同事务；事件回投闭环；审计记录完整。

### M4（认知与晋升闭环）

1. memory-worker 进程化（LLM 可接本地 Ollama 或 stub）。
2. `assemble_context` 三层装配 + `personal_stack_v1` + Auto Dream 整合。
3. Memory Arbiter 分拣；晋升队列 → 版本检查 → 版本历史 → 失效事件。
4. 冲突注入测试（7.4 双向冲突）；D-11 简化（`context_version + involved_entity_ids`；向量侧表停用）。
5. 验收：验收问题 5（invalidation 三场景降级语义）+ 7.4 冲突各一例；用户级 Bot 记忆 profile 可见且隔离成立。

## 10. 与上游文档的映射

| 本文档章节 | 架构总结对应 |
| --- | --- |
| 1 / 2 | 3.1 / 3.2 / 3.3.1 / 8.4 |
| 3 | 8.1 / 8.3 / 8.4 |
| 4 | 7.3 / 7.4 / 7.5 |
| 5 | 7.1 / 7.2 / 用户级 Bot 记忆架构 |
| 6 / 7 | 3.4 / 6.7 / 8.4 |
| 8 / 9 | 11 Step 2 / Step 5 / 开发规划 M3 / M4 |
