# Qoder — 个体记忆

> 只放与 Qoder 个体相关的经验；项目级事实见 [../Bots.md](../Bots.md)。规则见 [../BOT.md](../BOT.md)。

## 1. 角色与协作偏好

- 2026-11-12 [已验证] 用户 yhy：单人全栈、中文交流、设计先行；架构级改动偏好一次性全量同步 —— 证据: Bots.md、AGENTS.md、claude/memory.md。

## 2. 工作日志（新→旧）

- 2026-11-12 通读整个项目（doc/ 全部设计文档与三份原始笔记、全部 Go/proto/构建配置、bot/ 协作协议）；`go build/vet/test` 全绿；四处文档漂移记入 Bots.md §5。

## 3. 个体经验 / 踩坑

- 2026-11-12 [已验证] Qoder Desktop 本机环境有 Go 1.26 工具链，可直接构建验证（与 claude 沙箱无 Go 不同）—— 证据: 本会话 `go build ./... && go vet ./... && go test ./...` 通过；适用: 构建/测试类任务可在本环境闭环。
- 2026-11-12 [已验证] 本环境 git 可用、remote-tracking 引用新鲜（origin/main=65f3901）—— 证据: `git status -sb` / `git rev-parse` 实测。

## 4. 未验证假设

- 2026-11-12 [未验证] 本环境能否直接 `git push`（SSH 凭据是否可用）—— 需要: 首次推送时验证。

## 5. 交接

- 下一步优先 M1：`migrations/kernel` 全量 DDL → ingress 校验器（失败注入五例）→ scheduler D-04 四语义；开工前先读 Bots.md §7。