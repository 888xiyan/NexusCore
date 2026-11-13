# Bots.md — 项目共享记忆与进度

> 本文件是 NexusCore 项目级记忆的唯一入口；规则见 [BOT.md](BOT.md)。
> 上限 200 行；每条记忆带日期与证据；过期条目标 `[过期]` 而不是删除。

## 1. 项目一句话

NexusCore 是以微内核为基础的个人智能工作台：hub 只做队列 / 事件、Bot 注册与运行时、传输路由三类原语；记忆与知识能力完整属于 knowledge 类插件 `plugin_memory`；内核与内核侧四角色不感知记忆。

## 2. 当前阶段

- **M0 工程骨架**：完成。
- **D-22 记忆插件化重构**：完成，commit `65f3901` 已推送（2026-11-12 核对 origin/main 一致）。
- **D-23 PostgreSQL + 配置中心**：完成，2026-11-13 随 M1 一并提交（此前长期挂在工作区未提交）。
- **M1 纯内核 MVP**：**完成**（2026-11-13）。hub 10 模块 + bot-runtime + loopback agent + D-04 四语义 + 失败注入五例；内核 MVP 详设第 8 节六条验收全过，偏离与证据见详设 §10。D-04 已移入已修正归档。
- **M2 Bot 管理与插件运营**：下一步（配置完整审批流、插件生命周期、权限授权、DLQ 运营、docker-compose、两级进程模型 L2）。
- 设计文档已冻结；实现进度详见 `doc/开发规划.md`。

## 3. 架构记忆（已冻结）

