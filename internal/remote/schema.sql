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

-- One row per machine, written on every push attempt that reaches the database.
-- `sessions health` reads it with the read-only role. last_error is a short,
-- fixed message from this program, never transcript text or a secret.
CREATE TABLE IF NOT EXISTS sessions_push_heartbeats (
  machine text PRIMARY KEY,
  last_attempt_at timestamptz NOT NULL,
  last_success_at timestamptz,
  last_error text,
  last_error_at timestamptz,
  attempts bigint NOT NULL DEFAULT 0,
  successes bigint NOT NULL DEFAULT 0,
  failures bigint NOT NULL DEFAULT 0,
  consecutive_failures integer NOT NULL DEFAULT 0,
  uploaded integer NOT NULL DEFAULT 0,
  unchanged integer NOT NULL DEFAULT 0,
  deleted integer NOT NULL DEFAULT 0,
  skipped integer NOT NULL DEFAULT 0,
  duration_ms integer NOT NULL DEFAULT 0,
  version text NOT NULL DEFAULT '',
  os text NOT NULL DEFAULT ''
);

-- Failed attempts since the last success. One blip does not page; two in a row do.
-- Added after the table first shipped, hence the ALTER for existing databases.
ALTER TABLE sessions_push_heartbeats ADD COLUMN IF NOT EXISTS consecutive_failures integer NOT NULL DEFAULT 0;

GRANT SELECT ON TABLE sessions_push_heartbeats TO sessions_reader;

-- The read-only URL logs in as sessions_mcp_reader. Granting to a role that does
-- not exist would fail every push in a database without it, so check first.
DO $$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sessions_mcp_reader') THEN GRANT SELECT ON TABLE sessions, sessions_push_heartbeats TO sessions_mcp_reader; END IF; END $$;

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
