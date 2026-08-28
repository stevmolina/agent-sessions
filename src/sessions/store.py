from __future__ import annotations

import sqlite3
from pathlib import Path
from typing import Any

from .parse import Parsed

SCHEMA_VERSION = "2"

SCHEMA = """
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  path TEXT PRIMARY KEY,
  id TEXT NOT NULL,
  source TEXT NOT NULL,
  cwd TEXT,
  started_at TEXT,
  updated_at TEXT,
  mtime REAL NOT NULL,
  size INTEGER NOT NULL,
  title TEXT,
  parent_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_sessions_id ON sessions(id);
CREATE INDEX IF NOT EXISTS idx_sessions_source ON sessions(source);

CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(
  path UNINDEXED,
  body,
  title,
  tokenize = 'unicode61 remove_diacritics 2'
);
"""


def connect(db_path: Path) -> sqlite3.Connection:
    db_path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA foreign_keys=ON")
    _ensure_schema(conn)
    return conn


def _ensure_schema(conn: sqlite3.Connection) -> None:
    try:
        row = conn.execute(
            "SELECT value FROM meta WHERE key = 'schema_version'"
        ).fetchone()
        version = row["value"] if row else None
    except sqlite3.OperationalError:
        version = None
    if version != SCHEMA_VERSION:
        conn.executescript(
            """
            DROP TABLE IF EXISTS sessions_fts;
            DROP TABLE IF EXISTS sessions;
            DROP TABLE IF EXISTS meta;
            """
        )
        conn.executescript(SCHEMA)
        conn.execute(
            "INSERT INTO meta(key, value) VALUES ('schema_version', ?)",
            (SCHEMA_VERSION,),
        )
        conn.commit()


def indexed_files(conn: sqlite3.Connection) -> dict[str, tuple[float, int]]:
    rows = conn.execute("SELECT path, mtime, size FROM sessions")
    return {row["path"]: (row["mtime"], row["size"]) for row in rows}


def upsert(conn: sqlite3.Connection, parsed: Parsed) -> None:
    conn.execute(
        """
        INSERT INTO sessions (
          path, id, source, cwd, started_at, updated_at,
          mtime, size, title, parent_id
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(path) DO UPDATE SET
          id=excluded.id,
          source=excluded.source,
          cwd=excluded.cwd,
          started_at=excluded.started_at,
          updated_at=excluded.updated_at,
          mtime=excluded.mtime,
          size=excluded.size,
          title=excluded.title,
          parent_id=excluded.parent_id
        """,
        (
            parsed.path,
            parsed.id,
            parsed.source,
            parsed.cwd,
            parsed.started_at,
            parsed.updated_at,
            parsed.mtime,
            parsed.size,
            parsed.title,
            parsed.parent_id,
        ),
    )
    conn.execute("DELETE FROM sessions_fts WHERE path = ?", (parsed.path,))
    conn.execute(
        "INSERT INTO sessions_fts(path, body, title) VALUES (?, ?, ?)",
        (parsed.path, parsed.body, parsed.title),
    )


def delete_paths(conn: sqlite3.Connection, paths: list[str]) -> int:
    if not paths:
        return 0
    for path in paths:
        conn.execute("DELETE FROM sessions WHERE path = ?", (path,))
        conn.execute("DELETE FROM sessions_fts WHERE path = ?", (path,))
    return len(paths)


def get_by_id(conn: sqlite3.Connection, session_id: str) -> list[dict[str, Any]]:
    source = None
    lookup = session_id
    if ":" in session_id:
        maybe_source, rest = session_id.split(":", 1)
        if maybe_source in {"cursor", "claude", "codex"}:
            source = maybe_source
            lookup = rest
    if source:
        rows = conn.execute(
            "SELECT * FROM sessions WHERE source = ? AND id = ?",
            (source, lookup),
        ).fetchall()
    else:
        rows = conn.execute(
            "SELECT * FROM sessions WHERE id = ?", (lookup,)
        ).fetchall()
    return [dict(row) for row in rows]


def search(
    conn: sqlite3.Connection,
    query: str,
    source: str | None = None,
    cwd: str | None = None,
    limit: int = 20,
) -> list[dict[str, Any]]:
    fts = _fts_query(query)
    sql = """
        SELECT
          s.id, s.source, s.cwd, s.started_at, s.updated_at,
          s.path, s.title, s.parent_id,
          snippet(sessions_fts, 1, '', '', ' … ', 24) AS snippet
        FROM sessions_fts
        JOIN sessions s ON s.path = sessions_fts.path
        WHERE sessions_fts MATCH ?
    """
    params: list[Any] = [fts]
    if source:
        sql += " AND s.source = ?"
        params.append(source)
    if cwd:
        sql += " AND (s.cwd = ? OR s.cwd LIKE ?)"
        params.append(cwd)
        params.append(cwd.rstrip("/") + "/%")
    sql += " ORDER BY rank LIMIT ?"
    params.append(limit)
    return [dict(row) for row in conn.execute(sql, params).fetchall()]


def _fts_query(raw: str) -> str:
    text = " ".join(raw.split()).strip()
    if not text:
        return '""'
    escaped = text.replace('"', '""')
    return f'"{escaped}"'
