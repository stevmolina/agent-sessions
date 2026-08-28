from __future__ import annotations

from dataclasses import dataclass

from . import paths, store
from .parse import parse_file


@dataclass
class RefreshStats:
    upserted: int = 0
    unchanged: int = 0
    deleted: int = 0
    failed: int = 0


def refresh(conn) -> RefreshStats:
    stats = RefreshStats()
    existing = store.indexed_files(conn)
    seen: set[str] = set()
    for source, path in paths.iter_transcripts():
        if not path.is_file():
            continue
        key = str(path)
        seen.add(key)
        try:
            st = path.stat()
        except OSError:
            stats.failed += 1
            continue
        previous = existing.get(key)
        if previous and previous[0] == st.st_mtime and previous[1] == st.st_size:
            stats.unchanged += 1
            continue
        parsed = parse_file(path, source)
        if parsed is None:
            stats.failed += 1
            continue
        store.upsert(conn, parsed)
        stats.upserted += 1
    vanished = [path for path in existing if path not in seen]
    stats.deleted = store.delete_paths(conn, vanished)
    conn.commit()
    return stats
