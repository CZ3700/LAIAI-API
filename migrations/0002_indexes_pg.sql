-- LAIAI v2 PostgreSQL Partial & Covering Indexes

CREATE INDEX IF NOT EXISTS idx_tasks_poll_active ON tasks (id) WHERE status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_tasks_org_submitted ON tasks (org_id, submitted_at DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_key_active ON tasks (org_id, api_key_id) WHERE status IN ('queued', 'running');

CREATE INDEX IF NOT EXISTS idx_notify_outbox_pending ON notify_outbox (next_attempt_at, id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_ledger_holds_open ON ledger_holds (ref_type, created_at) WHERE status = 0;
CREATE INDEX IF NOT EXISTS idx_topup_orders_reconcile ON topup_orders (created_at) WHERE status = 0;

CREATE INDEX IF NOT EXISTS idx_invocations_org_created_cover ON invocations (org_id, created_at DESC, id DESC) INCLUDE (status, cost_usd, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens);
CREATE INDEX IF NOT EXISTS idx_invocations_created_id ON invocations (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_invocations_channel_created ON invocations (channel_id, created_at DESC) WHERE channel_id > 0;

CREATE INDEX IF NOT EXISTS idx_api_keys_org_status ON api_keys (org_id, status);
CREATE INDEX IF NOT EXISTS idx_ledger_journal_account_id ON ledger_journal (account_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_channels_status_priority ON channels (status, priority DESC, id);
CREATE INDEX IF NOT EXISTS idx_channel_credentials_channel_status ON channel_credentials (channel_id, status);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs (created_at DESC);
