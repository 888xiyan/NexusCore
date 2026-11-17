DELETE FROM kernel_config_audit
 WHERE action IN ('install', 'upgrade', 'disable', 'uninstall');

DELETE FROM kernel_config_documents
 WHERE config_key = 'lifecycle';

ALTER TABLE kernel_config_audit DROP CONSTRAINT IF EXISTS kernel_config_audit_action_check;

ALTER TABLE kernel_config_audit
  ADD CONSTRAINT kernel_config_audit_action_check
  CHECK (action IN (
    'propose', 'security_review', 'approve', 'reject', 'activate', 'retire',
    'rollback', 'read', 'seed'
  ));
