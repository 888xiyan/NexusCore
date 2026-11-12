# Bots.md — 项目共享记忆与进度

> 本文件是 NexusCore 项目级记忆的唯一入口；规则见 [BOT.md](BOT.md)。
> 上限 200 行；每条记忆带日期与证据；过期条目标 `[过期]` 而不是删除。

## 1. 项目一句话

NexusCore 是以微内核为基础的个人智能工作台：hub 只做队列 / 事件、Bot 注册与运行时、传输路由三类原语；记忆与知识能力完整属于 knowledge 类插件 `plugin_memory`；内核与内核侧四角色不感知记忆。

## 2. 当前阶段

- **M0 工程骨架**：完成。
- **D-22 记忆插件化重构**：完成并已提交（commit `65f3901`）；尚未推送，见 §6。
- **M1 纯内核 MVP**：下一步开工（D-04 队列四语义 + `kernel` schema DDL + 纯内核冒烟链路）。
- 设计文档已冻结；实现从 M1 开始，详见 `doc/开发规划.md`。

## 3. 架构记忆（已冻结）

- 2026-11-12 [已验证] hub 只有三类原语，不得吸收任何业务 / 记忆逻辑 —— 证据: `doc/NexusCore架构设计总结.md` §3.1；适用: 所有内核改动。
- 2026-11-12 [已验证] 记忆与知识能力（四象限、金库、图谱、晋升、上下文装配）全部属于 `plugin_memory`；内核与内核侧四角色不感知 —— 证据: D-22 归档 + `doc/MemoryPlugin/`；适用: 任何涉及记忆的需求先放进插件。
- 2026-11-12 [已验证] schema 边界：`kernel`（hub 独占）/ `memory` / `vault`（plugin_memory 独占）；图谱表名为 `memory_entities / memory_relations / memory_entity_versions / memory_audit_log / memory_outbox / memory_external_refs` —— 证据: 架构总结 §8、MemoryPlugin 详设 §3。
- 2026-11-12 [已验证] 只有 bot 能投递 / 消费消息；插件经其注册 bot 的单次投递委托入总线（`plugin_memory` → `memory_bot`）—— 证据: AGENTS.md 总线约束、架构总结 §3.3.1 / 8.4。
- 2026-11-12 [已验证] `IngressEnvelope` 只承载通讯语义；业务维度随载荷走，接收 bot 按 `payload_ref` 自取；内核不做上下文水合 —— 证据: 架构总结 §6.10、D-19。
- 2026-11-12 [已验证] Health 聚合由 `monitor_bot` 承担，内核只持久化报告、状态迁移与事件广播 —— 证据: 架构总结 §6.9、D-22。
- 2026-11-12 [已验证] M1 冒烟使用 `echo_bot` 测试夹具（bot-runtime 内置 echo 模式 + 种子 manifest），不引入插件生命周期 —— 证据: 内核 MVP 详设 §6.1。

## 4. 环境与命令（已验证）

- 2026-11-12 [已验证] Go 1.26 + MySQL 8.4；模块 `github.com/888xiyan/NexusCore` —— 证据: `go.mod`、AGENTS.md。
- 2026-11-12 [已验证] 内核构建与测试：`make build`（5 个内核二进制）、`make build-plugins`、`make test`、`make vet` —— 证据: `Makefile`。
- 2026-11-12 [已验证] 内核迁移只读 `MYSQL_KERNEL_DSN`，目录 `migrations/kernel`；插件迁移目录 `migrations/plugins/plugin_memory/{memory,vault}` —— 证据: Makefile、AGENTS.md。
- 2026-11-12 [已验证] Claude 桌面会话的 Linux 沙箱**没有 Go 工具链，也没有 SSH 私钥**：`go build/test` 无法在沙箱执行，`git push` 报 `Permission denied (publickey)`；构建验证与最终 push 需要在宿主机完成 —— 证据: 2026-11-12 会话实测；适用: 所有依赖构建 / 推送的 agent。

## 5. 踩坑与验证结论

- 2026-11-12 [已验证] `migrate.Probe` 曾未限定 `table_schema = DATABASE()`，同实例多 schema 时会误判 `_migrations` 存在；已修复 —— 证据: `internal/migrate/migrate.go`、commit `65f3901`。
- 2026-11-12 [已验证] 文档重构后需 grep 旧术语（`Memory Gateway` / `memory-gateway` / `kernel_entities` / `gateway_outbox`）并检查相对链接，历史评审 / 归档 / 修订记录中的旧词可保留 —— 证据: 2026-11-12 D-22 重构验证流程。
- 2026-11-12 [已验证] `bot/` 目录的 "agent" 指 AI 编码助手，与内核运行时 Bot 不是同一概念；写文档时注意区分 —— 证据: BOT.md 开头说明。

## 6. 进度日志（新→旧，最近 20 条）

- 2026-11-12 建立多 agent 协作规范与记忆目录（`bot/BOT.md`、`bot/Bots.md`、`bot/claude/`、`bot/_template/`）。
- 2026-11-12 D-22 记忆插件化全面重构完成并提交 `65f3901`（25 文件，+1092/−514）；**未推送**：沙箱无 SSH 凭据，需在宿主机执行 `git push origin main`。
- 2026-11-12 新增 `doc/MemoryPlugin/` 详设；内核 MVP 详设重写为纯内核；`memory-gateway/` 迁移为 `plugins/plugin_memory/`。
- 2026-11-11 M0 骨架里程碑提交（`b258c3a` 之前）与设计文档冻结。

## 7. 交接 / 待办

- **P0**：在宿主机推送 `65f3901` 到 `origin/main`，确认远端与本地一致。
- **P0**：M1 第一项——`migrations/kernel` 全量 DDL（queue / bot / plugin / ingress / outbox / payload 表，含 CHECK 与 UNIQUE 约束）。
- **P1**：M1 ingress 校验器（失败注入五例）与 `kernel_ingress_log` 同事务落库。
- **P1**：M1 scheduler D-04 四语义（幂等键、租约续期、suspended、人工确认 replay CLI）。
- **P2**：proto 的 `SubscribeRequest` 补 `scope` 与 `request_id`（需用户确认后改）。
- 已知开放问题：D-12 业务信封 schema 未定稿（`partition_key` 来源暂退化为 `queue_name` 级并行）。

## 8. 指向文档

| 主题 | 文档 |
| --- | --- |
| 总设计 / 边界 | `doc/NexusCore架构设计总结.md` |
| 内核 MVP（纯内核） | `doc/内核mvp/内核MVP详细设计.md` |
| 记忆插件 | `doc/MemoryPlugin/MemoryPlugin详细设计.md` |
| Bot 体系 | `doc/BotMvp/BotMvp详细设计.md` |
| 插件体系 | `doc/PluginMvp/PluginMvp详细设计.md` |
| 开发规划 | `doc/开发规划.md` |
| 编码硬约束 | `AGENTS.md` |
