from __future__ import annotations

import argparse
import sys
from pathlib import Path

from . import paths, store
from .index import refresh
from .parse import format_turns, parse_file


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="sessions",
        description="Search Cursor, Claude Code, and Codex transcripts in place.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    sub.add_parser("index", help="Walk transcript roots and refresh the SQLite index.")

    search_p = sub.add_parser("search", help="Reindex stale files, then FTS search.")
    search_p.add_argument("query", nargs="+", help="Search phrase")
    search_p.add_argument(
        "--source",
        choices=("cursor", "claude", "codex"),
        help="Limit to one harness",
    )
    search_p.add_argument("--cwd", help="Limit to this working directory")
    search_p.add_argument("--limit", type=int, default=20)

    show_p = sub.add_parser("show", help="Print human turns for a session id.")
    show_p.add_argument("id", help="Session id from search (optionally source:id)")

    args = parser.parse_args(argv)
    if args.command == "index":
        return cmd_index()
    if args.command == "search":
        return cmd_search(" ".join(args.query), args.source, args.cwd, args.limit)
    if args.command == "show":
        return cmd_show(args.id)
    parser.error("unknown command")
    return 2


def cmd_index() -> int:
    conn = store.connect(paths.index_path())
    try:
        stats = refresh(conn)
    finally:
        conn.close()
    print(
        f"{stats.upserted} upserted, {stats.unchanged} unchanged, "
        f"{stats.deleted} deleted, {stats.failed} failed",
        file=sys.stderr,
    )
    return 0


def cmd_search(query: str, source: str | None, cwd: str | None, limit: int) -> int:
    conn = store.connect(paths.index_path())
    try:
        stats = refresh(conn)
        print(
            f"{stats.upserted} upserted, {stats.unchanged} unchanged, "
            f"{stats.deleted} deleted, {stats.failed} failed",
            file=sys.stderr,
        )
        rows = store.search(conn, query, source=source, cwd=cwd, limit=limit)
    finally:
        conn.close()
    if not rows:
        print("no matches")
        return 0
    for row in rows:
        date = row["started_at"] or row["updated_at"] or ""
        snippet = " ".join((row["snippet"] or "").split())
        print(f"{row['id']}\t{row['source']}\t{date}\t{row['cwd'] or ''}")
        if row["title"]:
            print(f"  title: {row['title']}")
        print(f"  snippet: {snippet}")
        print(f"  path: {row['path']}")
    return 0


def cmd_show(session_id: str) -> int:
    conn = store.connect(paths.index_path())
    try:
        rows = store.get_by_id(conn, session_id)
    finally:
        conn.close()
    if not rows:
        print(f"not found: {session_id}", file=sys.stderr)
        return 1
    if len(rows) > 1:
        print(
            f"multiple sessions match {session_id}; use source:id",
            file=sys.stderr,
        )
        for row in rows:
            print(f"{row['source']}:{row['id']}\t{row['path']}", file=sys.stderr)
        return 1
    row = rows[0]
    path = Path(row["path"])
    if not path.is_file():
        print(f"missing file: {path}", file=sys.stderr)
        return 1
    parsed = parse_file(path, row["source"])
    if parsed is None:
        print(f"failed to parse: {path}", file=sys.stderr)
        return 1
    sys.stdout.write(format_turns(parsed))
    return 0
