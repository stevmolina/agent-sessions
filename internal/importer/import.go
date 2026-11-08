package importer

import (
	"fmt"

	"github.com/usuario/sessions/internal/source"
)

// Summary counts what happened to one source's conversations.
type Summary struct {
	Source                       string
	Imported, Unchanged, Skipped int
}

// Store writes every conversation of a parsed export under root. A
// conversation already stored with the same content is left alone.
func Store(root string, res Result) ([]Summary, error) {
	counts := map[string]*Summary{}
	get := func(src string) *Summary {
		if counts[src] == nil {
			counts[src] = &Summary{Source: src}
		}
		return counts[src]
	}
	for _, conv := range res.Conversations {
		changed, err := source.WriteImported(root, conv)
		if err != nil {
			return summaries(counts), fmt.Errorf("could not store %s conversation: %w", conv.Source, err)
		}
		if changed {
			get(conv.Source).Imported++
		} else {
			get(conv.Source).Unchanged++
		}
	}
	for src, n := range res.Skipped {
		get(src).Skipped += n
	}
	return summaries(counts), nil
}

// Merge adds b's counts into a, keeping the order of Order.
func Merge(a, b []Summary) []Summary {
	counts := map[string]*Summary{}
	for _, list := range [][]Summary{a, b} {
		for _, s := range list {
			if counts[s.Source] == nil {
				counts[s.Source] = &Summary{Source: s.Source}
			}
			c := counts[s.Source]
			c.Imported += s.Imported
			c.Unchanged += s.Unchanged
			c.Skipped += s.Skipped
		}
	}
	return summaries(counts)
}

// Order is how summaries are listed.
var Order = append(source.ImportSources(), "unknown")

func summaries(counts map[string]*Summary) []Summary {
	var out []Summary
	for _, src := range Order {
		if s := counts[src]; s != nil {
			out = append(out, *s)
		}
	}
	return out
}
