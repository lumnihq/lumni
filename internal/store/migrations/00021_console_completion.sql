-- +goose Up
CREATE INDEX decisions_recent_admissions ON decisions(tenant_id,created_at DESC,action_id)
 WHERE decision IN('ALLOW','WOULD_ALLOW');
CREATE TRIGGER console_notifications_audit AFTER INSERT OR UPDATE OR DELETE ON notification_config
 FOR EACH ROW EXECUTE FUNCTION console_audit('notifications.updated');

-- +goose Down
DROP TRIGGER console_notifications_audit ON notification_config;
DROP INDEX decisions_recent_admissions;
