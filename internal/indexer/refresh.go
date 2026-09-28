package indexer

import (
	"database/sql"
	"errors"

	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/source"
	"github.com/usuario/sessions/internal/store"
)

const cleanFingerprintKey = "clean_fingerprint"

// Cleaner redacts a session before it is stored. A failure must skip that session.
type Cleaner interface {
	Apply(*model.Session) error
	Fingerprint() string
}

type Stats struct {
	Upserted, Unchanged, Deleted, Failed int
	Warnings                             []string
}

func Refresh(db *sql.DB, cleaner Cleaner) (Stats, error) {
	var stats Stats
	if cleaner == nil || cleaner.Fingerprint() == "" {
		return stats, errors.New("redaction unavailable")
	}
	fingerprint := cleaner.Fingerprint()
	stored, err := store.Meta(db, cleanFingerprintKey)
	if err != nil {
		return stats, err
	}
	reindex := stored != fingerprint
	existing, err := store.Indexed(db)
	if err != nil {
		return stats, err
	}
	tx, err := db.Begin()
	if err != nil {
		return stats, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = tx.Rollback()
		}
	}()
	seen := map[string]bool{}
	for _, adapter := range source.Adapters() {
		candidates, err := adapter.Discover()
		if err != nil {
			stats.Failed++
			stats.Warnings = append(stats.Warnings, err.Error())
			continue
		}
		for _, item := range candidates {
			seen[item.Locator] = true
			prev, exists := existing[item.Locator]
			if exists && !reindex && sameRevision(item, prev) {
				stats.Unchanged++
				continue
			}
			s, err := adapter.Load(item)
			if err != nil {
				stats.Failed++
				continue
			}
			if err := applyClean(cleaner, s); err != nil {
				stats.Failed++
				stats.Warnings = append(stats.Warnings, "redaction failed, skipped "+item.Locator)
				if exists {
					if err := store.Delete(tx, item.Locator); err != nil {
						return stats, err
					}
				}
				continue
			}
			if err := store.Upsert(tx, s); err != nil {
				return stats, err
			}
			stats.Upserted++
		}
	}
	for path := range existing {
		if !seen[path] {
			if err := store.Delete(tx, path); err != nil {
				return stats, err
			}
			stats.Deleted++
		}
	}
	if err := store.RebuildAliases(tx); err != nil {
		return stats, err
	}
	if err := store.SetMeta(tx, cleanFingerprintKey, fingerprint); err != nil {
		return stats, err
	}
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	ok = true
	return stats, nil
}

func applyClean(c Cleaner, s *model.Session) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("redaction failed")
		}
	}()
	return c.Apply(s)
}

func sameRevision(item source.Candidate, prev store.IndexedMeta) bool {
	if item.Source == "t3code" {
		return item.Revision != "" && item.Revision == prev.Revision
	}
	return prev.MTime == item.MTime && prev.Size == item.Size
}

func CandidateFor(row model.Row) source.Candidate {
	origin := row.OriginPath
	if origin == "" {
		origin = row.Path
	}
	return source.Candidate{
		Locator:    row.Path,
		Source:     row.Source,
		OriginPath: origin,
		RecordID:   row.RecordID,
		Revision:   row.Revision,
	}
}
