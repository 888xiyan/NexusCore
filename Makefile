BIN_DIR := bin

.PHONY: build test vet fmt proto migrate-probe migrate-up migrate-down smoke-empty clean

build:
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/hub ./hub
	@go build -o $(BIN_DIR)/bot-runtime ./bot-runtime
	@go build -o $(BIN_DIR)/agent-daemon ./agent-daemon
	@go build -o $(BIN_DIR)/cli ./cli
	@go build -o $(BIN_DIR)/migrate ./cmd/migrate
	@echo "build ok: $(BIN_DIR)/{hub,bot-runtime,agent-daemon,cli,migrate}"

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

# 需要环境变量 MYSQL_VAULT_DSN / MYSQL_MEMORY_DSN 指向已运行的 MySQL（8.4 客户端可用，服务端到哪里部署由环境决定；docker-compose 按 M2 排期）
migrate-probe:
ifndef MYSQL_VAULT_DSN
	@echo "未设置 MYSQL_VAULT_DSN / MYSQL_MEMORY_DSN：M0 允许跳过；真正的探活在配置 MySQL 后执行"
	@exit 0
endif
	@go run ./cmd/migrate -cmd probe -dsn "$(MYSQL_VAULT_DSN)" -dir migrations/vault
ifndef MYSQL_MEMORY_DSN
	@echo "未设置 MYSQL_MEMORY_DSN，跳过记忆库探活"
	@exit 0
endif
	@go run ./cmd/migrate -cmd probe -dsn "$(MYSQL_MEMORY_DSN)" -dir migrations/memory

migrate-up:
	@test -n "$(MYSQL_VAULT_DSN)" && go run ./cmd/migrate -cmd up -dsn "$(MYSQL_VAULT_DSN)" -dir migrations/vault
	@test -n "$(MYSQL_MEMORY_DSN)" && go run ./cmd/migrate -cmd up -dsn "$(MYSQL_MEMORY_DSN)" -dir migrations/memory

migrate-down:
	@test -n "$(MYSQL_VAULT_DSN)" && go run ./cmd/migrate -cmd down -dsn "$(MYSQL_VAULT_DSN)" -dir migrations/vault
	@test -n "$(MYSQL_MEMORY_DSN)" && go run ./cmd/migrate -cmd down -dsn "$(MYSQL_MEMORY_DSN)" -dir migrations/memory

smoke-empty: migrate-probe
	@echo "smoke-empty ok（无库环境为跳过模式）"

clean:
	@rm -rf $(BIN_DIR)
