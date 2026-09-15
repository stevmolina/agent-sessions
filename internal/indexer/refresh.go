package indexer

import (
	"database/sql"
	"os"

	"github.com/usuario/sessions/internal/config"
	"github.com/usuario/sessions/internal/store"
	"github.com/usuario/sessions/internal/transcript"
)

type Stats struct{ Upserted, Unchanged, Deleted, Failed int }

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
	for _, item := range config.Discover() {
		seen[item.Path] = true
		info, err := os.Stat(item.Path)
		if err != nil {
			stats.Failed++
			continue
		}
		prev, exists := existing[item.Path]
		mtime := float64(info.ModTime().Unix()) + float64(info.ModTime().Nanosecond())/1e9
		if exists && prev[0] == mtime && int64(prev[1]) == info.Size() {
			stats.Unchanged++
			continue
		}
		s, err := transcript.ParseFile(item.Path, item.Source)
		if err != nil {
			stats.Failed++
			continue
		}
		if err := store.Upsert(tx, s); err != nil {
			return stats, err
		}
		stats.Upserted++
	}
	for path := range existing {
		if !seen[path] {
			if err := store.Delete(tx, path); err != nil {
				return stats, err
			}
			stats.Deleted++
		}
	}
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	ok = true
	return stats, nil
}
