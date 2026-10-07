package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/usuario/sessions/internal/clean"
	"github.com/usuario/sessions/internal/config"
	"github.com/usuario/sessions/internal/indexer"
	"github.com/usuario/sessions/internal/mcpserver"
	"github.com/usuario/sessions/internal/remote"
	"github.com/usuario/sessions/internal/source"
	"github.com/usuario/sessions/internal/store"
	"github.com/usuario/sessions/internal/transcript"
)

const help = `usage: sessions [-h] {index,search,show,push,mcp} ...

Search Cursor, Claude Code, Codex, and T3 Code transcripts in place.

index and search redact secrets before writing the local index. Transcript
files on disk are not modified. show prints the raw file. push uploads the
cleaned index. search --remote and show --remote read that upload.

positional arguments:
  {index,search,show,push,mcp}
    index              Walk transcript roots and refresh the SQLite index.
    search             Reindex stale sources, then FTS search.
    show               Print human turns for a session id or locator.
    push               Refresh, then upsert cleaned sessions to Postgres.
    mcp                Serve authenticated remote search over HTTP.

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
  --remote             Query the Postgres copy instead of the local index.

show options:
  --remote             Print cleaned turns stored in Postgres.

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
		return show(args[1:], out, errOut)
	case "mcp":
		if len(args) > 1 {
			return usage(errOut, "unrecognized arguments: "+strings.Join(args[1:], " "))
		}
		if err := mcpserver.Run(); err != nil {
			return fail(errOut, err)
		}
		return 0
	case "push":
		if len(args) > 1 {
			return usage(errOut, "unrecognized arguments: "+strings.Join(args[1:], " "))
		}
		return push(out, errOut)
	default:
		return usage(errOut, "argument command: invalid choice: '"+args[0]+"' (choose from 'index', 'search', 'show', 'push', 'mcp')")
	}
}
func usage(w io.Writer, msg string) int {
	fmt.Fprintln(w, "usage: sessions [-h] {index,search,show,push,mcp} ...")
	fmt.Fprintln(w, "sessions: error: "+msg)
	return 2
}
func openIndex() (*sql.DB, indexer.Cleaner, error) {
	db, err := store.Open(config.IndexPath())
	if err != nil {
		return nil, nil, err
	}
	cleaner, err := clean.Load(context.Background())
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, cleaner, nil
}
func index(out, errOut io.Writer) int {
	db, cleaner, err := openIndex()
	if err != nil {
		return fail(errOut, err)
	}
	defer db.Close()
	warnSecretsOverride(errOut)
	stats, err := indexer.Refresh(db, cleaner)
	if err != nil {
		return fail(errOut, err)
	}
	printStats(errOut, stats)
	return statsCode(stats)
}
func search(args []string, out, errOut io.Writer) int {
	var query []string
	sourceFilter, provider, cwd := "", "", ""
	limit := 20
	allCopies := false
	remoteSearch := false
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
		case "--remote":
			remoteSearch = true
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
	if remoteSearch {
		return searchRemote(strings.Join(query, " "), sourceFilter, provider, cwd, limit, out, errOut)
	}
	db, cleaner, err := openIndex()
	if err != nil {
		return fail(errOut, err)
	}
	defer db.Close()
	warnSecretsOverride(errOut)
	stats, err := indexer.Refresh(db, cleaner)
	if err != nil {
		return fail(errOut, err)
	}
	printStats(errOut, stats)
	if code := statsCode(stats); code != 0 {
		return code
	}
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
func show(args []string, out, errOut io.Writer) int {
	remoteShow := false
	var id string
	for _, arg := range args {
		switch arg {
		case "--remote":
			remoteShow = true
		default:
			if strings.HasPrefix(arg, "--") || id != "" {
				return usage(errOut, "unrecognized arguments: "+arg)
			}
			id = arg
		}
	}
	if id == "" {
		return usage(errOut, "the following arguments are required: id")
	}
	if remoteShow {
		return showRemote(id, out, errOut)
	}
	return showLocal(id, out, errOut)
}
func showLocal(id string, out, errOut io.Writer) int {
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
func searchRemote(query, sourceFilter, provider, cwd string, limit int, out, errOut io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hits, err := remote.Search(ctx, query, sourceFilter, provider, cwd, limit)
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
func showRemote(id string, out, errOut io.Writer) int {
	src := ""
	lookup := id
	if a, b, ok := strings.Cut(id, ":"); ok && source.ValidSource(a) {
		src = a
		lookup = b
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := remote.Show(ctx, src, lookup)
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
	fmt.Fprint(out, transcript.Format(&rows[0], 200000))
	return 0
}

type textCleaner interface {
	Text(string) (string, error)
}

func push(out, errOut io.Writer) int {
	db, cleaner, err := openIndex()
	if err != nil {
		return fail(errOut, err)
	}
	defer db.Close()
	warnSecretsOverride(errOut)
	stats, err := indexer.Refresh(db, cleaner)
	if err != nil {
		return fail(errOut, err)
	}
	printStats(errOut, stats)
	fingerprint, err := store.Meta(db, "clean_fingerprint")
	if err != nil {
		return fail(errOut, err)
	}
	if err := remote.CheckFingerprint(fingerprint); err != nil {
		return fail(errOut, err)
	}
	checker, ok := cleaner.(textCleaner)
	if !ok {
		return fail(errOut, errors.New("redaction unavailable"))
	}
	rows, err := store.List(db)
	if err != nil {
		return fail(errOut, err)
	}
	cleanRows := make([]store.ExportRow, 0, len(rows))
	verifyFailed := 0
	for _, row := range rows {
		if !rowStillClean(checker, row) {
			verifyFailed++
			fmt.Fprintln(errOut, "redaction failed, skipped "+row.Path)
			continue
		}
		cleanRows = append(cleanRows, row)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	uploaded, err := remote.Push(ctx, cleanRows, fingerprint, "", stats.Failed == 0 && verifyFailed == 0)
	if err != nil {
		return fail(errOut, err)
	}
	fmt.Fprintf(errOut, "%d uploaded, %d unchanged, %d deleted, %d failed\n", uploaded.Uploaded, uploaded.Unchanged, uploaded.Deleted, uploaded.Failed+verifyFailed+stats.Failed)
	if verifyFailed > 0 || stats.Failed > 0 {
		return 1
	}
	return 0
}
func rowStillClean(checker textCleaner, row store.ExportRow) bool {
	if !stillClean(checker, row.Title) || !stillClean(checker, row.Body) {
		return false
	}
	for _, turn := range row.Turns {
		if !stillClean(checker, turn.Text) {
			return false
		}
	}
	return true
}
func stillClean(checker textCleaner, text string) bool {
	got, err := checker.Text(text)
	return err == nil && got == text
}
func statsCode(s indexer.Stats) int {
	for _, warning := range s.Warnings {
		if strings.HasPrefix(warning, "redaction failed") {
			return 1
		}
	}
	return 0
}
func warnSecretsOverride(w io.Writer) {
	if os.Getenv("SESSIONS_SECRETS_JSON") != "" {
		fmt.Fprintln(w, "warning: SESSIONS_SECRETS_JSON is set; Doppler was not read")
	}
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
