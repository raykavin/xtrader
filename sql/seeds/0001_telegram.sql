-- The Telegram bot of the controller, off until it has a token and the users
-- it answers (see the settings of the dashboard): no credential here.
INSERT INTO telegram_settings (id, enabled)
VALUES (1, FALSE)
ON CONFLICT (id) DO NOTHING;
