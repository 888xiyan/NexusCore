-- 插件身份 / 授权 / 订阅 / 事件注册表 + 项目绑定
-- 依据：内核 MVP 详设 §4.1、架构总结 §3.4 / §6.7
CREATE TABLE kernel_projects (
  project_id   text        PRIMARY KEY,
  display_name text        NOT NULL,
  device_ref   text,
  status       text        NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE kernel_plugins (
  plugin_id       text        PRIMARY KEY CHECK (plugin_id ~ '^plugin_[a-z0-9_]{3,24}$'),
  plugin_type     text        NOT NULL CHECK (plugin_type IN ('ingress','workspace','executor','knowledge')),
  display_name    text        NOT NULL,
  namespace       text        NOT NULL,
  manifest_digest char(64)    NOT NULL,
  manifest_json   jsonb       NOT NULL,
  status          text        NOT NULL DEFAULT 'installed'
                  CHECK (status IN ('installed','enabled','disabled','uninstalled')),
  config_version  bigint,
  config_doc_id   uuid,
  revoked_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE kernel_plugin_grants (
  grant_id    bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  plugin_id   text        NOT NULL REFERENCES kernel_plugins (plugin_id),
  perm_type   text        NOT NULL CHECK (perm_type IN ('subscribe','tool','namespace','transport')),
  perm_target text        NOT NULL,
  scope_filter jsonb,
  granted_by  text        NOT NULL,
  granted_at  timestamptz NOT NULL DEFAULT now(),
  revoked_at  timestamptz,
  UNIQUE (plugin_id, perm_type, perm_target)
);

-- Consumer Group 主体：订阅在 Bot dormant 期间由内核代持。
-- plugin_id 可空：内核侧角色（如 user_bot）的组无插件归属，此时必须有 bot_id。
CREATE TABLE kernel_plugin_subs (
  consumer_group_id text        PRIMARY KEY,
  plugin_id         text        REFERENCES kernel_plugins (plugin_id),
  bot_id            text,
  scope_filter      jsonb,
  status            text        NOT NULL DEFAULT 'active' CHECK (status IN ('active','stopped')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  stopped_at        timestamptz,
  CHECK (plugin_id IS NOT NULL OR bot_id IS NOT NULL)
);

CREATE TABLE kernel_event_subscriptions (
  subscription_id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  consumer_group_id         text        NOT NULL REFERENCES kernel_plugin_subs (consumer_group_id),
  plugin_id                 text        REFERENCES kernel_plugins (plugin_id),
  event_type                text        NOT NULL,
  scope_filter              jsonb,
  payload_policy            jsonb,
  granted_by_user           text        NOT NULL,
  last_dispatched_outbox_id bigint      NOT NULL DEFAULT 0,
  created_at                timestamptz NOT NULL DEFAULT now(),
  UNIQUE (consumer_group_id, event_type)
);

-- 插件只能注册自己命名空间下的事件；内核保留前缀由 registry 校验后写入
CREATE TABLE kernel_plugin_event_registry (
  event_type  text        PRIMARY KEY,
  plugin_id   text        NOT NULL REFERENCES kernel_plugins (plugin_id),
  category    text        NOT NULL CHECK (category IN ('persistent','best_effort')),
  schema_json jsonb,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (event_type LIKE plugin_id || '.%')
);

CREATE INDEX kernel_plugin_grants_active ON kernel_plugin_grants (plugin_id, perm_type) WHERE revoked_at IS NULL;
CREATE INDEX kernel_event_subs_type ON kernel_event_subscriptions (event_type);