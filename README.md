# sessions

Search local coding-agent transcripts from the command line.

The index covers native Cursor, Claude Code, and Codex JSONL files, plus T3 Code threads from `state.sqlite`. T3 is an extra entry point, not a replacement for those native files.

## Install

From this repo:

```bash
go build -o sessions ./cmd/sessions
```

## Commands

```bash
sessions index
sessions search "query"
sessions show SOURCE:ID
```

`search` refreshes stale sources first. You do not need `index` unless you want a refresh without searching.

## Source vs provider

`source` is where you opened the conversation. `provider` is which agent ran it.

| Conversation | Source | Provider |
|---|---|---|
| Direct Codex session | `codex` | `codex` |
| Direct Claude session | `claude` | `claude` |
| Direct Cursor session | `cursor` | `cursor` |
| T3 thread using Codex | `t3code` | `codex` |
| T3 thread using Claude | `t3code` | `claude` |
| T3 thread using Cursor | `t3code` | `cursor` |

```bash
sessions search "query" --source codex
sessions search "query" --source t3code
sessions search "query" --provider codex
sessions search "query" --source t3code --provider claude
sessions show t3code:THREAD-ID
sessions show t3code://ENVIRONMENT-ID/THREAD-ID
```

A T3 hit prints an extra `provider:` line when the agent is not T3 itself:

```text
thread-id	t3code	2026-09-14T12:00:00Z	/path/to/project
  provider: codex
  title: ...
  snippet: ...
  path: t3code://...
```

## Duplicates

T3 often has a native Codex/Claude/Cursor transcript for the same turn. When `provider_session_id` matches the native session id unambiguously, default search keeps one row and prefers the T3 thread. Both records stay in the index.

- `--source codex` still returns the native Codex file, including one linked to T3.
- `--source t3code` returns T3 threads.
- `--provider codex` returns direct Codex sessions and T3 threads run by Codex.
- `--all-copies` returns both rows.
- Ambiguous or guessed matches stay visible. Nothing is hidden on a fuzzy match.

`show` works for either id.

## Roots

| Variable | Default |
|---|---|
| `SESSIONS_INDEX` | `~/.cache/sessions/index.sqlite` |
| `SESSIONS_CURSOR_ROOT` | `~/.cursor/projects` |
| `SESSIONS_CLAUDE_ROOT` | `~/.claude/projects` |
| `SESSIONS_CODEX_ROOT` | `~/.codex/sessions` |
| `SESSIONS_CODEX_ARCHIVE_ROOT` | `~/.codex/archived_sessions` |
| `SESSIONS_T3_ROOT` | `~/.t3/userdata` |

`SESSIONS_T3_ROOT` may be the userdata directory or a `state.sqlite` file. The adapter reads that database and does not write to it.

Older T3 desktop installs stored threads in Chromium IndexedDB under `~/Library/Application Support/t3code/...`. That format is not indexed. If SQLite is missing and that LevelDB directory exists, `index` prints a diagnostic instead of scraping `.ldb` files.

## Index schema

Schema version 3 stores a locator in `sessions.path` (the native file path, or `t3code://<environment-id>/<thread-id>`), plus `origin_path`, `provider`, and related metadata. Opening a version 2 index migrates it in place and keeps FTS rows. Unknown versions are refused; the file is not deleted.

Python was removed. This Go CLI is the only supported implementation.
