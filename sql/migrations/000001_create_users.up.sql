-- The schema of xtrader: the settings of the controller and of its agents, and
-- the data of the agents (orders, decisions of the LLM, its journal), one
-- schema for all of them, each row of an agent scoped by its agent_id. Only the
-- controller reads and writes it; the agents go through its internal API.
--
-- Each file creates one table whole, its columns, defaults, keys and checks in
-- its CREATE TABLE, its indexes after it, in the order of their dependencies:
-- the schema of a new database, its settings then inserted by sql/seeds.
--
-- The secrets (the API keys, the tokens) are kept encrypted, AES-256-GCM with
-- the secrets_key of config.yml (gobox/secure): the BYTEA columns noted so.
--
-- golang-migrate sends the file as a single simple query, which PostgreSQL runs
-- as one transaction: a failure leaves nothing behind. An explicit BEGIN would
-- leave the session in a failed block instead, which golang-migrate can't mark.

-- the users of the dashboard, their passwords as bcrypt hashes
CREATE TABLE IF NOT EXISTS users (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      TEXT        NOT NULL CHECK (username ~ '^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$'),
    password_hash TEXT        NOT NULL,
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_users_username ON users (lower(username)) WHERE deleted_at IS NULL;
