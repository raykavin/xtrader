-- the roles of the users of the dashboard (Configurações > Usuários e papéis):
-- what each user may see and do, the permissions of its role (see
-- role_permissions and ui.PermissionAreas), checked by the controller on every
-- route of its API; system 'admin' marks the fixed role holding every
-- permission, the ones added later too, never changed nor deleted (created by
-- sql/seeds); updated_by who changed it last
CREATE TABLE IF NOT EXISTS roles (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    description TEXT        NOT NULL DEFAULT '' CHECK (length(description) <= 280),
    system      TEXT        NOT NULL DEFAULT '' CHECK (system IN ('', 'admin')),
    updated_by  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_roles_name ON roles (lower(name));
CREATE UNIQUE INDEX IF NOT EXISTS idx_roles_system ON roles (system) WHERE system <> '';
