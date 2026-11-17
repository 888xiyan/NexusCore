-- M11：安装 / 升级 / 禁用 / 卸载写入既有 kernel_config_audit。
-- 不新增 kernel_plugin_audit_logs。动作记在 action，插件、摘要、结果与原因记在
-- config_key=lifecycle 的文档 spec。文档状态固定为 rejected，避免占用 active 配置。

ALTER TABLE kernel_config_audit DROP CONSTRAINT IF EXISTS kernel_config_audit_action_check;

ALTER TABLE kernel_config_audit
  ADD CONSTRAINT kernel_config_audit_action_check
  CHECK (action IN (
    'propose', 'security_review', 'approve', 'reject', 'activate', 'retire',
    'rollback', 'read', 'seed',
    'install', 'upgrade', 'disable', 'uninstall'
  ));
