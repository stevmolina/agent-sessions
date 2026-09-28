# Remote sync

Cleaned session text from both machines should land in one Postgres database so it can be searched from anywhere. This note is the map. The code is the spec.

## What is stored today

`sessions_fts.body` holds the full user and assistant turns, joined as `role: text`. Snippets are built at search time in `store.SearchOptsQuery`. Tool output is dropped in `transcript.blockText` before the body is built.

Measured on this Mac on 2026-09-28: 1,006 sessions, 14.0 MB of body text, average 14 KB, max 246 KB. Titles add about 145 KB. Raw transcripts are much larger (Claude about 435 MB, Codex about 535 MB, Cursor about 66 MB) because they include tool output.

`gitleaks dir --redact --max-target-megabytes 200` on the raw trees, same day: Claude 124 findings in 32 files, Codex 270 in 31 files, Cursor 20 in 4 files. The same command on `~/.cache/sessions` reported 0. That is the bar after a cleaned reindex. A SQLite file can hide a match that the raw JSONL shows, so the raw counts are the ones that matter.

`show` does not read that body. It loads the raw transcript through `source.Adapter.Load`. A copy of the index on another machine cannot render a conversation until the turns themselves are stored. Raw files stay owned by the agent tools.

## Pipeline

1. **Collect.** `indexer.Refresh` walks Claude, Codex, Cursor, and T3.
2. **Clean.** `internal/clean`, called from `Refresh` before `store.Upsert`.
   - Exact values come from Doppler at run time and stay in memory. Each hit becomes `[REDACTED:NAME]`.
   - Pattern hits use the gitleaks rules linked in as a Go library (`github.com/zricethezav/gitleaks/v8`). Those become `[REDACTED:RULE]`.
   - Values shorter than 8 bytes are skipped. So are `true`, `false`, `yes`, `no`, `null`, `none`, `on`, `off`, all-digit values under 8 bytes, and letter-only values under 16 bytes. `DOPPLER_*` names are skipped.
   - Every project and config the logged-in Doppler CLI can read is included. A full read on this Mac took about 7 seconds. If the same name has different values, the placeholder is `PROJECT/NAME`, then `PROJECT/CONFIG/NAME`.
   - If cleaning a session fails, that session is not written. An older row for it is deleted. The raw file is not modified.
   - `meta.clean_fingerprint` is a hash of the secret set plus the gitleaks rule file. A change reindexes every session. The hash is not reversible.
3. **Verify.** `internal/clean/clean_test.go` builds fake keys at run time and checks that none of them survive. Before any upload, `gitleaks dir ~/.cache/sessions --redact --max-target-megabytes 200` must report 0 findings. A one-time canary and a weekly trufflehog scan of exported rows come later and do not block the first upload.
4. **Upload.** Not built yet. `sessions push` will refresh, then upsert by source and id, and record which machine sent the row. A 15 minute timer (launchd on macOS, a systemd user timer on Fedora) will be installed through chezmoi after it works. `uname -s` tells the machines apart. Paths go through `$(brew --prefix)`.
5. **Store.** Not created yet. Neon Postgres, one row per session, metadata plus the cleaned turns, `tsvector` with a GIN index. No raw JSONL. The connection string lives in Doppler, not in the repo or a `.env`.
6. **Access.** Not built yet. `sessions search --remote` and `show --remote`, plus a read-only Postgres role for SQL.

`SESSIONS_SECRETS_JSON` points at a JSON object and replaces the Doppler fetch. `index` and `search` print a warning when it is set. Tests set it. Leave it unset for a real index.

If Doppler cannot be read, `index` and `search` stop before writing. The previous index is left as it is. `show` does not contact Doppler.

Creating the Neon project, Doppler entries, or the timers needs an explicit yes before anything billable is created.
