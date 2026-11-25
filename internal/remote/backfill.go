package remote

import (
	"context"
	"encoding/json"
	"time"

	"github.com/usuario/sessions/internal/digest"
	"github.com/usuario/sessions/internal/model"
)

const backfillSelect = `SELECT source, id, machine, provider, cwd, title, brain, started_at, updated_at, turns, digest FROM sessions`

// updateCardAnySQL is updateCardSQL without the owner check: the backfill runs
// with the owner role and writes derived text for every row, including rows that
// another machine pushed and that this computer has never seen.
const updateCardAnySQL = `UPDATE sessions SET title=$1, card=$2, card_updated_at=$3, digest=$4 WHERE source=$5 AND id=$6`

// BackfillStats says what a backfill did.
type BackfillStats struct {
	Rows, Updated, Skipped int
	Elapsed                time.Duration
}

// BackfillCards gives every row in the shared database a clean title and a card,
// built from the turns already stored there (which were redacted before they
// were uploaded). It needs no local transcript, so it covers rows pushed by other
// machines. A row whose digest already matches is left alone, so running it twice
// writes nothing the second time. ok, when set, is the redaction check: a
// title or card that fails it is skipped. dryRun counts without writing.
func BackfillCards(ctx context.Context, ok func(string) bool, dryRun bool) (BackfillStats, error) {
	start := time.Now()
	var stats BackfillStats
	db, err := connect(ctx, "DATABASE_URL")
	if err != nil {
		return stats, err
	}
	defer db.Close(ctx)
	if err := migrateInTx(ctx, db); err != nil {
		return stats, err
	}
	rows, err := db.Query(ctx, backfillSelect)
	if err != nil {
		return stats, sqlError("read", err)
	}
	var updates [][]any
	now := time.Now().UTC()
	for rows.Next() {
		var source, id, machine, provider string
		var cwd, title, brain, digestOld *string
		var started, updated *time.Time
		var raw []byte
		if err := rows.Scan(&source, &id, &machine, &provider, &cwd, &title, &brain, &started, &updated, &raw, &digestOld); err != nil {
			rows.Close()
			return stats, sqlError("read", err)
		}
		stats.Rows++
		var turns []model.Turn
		if json.Unmarshal(raw, &turns) != nil {
			stats.Skipped++
			continue
		}
		res := digest.Build(digest.Input{
			Source: source, Provider: provider, Machine: machine, Brain: deref(brain),
			CWD: deref(cwd), StartedAt: timeText(started), UpdatedAt: timeText(updated),
			Title: deref(title), Turns: turns,
		})
		if digestOld != nil && *digestOld == res.Digest {
			continue
		}
		if ok != nil && (!ok(res.Title) || !ok(res.Card)) {
			stats.Skipped++
			continue
		}
		updates = append(updates, []any{res.Title, nullable(res.Card), now, res.Digest, source, id})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, sqlError("read", err)
	}
	stats.Updated = len(updates)
	if dryRun || len(updates) == 0 {
		stats.Elapsed = time.Since(start)
		return stats, nil
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return stats, sqlError("begin", err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1979716451)`); err != nil {
		return stats, sqlError("lock", err)
	}
	if _, err := execAll(ctx, tx, "card", updateCardAnySQL, updates, upsertChunk); err != nil {
		return stats, err
	}
	if err := tx.Commit(ctx); err != nil {
		return stats, sqlError("commit", err)
	}
	stats.Elapsed = time.Since(start)
	return stats, nil
}

func timeText(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
