package remote

import (
	"context"
	"embed"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/store"
)

//go:embed schema.sql
var schemaFS embed.FS

var requiredColumns = []string{
	"source", "id", "machine", "provider", "provider_session_id", "cwd", "title",
	"path", "origin_path", "revision", "clean_fingerprint", "started_at", "updated_at",
	"body", "turns", "pushed_at", "search", "owner", "brain",
}

type Stats struct {
	Uploaded, Unchanged, Deleted, Failed int
	// Retagged are unchanged rows whose owner or brain was rewritten. They are
	// also counted in Unchanged, since their content was not uploaded again.
	Retagged int
	// SkippedPaths are rows that failed the pre-upload verification.
	SkippedPaths []string
}

type conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func connect(ctx context.Context, secretName string) (*pgx.Conn, error) {
	url, err := Secret(ctx, secretName)
	if err != nil {
		return nil, err
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, errors.New("remote database unavailable")
	}
	cfg.RuntimeParams["application_name"] = "sessions"
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("remote database unavailable")
	}
	return db, nil
}

// Statements per pipelined round trip. Upserts carry whole bodies, so they go in
// smaller groups than the tiny ownership rows.
const (
	upsertChunk    = 100
	ownershipChunk = 1000
)

// batcher is the pipelining half of pgx.Tx and pgx.Conn.
type batcher interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// execAll runs one statement per argument list and returns the rows affected.
// On a live Postgres connection the statements are pipelined in chunks, so two
// thousand rows cost a few round trips instead of two thousand. Other conns
// (the SQLite test double) run them one by one.
func execAll(ctx context.Context, db conn, action, sql string, args [][]any, chunk int) (int64, error) {
	var total int64
	b, pipelined := db.(batcher)
	if !pipelined {
		for _, a := range args {
			tag, err := db.Exec(ctx, sql, a...)
			if err != nil {
				return total, sqlError(action, err)
			}
			total += tag.RowsAffected()
		}
		return total, nil
	}
	if chunk < 1 {
		chunk = 1
	}
	for start := 0; start < len(args); start += chunk {
		end := min(start+chunk, len(args))
		batch := &pgx.Batch{}
		for _, a := range args[start:end] {
			batch.Queue(sql, a...)
		}
		res := b.SendBatch(ctx, batch)
		for range args[start:end] {
			tag, err := res.Exec()
			if err != nil {
				res.Close()
				return total, sqlError(action, err)
			}
			total += tag.RowsAffected()
		}
		if err := res.Close(); err != nil {
			return total, sqlError(action, err)
		}
	}
	return total, nil
}

func schemaStatements() ([]string, error) {
	raw, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, stmt := range strings.Split(string(raw), ";") {
		if stmt = strings.TrimSpace(stmt); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out, nil
}

func migrate(ctx context.Context, db conn) error {
	stmts, err := schemaStatements()
	if err != nil {
		return err
	}
	if _, ok := db.(batcher); ok {
		// One round trip for the whole schema, in order. Exec with no arguments uses
		// the simple protocol, which runs each statement before parsing the next. A
		// pgx.Batch would prepare every statement first and fail on an empty database,
		// where later statements name tables the earlier ones create.
		raw, err := schemaFS.ReadFile("schema.sql")
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, string(raw)); err != nil {
			return sqlError("migrate", err)
		}
	} else {
		for _, stmt := range stmts {
			if _, err := db.Exec(ctx, stmt); err != nil {
				return sqlError("migrate", err)
			}
		}
	}
	return checkColumns(ctx, db)
}

func checkColumns(ctx context.Context, db conn) error {
	rows, err := db.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'sessions'`)
	if err != nil {
		return sqlError("schema", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return sqlError("schema", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return sqlError("schema", err)
	}
	for _, name := range requiredColumns {
		if !have[name] {
			return errors.New("remote sessions table is missing a column")
		}
	}
	return nil
}

func sqlError(action string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return errors.New(action + " failed (" + pgErr.Code + ")")
	}
	return errors.New(action + " failed")
}

func parseTime(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	text := strings.TrimSpace(*value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC()
		}
	}
	return nil
}

func turnsOf(row store.ExportRow) []model.Turn {
	if row.Turns == nil {
		return []model.Turn{}
	}
	return row.Turns
}
