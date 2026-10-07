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
   - Values shorter than 8 bytes are skipped. So are `true`, `false`, `yes`, `no`, `null`, `none`, `on`, `off`, letter-only values under 16 bytes, and other values under 16 bytes whose Shannon entropy is below 3.5 (regions, short hostnames). `DOPPLER_*` names are skipped.
   - Every project and config the logged-in Doppler CLI can read is included. A full read on this Mac took about 7 seconds. If the same name has different values, the placeholder is `PROJECT/NAME`, then `PROJECT/CONFIG/NAME`.
   - If cleaning a session fails, that session is not written. An older row for it is deleted. The raw file is not modified.
   - `meta.clean_fingerprint` is a hash of the secret set plus the gitleaks rule file. A change reindexes every session. The hash is not reversible. A Doppler read that yields no usable values is an error. `SESSIONS_SECRETS_JSON` writes the prefix `v1-override:` so that index is not mistaken for a Doppler clean later.
   - Paths, cwd, branch, and model are not redacted. `show` needs the real path. Exact matching does not catch percent-encoded or re-wrapped copies of a value. Gitleaks still runs on the text it can see.
   - The index schema is version 4. An older binary refuses it, so it cannot write raw turns over cleaned ones.
3. **Verify.** `internal/clean/clean_test.go` builds fake keys at run time and checks that none of them survive. Before any upload, `gitleaks dir ~/.cache/sessions --redact --max-target-megabytes 200` must report 0 findings. A one-time canary and a weekly trufflehog scan of exported rows come later and do not block the first upload.
4. **Upload.** `sessions push` refreshes, checks each stored title and body still redacts to itself, then upserts by source and id. The row records `os.Hostname()`. A clean failure skips that session and does not delete the cloud copy. Missing previously indexed roots and discovery errors prevent deletion. A complete empty snapshot releases that machine's remote ownership. Shared source/id rows have separate machine ownership records and disappear only after the last machine releases them. `SESSIONS_SECRETS_JSON` (`v1-override:`) is refused. `deploy/install-timer.sh` installs a 15 minute timer: launchd on macOS, a systemd user timer on Linux. It uses `uname -s` and `$HOME/.local/bin/sessions`; only macOS requires `brew --prefix`.
5. **Store.** Neon project `sessions` (`falling-field-30105744`), database `neondb`, table `sessions`. Schema is `internal/remote/schema.sql`: metadata, cleaned body, turns as jsonb, a generated `search` tsvector, and a GIN index. No raw JSONL. `DATABASE_URL` (owner) and `DATABASE_URL_READONLY` live in Doppler project `sessions`, configs `dev` and `prd`. `doppler.yaml` names that project. Nothing in the repo is a connection string.
6. **Access.** `sessions search --remote` and `sessions show --remote` use the read-only URL and do not refresh. SQL-created role `sessions_mcp_reader` has `SELECT` only. `push` uses the owner URL and applies the schema.

`SESSIONS_SECRETS_JSON` points at a JSON object and replaces the Doppler fetch. `index` and `search` print a warning when it is set. Tests set it. Leave it unset for a real index.

If Doppler cannot be read, `index` and `search` stop before writing. The previous index is left as it is. Local `show` does not contact Doppler. `search --remote` and `show --remote` need `DATABASE_URL_READONLY` and do not read Doppler secrets for redaction.

## Access

`sessions mcp` serves two read-only tools over streamable HTTP at `/mcp`:

- `search`: `query`, optional `source`, `provider`, `all_copies`, and `limit`. The default limit is 20. Like the CLI's remote search, it returns stored copies without local duplicate collapse, so `all_copies` does not change remote results.
- `show`: `id`, accepting a session ID, `source:id`, or a path locator. Ambiguous IDs require a source or locator. The response contains cleaned turns, with the same output limit as `show --remote`.

The server uses only `DATABASE_URL_READONLY`. It requires that URL in its environment and does not fetch Doppler secrets, refresh an index, apply the schema, or upload transcripts. The deployed application receives only the read-only DB URL and auth configuration.

Create the reader through SQL, then grant schema usage and table SELECT. [Neon Console, CLI, and API roles inherit `neon_superuser`](https://neon.com/docs/manage/roles), which can permit writes despite an explicit SELECT-only table grant. Verify effective write privileges before deploying. The read-only URL uses the restricted SQL role in both Doppler configs and Dokploy.

Required environment variables, names only:

| Variable | Purpose |
|---|---|
| `DATABASE_URL_READONLY` | Postgres URL for the SELECT-only role |
| `AUTHKIT_ISSUER` | WorkOS AuthKit authorization server issuer |
| `WORKOS_ALLOWED_USER_ID` | The only WorkOS subject permitted to use the tools |
| `MCP_RESOURCE_URL` | Public HTTPS URL of the `/mcp` resource |

Optional `PORT` defaults to `8080`, and `BIND_HOST` defaults to `0.0.0.0`. Store auth configuration in Doppler project `sessions`, configs `dev` and `prd`, and mirror the runtime values into Dokploy. Do not supply `DATABASE_URL` or a WorkOS API key to the deployed server.

WorkOS issues tokens. The server checks AuthKit's authorization server metadata issuer, the JWT signature against its JWKS, and the token's issuer, resource audience, expiration, and allowed subject. Token failures return 401 with an `invalid_token` bearer challenge and a link to `/.well-known/oauth-protected-resource`. That public metadata points clients at AuthKit. `/healthz` is public liveness only and never queries Neon, allowing the database to autosuspend.

Before connecting a client, enable Client ID Metadata Document support in WorkOS Connect configuration. Enable Dynamic Client Registration as well if an older MCP client needs it. Add the public MCP resource URL as a Resource Indicator. See the [AuthKit MCP setup](https://workos.com/docs/authkit/mcp) for client registration details. In claude.ai, add a custom connector using that URL and sign in. Claude Code can connect with:

```bash
claude mcp add --transport http sessions https://sessions.mcpbox.dev/mcp
```

Authenticate through `/mcp` in Claude Code, then run one `search` and one `show`. The Claude phone app can use the connector added to the same claude.ai account.

The Docker image runs `sessions mcp` as a non-root user. Deploy it with a 512 MiB memory cap. Its health check calls `/healthz` only. Keep secrets in Doppler and Dokploy, outside the image and repository.
