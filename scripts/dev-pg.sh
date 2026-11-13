#!/usr/bin/env bash
# M1 开发 / 集成测试用 PostgreSQL 夹具。
# docker-compose 按用户决策留在 M2，本脚本只是可选的本地测试库，不是部署形态。
# 只创建 kernel schema 与两个角色；memory / vault 属于 plugin_memory（M3），此处不建。
set -euo pipefail

CONTAINER="${NEXUS_DEV_PG_CONTAINER:-nexus-pg-m1}"
PORT="${NEXUS_DEV_PG_PORT:-55432}"
IMAGE="${NEXUS_DEV_PG_IMAGE:-postgres:16-alpine}"
SUPER_PASS="${NEXUS_DEV_PG_SUPER_PASS:-nexus_dev_super}"
MIGRATE_PASS="${NEXUS_DEV_PG_MIGRATE_PASS:-nexus_dev_migrate}"
KERNEL_PASS="${NEXUS_DEV_PG_KERNEL_PASS:-nexus_dev_kernel}"

# 口令直接内插进 SQL，故先限制字符集，避免注入。
for v in "$SUPER_PASS" "$MIGRATE_PASS" "$KERNEL_PASS"; do
  if [[ ! "$v" =~ ^[A-Za-z0-9_]+$ ]]; then
    echo "口令只允许字母数字下划线（开发夹具限制）：$v" >&2
    exit 2
  fi
done

if ! docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
  docker run -d --name "$CONTAINER" \
    -e POSTGRES_PASSWORD="$SUPER_PASS" -e POSTGRES_DB=nexus \
    -p "127.0.0.1:${PORT}:5432" "$IMAGE" >/dev/null
  echo "已创建容器 $CONTAINER（镜像 $IMAGE，端口 $PORT）"
elif ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
  docker start "$CONTAINER" >/dev/null
  echo "已启动既有容器 $CONTAINER"
fi

for _ in $(seq 1 60); do
  if docker exec "$CONTAINER" pg_isready -U postgres -d nexus >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$CONTAINER" pg_isready -U postgres -d nexus >/dev/null

docker exec -i "$CONTAINER" psql -U postgres -d nexus -v ON_ERROR_STOP=1 >/dev/null <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nexus_migrator') THEN
    CREATE ROLE nexus_migrator LOGIN PASSWORD '$MIGRATE_PASS';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nexus_kernel') THEN
    CREATE ROLE nexus_kernel LOGIN PASSWORD '$KERNEL_PASS';
  END IF;
END
\$\$;

GRANT CONNECT ON DATABASE nexus TO nexus_migrator, nexus_kernel;
GRANT CREATE ON DATABASE nexus TO nexus_migrator;
-- 集成测试按包创建独立 database 做隔离（go test ./... 并行执行），故迁移角色需要 CREATEDB。
-- 这是开发夹具权限，生产部署不应授予。
ALTER ROLE nexus_migrator CREATEDB;

CREATE SCHEMA IF NOT EXISTS kernel AUTHORIZATION nexus_migrator;

ALTER ROLE nexus_migrator SET search_path = kernel;
ALTER ROLE nexus_kernel   SET search_path = kernel;

GRANT USAGE ON SCHEMA kernel TO nexus_kernel;
-- 运行角色只有 DML，无 DDL；memory / vault 不授予任何权限。
ALTER DEFAULT PRIVILEGES FOR ROLE nexus_migrator IN SCHEMA kernel
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nexus_kernel;
ALTER DEFAULT PRIVILEGES FOR ROLE nexus_migrator IN SCHEMA kernel
  GRANT USAGE, SELECT ON SEQUENCES TO nexus_kernel;
SQL

cat <<EOF
bootstrap ok

export NEXUS_PG_MIGRATE_DSN="postgres://nexus_migrator:${MIGRATE_PASS}@127.0.0.1:${PORT}/nexus"
export NEXUS_PG_KERNEL_DSN="postgres://nexus_kernel:${KERNEL_PASS}@127.0.0.1:${PORT}/nexus"
EOF