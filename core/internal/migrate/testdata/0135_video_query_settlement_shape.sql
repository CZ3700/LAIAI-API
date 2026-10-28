-- Candidate only: requires migration-specific exact-schema validation before release.
-- One atomic expand. Existing fields, indexes, defaults and writer authority stay.
-- This derived shape is only an access-path aid, never settlement permission.
ALTER TABLE video_query_receipts
 ADD COLUMN settlement_shape_ready TINYINT UNSIGNED GENERATED ALWAYS AS
  (CASE WHEN generation_applied_at >= CAST('1000-01-01 00:00:00' AS DATETIME)
    AND CAST(settlement_kind AS BINARY) IN (_binary'usage',_binary'provider_nocharge')
   THEN 1 ELSE 0 END) VIRTUAL NOT NULL,
 ADD KEY idx_video_query_settlement_due
  (runtime_version,adapter_version,protocol_contract_version,
   observation_schema_version,settlement_shape_ready,applied_at,
   recovery_due_at,received_at,attempt_id);
