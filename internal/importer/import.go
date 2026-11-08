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

// tally counts stored conversations by source.
type tally map[string]*Summary

func (t tally) get(src string) *Summary {
	if t[src] == nil {
		t[src] = &Summary{Source: src}
	}
	return t[src]
}

func (t tally) store(root string, conv source.Imported) error {
	changed, err := source.WriteImported(root, conv)
	if err != nil {
		return fmt.Errorf("could not store %s conversation: %w", conv.Source, err)
	}
	if changed {
		t.get(conv.Source).Imported++
	} else {
		t.get(conv.Source).Unchanged++
	}
	return nil
}

func (t tally) addSkipped(skipped map[string]int) {
	for src, n := range skipped {
		t.get(src).Skipped += n
	}
}

// ImportZip stores each conversation of one export zip under root as soon as
// it is parsed, so only one conversation is held in memory at a time. A
// conversation already stored with the same content is left alone.
func ImportZip(file, root string, lim Limits) ([]Summary, error) {
	zr, err := openZip(file)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	counts := tally{}
	c := &collector{emit: func(conv source.Imported) error { return counts.store(root, conv) }}
	err = readZip(&zr.Reader, lim, c)
	counts.addSkipped(c.skipped)
	return summaries(counts), err
}

// Store writes the conversations of an already parsed export under root.
func Store(root string, res Result) ([]Summary, error) {
	counts := tally{}
	for _, conv := range res.Conversations {
		if err := counts.store(root, conv); err != nil {
			return summaries(counts), err
		}
	}
	counts.addSkipped(res.Skipped)
	return summaries(counts), nil
}

// Merge adds b's counts into a, keeping the order of Order.
func Merge(a, b []Summary) []Summary {
	counts := tally{}
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

func summaries(counts tally) []Summary {
	var out []Summary
	for _, src := range Order {
		if s := counts[src]; s != nil {
			out = append(out, *s)
		}
	}
	return out
}
