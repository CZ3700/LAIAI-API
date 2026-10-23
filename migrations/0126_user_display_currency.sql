-- Presentation preference only. All ledger, quota and API amounts remain USD.
ALTER TABLE users ADD COLUMN display_currency CHAR(3) NOT NULL DEFAULT 'USD';
