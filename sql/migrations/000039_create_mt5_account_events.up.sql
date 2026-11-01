-- the record of the accounts of MetaTrader 5, never deleted and never holding
-- a secret: an account created, changed (previous and next, the password and
-- the token only as set or not), activated, deactivated or deleted, imported
-- from the bridge, linked to an agent or unlinked, tested, or its session
-- given to the terminal; by whom (requested_by, user_id) and why
CREATE TABLE IF NOT EXISTS mt5_account_events (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    account_id   BIGINT      REFERENCES mt5_accounts (id) ON DELETE SET NULL,
    agent_id     BIGINT      REFERENCES agents (id),
    kind         TEXT        NOT NULL CHECK (kind IN ('created', 'changed', 'activated', 'deactivated', 'deleted',
                                                      'imported', 'linked', 'unlinked', 'tested', 'session')),
    requested_by TEXT        NOT NULL DEFAULT '',
    user_id      BIGINT      REFERENCES users (id),
    reason       TEXT        NOT NULL DEFAULT '',
    previous     JSONB,
    next         JSONB
);
CREATE INDEX IF NOT EXISTS idx_mt5_account_events_account ON mt5_account_events (account_id, id);
