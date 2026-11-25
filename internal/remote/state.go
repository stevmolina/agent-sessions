package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usuario/sessions/internal/store"
)

// HeartbeatInterval is the longest a machine with nothing new goes without
// connecting. Each such connection is a full push (it writes nothing when
// nothing changed, and repairs drift if another machine touched our rows) plus a
// heartbeat, so `sessions health` (24 hour --max-age) keeps seeing idle machines
// as alive.
//
// Before 2026-11-24 every 15 minute timer run connected and wrote a heartbeat
// even when nothing changed. Neon never got its 5 idle minutes to suspend, and
// the free plan's monthly compute quota ran out on 2026-11-24.
const HeartbeatInterval = 6 * time.Hour

// FailureHeartbeatInterval spaces out heartbeats for pushes that failed before
// reaching the database (for example a missing SESSIONS_OWNER), so a broken
// machine reports itself without waking the database every 15 minutes.
// `sessions health` marks a machine failing after two such reports in a row.
const FailureHeartbeatInterval = time.Hour

// stateFormat changes when PushState's meaning changes; an older file is ignored.
const stateFormat = 2

// PushState is what the last successful push left in the shared database for
// this machine's rows. It lives in the cache folder (push-state.json). The next
// push compares the local index with it and skips the database when a push
// would write nothing. A missing or unreadable file means a normal push.
type PushState struct {
	Format      int    `json:"format"`
	Binary      string `json:"binary"`
	Machine     string `json:"machine"`
	Owner       string `json:"owner"`
	Fingerprint string `json:"fingerprint"`
	// Database names the database the record describes (see DatabaseID).
	Database string    `json:"database"`
	PushedAt time.Time `json:"pushed_at"`
	// HeartbeatAt is the last heartbeat this machine recorded after a
	// successful push. Zero when that heartbeat could not be written.
	HeartbeatAt time.Time `json:"heartbeat_at"`
	// Remote is the database's copy of every row this machine has or owns.
	Remote []Row `json:"remote"`
	// Failed are rows that failed verification at this revision. The same row at
	// the same revision fails the same way, so it is not a reason to connect.
	Failed []Row `json:"failed,omitempty"`
}

// NewPushState records a successful push.
func NewPushState(opts Options, binary, database string, stats Stats, now time.Time) *PushState {
	return &PushState{
		Format:      stateFormat,
		Binary:      binary,
		Database:    database,
		Machine:     opts.Machine,
		Owner:       opts.Owner,
		Fingerprint: opts.Fingerprint,
		PushedAt:    now.UTC(),
		Remote:      stats.Remote,
		Failed:      stats.FailedRows,
	}
}

// DatabaseID names the database DATABASE_URL points at: a short hash of its
// host, port and database name. It reads the URL (env or Doppler) but never
// connects, and the password is not part of it. A push to a new or reset
// database is then a change, not something to wait 6 hours for.
func DatabaseID(ctx context.Context) (string, error) {
	url, err := Secret(ctx, "DATABASE_URL")
	if err != nil {
		return "", err
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return "", fmt.Errorf("remote database unavailable")
	}
	sum := sha256.Sum256([]byte(cfg.Host + ":" + strconv.Itoa(int(cfg.Port)) + "/" + cfg.Database))
	return hex.EncodeToString(sum[:8]), nil
}

// StillFailing counts the recorded verification failures that are still in the
// index at the same revision. A failed row deleted since is no longer skipped.
func (s *PushState) StillFailing(rows []store.ExportRow) int {
	if s == nil || len(s.Failed) == 0 {
		return 0
	}
	failedAt := make(map[string]string, len(s.Failed))
	for _, row := range s.Failed {
		failedAt[key(row)] = row.Revision
	}
	n := 0
	for _, row := range rows {
		if rev, ok := failedAt[key(Row{Source: row.Source, ID: row.ID})]; ok && rev == row.Revision {
			n++
		}
	}
	return n
}

// LoadState reads the record of the last push. Any problem returns an error,
// and the caller pushes normally.
func LoadState(path string) (*PushState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s PushState
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.Format != stateFormat {
		return nil, fmt.Errorf("push state format %d, want %d", s.Format, stateFormat)
	}
	return &s, nil
}

// SaveState writes the record atomically, so a crash leaves the old one or the new one.
func SaveState(path string, s *PushState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// HeartbeatDue reports whether an idle machine should connect anyway.
func (s *PushState) HeartbeatDue(now time.Time) bool {
	if s == nil || s.HeartbeatAt.IsZero() || s.HeartbeatAt.After(now) {
		return true
	}
	return now.Sub(s.HeartbeatAt) >= HeartbeatInterval
}

// Pending says why pushing rows must reach the database, or "" when a push would
// write nothing. It runs the same plan as Push against the recorded state:
// changed or new sessions, deletions and ownership releases, ownership claims,
// and stale owner, brain or repo tags all count. opts.Machine must be set.
// database is DatabaseID for the database this push would write to.
func (s *PushState) Pending(rows []store.ExportRow, opts Options, binary, database string) string {
	switch {
	case s == nil:
		return "no record of the last push"
	case s.Binary != binary:
		return "new binary"
	case database == "" || s.Database != database:
		return "database changed"
	case s.Machine != opts.Machine:
		return "host name changed"
	case s.Owner != opts.Owner:
		return "owner changed"
	case s.Fingerprint != opts.Fingerprint:
		return "redaction secrets changed"
	}
	failedAt := make(map[string]string, len(s.Failed))
	for _, row := range s.Failed {
		failedAt[key(row)] = row.Revision
	}
	all := make([]Row, 0, len(rows))
	byKey := make(map[string]store.ExportRow, len(rows))
	for _, row := range rows {
		item := Row{Source: row.Source, ID: row.ID, Revision: row.Revision, Fingerprint: opts.Fingerprint}
		all = append(all, item)
		byKey[key(item)] = row
	}
	failed := map[string]bool{}
	changed := 0
	for _, item := range planSync(all, s.Remote, false).upsert {
		if rev, ok := failedAt[key(item)]; ok && rev == item.Revision {
			failed[key(item)] = true // fails verification again; Push would skip it
			continue
		}
		changed++
	}
	if changed > 0 {
		return fmt.Sprintf("%d session(s) new or changed", changed)
	}
	allowDelete := opts.AllowDelete && len(failed) == 0
	local := make([]Row, 0, len(all))
	for _, item := range all {
		if !failed[key(item)] {
			local = append(local, item)
		}
	}
	remoteByKey := make(map[string]Row, len(s.Remote))
	var owned []Row
	for _, row := range s.Remote {
		remoteByKey[key(row)] = row
		if row.Owned {
			owned = append(owned, row)
		}
	}
	for _, item := range local {
		prev, ok := remoteByKey[key(item)]
		if !ok {
			continue
		}
		mine := prev.Owner == "" || prev.Owner == opts.Owner
		if mine && opts.Repos != nil && !prev.ReposKnown {
			return "sessions need repo tags" // checked first: avoids the full transcript scan
		}
		if _, stale := retagFor(prev, byKey[key(item)], opts); stale {
			return "tags changed"
		}
		if _, stale := cardFor(prev, byKey[key(item)], opts, time.Time{}); stale {
			return "cards changed"
		}
		if !prev.Owned {
			return "sessions to claim"
		}
	}
	if removed := len(planSync(local, owned, allowDelete).remove); removed > 0 {
		return fmt.Sprintf("%d session(s) removed", removed)
	}
	return ""
}
