# sessions

Search local coding-agent transcripts from the command line.

The index covers native Cursor, Claude Code, and Codex JSONL files, plus T3 Code threads from `state.sqlite`. T3 is an extra entry point, not a replacement for those native files. Conversations from ChatGPT, Claude.ai and Grok can be added from their data exports with `sessions import`.

## Install

On a machine that pushes, from the GitHub release (needs `gh` signed in):

```bash
gh release download latest -R stevmolina/agent-sessions -p install.sh -O - | sh
```

That installs `~/.local/bin/sessions` for this OS and CPU, then the 15 minute push timer, and checks that launchd or systemd really loaded it. `~/.agents/scripts/install-hooks.py` runs the same installer and adds the turn-end hooks, so a machine set up from `~/.agents` gets everything. Every push to `main` rebuilds the `latest` release.

From this repo:

```bash
go build -o sessions ./cmd/sessions
```

## Commands

```bash
sessions index
sessions search "query"
sessions show SOURCE:ID
sessions push
sessions push --trigger
sessions import EXPORT.zip... [--push]
sessions health
sessions version
sessions search "query" --remote
sessions show SOURCE:ID --remote
sessions mcp
```

`push --trigger` is for agent hooks. It returns at once and pushes in the background. One push runs per machine; a request made during a push makes that push run once more. `health` prints each machine's last push from the shared database and flags a machine whose last attempt failed or that has not pushed in 24 hours. See [docs/remote-sync.md](docs/remote-sync.md#keeping-it-running).

`push` needs `SESSIONS_OWNER` and files each session under a brain; see [Owner and brain](#owner-and-brain).

`search` refreshes stale sources first. You do not need `index` unless you want a refresh without searching. `search --remote` and `show --remote` read the Postgres copy and do not refresh.

`index` and `search` redact secrets into the local index before storing text. Transcript files are left unchanged, and local `show` still prints the raw file. `push` uploads the cleaned index. See [docs/remote-sync.md](docs/remote-sync.md).

`mcp` serves the cleaned Postgres copy over streamable HTTP for Claude connectors. Its `search` and `show` tools use the same remote queries as the CLI. WorkOS AuthKit handles login, and the server accepts only the configured user's tokens. See the [access setup](docs/remote-sync.md#access) for environment variables and connection instructions.

## Owner and brain

Every row `push` writes carries two tags, so a reader such as brain-mcp can show each person only their own sessions, filtered by project.

- `owner` is the WorkOS user id of the person pushing. `push` reads `SESSIONS_OWNER` from the environment, else from Doppler project `sessions` (config `dev`, or `SESSIONS_DOPPLER_CONFIG`). Without it `push` stops with an error naming `SESSIONS_OWNER`. Other commands do not need it.
- `brain` is the knowledge repo (`owner/repo`) a session belongs to, picked from its working folder by a map at `$SESSIONS_BRAINS_FILE`, else `~/.config/sessions/brains.json`:

```json
{
  "default": "someone/personal-brain",
  "rules": [
    {"brain": "someone/client-knowledge", "match": ["engagements/client", "worktrees/client-*"]}
  ]
}
```

The first rule with a matching pattern wins. A pattern matches whole folder names anywhere in the path: `engagements/client` matches `/home/me/dev/engagements/client/app` but not `.../engagements/client2`. A pattern ending in `*` matches any folder name that starts with it: `worktrees/client-*` matches `.../worktrees/client-fix-login`. Patterns are relative (no leading `/`), and `\` counts as `/`. A session with no folder (imports) or no match gets `default`; with no default, or no file at all, the brain is empty. A malformed file stops `push`.

Change the map and the next push retags the affected rows in place without uploading them again. The push summary counts them: `0 uploaded, 812 unchanged (40 retagged), ...`. A row's owner never changes once set, so another person pushing the same chat does not take it over or refile it. An empty brain never clears one: a machine with no map, or no matching rule, leaves the brain another machine set.

## Import chat exports

```bash
sessions import ~/Downloads/chatgpt-export.zip ~/Downloads/claude-export.zip
sessions import ~/Downloads/grok-export.zip --push
```

The format is detected from the zip. Each conversation becomes one session with source and provider `chatgpt`, `claude-ai` or `grok`, keyed by the app's own conversation id, so importing a newer export updates the same sessions instead of adding copies. Only user and assistant text is kept. For ChatGPT, the branch you last saw is kept and edited or regenerated branches are dropped; Grok branches are handled the same way.

Conversations are stored as one JSON file each under `~/.local/share/sessions/imports/<source>/` (`SESSIONS_IMPORTS_ROOT`). The normal `index` and `push` read them, so they are redacted like every other session before anything is uploaded. Keep that folder: a push uploads what is in it and removes from the shared copy what is no longer there. `--push` pushes right away; otherwise the next push (timer or hook) uploads them.

Where to request an export (each app emails a download link):

| App | Where |
|---|---|
| ChatGPT | Settings > Data controls > Export data |
| Claude.ai | Settings > Privacy > Export data (web or desktop, not the mobile apps) |
| Grok | accounts.x.ai/data, also reachable from grok.com Settings > Data controls |

The zip is treated as untrusted: it is read in memory, file names inside it are never used as paths, and reading stops past 512 MiB for one file or 2 GiB in total. Conversations are stored as they are read, so memory follows the largest conversation, not the whole export.

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
| Imported ChatGPT export | `chatgpt` | `chatgpt` |
| Imported Claude.ai export | `claude-ai` | `claude-ai` |
| Imported Grok export | `grok` | `grok` |

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
| `SESSIONS_IMPORTS_ROOT` | `~/.local/share/sessions/imports` |
| `SESSIONS_BRAINS_FILE` | `~/.config/sessions/brains.json` |

`SESSIONS_T3_ROOT` may be the userdata directory or a `state.sqlite` file. The adapter reads that database and does not write to it.

Older T3 desktop installs stored threads in Chromium IndexedDB under `~/Library/Application Support/t3code/...`. That format is not indexed. If SQLite is missing and that LevelDB directory exists, `index` prints a diagnostic instead of scraping `.ldb` files.

## Index schema

Schema version 4 stores a locator in `sessions.path` (the native file path, or `t3code://<environment-id>/<thread-id>`), plus `origin_path`, `provider`, and related metadata. Opening a version 2 or 3 index migrates it in place and keeps FTS rows. Unknown versions are refused; the file is not deleted. Version 4 exists so an older binary will not open a redacted index and write raw text back into it.

Python was removed. This Go CLI is the only supported implementation.
