package remote

import (
	"context"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Heartbeat is one push attempt, as recorded for `sessions health`.
type Heartbeat struct {
	Machine                               string
	OK                                    bool
	Error                                 string
	Uploaded, Unchanged, Deleted, Skipped int
	Duration                              time.Duration
	Version, OS                           string
}

// HeartbeatRow is what the shared database holds for one machine.
type HeartbeatRow struct {
	Machine                               string
	LastAttempt                           time.Time
	LastSuccess, LastErrorAt              *time.Time
	LastError                             string
	Attempts, Successes, Failures         int64
	Uploaded, Unchanged, Deleted, Skipped int
	DurationMS                            int
	Version, OS                           string
}

// $2 is 1 for a success and 0 for a failure, so the counters are plain sums.
const heartbeatSQL = `INSERT INTO sessions_push_heartbeats (
  machine, last_attempt_at, last_success_at, last_error, last_error_at,
  attempts, successes, failures, uploaded, unchanged, deleted, skipped, duration_ms, version, os
) VALUES (
  $1, now(), CASE WHEN $2 = 1 THEN now() END, NULLIF($3, ''), CASE WHEN $2 = 1 THEN NULL ELSE now() END,
  1, $2, 1 - $2, $4, $5, $6, $7, $8, $9, $10
) ON CONFLICT (machine) DO UPDATE SET
  last_attempt_at = now(),
  last_success_at = CASE WHEN $2 = 1 THEN now() ELSE sessions_push_heartbeats.last_success_at END,
  last_error = NULLIF($3, ''),
  last_error_at = CASE WHEN $2 = 1 THEN sessions_push_heartbeats.last_error_at ELSE now() END,
  attempts = sessions_push_heartbeats.attempts + 1,
  successes = sessions_push_heartbeats.successes + $2,
  failures = sessions_push_heartbeats.failures + 1 - $2,
  uploaded = $4, unchanged = $5, deleted = $6, skipped = $7,
  duration_ms = $8, version = $9, os = $10`

const heartbeatsSQL = `SELECT machine, last_attempt_at, last_success_at, last_error_at, coalesce(last_error, ''),
 attempts, successes, failures, uploaded, unchanged, deleted, skipped, duration_ms, version, os
 FROM sessions_push_heartbeats ORDER BY machine`

// Machine is the name rows and heartbeats are filed under: the host name.
func Machine() (string, error) {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "", err
	}
	return name, nil
}

// RecordHeartbeat writes one attempt with the owner URL. It needs the database to
// be reachable, so a machine that cannot read Doppler at all never appears here;
// `sessions health --expect` covers that case.
func RecordHeartbeat(ctx context.Context, hb Heartbeat) error {
	db, err := connect(ctx, "DATABASE_URL")
	if err != nil {
		return err
	}
	defer db.Close(ctx)
	return recordHeartbeat(ctx, db, hb)
}

func recordHeartbeat(ctx context.Context, db conn, hb Heartbeat) error {
	ok := 0
	if hb.OK {
		ok = 1
	}
	_, err := db.Exec(ctx, heartbeatSQL,
		hb.Machine, ok, ShortError(hb.Error),
		hb.Uploaded, hb.Unchanged, hb.Deleted, hb.Skipped,
		int(hb.Duration/time.Millisecond), hb.Version, hb.OS,
	)
	if err != nil {
		return sqlError("heartbeat", err)
	}
	return nil
}

// ShortError keeps the first line of a message, at most 200 characters.
// Messages here come from this program (they already avoid secret values);
// the cut keeps a stray long one from filling the table.
func ShortError(msg string) string {
	msg = strings.TrimSpace(msg)
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = msg[:i]
	}
	const max = 200
	if utf8.RuneCountInString(msg) > max {
		msg = string([]rune(msg)[:max]) + "..."
	}
	return msg
}

// Heartbeats reads every machine's row with the read-only URL.
func Heartbeats(ctx context.Context) ([]HeartbeatRow, error) {
	db, err := connect(ctx, "DATABASE_URL_READONLY")
	if err != nil {
		return nil, err
	}
	defer db.Close(ctx)
	rows, err := db.Query(ctx, heartbeatsSQL)
	if err != nil {
		return nil, sqlError("health", err)
	}
	defer rows.Close()
	var out []HeartbeatRow
	for rows.Next() {
		var r HeartbeatRow
		if err := rows.Scan(&r.Machine, &r.LastAttempt, &r.LastSuccess, &r.LastErrorAt, &r.LastError,
			&r.Attempts, &r.Successes, &r.Failures, &r.Uploaded, &r.Unchanged, &r.Deleted, &r.Skipped,
			&r.DurationMS, &r.Version, &r.OS); err != nil {
			return nil, sqlError("health", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, sqlError("health", err)
	}
	return out, nil
}
