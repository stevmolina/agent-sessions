from __future__ import annotations

import html
import json
import re
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from .paths import cursor_cwd_from_path, cursor_parent_id

USER_QUERY_RE = re.compile(r"<user_query>\s*(.*?)\s*</user_query>", re.DOTALL)
TIMESTAMP_RE = re.compile(r"<timestamp>\s*(.*?)\s*</timestamp>", re.DOTALL)
COMMAND_RE = re.compile(r"<command-name>|<command-message>|<command-args>")
CLAUDE_SKIP_TYPES = {
    "mode",
    "file-history-snapshot",
    "attachment",
    "system",
    "progress",
    "queue-operation",
}
CONTENT_SKIP_TYPES = {
    "tool_use",
    "tool_result",
    "thinking",
    "server_tool_use",
    "function_call",
    "function_call_output",
    "custom_tool_call",
    "custom_tool_call_output",
}


@dataclass
class Turn:
    role: str
    text: str


@dataclass
class Parsed:
    id: str
    source: str
    cwd: str | None
    started_at: str | None
    updated_at: str | None
    path: str
    mtime: float
    size: int
    title: str
    parent_id: str | None
    body: str
    turns: list[Turn] = field(default_factory=list)


def parse_file(path: Path, source: str) -> Parsed | None:
    try:
        raw = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None
    stat = path.stat()
    parsed = parse_text(raw, source, path)
    if parsed is None:
        return None
    parsed.mtime = stat.st_mtime
    parsed.size = stat.st_size
    parsed.path = str(path)
    if not parsed.updated_at:
        parsed.updated_at = _iso_from_mtime(stat.st_mtime)
    if not parsed.started_at:
        parsed.started_at = parsed.updated_at
    return parsed


def parse_text(raw: str, source: str, path: Path) -> Parsed | None:
    if source == "cursor":
        return _parse_cursor(raw, path)
    if source == "claude":
        return _parse_claude(raw, path)
    if source == "codex":
        return _parse_codex(raw, path)
    raise ValueError(f"unknown source: {source}")


def _parse_cursor(raw: str, path: Path) -> Parsed | None:
    turns: list[Turn] = []
    for obj in _iter_jsonl(raw):
        role = obj.get("role")
        if role not in ("user", "assistant"):
            continue
        message = obj.get("message") or {}
        text = _blocks_text(message.get("content"))
        if role == "user":
            extracted = _cursor_user_text(text)
            if not extracted:
                continue
            text = extracted
        if not text:
            continue
        turns.append(Turn(role, text))
    session_id = path.stem
    title = _first_user_title(turns)
    body = _body_from_turns(turns)
    return Parsed(
        id=session_id,
        source="cursor",
        cwd=cursor_cwd_from_path(path),
        started_at=None,
        updated_at=None,
        path=str(path),
        mtime=0,
        size=0,
        title=title,
        parent_id=cursor_parent_id(path),
        body=body,
        turns=turns,
    )


def _parse_claude(raw: str, path: Path) -> Parsed | None:
    turns: list[Turn] = []
    cwd: str | None = None
    session_id: str | None = None
    parent_id: str | None = None
    started_at: str | None = None
    updated_at: str | None = None
    agent_id: str | None = None
    in_subagents = "subagents" in path.parts

    for obj in _iter_jsonl(raw):
        row_type = obj.get("type")
        if row_type in CLAUDE_SKIP_TYPES:
            continue
        if obj.get("cwd") and not cwd:
            cwd = obj["cwd"]
        if obj.get("sessionId") and not session_id:
            session_id = obj["sessionId"]
        if obj.get("agentId"):
            agent_id = obj["agentId"]
        ts = obj.get("timestamp")
        if isinstance(ts, str):
            if started_at is None:
                started_at = ts
            updated_at = ts
        if obj.get("isMeta"):
            continue
        message = obj.get("message") or {}
        role = row_type if row_type in ("user", "assistant") else message.get("role")
        if role not in ("user", "assistant"):
            continue
        text = _claude_message_text(message.get("content"))
        if role == "user" and _is_junk_user_text(text):
            continue
        if not text:
            continue
        turns.append(Turn(role, text))

    if in_subagents:
        session_id_for_row = agent_id or path.stem
        parent_id = session_id or _parent_dir_id(path)
    else:
        session_id_for_row = session_id or path.stem

    return Parsed(
        id=session_id_for_row,
        source="claude",
        cwd=cwd,
        started_at=started_at,
        updated_at=updated_at,
        path=str(path),
        mtime=0,
        size=0,
        title=_first_user_title(turns),
        parent_id=parent_id,
        body=_body_from_turns(turns),
        turns=turns,
    )


