-- M2: 插件进程表 (kernel_plugin_processes) 与内核侧三角色 (create_bot / monitor_bot / security_bot) 种子
-- 依据: PluginMvp §6.3、BotMvp §2 / §6.5

CREATE TABLE kernel_plugin_processes (
  process_id        text        PRIMARY KEY,
  plugin_id         text        NOT NULL UNIQUE REFERENCES kernel_plugins (plugin_id) ON DELETE CASCADE,
  owner_bot_id      text        NOT NULL REFERENCES bot_manifest (bot_id),
  owner_instance_id text        NOT NULL,
  pid               int         NOT NULL,
  pgid              int         NOT NULL,
  pid_start_time    bigint      NOT NULL,
  binary_digest     char(64)    NOT NULL,
  config_version    bigint      NOT NULL DEFAULT 0,
  state             text        NOT NULL
                    CHECK (state IN ('pending','starting','running','stopping','stopped','killed','crashed','failed','orphaned')),
  stop_mode         text        CHECK (stop_mode IN ('graceful','force')),
  stop_requested_at timestamptz,
  started_at        timestamptz NOT NULL DEFAULT now(),
  stopped_at        timestamptz,
  last_seen_at      timestamptz,
  last_error        text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX kernel_plugin_processes_state ON kernel_plugin_processes (state, last_seen_at);

-- 种子：内核侧三角色（create_bot 为 lazy worker，monitor_bot / security_bot 为 resident）
INSERT INTO bot_manifest (
  bot_id, role, managed_by, parent_plugin, owner, display_name,
  binary_path, binary_digest, manifest_digest, capabilities, requiring_tools,
  memory_profile, reserved, activation_mode, idle_ttl_seconds, start_timeout_seconds,
  prompt_ref, model, config_version, config_doc_id
) VALUES
  ('create_bot', 'create', 'kernel', NULL, NULL, 'Bot 工厂与生命周期决策者',
   'bin/bot-runtime', repeat('0', 64), repeat('0', 64), '["control"]'::jsonb, '[]'::jsonb,
   NULL, true, 'lazy', 120, 30,
   NULL, NULL, NULL, NULL),
  ('monitor_bot', 'monitor', 'kernel', NULL, NULL, '系统健康与配置漂移监督者',
   'bin/bot-runtime', repeat('0', 64), repeat('0', 64), '["health"]'::jsonb, '[]'::jsonb,
   NULL, true, 'resident', 300, 30,
   NULL, NULL, NULL, NULL),
  ('security_bot', 'security', 'kernel', NULL, NULL, '安全审查与配置预审者',
   'bin/bot-runtime', repeat('0', 64), repeat('0', 64), '["security"]'::jsonb, '[]'::jsonb,
   NULL, true, 'resident', 300, 30,
   NULL, NULL, NULL, NULL)
ON CONFLICT (bot_id) DO NOTHING;

INSERT INTO bots (bot_id, desired_state, desired_by, observed_state, enabled, min_instances, config_version)
VALUES
  ('create_bot', 'start', 'create_bot', 'dormant', true, 0, NULL),
  ('monitor_bot', 'start', 'create_bot', 'dormant', true, 1, NULL),
  ('security_bot', 'start', 'create_bot', 'dormant', true, 1, NULL)
ON CONFLICT (bot_id) DO NOTHING;
