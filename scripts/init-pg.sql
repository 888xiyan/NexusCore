-- PostgreSQL 16+ 初始化脚本（docker-compose / dev-pg 共享）
-- 依据：M0/M1 存储设计与 M2 部署规范
-- 创建迁移角色与运行角色，隔离 schema 权限
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nexus_migrator') THEN
    CREATE ROLE nexus_migrator LOGIN PASSWORD 'nexus_dev_migrate';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nexus_kernel') THEN
    CREATE ROLE nexus_kernel LOGIN PASSWORD 'nexus_dev_kernel';
  END IF;
END
$$;

GRANT CONNECT ON DATABASE nexus TO nexus_migrator, nexus_kernel;
GRANT CREATE ON DATABASE nexus TO nexus_migrator;
ALTER ROLE nexus_migrator CREATEDB;

CREATE SCHEMA IF NOT EXISTS kernel AUTHORIZATION nexus_migrator;

ALTER ROLE nexus_migrator SET search_path = kernel;
ALTER ROLE nexus_kernel   SET search_path = kernel;

GRANT USAGE ON SCHEMA kernel TO nexus_kernel;

ALTER DEFAULT PRIVILEGES FOR ROLE nexus_migrator IN SCHEMA kernel
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nexus_kernel;
ALTER DEFAULT PRIVILEGES FOR ROLE nexus_migrator IN SCHEMA kernel
  GRANT USAGE, SELECT ON SEQUENCES TO nexus_kernel;
