package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/usuario/sessions/internal/store"
)

const upsertSQL = `INSERT INTO sessions (
  source, id, machine, provider, provider_session_id, cwd, title, path, origin_path,
  revision, clean_fingerprint, started_at, updated_at, body, turns
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb
) ON CONFLICT (source, id) DO UPDATE SET
  machine = EXCLUDED.machine,
  provider = EXCLUDED.provider,
  provider_session_id = EXCLUDED.provider_session_id,
  cwd = EXCLUDED.cwd,
  title = EXCLUDED.title,
  path = EXCLUDED.path,
  origin_path = EXCLUDED.origin_path,
  revision = EXCLUDED.revision,
  clean_fingerprint = EXCLUDED.clean_fingerprint,
  started_at = EXCLUDED.started_at,
  updated_at = EXCLUDED.updated_at,
  body = EXCLUDED.body,
  turns = EXCLUDED.turns,
  pushed_at = now()`

const remoteStateSQL = `SELECT s.source, s.id,
 CASE WHEN s.turns = '[]'::jsonb THEN NULL ELSE s.revision END,
 s.clean_fingerprint,
 EXISTS (SELECT 1 FROM session_machines m WHERE m.source=s.source AND m.id=s.id AND m.machine=$1)
 FROM sessions s`
const addOwnershipSQL = `INSERT INTO session_machines(source,id,machine) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`
const removeOwnershipSQL = `DELETE FROM session_machines WHERE machine=$1 AND source=$2 AND id=$3`
const deleteOrphanSQL = `DELETE FROM sessions AS s WHERE s.source=$1 AND s.id=$2 AND NOT EXISTS (SELECT 1 FROM session_machines m WHERE m.source=s.source AND m.id=s.id)`

// Verifier re-checks a row right before it is uploaded. A false result skips the
// row and blocks deletes for this push.
type Verifier func(store.ExportRow) bool

// Push upserts cleaned rows for this machine and deletes rows this machine no longer has.
// allowDelete stays false when a local clean pass failed, so a transient failure cannot
// remove an existing cloud copy. verify, when set, runs only on rows that will be
// uploaded: an unchanged row was verified when it was uploaded under the same
// clean fingerprint, and re-checking all of them cost most of a no-change push.
func Push(ctx context.Context, rows []store.ExportRow, fingerprint, machine string, allowDelete bool, verify Verifier) (Stats, error) {
	if err := CheckFingerprint(fingerprint); err != nil {
		return Stats{}, err
	}
	if machine == "" {
		var err error
		machine, err = os.Hostname()
		if err != nil || machine == "" {
			return Stats{}, err
		}
	}
	db, err := connect(ctx, "DATABASE_URL")
	if err != nil {
		return Stats{}, err
	}
	defer db.Close(ctx)

	tx, err := db.Begin(ctx)
	if err != nil {
		return Stats{}, sqlError("begin", err)
	}
	defer tx.Rollback(ctx)
	// Serialize snapshot ownership changes, including migration and orphan removal.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1979716451)`); err != nil {
		return Stats{}, sqlError("lock", err)
	}
	if err := migrate(ctx, tx); err != nil {
		return Stats{}, err
	}
	stats, err := syncRowsVerified(ctx, tx, rows, fingerprint, machine, allowDelete, verify)
	if err != nil {
		return stats, err
	}
	if err = tx.Commit(ctx); err != nil {
		return stats, sqlError("commit", err)
	}
	return stats, nil
}

func syncRows(ctx context.Context, db conn, rows []store.ExportRow, fingerprint, machine string, allowDelete bool) (Stats, error) {
	return syncRowsVerified(ctx, db, rows, fingerprint, machine, allowDelete, nil)
}