- 2026-11-12 [已验证] hub 只有三类原语，不得吸收任何业务 / 记忆逻辑 —— 证据: `doc/NexusCore架构设计总结.md` §3.1；适用: 所有内核改动。
- 2026-11-12 [已验证] 记忆与知识能力（四象限、金库、图谱、晋升、上下文装配）全部属于 `plugin_memory`；内核与内核侧四角色不感知 —— 证据: D-22 归档 + `doc/MemoryPlugin/`；适用: 任何涉及记忆的需求先放进插件。
- 2026-11-12 [已验证] schema 边界：`kernel`（hub 独占）/ `memory` / `vault`（plugin_memory 独占）；图谱表名为 `memory_entities / memory_relations / memory_entity_versions / memory_audit_log / memory_outbox / memory_external_refs` —— 证据: 架构总结 §8、MemoryPlugin 详设 §3。
- 2026-11-12 [已验证] 只有 bot 能投递 / 消费消息；插件经其注册 bot 的单次投递委托入总线（`plugin_memory` → `memory_bot`）—— 证据: AGENTS.md 总线约束、架构总结 §3.3.1 / 8.4。
- 2026-11-12 [已验证] `IngressEnvelope` 只承载通讯语义；业务维度随载荷走，接收 bot 按 `payload_ref` 自取；内核不做上下文水合 —— 证据: 架构总结 §6.10、D-19。
- 2026-11-12 [已验证] Health 聚合由 `monitor_bot` 承担，内核只持久化报告、状态迁移与事件广播 —— 证据: 架构总结 §6.9、D-22。
- 2026-11-12 [已验证] M1 冒烟使用 `echo_bot` 测试夹具（bot-runtime 内置 echo 模式 + 种子 manifest），不引入插件生命周期 —— 证据: 内核 MVP 详设 §6.1。
- 2026-11-12 [已验证] **两级进程模型**：Bot 进程（L1）由 `create_bot` 决策 + supervisor exec；插件进程（L2 = `plugin_manifest.binary`）由 owner bot（`bot_templates[0]`）exec 与回收，PID 经 `channel=health` 落 `kernel_plugin_processes` 供 `monitor_bot` 只读巡检；L3（memory-worker）内核不登记 —— 证据: PluginMvp §6.1、架构总结 3.3.1 规则 1、AGENTS.md 硬约束 9；适用: M2 插件进程管理实现。
- 2026-11-12 [已验证] 插件进程关闭双路径：优雅 `user_bot → owner bot → 插件进程`（IPC shutdown + SIGTERM，15s 超时 owner bot 自行升级 SIGKILL）；强制 `user_bot → create_bot → supervisor`（`action=kill_process`，需 `--confirm`）。**user_bot 与 monitor_bot 都不发 OS 信号**；下线恒为先子后父；kill 前必须 `pid + pid_start_time` 双匹配 + pgid 归属 + 保留 PID 黑名单 —— 证据: PluginMvp §6.3-6.6、BotMvp §8.6/§8.7；适用: 任何进程回收与 PID 相关代码。
- 2026-11-12 [已验证] 架构总结 §12.3"后端非 Bot 插件进程隔离方式"已收口为已决策；`CreateBotControl` 只扩展 `action` 取值域（加 `kill_process`），字段与 RPC 签名不变，manifest schema 也未新增字段（owner = `bot_templates[0]` 顺序约定）—— 证据: 架构总结 §12.3、BotMvp §7.2、PluginMvp §6.1 规则 1；适用: 避免误触发"改 proto / 改 manifest 需用户确认"。
- 2026-11-13 [已验证] **PostgreSQL 是内核唯一状态底座**（D-23）：单 database `nexus`，`kernel / memory / vault` 三 schema；队列 `SKIP LOCKED` + `LISTEN/NOTIFY`（仅唤醒）+ 5-10s 兜底轮询；严格分区用 advisory lock —— 证据: 架构总结 §6.5/§6.6、内核 MVP §3.5/§4.3。
- 2026-11-13 [已验证] **配置中心是 hub 模块**（非第四原语）：`plugin / bot / agent / global` 四域；`proposal → security_bot 预审（可执行类）→ user_bot 批准（绑定 checksum）→ active`；只存 `secret_ref`；**MCP / Skill 不在范围** —— 证据: `doc/ConfigCenter/`、架构总结 §6.11、AGENTS 硬约束 5 段落。
- 2026-11-13 [已验证] 内核侧四角色配置职责：user_bot 唯一批准人、create_bot 读 active 投影、monitor_bot 观测 `config_version_drift`、security_bot 只预审不批准 —— 证据: BotMvp §4/§8.8。
- 2026-11-13 [已验证] 迁移工具保留自研 `cmd/migrate`，驱动换 `pgx/v5/stdlib`；`Probe` 用 `to_regclass('_migrations')` 按 search_path 探活 —— 证据: `cmd/migrate/main.go`、`internal/migrate/migrate.go`。
- 2026-11-13 [已验证] **M1 入口适配器用 hub HTTP 网关，不改 proto 契约**：§5.0 七个 RPC 是 Bot↔Kernel 控制面，不含 user_bot 侧投递 / 载荷拉取 / 人工确认 / 凭证签发；这些走 `POST /v1/ingress`、`GET /v1/payload/{ref}`、`/v1/jobs/*`、`/v1/credentials`，内部复用同一 ingress/scheduler 模块 —— 证据: 内核 MVP 详设 §10.1 第 1 条、`hub/httpapi/`；适用: M2 决定是否把 ConfigControl 暴露为 RPC 前。
- 2026-11-13 [已验证] `bots` 与 `bot_manifest` 职责切分以 ConfigCenter §6 为准：manifest = 不可变制品 + 配置投影；bots = 生命周期 desired/observed，`desired_state` 只由 create_bot 写（D-20），supervisor 只读 —— 证据: `migrations/kernel/0002_bot.up.sql`、详设 §10.1 第 2 条。
- 2026-11-13 [已验证] 租约过期判定口径：有 `idempotency_key` **且无 executed 意图**才自动 `retry_backoff`，否则 `suspended/lease_expired` 等人工确认；已执行副作用绝不自动重放 —— 证据: `hub/scheduler`、`hub/recovery_test.go`、详设 §10.1 第 4 条。
- 2026-11-13 [已验证] `event.deliver` 的 `causal_path` 留空而非伪造：outbox 只持久化 correlation/causation/root，祖先路径未随事件落库（硬约束 12）—— 证据: `hub/outbox/outbox.go`、`TestDeliveryJobDoesNotFabricateCausalPath`。
- 2026-11-13 [已验证] `/v1/dev/fixture` 仅 M1 夹具（绑定 echo_bot 摘要 + 登记 loopback agent），M2 由插件安装 + 安全审查替代 —— 证据: 详设 §10.1 第 1 条表格。

