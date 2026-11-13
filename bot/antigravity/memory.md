# Antigravity — 个体记忆

> 只放个体经验；项目级事实写入 [../Bots.md](../Bots.md)。规则见 [../BOT.md](../BOT.md)。
> 上限 100 行；新条目从 `[未验证]` 开始，验证后升级并补证据。

## 1. 角色与协作偏好

- 2026-11-13 [已验证] 用户 yhy：单人全栈、中文交流、设计先行、边界严格；代码与文档均要求高完整度 —— 证据: 仓库规范与会话记录。
- 2026-11-13 [已验证] 遵从多 agent 工作流：开工登记 BOT.md 通知板，按需更新 Bots.md，收工标记 done。

## 2. 工作日志（新→旧）

- 2026-11-13 修复 M1 审查缺陷：消除 `grpcd.authenticate` 双重凭证校验 TOCTOU、补充 `DeadLetter` 状态转移 RowsAffected 检查、优化 `DetectExpiredLeases` 批次与合并意图查询、原子化 `Replay` 委托单事务、对齐 `StreamTasks` 查 `bot_manifest` 表名、治理 `RenewLease`/`Release` 错误吞没、`Deregister` 双更新包裹显式事务、使用 `make_interval` 优化 PostgreSQL 时间构造、提炼 `enqueueArgs` 去重样板、新增 `TestRelease` 与 `TestDeadLetter` 单测。
- 2026-11-13 修复 M0 审计缺陷：Makefile `migrate-probe`/`smoke-empty` 子shell执行逻辑、`internal/migrate` 同版本不同名称冲突检测、`Run` 异常时空指针保护、单迁移事务原子性、`cmd/migrate` filepath.Join 优化及增加覆盖单测。
- 2026-11-13 全仓库通读与 M0 架构代码审计，输出 4 项确凿缺陷（Makefile subshell、版本冲突漏检、空指针 dereference、事务原子性）与 1 项建议。

## 3. 个体经验 / 踩坑

- 2026-11-13 [已验证] PostgreSQL 时间区间拼装 `($N || ' seconds')::interval` 容易引起类型模糊与字符串拼接开销，原生安全函数 `make_interval(secs => $N)` 结合整数传参更为规范稳健 —— 证据: `hub/scheduler/`、`hub/supervisor/`。
- 2026-11-13 [已验证] Makefile 多行命令默认在独立 subshell 运行，`if [ -z ... ]; then exit 0; fi` 无法阻止下一行无 DSN 时执行；必须使用 `if ... else ... fi` 单条 Shell 语句 —— 证据: `make smoke-empty` 实测。
- 2026-11-13 [已验证] `database/sql.Rows.Err()` 在 `rows == nil` 时直接解引用指针并 panic；遍历查询前若发生错误必须严防调用 `rows.Err()` —— 证据: `database/sql` 源码与实测。

## 4. 未验证假设

- （暂无）

## 5. 交接

- M0 修复就绪后验证 `make build && make test && make vet && make smoke-empty`。
