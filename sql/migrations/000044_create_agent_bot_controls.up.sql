-- the bot of each agent stopped by a user, from the dashboard (Parar bot) or
-- from Telegram (/stop): kept so that the agent starts with its bot stopped
-- again, whatever restarts its process (a deploy, a reboot of the host, a
-- crash, Reiniciar), until a user starts it (Iniciar bot, /start); who changed
-- it last, and when; a row for an agent once its bot was stopped or started
CREATE TABLE IF NOT EXISTS agent_bot_controls (
    agent_id   BIGINT      PRIMARY KEY REFERENCES agents (id),
    stopped    BOOLEAN     NOT NULL DEFAULT FALSE,
    changed_by TEXT        NOT NULL DEFAULT '',
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
