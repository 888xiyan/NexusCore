BIN_DIR := bin
PLUGIN_BIN_DIR := bin/plugins

.PHONY: build build-frontend build-all build-plugins test test-pg vet fmt proto migrate-probe migrate-up migrate-down compose-up compose-down smoke-empty smoke-m1 smoke-m2 smoke-m3 smoke-m4 smoke-m5 smoke-m6 smoke-m7 smoke-m8 smoke-m9 smoke-m10 smoke-m11 smoke-m12 smoke-m13 launch-m4 drill-crash-m1 clean

build-frontend:
	@echo "==> 构建前端生产产物..."
	@cd Frontend && npm install && npm run build
	@echo "==> 同步静态资源到 hub/web/dist..."
	@mkdir -p hub/web/dist
	@cp -r Frontend/dist/* hub/web/dist/
	@echo "前端静态产物已就绪并同步至 hub/web/dist"

build:
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/hub ./hub
	@go build -o $(BIN_DIR)/bot-runtime ./bot-runtime
	@go build -o $(BIN_DIR)/agent-daemon ./agent-daemon
	@go build -o $(BIN_DIR)/cli ./cli
	@go build -o $(BIN_DIR)/migrate ./cmd/migrate
	@echo "build ok: $(BIN_DIR)/{hub,bot-runtime,agent-daemon,cli,migrate}"

build-all: build-frontend build build-plugins
	@echo "build-all ok: 前端嵌入完成，微内核与插件全量编译完毕"

build-plugins:
	@mkdir -p $(PLUGIN_BIN_DIR)
	@go build -o $(PLUGIN_BIN_DIR)/plugin_memory ./plugins/plugin_memory
	@go build -o $(PLUGIN_BIN_DIR)/plugin_demo ./plugins/plugin_demo
	@go build -o $(PLUGIN_BIN_DIR)/plugin_groupchat ./GroupChatPlugin
	@go build -o $(PLUGIN_BIN_DIR)/plugin_agent_discovery ./plugins/plugin_agent_discovery
	@go build -o $(PLUGIN_BIN_DIR)/plugin_bot_creator ./plugins/plugin_bot_creator
	@go build -o $(PLUGIN_BIN_DIR)/plugin_tool_center ./plugins/plugin_tool_center
	@DISCOVERY_DIGEST=$$(sha256sum $(PLUGIN_BIN_DIR)/plugin_agent_discovery | awk '{print $$1}'); \
	python3 -c "import json; p='plugins/plugin_agent_discovery/manifest.json'; m=json.load(open(p)); m['binary_digest']='$$DISCOVERY_DIGEST'; json.dump(m, open(p,'w'), indent=2)"
	@CREATOR_DIGEST=$$(sha256sum $(PLUGIN_BIN_DIR)/plugin_bot_creator | awk '{print $$1}'); \
	python3 -c "import json; p='plugins/plugin_bot_creator/manifest.json'; m=json.load(open(p)); m['binary_digest']='$$CREATOR_DIGEST'; json.dump(m, open(p,'w'), indent=2)"
	@echo "build-plugins ok: $(PLUGIN_BIN_DIR)/{plugin_memory,plugin_demo,plugin_groupchat,plugin_agent_discovery,plugin_bot_creator,plugin_tool_center}"

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

# 内核库：使用 NEXUS_PG_KERNEL_DSN（运行角色）指向 PostgreSQL；迁移使用 NEXUS_PG_MIGRATE_DSN
# （未设置时回退运行 DSN；默认部署的运行角色无 DDL 权限，请显式指定迁移角色）。
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

# M4 验收：Vue 3 前端工作台构建、多 Bot 群聊交互、单体记忆抽屉编辑与重启、Agent 实时监控与端到端实机演练
smoke-m4:
	@./scripts/smoke-m4.sh

# M5 验收：本地 Coding Agent 搜寻、LocalCLIDriver 受控执行、Bot 结构化调度与抗噪音蒸馏闭环
smoke-m5:
	@./scripts/smoke-m5.sh

smoke-m6:
	@./scripts/smoke-m6.sh

# M7 验收：前端微模块化、ESM 动态插件注册表、宿主纯净 Shell 与视区解耦
smoke-m7:
	@./scripts/smoke-m7.sh

# M8 验收：基础模块无头化沉淀、接口契约入配置中心、DAG 拓扑加载、循环依赖拦截与两阶段依赖治理
smoke-m8:
	@./scripts/smoke-m8.sh

# M9 验收：全局 UI 原型、标准群聊插件与沙盒工具轨迹审计闭环
smoke-m9:
	@./scripts/smoke-m9.sh

# M10 验收：工具中心、MCP/Skill 插件化治理与中台 Bot 审批闭环
smoke-m10:
	@./scripts/smoke-m10.sh

# M11 验收：Bot 身份可列举，安装/升级/停用/卸载成功与失败都留在内核
smoke-m11:
	@./scripts/smoke-m11.sh

# M12 验收：工具、Skill、MCP 登记与授权在工具插件重启后仍在
smoke-m12:
	@./scripts/smoke-m12.sh

# M13 验收：默认离线、搜索 DNS/HTTPS 通知 user_bot、未绑定探针不能派发 CLI
smoke-m13:
	@./scripts/smoke-m13.sh

# M4 实机启动：一键启动微内核网关、群聊插件、受控探针与 Vue 3 前端工作台
launch-m4:
	@./scripts/launch-dev.sh

# M1 验收：真实 SIGKILL 崩溃恢复演练（详设 §6.8 / §8.4）
drill-crash-m1:
	@./scripts/drill-crash-m1.sh

clean:
	@rm -rf $(BIN_DIR)
