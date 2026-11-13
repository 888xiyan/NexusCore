-- Bot 制品 / 生命周期 / 运行实例 + Agent 身份
-- 依据：内核 MVP 详设 §4.1、BotMvp §6.1-6.2、ConfigCenter §6、D-20 决策执行分离
-- 归属划分：bot_manifest = 不可变制品 + 配置投影（只由激活事务写）；
--           bots = 生命周期期望/观测（desired 只由 create_bot 写，supervisor 只写 observed）；
--           bot_instances = 进程级运行实例。
CREATE TABLE bot_manifest (
  bot_id                text        PRIMARY KEY CHECK (bot_id ~ '^[a-z0-9_]{5,64}$'),
  role                  text        NOT NULL CHECK (role IN ('user','create','monitor','security','plugin')),
  managed_by            text        NOT NULL,
  parent_plugin         text        REFERENCES kernel_plugins (plugin_id),
  owner                 text,
  display_name          text        NOT NULL,
  binary_path           text        NOT NULL,
  binary_digest         char(64)    NOT NULL,
  manifest_digest       char(64)    NOT NULL,
  capabilities          jsonb       NOT NULL DEFAULT '[]'::jsonb,
  requiring_tools       jsonb       NOT NULL DEFAULT '[]'::jsonb,
  -- 插件声明字段：内核原样存储，不解释、不校验语义
  memory_profile        jsonb,
  reserved              boolean     NOT NULL DEFAULT false,
  activation_mode       text        NOT NULL DEFAULT 'lazy' CHECK (activation_mode IN ('lazy','resident')),
  idle_ttl_seconds      int         NOT NULL DEFAULT 300 CHECK (idle_ttl_seconds > 0),
  start_timeout_seconds int         NOT NULL DEFAULT 30 CHECK (start_timeout_seconds > 0),
  prompt_ref            text,
  model                 text,
  config_version        bigint,
  config_doc_id         uuid,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  -- 保留名只能属于内核，任何插件不得持有
  CHECK (bot_id NOT IN ('user_bot','create_bot','monitor_bot','security_bot') OR parent_plugin IS NULL)
);

CREATE TABLE bots (
  bot_id         text        PRIMARY KEY REFERENCES bot_manifest (bot_id),
  desired_state  text        NOT NULL DEFAULT 'stop' CHECK (desired_state IN ('start','stop','drain')),
  desired_by     text        NOT NULL DEFAULT 'create_bot' CHECK (desired_by = 'create_bot'),
  observed_state text        NOT NULL DEFAULT 'dormant'
                 CHECK (observed_state IN ('dormant','starting','ready','busy','draining','stopped','crashed')),
  enabled        boolean     NOT NULL DEFAULT true,
  min_instances  int         NOT NULL DEFAULT 0 CHECK (min_instances >= 0),
  config_version bigint,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE bot_instances (
  instance_id       bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  bot_id            text        NOT NULL REFERENCES bot_manifest (bot_id),
  observed_state    text        NOT NULL DEFAULT 'starting'
                    CHECK (observed_state IN ('dormant','starting','ready','busy','draining','stopped','crashed')),
  pid               int,
  pgid              int,
  pid_start_time    timestamptz,
  binary_digest     char(64)    NOT NULL,
  config_version    bigint      NOT NULL,
  lease_token       text,
  last_heartbeat_at timestamptz,
  started_at        timestamptz NOT NULL DEFAULT now(),
  stopped_at        timestamptz,
  last_error        text,
  restart_count     int         NOT NULL DEFAULT 0,
  backoff_until     timestamptz
);

CREATE TABLE kernel_agents (
  agent_id       text        PRIMARY KEY,
  display_name   text,
  capabilities   jsonb       NOT NULL DEFAULT '[]'::jsonb,
  project_scope  jsonb,
  status         text        NOT NULL DEFAULT 'offline' CHECK (status IN ('online','offline','revoked')),
  config_version bigint,
  config_doc_id  uuid,
  last_seen_at   timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX bot_instances_active ON bot_instances (bot_id, observed_state) WHERE stopped_at IS NULL;
CREATE INDEX bot_manifest_plugin ON bot_manifest (parent_plugin);