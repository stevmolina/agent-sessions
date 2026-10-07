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

// Push upserts cleaned rows for this machine and deletes rows this machine no longer has.
// allowDelete stays false when a local clean pass failed, so a transient failure cannot
// remove an existing cloud copy.
func Push(ctx context.Context, rows []store.ExportRow, fingerprint, machine string, allowDelete bool) (Stats, error) {
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
	stats, err := syncRows(ctx, tx, rows, fingerprint, machine, allowDelete)
	if err != nil {
		return stats, err
	}
	if err = tx.Commit(ctx); err != nil {
		return stats, sqlError("commit", err)
	}
	return stats, nil
}

func syncRows(ctx context.Context, db conn, rows []store.ExportRow, fingerprint, machine string, allowDelete bool) (Stats, error) {
	remoteRows, err := loadRemote(ctx, db, machine)
	if err != nil {
		return Stats{}, err
	}
	local := make([]Row, 0, len(rows))
	byKey := map[string]store.ExportRow{}
	for _, row := range rows {
		item := Row{Source: row.Source, ID: row.ID, Revision: row.Revision, Fingerprint: fingerprint}
		local = append(local, item)
		byKey[key(item)] = row
	}
	decision := planSync(local, remoteRows, false)
	owned := make([]Row, 0)
	for _, row := range remoteRows {
		if row.Owned {
			owned = append(owned, row)
		}
	}
	removals := planSync(local, owned, allowDelete).remove
	var stats Stats
	stats.Unchanged = len(rows) - len(decision.upsert)
	for _, item := range decision.upsert {
		row := byKey[key(item)]
		if err := upsert(ctx, db, row, machine, fingerprint); err != nil {
			return stats, err
		}
		stats.Uploaded++
	}

	// An unchanged shared chat still belongs to this machine.
	for _, item := range local {
		if _, err := db.Exec(ctx, addOwnershipSQL, item.Source, item.ID, machine); err != nil {
			return stats, sqlError("ownership", err)
		}
	}
	for _, item := range removals {
		if _, err := db.Exec(ctx, removeOwnershipSQL, machine, item.Source, item.ID); err != nil {
			return stats, sqlError("ownership", err)
		}
		tag, err := db.Exec(ctx, deleteOrphanSQL, item.Source, item.ID)
		if err != nil {
			return stats, sqlError("delete", err)
		}
		stats.Deleted += int(tag.RowsAffected())
	}
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
	payload, err := json.Marshal(turnsOf(row))
	if err != nil {
		return errors.New("upsert failed")
	}
	_, err = db.Exec(ctx, upsertSQL,
		row.Source, row.ID, machine, row.Provider, row.ProviderSessionID, row.CWD, row.Title, row.Path, row.OriginPath,
		row.Revision, fingerprint, parseTime(row.StartedAt), parseTime(row.UpdatedAt), row.Body, payload,
	)
	if err != nil {
		return sqlError("upsert", err)
	}
	return nil
}
