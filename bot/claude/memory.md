# Claude — 个体记忆

> 只放与 Claude 个体相关的经验；项目级事实见 [../Bots.md](../Bots.md)。规则见 [../BOT.md](../BOT.md)。

## 1. 角色与协作偏好

- 2026-11-12 [已验证] 用户 yhy：单人全栈、中文交流、设计先行；架构级改动偏好一次性全量同步，避免文档中间态 —— 证据: 多次会话确认。
- 2026-11-12 [已验证] commit 使用 `docs:/feat:/fix:` 前缀 + 中文详细 body；错误消息中文带上下文 —— 证据: AGENTS.md、历史 commit。

## 2. 工作日志（新→旧）

- 2026-11-13 落实 D-23：PostgreSQL 内核化 + 配置中心，全量更新架构总结、内核 MVP v0.4、新增 `doc/ConfigCenter/`、Bot/Plugin/Memory 详设、开发规划、AGENTS、README、归档；代码切 pgx/v5 并用临时 Go 1.27.4 跑通 build/vet/test。
- 2026-11-12 评估并落地多 agent 文件协作与记忆规范；创建 `bot/BOT.md`、`bot/Bots.md`、本文件与模板。
- 2026-11-12 完成 D-22 记忆插件化重构并提交 `65f3901`；push 因沙箱无凭据未完成，已写入交接。

## 3. 个体经验 / 踩坑

- 2026-11-12 [已验证] Bash 沙箱与文件工具路径不同（`/sessions/quirky-stoic-cannon/mnt/NexusCore` ↔ `/home/yhy/Projects/NexusCore`），跨工具操作需换算。
- 2026-11-12 [已验证] 沙箱内的 git 身份未配置；提交前需按历史 commit 设置仓库级 `user.name/email`（888xiyan / 3551206040@qq.com）。
- 2026-11-12 [已验证] 修改文档后先跑两类检查再收工：相对链接存在性、旧术语残留 grep（历史评审 / 归档 / 修订记录中的旧词允许保留）。
- 2026-11-12 [已验证] 删除用户目录文件时若报 `Operation not permitted`，先调用 `allow_cowork_file_delete`（需传 VM 路径）。
- 2026-11-13 [已验证] Claude 沙箱默认无 Go，可从 `https://go.dev/dl/` 下载 linux-amd64 tarball 到 `/tmp` 临时使用（68MB）；设置 `GOCACHE=/tmp/gocache GOPATH=/tmp/gopath`，可跑 `go mod tidy/build/vet/test` —— 证据: 本次 D-23 验证。
- 2026-11-13 [已验证] PostgreSQL 迁移 CLI 用 `database/sql` + `pgx/v5/stdlib`（driver 名 `pgx`）；探活用 `to_regclass('_migrations')`，避免 MySQL 的 information_schema/`DATABASE()` 写法。
- 2026-11-13 [已验证] 改 AGENTS 硬约束时不要插入新编号（9/14/17 被多处引用），把新约束写成既有条目的第二段 —— 证据: qoder 记忆 + 本次配置中心约束落在硬约束 5 第二段。

## 4. 未验证假设

- 2026-11-12 [未验证] 用户后续可能希望把 `bot/` 改名为 `agents/` 以避免与运行时 Bot 混淆 —— 需要: 用户确认后再迁移。

## 5. 交接

- D-23 改动（文档 + pgx 骨架 + `doc/ConfigCenter/`）尚未提交；先在宿主机跑 `go mod tidy && make build && make test && make vet`，再提交并推送。
- 下一步优先 M1：`migrations/kernel` PostgreSQL DDL → ingress 校验器 → scheduler D-04 四语义（NOTIFY + 兜底轮询）→ 配置最小读集；开工前先读 Bots.md §7。
