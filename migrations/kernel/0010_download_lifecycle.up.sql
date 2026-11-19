-- M14：下载成败写入既有 kernel_config_audit。不新增审计表。
-- 下载不是安装：文档仍用 config_key=lifecycle，且不把插件标成已安装。

ALTER TABLE kernel_config_audit DROP CONSTRAINT IF EXISTS kernel_config_audit_action_check;

ALTER TABLE kernel_config_audit
  ADD CONSTRAINT kernel_config_audit_action_check
  CHECK (action IN (
    'propose', 'security_review', 'approve', 'reject', 'activate', 'retire',
    'rollback', 'read', 'seed',
    'install', 'upgrade', 'disable', 'uninstall', 'download'
  ));
