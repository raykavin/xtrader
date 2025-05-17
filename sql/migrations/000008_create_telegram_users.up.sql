-- the Telegram users the bot answers
CREATE TABLE IF NOT EXISTS telegram_users (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    telegram_id BIGINT      NOT NULL CHECK (telegram_id > 0),
    name        TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_telegram_users_telegram_id ON telegram_users (telegram_id) WHERE deleted_at IS NULL;
