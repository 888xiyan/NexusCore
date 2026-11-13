-- 持久事件：Transactional Outbox + 消费去重
-- 依据：内核 MVP 详设 §3.6 / §3.7、架构总结 §6.7
CREATE TABLE kernel_event_outbox (
  outbox_id        bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  event_id         text        NOT NULL UNIQUE,
  event_type       text        NOT NULL,
  correlation_id   text        NOT NULL,
  causation_id     text,
  root_envelope_id text        NOT NULL,
  aggregate_type   text        NOT NULL
                   CHECK (aggregate_type IN ('job','project','audit','plugin','config','bot','agent')),
  aggregate_id     text        NOT NULL,
  -- 只放 ID 与摘要，大对象走 payload_ref 引用
  payload_json     jsonb       NOT NULL,
  status           text        NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','dispatched','archived')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  dispatched_at    timestamptz,
  CHECK (status = 'pending' OR dispatched_at IS NOT NULL)
);

CREATE INDEX kernel_outbox_pending ON kernel_event_outbox (status, outbox_id);
CREATE INDEX kernel_outbox_correlation ON kernel_event_outbox (correlation_id);

CREATE TABLE kernel_event_dedup (
  consumer_group_id text        NOT NULL,
  event_id          text        NOT NULL,
  processed_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer_group_id, event_id)
);