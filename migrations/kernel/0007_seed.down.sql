DELETE FROM kernel_event_subscriptions WHERE consumer_group_id = 'user_bot';
DELETE FROM kernel_plugin_subs WHERE consumer_group_id = 'user_bot';
DELETE FROM kernel_config_audit WHERE doc_id IN (
  '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002');
DELETE FROM kernel_config_documents WHERE doc_id IN (
  '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002');
DELETE FROM bots WHERE bot_id IN ('echo_bot', 'user_bot');
DELETE FROM bot_manifest WHERE bot_id IN ('echo_bot', 'user_bot');