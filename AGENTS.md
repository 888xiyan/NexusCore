# AGENTS.md

AI 助手（含 Codex / Copilot / Claude 等）在本仓库内工作时，请遵守以下约定。

## 项目概述

NexusCore 是以微内核为基础的个人智能工作台。内核只负责知识访问与图谱存储、持久化事件与任务队列、Bot 注册和跨设备传输路由；所有具体业务以插件形式接入。

- 语言：Go 1.26（hub / bot-runtime / agent-daemon / cli / migrate），Python（memory-worker，M4 引入），TypeScript（workbench，M7 引入）
- 数据库：MySQL 8.4+，同一实例内双 schema（vault / memory），双凭据隔离
- 总线：只有 bot 能投递与消费消息；用户以 `user_bot` 身份入总线

## 目录结构

```
hub/                # Go 微内核网关（Step 0 骨架，M1 填充实现）
bot-runtime/        # Bot 独立二进制运行时（lazy / resident 双类型）
agent-daemon/       # 跨设备探针（M5）
cli/                # 用户(user_bot)的投递与管理代理
cmd/migrate/        # 双库迁移 CLI
internal/           # hub 共享内部包
  ├─ version/       # 统一版本号
  └─ migrate/       # 迁移库（不依赖 golang-migrate）
migrations/         # 双库差异目录
  ├─ vault/         # 金库库 DDL（M1 填充）
  └─ memory/        # 记忆库 DDL（M1 填充）
proto/              # gRPC proto 契约
doc/                # 设计文档（见下）
```

## 常用命令

```bash
make build          # 编译 5 个二进制到 bin/
make test           # go test ./...
make vet            # go vet ./...
make fmt            # go fmt ./...
make lint           # vet + golangci-lint（可选）
make proto          # buf generate（buf 未装时提示）
make migrate-probe  # 检查 DSN 可连通性（需 env）
make migrate-up     # 执行 up 迁移
make migrate-down   # 执行 down 迁移
make smoke-empty    # 组合冒烟（M0 为探活模式）
make clean          # 清 bin/
```

MySQL 连接用环境变量：

```bash
export MYSQL_VAULT_DSN="user:pass@tcp(host:3306)/nexus_vault"
export MYSQL_MEMORY_DSN="user:pass@tcp(host:3306)/nexus_memory"
```

## 设计文档索引（必读）

写代码前，先按需读对应章节：

| 文档 | 何时读 |
| --- | --- |
| [doc/NexusCore架构设计总结.md](doc/NexusCore架构设计总结.md) | 上游总设计；本文档"架构边界"部分引用它 |
| [doc/内核mvp/内核MVP详细设计.md](doc/内核mvp/内核MVP详细设计.md) | hub 10 模块 / 双库数据模型 / 接口契约 / 验收标准 |
| [doc/BotMvp/BotMvp详细设计.md](doc/BotMvp/BotMvp详细设计.md) | Bot 角色体系 / 生命周期 / gRPC 控制面 / 插件侧 Bot |
| [doc/PluginMvp/PluginMvp详细设计.md](doc/PluginMvp/PluginMvp详细设计.md) | 三类插件 / manifest schema / 命名规范 / 生命周期 / 卸载 |
| [doc/开发规划.md](doc/开发规划.md) | M0-M7 里程碑 / 估计 / 停线点 |
| [doc/NexusCore待修正问题清单.md](doc/NexusCore待修正问题清单.md) | D-04 / D-11 / D-12（未解决问题） |
| [doc/NexusCore已修正问题归档.md](doc/NexusCore已修正问题归档.md) | D-01 至 D-03 / D-05 至 D-10 / D-13 至 D-20（已解决） |

## 架构硬约束（不可违背）

以下边界是本项目的核心价值。任何 PR / commit 若破坏这些约束，视为错误提交。

### 总线与信封

1. **只有 bot 能投递与消费消息**。用户以 `user_bot` 身份入总线（常驻席位）；客户端插件只是 user_bot 的投递 / 展示代理。
2. **信封只承载通讯语义**：身份（`from_bot_id / channel / channel_ref / msg_type`）、因果链（`correlation_id / causation_id / root_envelope_id / causal_path / traceparent`）、幂等（`idempotency_key`）与审计挂点。
3. **信封不含业务维度**：`project_id / conversation_id` 等不进信封；业务维度由来源插件装入业务载荷（`ContextEnvelope`），经 `payload_ref` 由接收 bot 自行拉取。
4. **内核不做上下文水合推送**：接收 bot 拉取，读取授权来自其收到的信封 + 3.4 ACL。

### 内核边界

