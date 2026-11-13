-- 入口审计 / 加密上下文快照 / 载荷引用 / 健康证据 / 安全审查
-- 依据：内核 MVP 详设 §3.4 / §4.2、架构总结 §6.9 / §6.10、BotMvp §6.3
-- 审计表 append-only：运行角色只被授予 INSERT / SELECT，UPDATE / DELETE / TRUNCATE 显式回收。
CREATE TABLE kernel_ingress_log (
  log_id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  envelope_id         text,
  schema_version      int,
  msg_type            text,
  from_bot_id         text,
  channel             text,
  channel_ref         text,
  correlation_id      text,
  causation_id        text,
  root_envelope_id    text,
  causal_path_json    jsonb,
  traceparent         text,
  idempotency_key     text,
  decision            text        NOT NULL CHECK (decision IN ('accepted','rejected','duplicate')),
  reason              text,
  payload_hash        char(64),
  payload_ref         text,
  context_snapshot_id text,
  policy_version      text,
  received_at         timestamptz,
  persisted_at        timestamptz NOT NULL DEFAULT now()
);

-- 被拒绝的信封可能缺必填字段，故除 decision 外均可空：拒绝也要留完整现场
CREATE INDEX kernel_ingress_correlation ON kernel_ingress_log (correlation_id);
CREATE INDEX kernel_ingress_causation   ON kernel_ingress_log (causation_id);
CREATE INDEX kernel_ingress_root        ON kernel_ingress_log (root_envelope_id);
CREATE INDEX kernel_ingress_envelope    ON kernel_ingress_log (envelope_id);
CREATE INDEX kernel_ingress_idem        ON kernel_ingress_log (idempotency_key);

REVOKE UPDATE, DELETE, TRUNCATE ON kernel_ingress_log FROM nexus_kernel;

CREATE TABLE kernel_context_snapshots (
  snapshot_id       text        PRIMARY KEY,
  envelope_id       text        NOT NULL,
  content_hash      char(64)    NOT NULL,
  storage_uri       text        NOT NULL,
  encryption_key_id text        NOT NULL,
  redaction_policy  text        NOT NULL DEFAULT 'default',
  size_bytes        bigint      NOT NULL CHECK (size_bytes >= 0),
  created_at        timestamptz NOT NULL DEFAULT now(),
  -- 过期只删快照，日志与 hash 作为审计墓碑保留
  expires_at        timestamptz NOT NULL
);

CREATE TABLE kernel_payloads (
  payload_id        text        PRIMARY KEY,
  envelope_id       text        NOT NULL,
  storage_uri       text        NOT NULL,
  encryption_key_id text        NOT NULL,
  content_hash      char(64)    NOT NULL,
  size_bytes        bigint      NOT NULL CHECK (size_bytes >= 0),
  created_at        timestamptz NOT NULL DEFAULT now(),
  expires_at        timestamptz NOT NULL
);

CREATE INDEX kernel_payloads_envelope ON kernel_payloads (envelope_id);

CREATE TABLE kernel_health_reports (
  report_id       text        PRIMARY KEY,
  source_type     text        NOT NULL CHECK (source_type IN ('bot','agent','supervisor')),
  source_id       text        NOT NULL,
  target_type     text        NOT NULL CHECK (target_type IN ('bot','agent','node','project','plugin_process')),
  target_id       text        NOT NULL,
  failure_class   text        NOT NULL,
  severity        text        NOT NULL CHECK (severity IN ('info','warning','critical')),
  evidence_json   jsonb,
  observed_at     timestamptz NOT NULL,
  correlation_id  text        NOT NULL,
  causation_id    text,
  idempotency_key text        UNIQUE,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX kernel_health_target ON kernel_health_reports (target_id, failure_class, observed_at);

CREATE TABLE kernel_security_decisions (
  decision_id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  subject_type        text        NOT NULL
                      CHECK (subject_type IN ('plugin','bot','hot_update','config_proposal','plugin_process')),
  subject_id          text        NOT NULL,
  action              text        NOT NULL
                      CHECK (action IN ('register','hot_update','approve','review_config','force_kill')),
  decision            text        NOT NULL DEFAULT 'pending'
                      CHECK (decision IN ('pending','approved','rejected','rolled_back')),
  requested_by        text        NOT NULL,
  decided_by          text        NOT NULL DEFAULT 'security_bot',
  reason              text,
  checksum            char(64),
  evidence_json       jsonb,
  grace_period_expiry timestamptz,
  correlation_id      text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX kernel_security_subject ON kernel_security_decisions (subject_type, subject_id, decision);