package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/usuario/sessions/internal/brainmap"
	"github.com/usuario/sessions/internal/store"
)

const upsertSQL = `INSERT INTO sessions (
  source, id, machine, provider, provider_session_id, cwd, title, path, origin_path,
  revision, clean_fingerprint, started_at, updated_at, body, turns, owner, brain
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb, $16, $17
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
  owner = COALESCE(sessions.owner, EXCLUDED.owner),
  brain = CASE WHEN sessions.owner IS NULL OR sessions.owner = EXCLUDED.owner
    THEN COALESCE(EXCLUDED.brain, sessions.brain) ELSE sessions.brain END,
  pushed_at = now()`

const remoteStateSQL = `SELECT s.source, s.id,
 CASE WHEN s.turns = '[]'::jsonb THEN NULL ELSE s.revision END,
 s.clean_fingerprint, s.owner, s.brain,
 EXISTS (SELECT 1 FROM session_machines m WHERE m.source=s.source AND m.id=s.id AND m.machine=$1)
 FROM sessions s`
const addOwnershipSQL = `INSERT INTO session_machines(source,id,machine) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`
const removeOwnershipSQL = `DELETE FROM session_machines WHERE machine=$1 AND source=$2 AND id=$3`

// An owner, once set, never changes: a second person pushing the same chat
// does not take it over. An empty brain ($2 NULL) never clears a brain another
// machine's map set, so two machines with different maps cannot undo each
// other on every push. The WHERE repeats the owner check the caller made.
const retagSQL = `UPDATE sessions SET owner=COALESCE(owner, $1), brain=COALESCE($2, brain)
 WHERE source=$3 AND id=$4 AND (owner IS NULL OR owner=$1)`
const deleteOrphanSQL = `DELETE FROM sessions AS s WHERE s.source=$1 AND s.id=$2 AND NOT EXISTS (SELECT 1 FROM session_machines m WHERE m.source=s.source AND m.id=s.id)`

// Verifier re-checks a row right before it is uploaded. A false result skips the
// row and blocks deletes for this push.
type Verifier func(store.ExportRow) bool

// Options describe one push.
type Options struct {
	// Fingerprint is the index's clean fingerprint; every row carries it.
	Fingerprint string
	// Machine owns the rows; empty means the host name.
	Machine string
	// AllowDelete stays false when a local clean pass failed, so a transient
	// failure cannot remove an existing cloud copy.
	AllowDelete bool
	// Verify, when set, runs only on rows that will be uploaded: an unchanged
	// row was verified when it was uploaded under the same clean fingerprint,
	// and re-checking all of them cost most of a no-change push.
	Verify Verifier
	// Owner is the WorkOS user id of the person pushing. Required.
	Owner string
	// Brains files each row under a brain by its cwd. Nil files none.
	Brains *brainmap.Map
}

var errNoOwner = errors.New("refusing to push: no owner (set SESSIONS_OWNER)")

// Push upserts cleaned rows for this machine and deletes rows this machine no
// longer has. A row it writes gets the owner from opts unless it already has
// one, and the brain from opts unless that is empty or the row is someone
// else's. An unchanged row whose tags are missing or stale is retagged in place.
func Push(ctx context.Context, rows []store.ExportRow, opts Options) (Stats, error) {
	if err := CheckFingerprint(opts.Fingerprint); err != nil {
		return Stats{}, err
	}
	if opts.Owner == "" {
		return Stats{}, errNoOwner
	}
	if opts.Machine == "" {
		machine, err := os.Hostname()
		if err != nil || machine == "" {
			return Stats{}, err
		}
		opts.Machine = machine
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
	stats, err := syncRows(ctx, tx, rows, opts)
	if err != nil {
		return stats, err
	}
	if err = tx.Commit(ctx); err != nil {
		return stats, sqlError("commit", err)
	}
	return stats, nil
}

func syncRows(ctx context.Context, db conn, rows []store.ExportRow, opts Options) (Stats, error) {
	if opts.Owner == "" {
		return Stats{}, errNoOwner
	}
	machine, fingerprint, allowDelete := opts.Machine, opts.Fingerprint, opts.AllowDelete
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
		if opts.Verify != nil && !opts.Verify(byKey[key(item)]) {
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
	uploading := make(map[string]bool, len(toUpload))
	upserts := make([][]any, 0, len(toUpload))
	for _, item := range toUpload {
		uploading[key(item)] = true
		row := byKey[key(item)]
		args, err := upsertArgs(row, machine, fingerprint, opts.Owner, brainOf(opts.Brains, row))
		if err != nil {
			return stats, err
		}
		upserts = append(upserts, args)
	}
	if _, err := execAll(ctx, db, "upsert", upsertSQL, upserts, upsertChunk); err != nil {
		return stats, err
	}
	stats.Uploaded = len(upserts)

	// An unchanged row keeps its content but may carry stale tags: the map
	// changed, or an older client wrote it before owner and brain existed. Only
	// rows whose tags differ are written, so a push with no changes writes nothing.
	// A row this machine cannot file keeps the brain another machine gave it,
	// and a row someone else owns is left alone.
	remoteByKey := make(map[string]Row, len(remoteRows))
	for _, row := range remoteRows {
		remoteByKey[key(row)] = row
	}
	var retags [][]any
	for _, item := range local {
		if uploading[key(item)] {
			continue
		}
		prev, ok := remoteByKey[key(item)]
		if !ok {
			continue
		}
		if prev.Owner != "" && prev.Owner != opts.Owner {
			continue // another person's row: neither its owner nor its brain is ours to set
		}
		brain := brainOf(opts.Brains, byKey[key(item)])
		brainStale := brain != "" && prev.Brain != brain
		if prev.Owner == "" || brainStale {
			retags = append(retags, []any{opts.Owner, nullable(brain), item.Source, item.ID})
		}
	}
	if _, err := execAll(ctx, db, "retag", retagSQL, retags, ownershipChunk); err != nil {
		return stats, err
	}
	stats.Retagged = len(retags)

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

func brainOf(m *brainmap.Map, row store.ExportRow) string {
	if row.CWD == nil {
		return m.Brain("")
	}
	return m.Brain(*row.CWD)
}

// nullable stores an empty tag as NULL.
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
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
		var revision, fingerprint, owner, brain *string
		if err := rows.Scan(&row.Source, &row.ID, &revision, &fingerprint, &owner, &brain, &row.Owned); err != nil {
			return nil, sqlError("read", err)
		}
		if revision != nil {
			row.Revision = *revision
		}
		if fingerprint != nil {
			row.Fingerprint = *fingerprint
		}
		if owner != nil {
			row.Owner = *owner
		}
		if brain != nil {
			row.Brain = *brain
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, sqlError("read", err)
	}
	return out, nil
}

func upsert(ctx context.Context, db conn, row store.ExportRow, machine, fingerprint, owner, brain string) error {
	args, err := upsertArgs(row, machine, fingerprint, owner, brain)
	if err != nil {
		return err
	}
	_, err = execAll(ctx, db, "upsert", upsertSQL, [][]any{args}, 1)
	return err
}

func upsertArgs(row store.ExportRow, machine, fingerprint, owner, brain string) ([]any, error) {
	payload, err := json.Marshal(turnsOf(row))
	if err != nil {
		return nil, errors.New("upsert failed")
	}
	return []any{
		row.Source, row.ID, machine, row.Provider, row.ProviderSessionID, row.CWD, row.Title, row.Path, row.OriginPath,
		row.Revision, fingerprint, parseTime(row.StartedAt), parseTime(row.UpdatedAt), row.Body, payload,
		nullable(owner), nullable(brain),
	}, nil
}
