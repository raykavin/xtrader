-- the Telegram bot of the controller, a single row
CREATE TABLE IF NOT EXISTS telegram_settings (
    id         SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled    BOOLEAN     NOT NULL DEFAULT FALSE,
    token      BYTEA,       -- encrypted
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
