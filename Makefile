BIN_DIR := bin
PLUGIN_BIN_DIR := bin/plugins

.PHONY: build build-plugins test test-pg vet fmt proto migrate-probe migrate-up migrate-down compose-up compose-down smoke-empty smoke-m1 smoke-m2 smoke-m3 drill-crash-m1 clean

build:
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/hub ./hub
	@go build -o $(BIN_DIR)/bot-runtime ./bot-runtime
	@go build -o $(BIN_DIR)/agent-daemon ./agent-daemon
	@go build -o $(BIN_DIR)/cli ./cli
	@go build -o $(BIN_DIR)/migrate ./cmd/migrate
	@echo "build ok: $(BIN_DIR)/{hub,bot-runtime,agent-daemon,cli,migrate}"

build-plugins:
	@mkdir -p $(PLUGIN_BIN_DIR)
	@go build -o $(PLUGIN_BIN_DIR)/plugin_memory ./plugins/plugin_memory
	@go build -o $(PLUGIN_BIN_DIR)/plugin_demo ./plugins/plugin_demo
	@go build -o $(PLUGIN_BIN_DIR)/plugin_groupchat ./GroupChatPlugin
	@echo "build-plugins ok: $(PLUGIN_BIN_DIR)/{plugin_memory,plugin_demo,plugin_groupchat}"

test:
	@go test ./...

# 集成测试需要真库：先 scripts/dev-pg.sh 起夹具，再 make test-pg。
# 未设置 DSN 时集成用例自动跳过（等同 make test）。
test-pg:
	@NEXUS_PG_KERNEL_DSN="$${NEXUS_PG_KERNEL_DSN:-postgres://nexus_kernel:nexus_dev_kernel@127.0.0.1:$${NEXUS_DEV_PG_PORT:-55432}/nexus}" \
	 NEXUS_PG_MIGRATE_DSN="$${NEXUS_PG_MIGRATE_DSN:-postgres://nexus_migrator:nexus_dev_migrate@127.0.0.1:$${NEXUS_DEV_PG_PORT:-55432}/nexus}" \
	 go test ./...

vet:
	@go vet ./...

fmt:
	@go fmt ./...

lint: vet
	@which golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint 未安装，已执行 go vet 兜底"

# proto/gen/v1 下的生成物已提交，普通构建不需要 buf / protoc。
# 仅在改动 proto/botruntime.proto 后需要重新生成：
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
#   go install github.com/bufbuild/buf/cmd/buf@latest
proto:
	@which buf >/dev/null 2>&1 || { echo "buf 未安装：见本目标上方注释的 go install 命令"; exit 1; }
	@which protoc-gen-go >/dev/null 2>&1 || { echo "protoc-gen-go 未安装"; exit 1; }
	@which protoc-gen-go-grpc >/dev/null 2>&1 || { echo "protoc-gen-go-grpc 未安装"; exit 1; }
	buf generate

# 内核库：使用 NEXUS_PG_KERNEL_DSN（运行角色）指向 PostgreSQL；迁移 DSN 可选（NEXUS_PG_MIGRATE_DSN，未设置时回退运行 DSN）。
# 插件库（memory / vault）的迁移属于插件 install / upgrade 流程（M2/M3），不走本 Makefile。
migrate-probe:
	@if [ -z "$$NEXUS_PG_KERNEL_DSN" ]; then \
		echo "未设置 NEXUS_PG_KERNEL_DSN：M0 允许跳过；真正的探活在配置 PostgreSQL 后执行"; \
	else \
		go run ./cmd/migrate -cmd probe -dsn "$${NEXUS_PG_MIGRATE_DSN:-$$NEXUS_PG_KERNEL_DSN}" -dir migrations/kernel; \
	fi

migrate-up:
	@if [ -z "$$NEXUS_PG_KERNEL_DSN" ]; then \
		echo "未设置 NEXUS_PG_KERNEL_DSN：请先配置数据库连接"; \
		exit 1; \
	else \
		go run ./cmd/migrate -cmd up -dsn "$${NEXUS_PG_MIGRATE_DSN:-$$NEXUS_PG_KERNEL_DSN}" -dir migrations/kernel; \
	fi

migrate-down:
	@if [ -z "$$NEXUS_PG_KERNEL_DSN" ]; then \
		echo "未设置 NEXUS_PG_KERNEL_DSN：请先配置数据库连接"; \
		exit 1; \
	else \
		go run ./cmd/migrate -cmd down -dsn "$${NEXUS_PG_MIGRATE_DSN:-$$NEXUS_PG_KERNEL_DSN}" -dir migrations/kernel; \
	fi

smoke-empty: migrate-probe
	@echo "smoke-empty ok（无库环境为跳过模式）"

# M2：docker-compose 管理本地 PostgreSQL 16+ 环境
compose-up:
	@docker compose up -d
	@echo "PostgreSQL 16+ 已启动 (端口 $${NEXUS_DEV_PG_PORT:-55432})"

compose-down:
	@docker compose down

# M1 验收：九步冒烟链路 + 边界验收 + 兜底轮询（详设 §8.1 / §8.4 / §8.6）
smoke-m1:
	@./scripts/smoke-m1.sh

# M2 验收：Bot 管理、配置审批全链路、插件生命周期与两级进程、DLQ 与 Health
smoke-m2:
	@./scripts/smoke-m2.sh

# M3 验收：GroupChatPlugin 插件生命周期、仅用户@触发路由、简单 Agent 操控 loopback_agent、Claude Code 单体记忆与防死循环闭环
smoke-m3:
	@./scripts/smoke-m3.sh

# M1 验收：真实 SIGKILL 崩溃恢复演练（详设 §6.8 / §8.4）
drill-crash-m1:
	@./scripts/drill-crash-m1.sh

clean:
	@rm -rf $(BIN_DIR)
