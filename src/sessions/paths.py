from __future__ import annotations

import os
from pathlib import Path

HOME = Path.home()


def index_path() -> Path:
    override = os.environ.get("SESSIONS_INDEX")
    if override:
        return Path(override).expanduser()
    return HOME / ".cache" / "sessions" / "index.sqlite"


def cursor_root() -> Path:
    override = os.environ.get("SESSIONS_CURSOR_ROOT")
    if override:
        return Path(override).expanduser()
    return HOME / ".cursor" / "projects"


def claude_root() -> Path:
    override = os.environ.get("SESSIONS_CLAUDE_ROOT")
    if override:
        return Path(override).expanduser()
    return HOME / ".claude" / "projects"


def codex_root() -> Path:
    override = os.environ.get("SESSIONS_CODEX_ROOT")
    if override:
        return Path(override).expanduser()
    return HOME / ".codex" / "sessions"


def codex_archive_root() -> Path:
    override = os.environ.get("SESSIONS_CODEX_ARCHIVE_ROOT")
    if override:
        return Path(override).expanduser()
    return HOME / ".codex" / "archived_sessions"


def iter_transcripts() -> list[tuple[str, Path]]:
    """Return (source, path) pairs for every transcript JSONL on disk."""
    found: list[tuple[str, Path]] = []
    found.extend(("cursor", p) for p in _cursor_files())
    found.extend(("claude", p) for p in _claude_files())
    found.extend(("codex", p) for p in _codex_files())
    return found


def _cursor_files() -> list[Path]:
    root = cursor_root()
    if not root.is_dir():
        return []
    files: list[Path] = []
    for project in root.iterdir():
        transcripts = project / "agent-transcripts"
        if transcripts.is_dir():
            files.extend(sorted(transcripts.rglob("*.jsonl")))
    return files


def _claude_files() -> list[Path]:
    root = claude_root()
    if not root.is_dir():
        return []
    skip = {"memory", "debug", "tool-results"}
    files: list[Path] = []
    for path in root.rglob("*.jsonl"):
        if skip.intersection(path.parts):
            continue
        files.append(path)
    return sorted(files)


def _codex_files() -> list[Path]:
    files: list[Path] = []
    sessions = codex_root()
    if sessions.is_dir():
        files.extend(sessions.rglob("*.jsonl"))
    archive = codex_archive_root()
    if archive.is_dir():
        files.extend(archive.glob("*.jsonl"))
    return sorted(files)


def cursor_cwd_from_path(path: Path) -> str | None:
    """Best-effort cwd from ~/.cursor/projects/<slug>/agent-transcripts/..."""
    root = cursor_root()
    try:
        relative = path.resolve().relative_to(root.resolve())
    except ValueError:
        return None
    slug = relative.parts[0] if relative.parts else None
    if not slug:
        return None
    return decode_cursor_slug(slug)


def decode_cursor_slug(slug: str) -> str:
    """Turn Cursor's slash-to-dash project slug back into a path."""
    tokens = slug.split("-")
    current = Path("/")
    i = 0
    while i < len(tokens):
        found = None
        for j in range(len(tokens), i, -1):
            candidate = current / "-".join(tokens[i:j])
            try:
                if candidate.is_dir():
                    found = (j, candidate)
                    break
            except OSError:
                continue
        if found is None:
            return str(current / "-".join(tokens[i:]))
        i, current = found
    return str(current)


def cursor_parent_id(path: Path) -> str | None:
    """Parent session id when this file lives in a subagents/ folder."""
    parts = path.parts
    if "subagents" not in parts:
        return None
    idx = parts.index("subagents")
    if idx == 0:
        return None
    return parts[idx - 1]
