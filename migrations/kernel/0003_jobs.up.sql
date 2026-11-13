-- 持久任务队列与副作用清单（D-04 四语义载体）
-- 依据：内核 MVP 详设 §3.5 / §4.2、架构总结 §6.5 / §6.6
CREATE TABLE kernel_jobs (
  job_id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  queue_name       text        NOT NULL,
  job_type         text        NOT NULL,
  status           text        NOT NULL DEFAULT 'queued'
                   CHECK (status IN ('queued','leasing','running','retry_backoff','suspended','done','dead','cancelled')),
  envelope_id      text,
  bot_id           text,
  project_id       text,
  partition_key    text,
  correlation_id   text        NOT NULL,
  causation_id     text,
  root_envelope_id text        NOT NULL,
  causal_path_json jsonb,
  traceparent      text,
  idempotency_key  text        UNIQUE,
  parent_job_id    bigint      REFERENCES kernel_jobs (job_id),
  lease_owner      text,
  lease_token      text,
  lease_until      timestamptz,
  attempt_count    int         NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_retry_at    timestamptz,
  priority         int         NOT NULL DEFAULT 0,
  payload_hash     char(64),
  payload_ref      text,
  payload_json     jsonb,
  result_json      jsonb,
  -- 领取时继承的 Bot 实例配置版本（审计用，不进信封）
  config_version   bigint,
  suspend_reason   text,
  resume_decision  text        CHECK (resume_decision IN ('continue','replay','discard')),
  resume_by        text,
  resumed_at       timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  -- 人工确认必须同时留下决策与确认人
  CHECK (resume_decision IS NULL OR (resume_by IS NOT NULL AND resumed_at IS NOT NULL)),
  CHECK (status <> 'suspended' OR suspend_reason IS NOT NULL)
) WITH (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.05, autovacuum_analyze_scale_factor = 0.02);

CREATE INDEX kernel_jobs_dispatch   ON kernel_jobs (queue_name, status, next_retry_at, priority);
CREATE INDEX kernel_jobs_correlation ON kernel_jobs (correlation_id);
CREATE INDEX kernel_jobs_lease      ON kernel_jobs (status, lease_until);
CREATE INDEX kernel_jobs_partition  ON kernel_jobs (queue_name, partition_key, status, job_id);
CREATE INDEX kernel_jobs_root       ON kernel_jobs (root_envelope_id);

CREATE TABLE kernel_job_intents (
  intent_id      bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  job_id         bigint      NOT NULL REFERENCES kernel_jobs (job_id),
  seq            int         NOT NULL CHECK (seq >= 0),
  tool           text        NOT NULL,
  request_digest char(64)    NOT NULL,
  status         text        NOT NULL DEFAULT 'declared'
                 CHECK (status IN ('declared','executed','failed','skipped')),
  executed_at    timestamptz,
  detail_json    jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (job_id, seq)
);

CREATE INDEX kernel_job_intents_job ON kernel_job_intents (job_id, seq);