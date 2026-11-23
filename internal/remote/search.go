package remote

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/repotag"
)

const searchSQL = `SELECT id, source, provider, cwd, started_at, updated_at, path, title, machine, repos,
  ts_headline('english', body, plainto_tsquery('english', $1), 'MaxWords=24, MinWords=8, MaxFragments=1')
FROM sessions
WHERE search @@ plainto_tsquery('english', $1)`

const showSQL = `SELECT id, source, provider, cwd, started_at, updated_at, path, title, turns, machine, repos
FROM sessions WHERE id = $1`

const showPathSQL = `SELECT id, source, provider, cwd, started_at, updated_at, path, title, turns, machine, repos
FROM sessions WHERE path = $1`

const showSourceSQL = `SELECT id, source, provider, cwd, started_at, updated_at, path, title, turns, machine, repos
FROM sessions WHERE source = $1 AND id = $2`

type Hit struct {
	ID, Source, Provider, Path, Title, Snippet string
	CWD, StartedAt, UpdatedAt                  *string
	Machine                                    string
	Repos                                      []string
}

// Filter narrows a search. Empty fields match everything.
type Filter struct {
	Source, Provider string
	// CWD matches any chat whose folder contains this text, so a whole path, a
	// folder name or "/data/OpenBot" all work. Case is ignored.
	CWD string
	// Repo is a GitHub repo, "owner/name", matched against the chat's repo tags.
	Repo string
	// Machine is the host name a chat was pushed from.
	Machine string
}

// escapeLike makes text safe inside a LIKE pattern.
func escapeLike(text string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
}

func Search(ctx context.Context, query string, f Filter, limit int) ([]Hit, error) {
	db, err := connect(ctx, "DATABASE_URL_READONLY")
	if err != nil {
		return nil, err
	}
	defer db.Close(ctx)
	if err := checkColumns(ctx, db); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	sql := searchSQL
	args := []any{query}
	if f.Source != "" {
		sql += " AND source = $" + itoa(len(args)+1)
		args = append(args, f.Source)
	}
	if f.Provider != "" {
		sql += " AND provider = $" + itoa(len(args)+1)
		args = append(args, f.Provider)
	}
	if f.CWD != "" {
		sql += " AND cwd ILIKE $" + itoa(len(args)+1)
		args = append(args, "%"+escapeLike(f.CWD)+"%")
	}
	if f.Repo != "" {
		repo := NormalizeRepoArg(f.Repo)
		if strings.Contains(repo, "/") {
			sql += " AND repos @> ARRAY[$" + itoa(len(args)+1) + "]::text[]"
			args = append(args, repo)
		} else {
			// A bare name such as "pokebots" matches that repo under any owner.
			sql += " AND EXISTS (SELECT 1 FROM unnest(repos) r WHERE r LIKE $" + itoa(len(args)+1) + ")"
			args = append(args, "%/"+escapeLike(repo))
		}
	}
	if f.Machine != "" {
		// The read-only role cannot see session_machines, so this is the machine that pushed the row last.
		sql += " AND machine = $" + itoa(len(args)+1)
		args = append(args, f.Machine)
	}
	sql += " ORDER BY ts_rank(search, plainto_tsquery('english', $1)) DESC, updated_at DESC NULLS LAST LIMIT $" + itoa(len(args)+1)
	args = append(args, limit)
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, sqlError("search", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var hit Hit
		var cwdText, title, path *string
		var startedAt, updatedAt *time.Time
		var repos []string
		if err := rows.Scan(&hit.ID, &hit.Source, &hit.Provider, &cwdText, &startedAt, &updatedAt, &path, &title, &hit.Machine, &repos, &hit.Snippet); err != nil {
			return nil, sqlError("search", err)
		}
		hit.CWD = cwdText
		hit.Repos = repos
		hit.Path = deref(path)
		hit.Title = deref(title)
		hit.StartedAt = timePtr(startedAt)
		hit.UpdatedAt = timePtr(updatedAt)
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, sqlError("search", err)
	}
	return out, nil
}

func Show(ctx context.Context, source, id string) ([]model.Session, error) {
	db, err := connect(ctx, "DATABASE_URL_READONLY")
	if err != nil {
		return nil, err
	}
	defer db.Close(ctx)
	if err := checkColumns(ctx, db); err != nil {
		return nil, err
	}
	var sql string
	var args []any
	switch {
	case source != "":
		sql = showSourceSQL
		args = []any{source, id}
	case strings.Contains(id, "/"):
		sql = showPathSQL
		args = []any{id}
	default:
		sql = showSQL
		args = []any{id}
	}
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, sqlError("show", err)
	}
	defer rows.Close()
	var out []model.Session
	for rows.Next() {
		var session model.Session
		var cwdText, title, path *string
		var startedAt, updatedAt *time.Time
		var turns []byte
		var repos []string
		if err := rows.Scan(&session.ID, &session.Source, &session.Provider, &cwdText, &startedAt, &updatedAt, &path, &title, &turns, &session.Machine, &repos); err != nil {
			return nil, sqlError("show", err)
		}
		session.Path = deref(path)
		session.Title = deref(title)
		session.CWD = cwdText
		session.Repos = repos
		session.StartedAt = timePtr(startedAt)
		session.UpdatedAt = timePtr(updatedAt)
		if len(turns) > 0 {
			if err := json.Unmarshal(turns, &session.Turns); err != nil {
				return nil, sqlError("show", err)
			}
		}
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, sqlError("show", err)
	}
	return out, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func timePtr(value *time.Time) *string {
	if value == nil {
		return nil
	}
	text := value.UTC().Format(time.RFC3339)
	return &text
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// NormalizeRepoArg turns what a person types for a repo filter into the stored
// form: "owner/name" in lower case, from a bare name, a URL or an ssh remote.
func NormalizeRepoArg(text string) string {
	text = strings.TrimSpace(text)
	if repo, ok := repotag.NormalizeRemote(text); ok {
		return repo
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(text, "/"), ".git"))
}
