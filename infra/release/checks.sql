-- Read-only release checks. No application worker or schema tool is started.
-- Fixed aggregate labels only: never return customer IDs, payloads or secrets.
SET NAMES utf8mb4 COLLATE utf8mb4_bin;
SET SESSION time_zone='+00:00';
SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ;
START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY;

SELECT 'check','ledger_posted_vs_journal',COUNT(*)
FROM ledger_accounts a LEFT JOIN
 (SELECT account_id,SUM(delta) total FROM ledger_journal GROUP BY account_id) j
 ON j.account_id=a.id WHERE a.posted_balance<>COALESCE(j.total,0)
UNION ALL SELECT 'check','ledger_held_vs_active_holds',COUNT(*)
FROM ledger_accounts a LEFT JOIN
 (SELECT account_id,SUM(amount) total FROM ledger_holds WHERE status=0 GROUP BY account_id) h
 ON h.account_id=a.id WHERE a.held_amount<>COALESCE(h.total,0)
UNION ALL SELECT 'check','journal_missing_account',COUNT(*) FROM ledger_journal j
 LEFT JOIN ledger_accounts a ON a.id=j.account_id WHERE a.id IS NULL
UNION ALL SELECT 'check','hold_missing_account',COUNT(*) FROM ledger_holds h
 LEFT JOIN ledger_accounts a ON a.id=h.account_id WHERE a.id IS NULL
UNION ALL SELECT 'check','invalid_hold_state_or_amount',COUNT(*) FROM ledger_holds
 WHERE status NOT IN(0,1,2) OR amount<0 OR captured_amount<0 OR captured_amount>amount
 OR (status=0 AND captured_amount<>0)
UNION ALL SELECT 'check','task_missing_hold',COUNT(*) FROM tasks t
 LEFT JOIN ledger_holds h ON h.id=t.hold_id WHERE h.id IS NULL
UNION ALL SELECT 'check','task_hold_cross_org',COUNT(*) FROM tasks t
 JOIN ledger_holds h ON h.id=t.hold_id JOIN ledger_accounts a ON a.id=h.account_id
 WHERE a.org_id<>t.org_id
UNION ALL SELECT 'check','membership_missing_org_or_user',COUNT(*) FROM org_members m
 LEFT JOIN organizations o ON o.id=m.org_id LEFT JOIN users u ON u.id=m.user_id
 WHERE o.id IS NULL OR u.id IS NULL;

-- The legacy delete implementation permits these orphans. Report separately;
-- never delete them or call them money corruption solely because of this count.
SELECT 'warning','channel_credential_missing_parent',COUNT(*) FROM channel_credentials cc
 LEFT JOIN channels c ON c.id=cc.channel_id WHERE c.id IS NULL;
SELECT 'drain','active_holds',COUNT(*) FROM ledger_holds WHERE status=0
UNION ALL SELECT 'drain','nonterminal_tasks',COUNT(*) FROM tasks
 WHERE status NOT IN('succeeded','failed','cancelled','expired') OR status IS NULL;

-- Official video tables do not exist on the legitimate 0048 baseline. Prepare
-- only a fixed SELECT when present; a missing legacy base table above is fatal.
SET @release_has_operations=(SELECT COUNT(*) FROM information_schema.tables
 WHERE table_schema=DATABASE() AND table_name='video_operations' AND table_type='BASE TABLE');
SET @release_has_states=(SELECT COUNT(*) FROM information_schema.tables
 WHERE table_schema=DATABASE() AND table_name='video_task_states' AND table_type='BASE TABLE');
SET @release_has_reservations=(SELECT COUNT(*) FROM information_schema.tables
 WHERE table_schema=DATABASE() AND table_name='video_quota_reservations' AND table_type='BASE TABLE');
SELECT 'presence','video_operations',@release_has_operations
UNION ALL SELECT 'presence','video_task_states',@release_has_states
UNION ALL SELECT 'presence','video_quota_reservations',@release_has_reservations;
SET @release_sql=IF(@release_has_operations=1,
 'SELECT ''drain'',''unresolved_video_operations'',COUNT(*) FROM video_operations WHERE status NOT IN(''confirmed'',''rejected'') OR status IS NULL',
 'SELECT ''drain'',''unresolved_video_operations'',0');
PREPARE release_check FROM @release_sql;
EXECUTE release_check;
DEALLOCATE PREPARE release_check;
SET @release_sql=IF(@release_has_states=1,
 'SELECT ''drain'',''unsettled_video_billing'',COUNT(*) FROM video_task_states WHERE billing_state NOT IN(''settled'',''released'',''adjusted'') OR billing_state IS NULL',
 'SELECT ''drain'',''unsettled_video_billing'',0');
PREPARE release_check FROM @release_sql;
EXECUTE release_check;
DEALLOCATE PREPARE release_check;
SET @release_sql=IF(@release_has_reservations=1,
 'SELECT ''drain'',''active_video_reservations'',COUNT(*) FROM video_quota_reservations WHERE state=''active''',
 'SELECT ''drain'',''active_video_reservations'',0');
PREPARE release_check FROM @release_sql;
EXECUTE release_check;
DEALLOCATE PREPARE release_check;

SELECT 'snapshot','ledger_accounts_rows',COUNT(*) FROM ledger_accounts
UNION ALL SELECT 'snapshot','ledger_journal_rows',COUNT(*) FROM ledger_journal
UNION ALL SELECT 'snapshot','ledger_holds_rows',COUNT(*) FROM ledger_holds
UNION ALL SELECT 'snapshot','tasks_rows',COUNT(*) FROM tasks;
SELECT 'amount','posted_balance_sum',CAST(COALESCE(SUM(posted_balance),0) AS DECIMAL(65,8)) FROM ledger_accounts
UNION ALL SELECT 'amount','held_amount_sum',CAST(COALESCE(SUM(held_amount),0) AS DECIMAL(65,8)) FROM ledger_accounts
UNION ALL SELECT 'amount','journal_delta_sum',CAST(COALESCE(SUM(delta),0) AS DECIMAL(65,8)) FROM ledger_journal
UNION ALL SELECT 'amount','hold_amount_sum',CAST(COALESCE(SUM(amount),0) AS DECIMAL(65,8)) FROM ledger_holds
UNION ALL SELECT 'amount','captured_amount_sum',CAST(COALESCE(SUM(captured_amount),0) AS DECIMAL(65,8)) FROM ledger_holds;

-- Hash individual rows inside MySQL; customer rows/IDs never leave the server.
-- Stable PK order plus fixed fields permits strict before/after comparison even
-- when totals remain equal after two offsetting changes. No GROUP_CONCAT limit.
SELECT 'fund','ledger_accounts',SHA2(CAST(JSON_ARRAY(
 id,org_id,type,shard,posted_balance,held_amount,created_at,updated_at
) AS CHAR CHARACTER SET utf8mb4),256) FROM ledger_accounts ORDER BY id;
SELECT 'fund','ledger_journal',SHA2(CAST(JSON_ARRAY(
 id,idempotency_key,account_id,kind,delta,balance_after,hold_id,ref_type,ref_id,reason,created_at
) AS CHAR CHARACTER SET utf8mb4),256) FROM ledger_journal ORDER BY id;
SELECT 'fund','ledger_holds',SHA2(CAST(JSON_ARRAY(
 id,idempotency_key,account_id,amount,captured_amount,status,ref_type,ref_id,expires_at,created_at,updated_at
) AS CHAR CHARACTER SET utf8mb4),256) FROM ledger_holds ORDER BY id;
SELECT 'schema',version,'' FROM schema_migrations ORDER BY version;
COMMIT;