## 4. 环境与命令（已验证）

- 2026-11-12 [过期] Go 1.26 + MySQL 8.4；模块 `github.com/888xiyan/NexusCore` —— 已被 2026-11-13 D-23 取代（PostgreSQL 16+）。
- 2026-11-13 [已验证] Go 1.26 + PostgreSQL 16+（database `nexus`，schema kernel/memory/vault）；模块 `github.com/888xiyan/NexusCore`；驱动 `pgx/v5` —— 证据: `go.mod`、AGENTS.md。
- 2026-11-12 [已验证] 内核构建与测试：`make build`（5 个内核二进制）、`make build-plugins`、`make test`、`make vet` —— 证据: `Makefile`。
- 2026-11-12 [过期] 内核迁移只读 `MYSQL_KERNEL_DSN` —— 已被 D-23 取代。
- 2026-11-13 [已验证] 内核迁移使用 `NEXUS_PG_KERNEL_DSN`（迁移可另设 `NEXUS_PG_MIGRATE_DSN`），目录 `migrations/kernel`；插件迁移目录 `migrations/plugins/plugin_memory/{memory,vault}` —— 证据: Makefile、AGENTS.md。
- 2026-11-12 [已验证] Claude 桌面会话的 Linux 沙箱**没有 Go 工具链，也没有 SSH 私钥**：`go build/test` 无法在沙箱执行，`git push` 报 `Permission denied (publickey)`；构建验证与最终 push 需要在宿主机完成 —— 证据: 2026-11-12 会话实测；适用: 所有依赖构建 / 推送的 agent。
- 2026-11-13 [已验证] 真库测试与演练：`scripts/dev-pg.sh` 起本地夹具（容器 `nexus-pg-m1`，端口 55432，postgres:16-alpine），再 `make test-pg` / `make smoke-m1` / `make drill-crash-m1`；未设 DSN 时集成用例自动跳过 —— 证据: 本轮三条命令全绿。
- 2026-11-13 [已验证] 集成测试按包用独立 database（`nexus_test_<包名>`）隔离，因为 `go test ./...` 并行跑包；开发夹具的迁移角色因此需 `CREATEDB`（**生产不授予**）—— 证据: `internal/pgtest/pgtest.go`、`scripts/dev-pg.sh`。
- 2026-11-13 [已验证] 本机 `buf` 装不上（`buf@latest` 要求 go1.26.7 并卡在下载 go1.26.11；并发安装争抢 module-cache 锁）。proto 生成物已提交，普通构建不需要 buf；改 proto 后按 Makefile `proto` 目标注释装工具链再生成 —— 证据: 本轮实测 + `buf.yaml`/`buf.gen.yaml`（`modules.path=proto`、`out=proto/gen/v1`）。

## 5. 踩坑与验证结论

