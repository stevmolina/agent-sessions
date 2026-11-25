package remote

import (
	"github.com/usuario/sessions/internal/brainmap"
	"github.com/usuario/sessions/internal/digest"
	"github.com/usuario/sessions/internal/store"
)

// Annotate sets the clean title, the card and the digest on every row. The
// rows hold cleaned turns, so the card carries nothing the turns did not. Call
// it once after reading the index and before verifying or pushing, so
// verification covers the card text and Pending sees the same digest as Push.
func Annotate(rows []store.ExportRow, machine string, brains *brainmap.Map) {
	for i := range rows {
		derive(&rows[i], machine, brainOf(brains, rows[i]))
	}
}

func derive(row *store.ExportRow, machine, brain string) {
	res := digest.Build(digest.Input{
		Source: row.Source, Provider: row.Provider, Machine: machine, Brain: brain,
		CWD: deref(row.CWD), StartedAt: deref(row.StartedAt), UpdatedAt: deref(row.UpdatedAt),
		Title: row.Title, Turns: row.Turns,
	})
	row.Title, row.Card, row.Digest = res.Title, res.Card, res.Digest
}
