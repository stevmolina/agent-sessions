package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/usuario/sessions/internal/config"
	"github.com/usuario/sessions/internal/indexer"
	"github.com/usuario/sessions/internal/source"
	"github.com/usuario/sessions/internal/store"
	"github.com/usuario/sessions/internal/transcript"
)

const help = `usage: sessions [-h] {index,search,show} ...

Search Cursor, Claude Code, Codex, and T3 Code transcripts in place.

positional arguments:
  {index,search,show}
    index              Walk transcript roots and refresh the SQLite index.
    search             Reindex stale sources, then FTS search.
    show               Print human turns for a session id or locator.

options:
  -h, --help           show this help message and exit

search options:
  --source cursor|claude|codex|t3code
                       Where the conversation was opened. Literal; does not
                       rewrite a native hit to its T3 twin.
  --provider cursor|claude|codex|grok|opencode
                       Which coding agent executed it. Direct Codex and a T3
                       thread run by Codex both match --provider codex.
  --cwd PATH           Restrict to this working directory or a subdirectory.
  --limit N            Max results (default 20), applied after duplicate collapse.
  --all-copies         Keep exact T3/native duplicates instead of collapsing.

source is the entry point. provider is the agent. A T3 thread run by Codex is
source=t3code provider=codex. Default search collapses exact T3/native twins
and prefers the T3 row. --source stays literal. show accepts source:id or a
t3code:// locator.
`

func Main(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		return usage(errOut, "the following arguments are required: command")
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, help)
		return 0
	}
	switch args[0] {
	case "index":
		if len(args) > 1 {
			return usage(errOut, "unrecognized arguments: "+strings.Join(args[1:], " "))
		}
		return index(out, errOut)
	case "search":
		return search(args[1:], out, errOut)
	case "show":
		if len(args) < 2 {
			return usage(errOut, "the following arguments are required: id")
		}
		if len(args) > 2 {
			return usage(errOut, "unrecognized arguments: "+strings.Join(args[2:], " "))
		}
		return show(args[1], out, errOut)
	default:
		return usage(errOut, "argument command: invalid choice: '"+args[0]+"' (choose from 'index', 'search', 'show')")
	}
}
func usage(w io.Writer, msg string) int {
	fmt.Fprintln(w, "usage: sessions [-h] {index,search,show} ...")
	fmt.Fprintln(w, "sessions: error: "+msg)
	return 2
}
func open() {}
func index(out, errOut io.Writer) int {
	db, err := store.Open(config.IndexPath())
	if err != nil {
		return fail(errOut, err)
	}
	defer db.Close()
	stats, err := indexer.Refresh(db)
	if err != nil {
		return fail(errOut, err)
	}
	printStats(errOut, stats)
	return 0
}
func search(args []string, out, errOut io.Writer) int {
	var query []string
	sourceFilter, provider, cwd := "", "", ""
	limit := 20
	allCopies := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--source":
			i++
			if i >= len(args) {
				return usage(errOut, "argument --source: expected one argument")
			}
			sourceFilter = args[i]
			if !source.ValidSource(sourceFilter) {
				return usage(errOut, "argument --source: invalid choice: '"+sourceFilter+"' (choose from 'cursor', 'claude', 'codex', 't3code')")
			}
		case "--provider":
			i++
			if i >= len(args) {
				return usage(errOut, "argument --provider: expected one argument")
			}
			parsed, ok := source.ParseProvider(args[i])
			if !ok {
				return usage(errOut, "argument --provider: invalid choice: '"+args[i]+"' (choose from 'cursor', 'claude', 'codex', 'grok', 'opencode')")
			}
			provider = parsed
		case "--cwd":
			i++
			if i >= len(args) {
				return usage(errOut, "argument --cwd: expected one argument")
			}
			cwd = args[i]
		case "--limit":
			i++
			if i >= len(args) {
				return usage(errOut, "argument --limit: expected one argument")
			}
			var e error
			limit, e = strconv.Atoi(args[i])
			if e != nil {
				return usage(errOut, "argument --limit: invalid int value: '"+args[i]+"'")
			}
		case "--all-copies":
			allCopies = true
		default:
			if strings.HasPrefix(args[i], "--") {
				return usage(errOut, "unrecognized arguments: "+args[i])
			}
			query = append(query, args[i])
		}
	}
	if len(query) == 0 {
		return usage(errOut, "the following arguments are required: query")
	}
	db, err := store.Open(config.IndexPath())
	if err != nil {
		return fail(errOut, err)
	}
	defer db.Close()
	stats, err := indexer.Refresh(db)
	if err != nil {
		return fail(errOut, err)
	}
	printStats(errOut, stats)
	hits, err := store.SearchOptsQuery(db, store.SearchOpts{
		Query:     strings.Join(query, " "),
		Source:    sourceFilter,
		Provider:  provider,
		CWD:       cwd,
		Limit:     limit,
		AllCopies: allCopies,
	})
	if err != nil {
		return fail(errOut, err)
	}
	if len(hits) == 0 {
		fmt.Fprintln(out, "no matches")
		return 0
	}
	for _, h := range hits {
		date := value(h.StartedAt)
		if date == "" {
			date = value(h.UpdatedAt)
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", h.ID, h.Source, date, value(h.CWD))
		if h.Source != h.Provider && h.Provider != "" {
			fmt.Fprintln(out, "  provider: "+h.Provider)
		}
		if h.Title != "" {
			fmt.Fprintln(out, "  title: "+h.Title)
		}
		fmt.Fprintln(out, "  snippet: "+strings.Join(strings.Fields(h.Snippet), " "))
		fmt.Fprintln(out, "  path: "+h.Path)
	}
	return 0
}
func show(id string, out, errOut io.Writer) int {
	db, err := store.Open(config.IndexPath())
	if err != nil {
		return fail(errOut, err)
	}
	rows, err := store.Lookup(db, id)
	db.Close()
	if err != nil {
		return fail(errOut, err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(errOut, "not found: "+id)
		return 1
	}
	if len(rows) > 1 {
		fmt.Fprintf(errOut, "multiple sessions match %s; use source:id or the path locator\n", id)
		for _, r := range rows {
			fmt.Fprintf(errOut, "%s:%s\t%s\n", r.Source, r.ID, r.Path)
		}
		return 1
	}
	s, err := source.AdapterFor(rows[0].Source).Load(indexer.CandidateFor(rows[0]))
	if err != nil {
		fmt.Fprintln(errOut, "failed to load: "+rows[0].Path)
		return 1
	}
	fmt.Fprint(out, transcript.Format(s, 200000))
	return 0
}
func printStats(w io.Writer, s indexer.Stats) {
	fmt.Fprintf(w, "%d upserted, %d unchanged, %d deleted, %d failed\n", s.Upserted, s.Unchanged, s.Deleted, s.Failed)
	for _, warning := range s.Warnings {
		fmt.Fprintln(w, warning)
	}
}
func fail(w io.Writer, err error) int { fmt.Fprintln(w, err); return 1 }
func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
