DELETE FROM bots WHERE bot_id IN ('create_bot', 'monitor_bot', 'security_bot');
DELETE FROM bot_manifest WHERE bot_id IN ('create_bot', 'monitor_bot', 'security_bot');
DROP TABLE IF EXISTS kernel_plugin_processes;