5. **hub 收敛为四类原语**：知识访问与图谱存储、持久化事件与任务队列、Bot 注册与运行时、跨设备传输路由。业务语义（笔记渲染、看板拖拽、IM SDK、LLM 推理）绝不出现在 hub 源码里。
6. **金库库凭据只存在于 Memory Gateway**：其他模块 / bot / 插件没有金库 DSN。
7. **内核不做上下文装配 / 记忆仲裁**：Context Assembler 与 Memory Arbiter 归 memory-worker 认知服务（M4）。

### Bot 边界

8. **Bot 是独立二进制进程**：内核 supervisor 是唯一执行器；`create_bot` 是决策者；业务插件不能自行拉起 Bot 进程。
9. **保留名禁用**：`user_bot / create_bot / monitor_bot / security_bot`，任何插件不得注册同名 bot。
10. **lazy Bot 不持有 Bot-Agent 长连接**；resident Bot 持有长连接但必须经 Agent Mesh 路由（5.1），不绕过 ACL。
11. **所有消息必须携带完整因果链**：缺失 / 不合法入口直接拒绝并记录 `decision=rejected`；内核不替调用方补造历史。

### 存储边界

12. **图谱只承载知识域**：PM 任务、队列、项目等强结构数据不入图谱；插件自有类型化表通过 `entity_id` 引用图谱节点。
13. **金库库只允许 `visibility=global`**；记忆库只允许其余三种；CHECK 约束 + `visibility NOT NULL` 双重锁定。
14. **`project_public` 写权限只授予晋升队列消费者的专用账号**：hub / 插件 / Bot 运行时账号均无此权限。
15. **内核不解析插件 payload**：5.1 的 `transport_frame` 只校验 length / protocol_id / stream_id；`payload` 是插件自定义的字节，由对应 Bot 协议解释。

## 命名规范

| 对象 | 规则 |
| --- | --- |
| `bot_id` | `^[a-z0-9_]{5,64}$`；保留名 `user_bot / create_bot / monitor_bot / security_bot` 禁用 |
| `plugin_id` | `^plugin_[a-z0-9_]{3,24}$`；命名 `plugin_architect` 等 |
| `event_type` | `<plugin_id>.<domain>.<action>[.state]`；内核保留前缀 `bot.* agent.* project.* plugin.* kernel.*` 禁用 |
| `namespace` | 默认 = `plugin_id` 去前缀；VARCHAR + 注册表校验，禁止 ENUM |
| `user_xxx_bot` | 用户自建 Bot，前缀 `user_`，xxx 由宿插件模板提供 |
| `xxx_bot` | 插件侧业务 Bot，`xxx` 由插件 / 用户决定 |

## 代码风格

1. **Go**：`gofmt`；`go vet ./...` 必须无警告；导入按标准库 → 第三方 → 本项目分组。
2. **跨模块调用**：只走 hub 内部接口，模块间不直接共享数据库连接对象。
3. **错误处理**：中文错误消息带上下文；不吞错；带 `fmt.Errorf("...: %w", err)`。
4. **DDL**：写在 `migrations/{vault,memory}/NNNN_name.up.sql` 与 `.down.sql`，成对文件；版本号递增；检查 CHECK / NOT NULL 约束。
5. **测试**：单元表驱动（状态机 / 校验器）+ 契约测试（信封 / proto 兼容）+ 失败注入（崩溃 / 超时 / 双写）；`go test ./...` 必须绿。
6. **提交信息**：中文一行 + 详细 body；`docs:` / `feat:` / `fix:` 前缀。

## 当前状态

| 里程碑 | 状态 |
| --- | --- |
| M0 工程骨架 | 已落地（git commit `c657459`） |
| M1 内核 MVP | 未开始（D-04 四语义是前置条件） |
| M2 Bot 管理与队列运营 | 未开始（含 docker-compose MySQL，D-20 四角色骨架） |
| M3+ | 未开始 |

## 常见禁忌（先问用户再做）

1. 把业务语义写进信封 / transport_frame。
2. 绕过 Memory Gateway 直连金库库。
3. 绕过 3.4 ACL 广播全量事件。
4. 在插件侧手动 `exec` Bot 进程。
5. 修改 `visibility` 列的 CHECK 约束使其变弱。
6. 新增 plugins 时直接 ALTER TABLE 图谱表。
7. 删除 `kernel_ingress_log` / `kernel_context_snapshots` 行（它们是 append-only / 审计墓碑）。
8. 绕过 7.4 乐观锁版本检查直接写公共记忆。

## 需要用户确认的场景

1. 需要新增 proto RPC 或改动已有 RPC 签名。
2. 需要改 `kernel_jobs` / `kernel_entities` / `kernel_relations` / `kernel_projects` 的字段集合。
3. 需要引入新的第三方库 / 依赖。
4. 需要改动架构总结的章节结构（不禁止，但需说明理由）。
5. 需要在 M2 之前引入 docker-compose / 多节点部署。
