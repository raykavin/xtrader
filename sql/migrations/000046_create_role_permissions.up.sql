-- the permissions each role grants, "area:action" (see ui.PermissionAreas),
-- as set on the dashboard: the implied ones (the view of an area managed) are
-- added by the controller, not kept; none for the fixed role, which holds
-- every one
CREATE TABLE IF NOT EXISTS role_permissions (
    role_id    BIGINT NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission TEXT   NOT NULL CHECK (permission ~ '^[a-z0-9-]+:[a-z]+$'),
    PRIMARY KEY (role_id, permission)
);
