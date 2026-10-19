-- +goose Up
ALTER TABLE steps ADD COLUMN IF NOT EXISTS admission_ciphertext BYTEA;
CREATE TRIGGER graph_change_steps AFTER INSERT OR UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_outcomes AFTER INSERT OR UPDATE ON outcomes FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_incidents AFTER INSERT OR UPDATE ON incidents FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE TRIGGER graph_change_effect_occurrences AFTER INSERT OR UPDATE ON effect_occurrences FOR EACH ROW EXECUTE FUNCTION lumni_graph_change();
CREATE INDEX outcomes_pending_idx ON outcomes(next_attempt_at) WHERE status='PENDING';
CREATE INDEX effect_occurrences_key_idx ON effect_occurrences(tenant_id,effect_key);

-- +goose Down
DROP INDEX effect_occurrences_key_idx, outcomes_pending_idx;
DROP TRIGGER graph_change_effect_occurrences ON effect_occurrences;
DROP TRIGGER graph_change_incidents ON incidents;
DROP TRIGGER graph_change_outcomes ON outcomes;
DROP TRIGGER graph_change_steps ON steps;
