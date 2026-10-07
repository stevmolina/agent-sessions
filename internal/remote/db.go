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
	"body", "turns", "pushed_at", "search",
}

type Stats struct {
	Uploaded, Unchanged, Deleted, Failed int
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

func migrate(ctx context.Context, db conn) error {
	raw, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	for _, stmt := range strings.Split(string(raw), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(ctx, stmt); err != nil {
			return sqlError("migrate", err)
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
