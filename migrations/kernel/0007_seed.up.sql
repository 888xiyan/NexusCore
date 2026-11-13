-- M1 种子：echo_bot 测试夹具 + user_bot 常驻席位 + active 配置 v1
-- 依据：内核 MVP 详设 §6.1 第 1 步、ConfigCenter 详设 §9 M1 第 2 条
-- 说明：binary_digest / manifest_digest 先置全零占位，由 `cli bot fixture-echo` 绑定真实二进制后写入；
--       占位期间 supervisor 的制品校验必然失败（fail closed），不会误执行。
--       配置 checksum 为 canonical JSON（键排序、紧凑、无 HTML 转义）的 sha256，与 config 模块算法一致。

INSERT INTO bot_manifest (
  bot_id, role, managed_by, parent_plugin, owner, display_name,
  binary_path, binary_digest, manifest_digest, capabilities, requiring_tools,
  memory_profile, reserved, activation_mode, idle_ttl_seconds, start_timeout_seconds,
  prompt_ref, model, config_version, config_doc_id
) VALUES
  ('echo_bot', 'plugin', 'kernel', NULL, NULL, 'Echo Bot（M1 冒烟夹具）',
   'bin/bot-runtime', repeat('0', 64), repeat('0', 64), '["echo"]'::jsonb, '[]'::jsonb,
   NULL, false, 'lazy', 300, 30,
   'echo', 'stub', 1, '00000000-0000-0000-0000-000000000001'),
  ('user_bot', 'user', 'kernel', NULL, NULL, '用户总线席位（CLI 为其投递代理）',
   'bin/bot-runtime', repeat('0', 64), repeat('0', 64), '["cli"]'::jsonb, '[]'::jsonb,
   NULL, true, 'lazy', 300, 30,
   NULL, NULL, NULL, NULL);

INSERT INTO bots (bot_id, desired_state, desired_by, observed_state, enabled, min_instances, config_version)
VALUES ('echo_bot', 'start', 'create_bot', 'dormant', true, 0, 1),
       ('user_bot', 'start', 'create_bot', 'dormant', true, 0, NULL);

-- bot / echo_bot / default v1（active，seed 由 user_bot 批准）
INSERT INTO kernel_config_documents (
  doc_id, scope_type, scope_id, config_key, version, status, spec, schema_version,
  checksum, base_version, created_by, approved_by, approved_checksum,
  approved_at, activated_at
) VALUES (
  '00000000-0000-0000-0000-000000000001', 'bot', 'echo_bot', 'default', 1, 'active',
  '{"activation_mode":"lazy","capabilities":["echo"],"idle_ttl_seconds":300,"model":"stub","prompt_ref":"echo","start_timeout_seconds":30}'::jsonb,
  1, 'e71009676c0cefd144f872dde5f0474d9c39ee2d29faf9cd36262666cb8e5e9d', 0,
  'user_bot', 'user_bot', 'e71009676c0cefd144f872dde5f0474d9c39ee2d29faf9cd36262666cb8e5e9d',
  now(), now()
);

-- global / kernel / default v1（active，内核运行参数）
INSERT INTO kernel_config_documents (
  doc_id, scope_type, scope_id, config_key, version, status, spec, schema_version,
  checksum, base_version, created_by, approved_by, approved_checksum,
  approved_at, activated_at
) VALUES (
  '00000000-0000-0000-0000-000000000002', 'global', 'kernel', 'default', 1, 'active',
  '{"heartbeat_seconds":10,"lease_seconds":60,"max_causal_depth":32,"outbox_batch_size":100,"outbox_scan_interval_ms":250,"poison_pill_attempts":5,"retry_backoff_seconds":5,"snapshot_retention_days":30,"wakeup_poll_seconds":5}'::jsonb,
  1, 'd115727d74d034f5d3d137173ece6de5e74a71720be182fa0b85e5f8d691a2f7', 0,
  'user_bot', 'user_bot', 'd115727d74d034f5d3d137173ece6de5e74a71720be182fa0b85e5f8d691a2f7',
  now(), now()
);

INSERT INTO kernel_config_audit (doc_id, action, actor, from_status, to_status, checksum, reason)
VALUES ('00000000-0000-0000-0000-000000000001', 'seed', 'user_bot', NULL, 'active',
        'e71009676c0cefd144f872dde5f0474d9c39ee2d29faf9cd36262666cb8e5e9d', 'M1 迁移种子：echo_bot active bot 配置 v1'),
       ('00000000-0000-0000-0000-000000000002', 'seed', 'user_bot', NULL, 'active',
        'd115727d74d034f5d3d137173ece6de5e74a71720be182fa0b85e5f8d691a2f7', 'M1 迁移种子：global 默认运行参数 v1');

-- user_bot 的 Consumer Group：内核代持，CLI 作为订阅端拉取
INSERT INTO kernel_plugin_subs (consumer_group_id, plugin_id, bot_id, status)
VALUES ('user_bot', NULL, 'user_bot', 'active');

INSERT INTO kernel_event_subscriptions (consumer_group_id, plugin_id, event_type, granted_by_user)
VALUES ('user_bot', NULL, 'bot.reply', 'user_bot'),
       ('user_bot', NULL, 'kernel.config.changed', 'user_bot');