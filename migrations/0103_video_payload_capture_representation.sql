-- Distinguish received JSON from historical typed reconstructions.
ALTER TABLE invocation_payloads
 ADD COLUMN capture_representation VARCHAR(32) NOT NULL DEFAULT 'legacy_unknown';