func syncRowsVerified(ctx context.Context, db conn, rows []store.ExportRow, fingerprint, machine string, allowDelete bool, verify Verifier) (Stats, error) {
	remoteRows, err := loadRemote(ctx, db, machine)
	if err != nil {
		return Stats{}, err
	}
	all := make([]Row, 0, len(rows))
	byKey := map[string]store.ExportRow{}
	for _, row := range rows {
		item := Row{Source: row.Source, ID: row.ID, Revision: row.Revision, Fingerprint: fingerprint}
		all = append(all, item)
		byKey[key(item)] = row
	}
	planned := planSync(all, remoteRows, false).upsert
	var stats Stats
	failed := map[string]bool{}
	var toUpload []Row
	for _, item := range planned {
		if verify != nil && !verify(byKey[key(item)]) {
			failed[key(item)] = true
			stats.Failed++
			stats.SkippedPaths = append(stats.SkippedPaths, byKey[key(item)].Path)
			continue
		}
		toUpload = append(toUpload, item)
	}
	// A row that failed verification is treated as absent, as before: it is not
	// uploaded or claimed, and the failure blocks deletes for this push.
	local := all
	if len(failed) > 0 {
		allowDelete = false
		local = make([]Row, 0, len(all))
		for _, item := range all {
			if !failed[key(item)] {
				local = append(local, item)
			}
		}
	}
	owned := make([]Row, 0)
	for _, row := range remoteRows {
		if row.Owned {
			owned = append(owned, row)
		}
	}
	removals := planSync(local, owned, allowDelete).remove
	stats.Unchanged = len(local) - len(toUpload)
	upserts := make([][]any, 0, len(toUpload))
	for _, item := range toUpload {
		args, err := upsertArgs(byKey[key(item)], machine, fingerprint)
		if err != nil {
			return stats, err
		}
		upserts = append(upserts, args)
	}
	if _, err := execAll(ctx, db, "upsert", upsertSQL, upserts, upsertChunk); err != nil {
		return stats, err
	}
	stats.Uploaded = len(upserts)

	// An unchanged shared chat still belongs to this machine. Rows it already owns
	// are skipped, so a push with no changes writes nothing here.
	ownedKeys := make(map[string]bool, len(owned))
	for _, row := range owned {
		ownedKeys[key(row)] = true
	}
	var adds [][]any
	for _, item := range local {
		if !ownedKeys[key(item)] {
			adds = append(adds, []any{item.Source, item.ID, machine})
		}
	}
	if _, err := execAll(ctx, db, "ownership", addOwnershipSQL, adds, ownershipChunk); err != nil {
		return stats, err
	}
	var releases, orphans [][]any
	for _, item := range removals {
		releases = append(releases, []any{machine, item.Source, item.ID})
		orphans = append(orphans, []any{item.Source, item.ID})
	}
	if _, err := execAll(ctx, db, "ownership", removeOwnershipSQL, releases, ownershipChunk); err != nil {
		return stats, err
	}
	deleted, err := execAll(ctx, db, "delete", deleteOrphanSQL, orphans, ownershipChunk)
	if err != nil {
		return stats, err
	}
	stats.Deleted = int(deleted)
	return stats, nil
}

func loadRemote(ctx context.Context, db conn, machine string) ([]Row, error) {
	rows, err := db.Query(ctx, remoteStateSQL, machine)
	if err != nil {
		return nil, sqlError("read", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var row Row
		var revision, fingerprint *string
		if err := rows.Scan(&row.Source, &row.ID, &revision, &fingerprint, &row.Owned); err != nil {
			return nil, sqlError("read", err)
		}
		if revision != nil {
			row.Revision = *revision
		}
		if fingerprint != nil {
			row.Fingerprint = *fingerprint
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, sqlError("read", err)
	}
	return out, nil
}

func upsert(ctx context.Context, db conn, row store.ExportRow, machine, fingerprint string) error {
	args, err := upsertArgs(row, machine, fingerprint)
	if err != nil {
		return err
	}
	_, err = execAll(ctx, db, "upsert", upsertSQL, [][]any{args}, 1)
	return err
}

func upsertArgs(row store.ExportRow, machine, fingerprint string) ([]any, error) {
	payload, err := json.Marshal(turnsOf(row))
	if err != nil {
		return nil, errors.New("upsert failed")
	}
	return []any{
		row.Source, row.ID, machine, row.Provider, row.ProviderSessionID, row.CWD, row.Title, row.Path, row.OriginPath,
		row.Revision, fingerprint, parseTime(row.StartedAt), parseTime(row.UpdatedAt), row.Body, payload,
	}, nil
}
