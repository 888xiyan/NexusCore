# ============================================================
# Stage 1: 前端静态资产构建 (Node.js)
# ============================================================
FROM node:22-alpine AS frontend-builder
WORKDIR /build/Frontend

# 先复制依赖定义，充分利用 Docker 层缓存
COPY Frontend/package*.json ./
RUN npm install

# 复制前端源码并执行打包构建
COPY Frontend/ ./
RUN npm run build

# ============================================================
# Stage 2: Go 微内核与插件二进制构建 (Golang)
# ============================================================
FROM golang:1.24-bookworm AS backend-builder
WORKDIR /build

# 下载 Go 依赖
COPY go.mod go.sum ./
RUN go mod download

# 复制全量源码
COPY . .

# 将 Stage 1 生成的前端静态产物复制并嵌入 Go 包目录
RUN rm -rf hub/web/dist/* && cp -r /build/Frontend/dist/* hub/web/dist/

# 编译微内核 5 大核心二进制 (带 -s -w 优化缩减二进制体积)
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/hub ./hub && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/bot-runtime ./bot-runtime && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/agent-daemon ./agent-daemon && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/cli ./cli && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/migrate ./cmd/migrate

# 编译插件二进制
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/plugins/plugin_memory ./plugins/plugin_memory && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/plugins/plugin_groupchat ./GroupChatPlugin && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/plugins/plugin_agent_discovery ./plugins/plugin_agent_discovery && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/plugins/plugin_bot_creator ./plugins/plugin_bot_creator && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/plugins/plugin_demo ./plugins/plugin_demo && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/plugins/plugin_tool_center ./plugins/plugin_tool_center && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bin/plugins/plugin_marketplace ./plugins/plugin_marketplace

# 复制数据库迁移文件及插件元数据配置
RUN mkdir -p /out/migrations && cp -r migrations/* /out/migrations/
RUN mkdir -p /out/plugins && cp -r plugins/* /out/plugins/ && cp -r GroupChatPlugin /out/plugins/GroupChatPlugin

# ============================================================
# Stage 3: 最小化轻量运行时 (Debian Bookworm Slim)
# ============================================================
FROM debian:bookworm-slim AS runtime

# 安装基础运行依赖与证书
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    curl \
    postgresql-client \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 从构建器拷贝产物与资产
COPY --from=backend-builder /out/bin /app/bin
COPY --from=backend-builder /out/migrations /app/migrations
COPY --from=backend-builder /out/plugins /app/plugins

# 将二进制加入系统 PATH
ENV PATH="/app/bin:${PATH}"

# 默认工作环境与端口
ENV NEXUS_HTTP_ADDR="0.0.0.0:7200"
EXPOSE 7200 50051

VOLUME ["/app/data"]

# 默认启动 Hub 微内核网关
ENTRYPOINT ["/app/bin/hub"]
CMD ["-http", "0.0.0.0:7200"]
