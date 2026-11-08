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

-- owner is the WorkOS user id of the person whose push wrote the row. brain is
-- the knowledge repo (owner/repo) the session is filed under, from that person's
-- brains.json; NULL when nothing matches. Readers show each person only their
-- own rows. Added after the table first shipped. ALTER TABLE ... IF NOT EXISTS
-- still takes an ACCESS EXCLUSIVE lock, which would block every reader for the
-- whole push, so the catalog is checked first and the ALTER runs only once.
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'sessions' AND column_name = 'owner') THEN
  ALTER TABLE sessions ADD COLUMN owner text;
 END IF;
 IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'sessions' AND column_name = 'brain') THEN
  ALTER TABLE sessions ADD COLUMN brain text;
 END IF;
END $$;

CREATE INDEX IF NOT EXISTS sessions_machine_idx ON sessions (machine);

-- CREATE INDEX locks the table against writes before it checks IF NOT EXISTS,
-- so these two are also created only when missing.
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'sessions_owner_updated_idx') THEN
  CREATE INDEX sessions_owner_updated_idx ON sessions (owner, updated_at DESC);
 END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'sessions_owner_brain_idx') THEN
  CREATE INDEX sessions_owner_brain_idx ON sessions (owner, brain);
 END IF;
END $$;

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
-- Guarded like the sessions columns, so the lock is taken only once.
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'sessions_push_heartbeats' AND column_name = 'consecutive_failures') THEN
  ALTER TABLE sessions_push_heartbeats ADD COLUMN consecutive_failures integer NOT NULL DEFAULT 0;
 END IF;
END $$;

GRANT SELECT ON TABLE sessions_push_heartbeats TO sessions_reader;

-- The read-only URL logs in as sessions_mcp_reader. Granting to a role that does
-- not exist would fail every push in a database without it, so check first.
DO $$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sessions_mcp_reader') THEN GRANT SELECT ON TABLE sessions, sessions_push_heartbeats TO sessions_mcp_reader; END IF; END $$;

-- brain-mcp reads sessions as brain_mcp and filters by owner itself. Same check,
-- and a grant this role may not make never fails the push.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'brain_mcp') THEN
  GRANT USAGE ON SCHEMA public TO brain_mcp;
  GRANT SELECT ON TABLE sessions TO brain_mcp;
 END IF;
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

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
