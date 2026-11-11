-- the role of each user of the dashboard: a user without one is refused at the
-- login; who gave it last, and when. A user enabled holding users:manage is
-- always kept, so that the dashboard can't lock everybody out
CREATE TABLE IF NOT EXISTS user_roles (
    user_id    BIGINT      PRIMARY KEY REFERENCES users (id),
    role_id    BIGINT      NOT NULL REFERENCES roles (id),
    updated_by TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_user_roles_role ON user_roles (role_id);
