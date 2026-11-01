-- the accounts of MetaTrader 5, set on the dashboard (Configurações > Contas
-- MT5): the login, the server and the password (encrypted: sent to the
-- terminal, never a hash) of each, the terminal serving it through its bridge
-- (bridge_url, its token encrypted; bridge_key its host:port, see
-- agentapi.MT5BridgeKey), whether the bridge may trade it real (allow_real) or
-- trade at all (read_only), and whether it is the account of its terminal
-- (active: a terminal is logged in one account at a time); kind, currency and
-- company as the bridge confirmed them, checked_at the last time it did and
-- check_error why it couldn't; updated_by who changed it last. The agents
-- trade the account they are linked to, see agent_mt5_accounts
CREATE TABLE IF NOT EXISTS mt5_accounts (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name         TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    login        BIGINT      NOT NULL CHECK (login > 0),
    server       TEXT        NOT NULL CHECK (length(server) BETWEEN 1 AND 128),
    password     BYTEA,      -- encrypted
    bridge_url   TEXT        NOT NULL CHECK (bridge_url ~ '^https?://'),
    bridge_key   TEXT        NOT NULL,
    bridge_token BYTEA,      -- encrypted
    allow_real   BOOLEAN     NOT NULL DEFAULT FALSE,
    read_only    BOOLEAN     NOT NULL DEFAULT FALSE,
    active       BOOLEAN     NOT NULL DEFAULT FALSE,
    kind         TEXT        NOT NULL DEFAULT '' CHECK (kind IN ('', 'demo', 'real', 'contest')),
    currency     TEXT        NOT NULL DEFAULT '',
    company      TEXT        NOT NULL DEFAULT '',
    checked_at   TIMESTAMPTZ,
    check_error  TEXT        NOT NULL DEFAULT '',
    updated_by   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_mt5_accounts_name ON mt5_accounts (lower(name));
CREATE UNIQUE INDEX IF NOT EXISTS idx_mt5_accounts_login ON mt5_accounts (login, lower(server));
CREATE UNIQUE INDEX IF NOT EXISTS idx_mt5_accounts_active ON mt5_accounts (bridge_key) WHERE active;
