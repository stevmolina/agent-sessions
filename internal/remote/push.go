package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usuario/sessions/internal/brainmap"
	"github.com/usuario/sessions/internal/store"
)

const upsertSQL = `INSERT INTO sessions (
  source, id, machine, provider, provider_session_id, cwd, title, path, origin_path,
  revision, clean_fingerprint, started_at, updated_at, body, turns, owner, brain, repos,
  card, card_updated_at, digest
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb, $16, $17, $18::text[],
  $19, $20, $21
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
  repos = CASE WHEN sessions.owner IS NULL OR sessions.owner = EXCLUDED.owner
    THEN COALESCE(EXCLUDED.repos, sessions.repos) ELSE sessions.repos END,
  card = COALESCE(EXCLUDED.card, sessions.card),
  card_updated_at = CASE WHEN EXCLUDED.card IS NULL THEN sessions.card_updated_at ELSE EXCLUDED.card_updated_at END,
  digest = COALESCE(EXCLUDED.digest, sessions.digest),
  pushed_at = now()`

const remoteStateSQL = `SELECT s.source, s.id,
 CASE WHEN s.turns = '[]'::jsonb THEN NULL ELSE s.revision END,
 s.clean_fingerprint, s.owner, s.brain, s.repos, s.digest,
 EXISTS (SELECT 1 FROM session_machines m WHERE m.source=s.source AND m.id=s.id AND m.machine=$1)
 FROM sessions s`

// afterStateSQL is remoteStateSQL with the stored revision as is. It is read
// after a push for PushState: a row just uploaded with no turns must not look
// changed on every later run.
const afterStateSQL = `SELECT s.source, s.id, s.revision,
 s.clean_fingerprint, s.owner, s.brain, s.repos, s.digest,
 EXISTS (SELECT 1 FROM session_machines m WHERE m.source=s.source AND m.id=s.id AND m.machine=$1)
 FROM sessions s`

// updateCardSQL rewrites the title and card of a row whose content did not
// change. It leaves a row alone that someone else owns, or whose digest already
// matches, so repeating it writes nothing.
const updateCardSQL = `UPDATE sessions SET title=$1, card=$2, card_updated_at=$3, digest=$4
 WHERE source=$5 AND id=$6 AND (owner IS NULL OR owner=$7) AND digest IS DISTINCT FROM $4`

const addOwnershipSQL = `INSERT INTO session_machines(source,id,machine) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`
const removeOwnershipSQL = `DELETE FROM session_machines WHERE machine=$1 AND source=$2 AND id=$3`

