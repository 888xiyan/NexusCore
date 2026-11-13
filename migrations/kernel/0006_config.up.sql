-- 配置中心：文档 / 版本 / 审批审计 / 密钥引用（只存期望状态，不存密钥值）
-- 依据：ConfigCenter 详设 §3、内核 MVP 详设 §3.10
CREATE TABLE kernel_config_documents (
  doc_id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  scope_type           text        NOT NULL CHECK (scope_type IN ('plugin','bot','agent','global')),
  scope_id             text        NOT NULL,
  config_key           text        NOT NULL DEFAULT 'default',
  version              bigint      NOT NULL CHECK (version > 0),
  status               text        NOT NULL
                       CHECK (status IN ('draft','proposed','security_review','approved','active','retired','rejected')),
  spec                 jsonb       NOT NULL,
  schema_version       int         NOT NULL DEFAULT 1,
  checksum             char(64)    NOT NULL,
  base_version         bigint,
  created_by           text        NOT NULL,
  approved_by          text,
  approved_checksum    char(64),
  security_decision_id bigint      REFERENCES kernel_security_decisions (decision_id),
  created_at           timestamptz NOT NULL DEFAULT now(),
  approved_at          timestamptz,
  activated_at         timestamptz,
  retired_at           timestamptz,
  UNIQUE (scope_type, scope_id, config_key, version),
  -- 只有 user_bot 能批准
  CHECK (approved_by IS NULL OR approved_by = 'user_bot'),
  -- 批准必须绑定 checksum
  CHECK (approved_by IS NULL OR approved_checksum IS NOT NULL),
  -- global 域的 scope_id 固定为 kernel
  CHECK (scope_type <> 'global' OR scope_id = 'kernel')
);

-- 每个 scope+config_key 同时只有一个 active
CREATE UNIQUE INDEX kernel_config_one_active
  ON kernel_config_documents (scope_type, scope_id, config_key)
  WHERE status = 'active';

CREATE INDEX kernel_config_pending ON kernel_config_documents (status, created_at);

CREATE TABLE kernel_config_audit (
  audit_id       bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  doc_id         uuid        NOT NULL,
  action         text        NOT NULL
                 CHECK (action IN ('propose','security_review','approve','reject','activate','retire','rollback','read','seed')),
  actor          text        NOT NULL,
  from_status    text,
  to_status      text,
  checksum       char(64),
  reason         text,
  correlation_id text,
  created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX kernel_config_audit_doc ON kernel_config_audit (doc_id, created_at);

REVOKE UPDATE, DELETE, TRUNCATE ON kernel_config_audit FROM nexus_kernel;

CREATE TABLE kernel_secret_refs (
  ref_id      text        PRIMARY KEY,
  provider    text        NOT NULL CHECK (provider IN ('env','file','keyring')),
  locator     text        NOT NULL,
  description text,
  created_by  text,
  deprecated_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);