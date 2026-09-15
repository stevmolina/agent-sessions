package indexer

import (
	"database/sql"

	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/source"
	"github.com/usuario/sessions/internal/store"
)

type Stats struct {
	Upserted, Unchanged, Deleted, Failed int
	Warnings                             []string
}

func Refresh(db *sql.DB) (Stats, error) {
	var stats Stats
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
			if exists && sameRevision(item, prev) {
				stats.Unchanged++
				continue
			}
			s, err := adapter.Load(item)
			if err != nil {
				stats.Failed++
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
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	ok = true
	return stats, nil
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
