-- the files of the previous version the migrator imported, once each, with
-- what it found in them
CREATE TABLE IF NOT EXISTS legacy_imports (
    source      TEXT        PRIMARY KEY,
    details     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
