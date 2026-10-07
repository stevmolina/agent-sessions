CREATE TABLE IF NOT EXISTS sessions (
  source text NOT NULL,
  id text NOT NULL,
  machine text NOT NULL,
  provider text NOT NULL,
  provider_session_id text,
  cwd text,
  title text,
  path text,
  origin_path text,
  revision text,
  clean_fingerprint text NOT NULL,
  started_at timestamptz,
  updated_at timestamptz,
  body text NOT NULL,
  turns jsonb NOT NULL,
  pushed_at timestamptz NOT NULL DEFAULT now(),
  search tsvector GENERATED ALWAYS AS (
    to_tsvector('english', coalesce(title, '') || ' ' || coalesce(body, ''))
  ) STORED,
  PRIMARY KEY (source, id)
);

CREATE INDEX IF NOT EXISTS sessions_machine_idx ON sessions (machine);

CREATE INDEX IF NOT EXISTS sessions_search_idx ON sessions USING GIN (search);

GRANT USAGE ON SCHEMA public TO sessions_reader;

GRANT SELECT ON TABLE sessions TO sessions_reader;

-- Separate machine ownership preserves shared chats until their last owner removes them.
-- RESTRICT prevents old clients from deleting rows with active owners.
CREATE TABLE IF NOT EXISTS session_machines (
 source text NOT NULL,
 id text NOT NULL,
 machine text NOT NULL,
 PRIMARY KEY (source,id,machine),
 FOREIGN KEY (source,id) REFERENCES sessions(source,id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS session_machines_machine_idx ON session_machines(machine);
CREATE TABLE IF NOT EXISTS sessions_sync_migrations (name text PRIMARY KEY);
INSERT INTO session_machines(source,id,machine)
 SELECT source,id,machine FROM sessions
 WHERE NOT EXISTS (SELECT 1 FROM sessions_sync_migrations WHERE name='machine-ownership-v1')
 ON CONFLICT DO NOTHING;
INSERT INTO sessions_sync_migrations(name) VALUES('machine-ownership-v1') ON CONFLICT DO NOTHING;