- 2026-11-13 [已验证] **流式 RPC 送达后的记账必须脱离流上下文**：`SubscribeEvents` 在 `stream.Send` 成功后若用 `stream.Context()` 做去重登记与 ack，订阅端收到即断开（CLI `-wait` 常态）会让两者随取消失败，任务泄漏在 `leasing` 并被反复重投。改用 `context.WithoutCancel` + 超时 —— 证据: 本轮冒烟由"重复投递旧事件"暴露，修后 `make drill-crash-m1` 三条链路各恰好 1 条 reply。
- 2026-11-13 [已验证] **测试夹具的清理必须覆盖所有被用例改写的种子字段**：漏了 `bots.enabled` 与 `kernel_secret_refs` 会让后续用例看到"停用的 bot""已存在的 ref"，表现为随机顺序失败。清理用迁移角色执行（审计表对运行角色回收了 DELETE，这正是被测边界）—— 证据: `internal/pgtest/pgtest.go` `CleanRuntime`。
- 2026-11-13 [已验证] 嵌套事务是 PG 队列的隐形坑：在 `WithKernelRuntimeTx` 内调用会自开事务的方法（如 `Enqueue`）会破坏原子性；且捕获唯一键冲突后继续执行会撞上 SQLSTATE 25P02（事务已 aborted）。解法是提供 `EnqueueTx` / `EnqueueTxIfAbsent`（`ON CONFLICT DO NOTHING`）—— 证据: `hub/scheduler`、`hub/outbox`。
- 2026-11-13 [已验证] JSON 的 `<` 会被 `json.Marshal` 转义成 `<`，而 `<` 解码后又是字面 `<`：断言"不得 HTML 转义"时若用 `strings.Contains(c, "<")` 会永远失败（反复踩 5 次）。改为断言精确等于期望串，并用 `SetEscapeHTML(false)` —— 证据: `hub/config/canonical.go`、`canonical_test.go`。
- 2026-11-13 [已验证] 内核不得为投递任务补造 `causal_path`：outbox 未持久化祖先路径，伪造节点会违反硬约束 12；留空才是正确实现 —— 证据: `TestDeliveryJobDoesNotFabricateCausalPath`。
- 2026-11-12 [已验证] `migrate.Probe` 曾未限定 `table_schema = DATABASE()`，同实例多 schema 时会误判 `_migrations` 存在；已修复 —— 证据: `internal/migrate/migrate.go`、commit `65f3901`。
- 2026-11-12 [已验证] 文档重构后需 grep 旧术语（`Memory Gateway` / `memory-gateway` / `kernel_entities` / `gateway_outbox`）并检查相对链接，历史评审 / 归档 / 修订记录中的旧词可保留 —— 证据: 2026-11-12 D-22 重构验证流程。
- 2026-11-12 [已验证] `bot/` 目录的 "agent" 指 AI 编码助手，与内核运行时 Bot 不是同一概念；写文档时注意区分 —— 证据: BOT.md 开头说明。
- 2026-11-12 [过期] 开发规划人日估计自相矛盾：开头"约 350-450 人日"与里程碑表合计"约 90-120 人日"不一致 —— 已于 2026-11-12 修复（commit 109e5c2），开头统一为 90-120 人日。
- 2026-11-12 [过期] cmd/migrate 帮助文本残留 D-22 前旧术语（nexus_memory、migrations/vault|memory、双库）—— 已于 2026-11-12 修复（commit 109e5c2），示例对齐 kernel。
- 2026-11-12 [过期] BotMvp 详设引用内核 MVP 章节号错位（§3.6/§3.10/§5.1）—— 已于 2026-11-12 修复（commit 109e5c2），对齐 §3.5/§3.9/§5.0。
- 2026-11-12 [过期] AGENTS.md"当前状态"中 M0"待提交"表述过时 —— 已于 2026-11-12 修复（commit 109e5c2），反映已提交并推送。

## 6. 进度日志（新→旧，最近 20 条）

