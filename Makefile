BIN_DIR := bin
PLUGIN_BIN_DIR := bin/plugins

.PHONY: build build-plugins test vet fmt proto migrate-probe migrate-up migrate-down smoke-empty clean

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
	@echo "build-plugins ok: $(PLUGIN_BIN_DIR)/plugin_memory"

test:
	@go test ./...

vet:
	@go vet ./...

fmt:
	@go fmt ./...

lint: vet
	@which golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint 未安装，已执行 go vet 兜底"

proto:
	@which buf >/dev/null 2>&1 && buf generate || echo "buf 未安装：go install github.com/bufbuild/buf/cmd/buf@latest 后重试（M0 占位目标）"

# 内核库：使用 MYSQL_KERNEL_DSN 指向已运行的 MySQL（8.4 客户端可用，服务端到哪里部署由环境决定；docker-compose 按 M2 排期）。
# 插件库（memory / vault）的迁移属于插件 install / upgrade 流程（M2/M3），不走本 Makefile。
migrate-probe:
	@if [ -z "$$MYSQL_KERNEL_DSN" ]; then \
		echo "未设置 MYSQL_KERNEL_DSN：M0 允许跳过；真正的探活在配置 MySQL 后执行"; \
		exit 0; \
	fi
	@go run ./cmd/migrate -cmd probe -dsn "$$MYSQL_KERNEL_DSN" -dir migrations/kernel

migrate-up:
	@test -n "$(MYSQL_KERNEL_DSN)" && go run ./cmd/migrate -cmd up -dsn "$(MYSQL_KERNEL_DSN)" -dir migrations/kernel

migrate-down:
	@test -n "$(MYSQL_KERNEL_DSN)" && go run ./cmd/migrate -cmd down -dsn "$(MYSQL_KERNEL_DSN)" -dir migrations/kernel

smoke-empty: migrate-probe
	@echo "smoke-empty ok（无库环境为跳过模式）"

clean:
	@rm -rf $(BIN_DIR)