// An owner, once set, never changes: a second person pushing the same chat
// does not take it over. An empty brain ($2 NULL) never clears a brain another
// machine's map set, so two machines with different maps cannot undo each
// other on every push. The WHERE repeats the owner check the caller made.
const retagSQL = `UPDATE sessions SET owner=COALESCE(owner, $1), brain=COALESCE($2, brain), repos=COALESCE($5::text[], repos)
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
	// Repos tags each row with the GitHub repos it belongs to. Nil tags none and
	// leaves the column alone. full is true when the row has never been tagged
	// or is being uploaded, so the transcript may be read; false asks for the
	// cheap folder-only answer.
	Repos func(row store.ExportRow, full bool) []string
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

	// The migration commits on its own before any real work. ALTER TABLE and
	// CREATE INDEX hold strong table locks until their transaction ends, and the
	// sync below can run for minutes (it reads transcripts to tag them). A short
	// lock_timeout makes a blocked migration give up instead of queueing a lock
	// that every reader would then wait behind.
	if err := migrateInTx(ctx, db); err != nil {
		return Stats{}, err
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return Stats{}, sqlError("begin", err)
	}
	defer tx.Rollback(ctx)
	// Serialize snapshot ownership changes, including orphan removal.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1979716451)`); err != nil {
		return Stats{}, sqlError("lock", err)
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

func migrateInTx(ctx context.Context, db *pgx.Conn) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return sqlError("begin", err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		return sqlError("lock", err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1979716451)`); err != nil {
		return sqlError("lock", err)
	}
	if err := migrate(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return sqlError("commit", err)
	}
	return nil
}

func syncRows(ctx context.Context, db conn, rows []store.ExportRow, opts Options) (Stats, error) {
	if opts.Owner == "" {
		return Stats{}, errNoOwner
	}
	machine, fingerprint, allowDelete := opts.Machine, opts.Fingerprint, opts.AllowDelete
	remoteRows, err := loadRemote(ctx, db, remoteStateSQL, machine)
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
	remoteByKey := make(map[string]Row, len(remoteRows))
	for _, row := range remoteRows {
		remoteByKey[key(row)] = row
	}
	for _, item := range toUpload {
		uploading[key(item)] = true
		row := byKey[key(item)]
		var repos []string
		if opts.Repos != nil {
			repos = mergeRepos(remoteByKey[key(item)].Repos, opts.Repos(row, true))
		}
		args, err := upsertArgs(row, machine, fingerprint, opts.Owner, brainOf(opts.Brains, row), repos)
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
	var retags, cardUpdates [][]any
	now := time.Now().UTC()
	for _, item := range local {
		if uploading[key(item)] {
			continue
		}
		prev, ok := remoteByKey[key(item)]
		if !ok {
			continue
		}
		if args, stale := retagFor(prev, byKey[key(item)], opts); stale {
			retags = append(retags, args)
		}
		if args, stale := cardFor(prev, byKey[key(item)], opts, now); stale {
			cardUpdates = append(cardUpdates, args)
		}
	}
	if _, err := execAll(ctx, db, "retag", retagSQL, retags, ownershipChunk); err != nil {
		return stats, err
	}
	stats.Retagged = len(retags)
	if _, err := execAll(ctx, db, "card", updateCardSQL, cardUpdates, upsertChunk); err != nil {
		return stats, err
	}
	stats.CardsUpdated = len(cardUpdates)

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

	// What the database now holds for this machine's rows, read inside the same
	// transaction. The caller saves it so the next push can tell, without
	// connecting, whether it would write anything (see PushState).
	after, err := loadRemote(ctx, db, afterStateSQL, machine)
	if err != nil {
		return stats, err
	}
	localKeys := make(map[string]bool, len(all))
	for _, item := range all {
		localKeys[key(item)] = true
	}
	for _, row := range after {
		if row.Owned || localKeys[key(row)] {
			stats.Remote = append(stats.Remote, row)
		}
	}
	for _, item := range all {
		if failed[key(item)] {
			stats.FailedRows = append(stats.FailedRows, item)
		}
	}
	return stats, nil
}

// retagFor decides whether an unchanged row needs its tags rewritten, and with
// what. A row someone else owns is left alone: neither its owner nor its brain
// is ours to set. Push and PushState.Pending both use it, so the local
// "nothing changed" answer cannot drift from what a real push would write.
func retagFor(prev Row, row store.ExportRow, opts Options) ([]any, bool) {
	if prev.Owner != "" && prev.Owner != opts.Owner {
		return nil, false
	}
	brain := brainOf(opts.Brains, row)
	brainStale := brain != "" && prev.Brain != brain
	var repos any
	reposStale := false
	if opts.Repos != nil {
		// Never-tagged rows get the full transcript scan once; tagged rows
		// only re-check the folder, which is cheap.
		merged := mergeRepos(prev.Repos, opts.Repos(row, !prev.ReposKnown))
		if !prev.ReposKnown || !sameRepos(prev.Repos, merged) {
			reposStale = true
			repos = merged
		}
	}
	if prev.Owner == "" || brainStale || reposStale {
		return []any{opts.Owner, nullable(brain), prev.Source, prev.ID, repos}, true
	}
	return nil, false
}

// cardFor decides whether an unchanged row needs a new title and card, and
// with what. A row someone else owns is left alone. A row that was not annotated
// (no digest) is never touched. Push and PushState.Pending both use it.
func cardFor(prev Row, row store.ExportRow, opts Options, now time.Time) ([]any, bool) {
	if row.Digest == "" || prev.Digest == row.Digest {
		return nil, false
	}
	if prev.Owner != "" && prev.Owner != opts.Owner {
		return nil, false
	}
	return []any{row.Title, nullable(row.Card), now, row.Digest, prev.Source, prev.ID, opts.Owner}, true
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

func loadRemote(ctx context.Context, db conn, query, machine string) ([]Row, error) {
	rows, err := db.Query(ctx, query, machine)
	if err != nil {
		return nil, sqlError("read", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var row Row
		var revision, fingerprint, owner, brain, digest *string
		var repos any
		if err := rows.Scan(&row.Source, &row.ID, &revision, &fingerprint, &owner, &brain, &repos, &digest, &row.Owned); err != nil {
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
		if digest != nil {
			row.Digest = *digest
		}
		row.Repos, row.ReposKnown = parseRepos(repos)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, sqlError("read", err)
	}
	return out, nil
}

func upsert(ctx context.Context, db conn, row store.ExportRow, machine, fingerprint, owner, brain string) error {
	args, err := upsertArgs(row, machine, fingerprint, owner, brain, nil)
	if err != nil {
		return err
	}
	_, err = execAll(ctx, db, "upsert", upsertSQL, [][]any{args}, 1)
	return err
}

// mergeRepos keeps every tag a row already has and adds new ones, so a machine
// that lacks a checkout never removes a tag another machine found. Capped.
func mergeRepos(prev, found []string) []string {
	out := append([]string{}, prev...)
	for _, repo := range found {
		if len(out) >= maxRepos {
			break
		}
		if !slices.Contains(out, repo) {
			out = append(out, repo)
		}
	}
	return out
}

const maxRepos = 10

func sameRepos(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// reposArg is the text[] parameter: NULL when no tags were computed, so an old
// value stays, and an empty array when the row was checked and has none.
func reposArg(repos []string) any {
	if repos == nil {
		return nil
	}
	return repos
}

func upsertArgs(row store.ExportRow, machine, fingerprint, owner, brain string, repos []string) ([]any, error) {
	payload, err := json.Marshal(turnsOf(row))
	if err != nil {
		return nil, errors.New("upsert failed")
	}
	return []any{
		row.Source, row.ID, machine, row.Provider, row.ProviderSessionID, row.CWD, row.Title, row.Path, row.OriginPath,
		row.Revision, fingerprint, parseTime(row.StartedAt), parseTime(row.UpdatedAt), row.Body, payload,
		nullable(owner), nullable(brain), reposArg(repos),
		nullable(row.Card), cardTime(row), nullable(row.Digest),
	}, nil
}

// cardTime is when the card was written: now, or NULL for a row with no card.
func cardTime(row store.ExportRow) any {
	if row.Card == "" {
		return nil
	}
	return time.Now().UTC()
}

// parseRepos reads a text[] column scanned into any. PostgreSQL arrives as
// []any; the SQLite test double stores JSON text.
func parseRepos(v any) ([]string, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case []string:
		return t, true
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	case string:
		return parseReposJSON([]byte(t))
	case []byte:
		return parseReposJSON(t)
	}
	return nil, false
}

func parseReposJSON(raw []byte) ([]string, bool) {
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		return nil, false
	}
	if out == nil {
		out = []string{}
	}
	return out, true
}