- 2026-11-13 qoder **M1 纯内核 MVP 完成**：`migrations/kernel` 七对 DDL + 种子、hub 10 模块（store/registry/config/ingress/scheduler/outbox/eventbus/transport/supervisor/grpcd）+ httpapi 入口网关、bot-runtime echo 夹具、loopback agent、CLI；D-04 四语义 + 失败注入五例；新增 supervisor/registry/transport/eventbus 测试与 `hub/recovery_test.go`；`scripts/dev-pg.sh`、`smoke-m1.sh`、`drill-crash-m1.sh`；详设 §10 回写 8 项偏离 + 六条验收证据；D-04 归档；版本升 `0.1.0-m1`。`go test -count=1 ./...`（真库）/ `vet` / `smoke-m1` / `drill-crash-m1` 全绿。**本次连同此前挂起的 D-23 与两级进程模型文档改动一并提交**。
- 2026-11-13 claude 落实 D-23：PostgreSQL 内核化 + 配置中心文档全量更新（架构总结 §3.1/3.3.1/4/6.5/6.6/6.7/6.9/6.11/8/11/12/13、内核 MVP v0.4、新增 `doc/ConfigCenter/`、Bot/Plugin/Memory 详设、开发规划、AGENTS、README、归档 D-23）；代码驱动切 pgx/v5、migrate 改 PG、Makefile DSN 改 `NEXUS_PG_*`；`go build/vet/test` 全绿（沙箱临时安装 Go 1.27.4 验证）；原记"未提交"，已随 2026-11-13 M1 提交入库。
- 2026-11-12 12:35 qoder 插件 MVP 细节优化：确立两级进程模型与启动 / 优雅关闭 / 强制终止三条链路，全量同步 10 个文件（PluginMvp v0.2 §6 重写、BotMvp v0.2 §8.6-8.7 新增、架构总结 3.3.1/4 图/5.5/6.9/12.3、内核 MVP §3.9/§4.1/§5.3/§6.7、MemoryPlugin §2.2/§8、AGENTS.md 硬约束 9 + 禁忌 5/11、开发规划 M2、两份 README）；build/vet/test 全绿、相对链接 0 broken；**未提交**。
- 2026-11-12 11:56 qoder 推送 `1c8cbe9` / `109e5c2` / `457a59d` 到 origin/main，本地与远端一致；§7 推送交接移除。
- 2026-11-12 11:53 qoder 修复四处文档漂移并提交 `109e5c2`（build/vet/test 全绿）；§5 对应条目标 [过期]；bot/ 记忆登记随本次提交入库。
- 2026-11-12 11:43 qoder 通读全项目（设计文档 + 代码骨架 + bot/ 协议），build/vet/test 全绿；四处文档漂移记入 §5；核对 git：65f3901 已推送，bot/ 已提交为 1c8cbe9（待推送）。
- 2026-11-12 建立多 agent 协作规范与记忆目录（`bot/BOT.md`、`bot/Bots.md`、`bot/claude/`、`bot/_template/`）。
- 2026-11-12 D-22 记忆插件化全面重构完成并提交 `65f3901`（25 文件，+1092/−514）；**未推送**：沙箱无 SSH 凭据，需在宿主机执行 `git push origin main`。
- 2026-11-12 新增 `doc/MemoryPlugin/` 详设；内核 MVP 详设重写为纯内核；`memory-gateway/` 迁移为 `plugins/plugin_memory/`。
- 2026-11-11 M0 骨架里程碑提交（`b258c3a` 之前）与设计文档冻结。

## 7. 交接 / 待办

- **P0**：推送本次提交到 origin/main（用户只要求提交，未授权推送；本机 SSH 凭据可用）。
- **P1（M2）**：配置中心完整审批流 + CLI + security 预审 + 投影 + 漂移告警；验收见 ConfigCenter §9。M1 已有 `hub/config` 模块与最小读集，M2 补 CLI 与完整流程。
- **P1（M2）**：插件生命周期（install / security-list / security-approve / upgrade / disable / uninstall）+ 权限授权 + DLQ 运营 + Health 最小聚合 + docker-compose。
- **P2（M2）**：`kernel_plugin_processes` DDL + owner bot 进程管理 + supervisor `kill_process` 与孤儿级联 + PID 可信性失败注入三例；验收见 PluginMvp §12 第 9-17 条、BotMvp §9 第 9-13 条。
- **P2（M2）**：替换 M1 夹具 `/v1/dev/fixture` 为真实插件安装 + 安全审查；决定是否把 ConfigControl 暴露为 gRPC 服务（新增 RPC 需用户确认）。
- **待用户定**：M2 因两级进程模型 + 配置中心，人日已重估为 16-20；总估计 100-135，需确认是否接受。
- 已知开放问题：D-12 业务信封 schema 未定稿（`partition_key` 来源暂退化为 `queue_name` 级并行）；D-11 向量索引方案待 M4 验证。
- 明确排除：MCP / Skill 不在配置中心与任何里程碑内（无运行时）。

## 8. 指向文档

| 主题 | 文档 |
| --- | --- |
| 总设计 / 边界 | `doc/NexusCore架构设计总结.md` |
| 内核 MVP（纯内核） | `doc/内核mvp/内核MVP详细设计.md` |
| 记忆插件 | `doc/MemoryPlugin/MemoryPlugin详细设计.md` |
| 配置中心 | `doc/ConfigCenter/ConfigCenter详细设计.md` |
| Bot 体系 | `doc/BotMvp/BotMvp详细设计.md` |
| 插件体系 | `doc/PluginMvp/PluginMvp详细设计.md` |
| 开发规划 | `doc/开发规划.md` |
| 编码硬约束 | `AGENTS.md` |