def _parse_codex(raw: str, path: Path) -> Parsed | None:
    turns: list[Turn] = []
    session_id = path.stem
    cwd: str | None = None
    started_at: str | None = None
    updated_at: str | None = None
    parent_id: str | None = None

    for obj in _iter_jsonl(raw):
        ts = obj.get("timestamp")
        if isinstance(ts, str):
            updated_at = ts
        row_type = obj.get("type")
        payload = obj.get("payload") or {}
        if row_type == "session_meta":
            session_id = payload.get("id") or payload.get("session_id") or session_id
            cwd = payload.get("cwd") or cwd
            started_at = payload.get("timestamp") or started_at
            parent_id = (
                payload.get("forked_from_id")
                or payload.get("parent_thread_id")
                or parent_id
            )
            continue
        if row_type != "response_item":
            continue
        if payload.get("type") != "message":
            continue
        role = payload.get("role")
        if role not in ("user", "assistant"):
            continue
        text = _blocks_text(payload.get("content"))
        if role == "user":
            text = _codex_user_text(text)
        if not text:
            continue
        turns.append(Turn(role, text))

    if parent_id == session_id:
        parent_id = None

    return Parsed(
        id=session_id,
        source="codex",
        cwd=cwd,
        started_at=started_at,
        updated_at=updated_at,
        path=str(path),
        mtime=0,
        size=0,
        title=_first_user_title(turns),
        parent_id=parent_id,
        body=_body_from_turns(turns),
        turns=turns,
    )


def format_turns(parsed: Parsed, limit: int = 200_000) -> str:
    lines = [
        f"# id={parsed.id} source={parsed.source} cwd={parsed.cwd or ''}",
        f"# started={parsed.started_at or ''} updated={parsed.updated_at or ''}",
        f"# path={parsed.path}",
    ]
    if parsed.parent_id:
        lines.append(f"# parent_id={parsed.parent_id}")
    lines.append("")
    chunks: list[str] = []
    used = 0
    truncated = False
    for turn in parsed.turns:
        block = f"{turn.role}:\n{turn.text}\n"
        if used + len(block) > limit:
            truncated = True
            remain = limit - used
            if remain > 32:
                chunks.append(block[:remain].rstrip() + "\n")
            break
        chunks.append(block)
        used += len(block)
    text = "\n".join(lines) + "\n" + "\n".join(chunks)
    if truncated:
        text += f"\n[truncated; read {parsed.path}]\n"
    return text.rstrip() + "\n"


def _iter_jsonl(raw: str):
    for line in raw.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict):
            yield obj


def _blocks_text(content: Any) -> str:
    if isinstance(content, str):
        return html.unescape(content).strip()
    if not isinstance(content, list):
        return ""
    parts: list[str] = []
    for block in content:
        if isinstance(block, str):
            parts.append(block)
            continue
        if not isinstance(block, dict):
            continue
        btype = block.get("type")
        if btype in CONTENT_SKIP_TYPES:
            continue
        for key in ("text", "input_text", "output_text"):
            value = block.get(key)
            if isinstance(value, str) and value.strip():
                parts.append(value)
                break
    return html.unescape("\n".join(parts)).strip()


def _claude_message_text(content: Any) -> str:
    return _blocks_text(content)


def _cursor_user_text(text: str) -> str:
    match = USER_QUERY_RE.search(text)
    if match:
        return match.group(1).strip()
    cleaned = TIMESTAMP_RE.sub("", text).strip()
    return cleaned


BROWSER_CTX_RE = re.compile(
    r"<in-app-browser-context\b.*?</in-app-browser-context>", re.DOTALL
)
SKILL_DUMP_RE = re.compile(r"<skill\b.*?</skill>", re.DOTALL)


def _codex_user_text(text: str) -> str:
    text = BROWSER_CTX_RE.sub("", text)
    text = SKILL_DUMP_RE.sub("", text)
    marker = "## My request:"
    if marker in text:
        text = text.split(marker, 1)[1]
    return text.strip()


def _is_junk_user_text(text: str) -> bool:
    if not text:
        return True
    if "<local-command-caveat>" in text:
        return True
    if COMMAND_RE.search(text):
        return True
    if text.startswith("<system-reminder>"):
        return True
    return False


def _first_user_title(turns: list[Turn]) -> str:
    for turn in turns:
        if turn.role == "user" and turn.text.strip():
            return _one_line(turn.text, 160)
    return ""


def _body_from_turns(turns: list[Turn]) -> str:
    return "\n\n".join(f"{t.role}: {t.text}" for t in turns)


def _one_line(text: str, limit: int) -> str:
    line = " ".join(text.split())
    if len(line) <= limit:
        return line
    return line[: limit - 1].rstrip() + "…"


def _parent_dir_id(path: Path) -> str | None:
    parts = path.parts
    if "subagents" not in parts:
        return None
    idx = parts.index("subagents")
    if idx == 0:
        return None
    return parts[idx - 1]


def _iso_from_mtime(mtime: float) -> str:
    return datetime.fromtimestamp(mtime, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
