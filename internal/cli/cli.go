package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/usuario/sessions/internal/clean"
	"github.com/usuario/sessions/internal/config"
	"github.com/usuario/sessions/internal/health"
	"github.com/usuario/sessions/internal/indexer"
	"github.com/usuario/sessions/internal/mcpserver"
	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/pushlock"
	"github.com/usuario/sessions/internal/remote"
	"github.com/usuario/sessions/internal/source"
	"github.com/usuario/sessions/internal/store"
	"github.com/usuario/sessions/internal/transcript"
	"github.com/usuario/sessions/internal/version"
)

const commands = "{index,search,show,push,health,version,mcp}"

const help = `usage: sessions [-h] ` + commands + ` ...

Search Cursor, Claude Code, Codex, and T3 Code transcripts in place.

index and search redact secrets before writing the local index. Transcript
files on disk are not modified. show prints the raw file. push uploads the
cleaned index. search --remote and show --remote read that upload.

positional arguments:
  ` + commands + `
    index              Walk transcript roots and refresh the SQLite index.
    search             Reindex stale sources, then FTS search.
    show               Print human turns for a session id or locator.
    push               Refresh, then upsert cleaned sessions to Postgres.
    health             Show each machine's last push and flag problems.
    version            Print the build version.
    mcp                Serve authenticated remote search over HTTP.

options:
  -h, --help           show this help message and exit

push options:
  --trigger            Start a push in the background and return at once.
                       Agent hooks call this when a turn ends. Only one push
                       runs per machine; a request made during a push makes
                       that push run once more when it finishes.

health options:
  --expect A,B         Machines that should be pushing, even if none of their
                       pushes ever reached the database. Default:
                       $SESSIONS_EXPECTED_MACHINES.
  --max-age DURATION   Flag a machine with no successful push for this long
                       (default 24h).
  --notify-slack ID    Post to this Slack channel when a machine turns bad or
                       recovers. Uses SLACK_BOT_TOKEN, else Doppler
                       claudito/prd. Exits 0 unless posting fails.
  --state PATH         Where --notify-slack remembers what it announced
                       (default ~/.cache/sessions/health-alert.json).

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
		switch {
		case len(args) == 1:
			return pushLocked(out, errOut)
		case len(args) == 2 && args[1] == "--trigger":
			return pushTrigger(errOut)
		default:
			return usage(errOut, "unrecognized arguments: "+strings.Join(args[1:], " "))
		}
	case "health":
		return healthCmd(args[1:], out, errOut)
	case "version":
		if len(args) > 1 {
			return usage(errOut, "unrecognized arguments: "+strings.Join(args[1:], " "))
		}
		fmt.Fprintln(out, version.String())
		return 0
	default:
		return usage(errOut, "argument command: invalid choice: '"+args[0]+"' (choose from 'index', 'search', 'show', 'push', 'health', 'version', 'mcp')")
	}
}
func usage(w io.Writer, msg string) int {
	fmt.Fprintln(w, "usage: sessions [-h] "+commands+" ...")
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
	return showRemoteWithLookup(id, out, errOut, remote.Show)
}

func showRemoteWithLookup(id string, out, errOut io.Writer, lookupFn func(context.Context, string, string) ([]model.Session, error)) int {
	src, lookup := remote.ParseLocator(id)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := lookupFn(ctx, src, lookup)
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

// pushLocked is `sessions push`: one push per machine at a time. A push asked
// for while another runs is folded into that one (see internal/pushlock).
func pushLocked(out, errOut io.Writer) int {
	dir := config.CacheDir()
	code, ran, err := pushlock.Run(dir, func() int { return pushOnce(errOut) })
	if err != nil {
		return fail(errOut, err)
	}
	if !ran {
		fmt.Fprintln(errOut, "a push is already running; it will run once more when it finishes")
		return code
	}
	// Run stops after MaxRuns. A request still waiting then gets its own
	// background push now instead of waiting up to 15 minutes for the timer.
	if pushlock.Pending(dir) && !pushlock.Held(dir) {
		pushTrigger(errOut)
	}
	return code
}

// pushOnce runs one push and records how it went, success or not, in the
// shared heartbeat table.
func pushOnce(errOut io.Writer) int {
	start := time.Now()
	// The upload has a 10 minute context, but reading transcripts does not. A read
	// stuck on a hung disk or network mount would hold the push lock forever and
	// every later trigger would just queue behind it. Exit instead; the kernel
	// drops the lock and the next trigger or timer starts fresh.
	watchdog := time.AfterFunc(pushWatchdog, func() {
		fmt.Fprintf(errOut, "push exceeded %s, exiting\n", pushWatchdog)
		os.Exit(3)
	})
	defer watchdog.Stop()
	res, err := runPush(errOut)
	hb := remote.Heartbeat{
		OK:        err == nil,
		Uploaded:  res.stats.Uploaded,
		Unchanged: res.stats.Unchanged,
		Deleted:   res.stats.Deleted,
		Skipped:   res.skipped,
		Duration:  time.Since(start),
		Version:   version.String(),
		OS:        runtime.GOOS + "/" + runtime.GOARCH,
	}
	if err != nil {
		hb.Error = err.Error()
		var pub publicError
		if errors.As(err, &pub) {
			hb.Error = pub.public
		}
	}
	if machine, merr := remote.Machine(); merr == nil && machine != "" {
		hb.Machine = machine
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if herr := remote.RecordHeartbeat(ctx, hb); herr != nil {
			fmt.Fprintln(errOut, "heartbeat not recorded: "+herr.Error())
		}
		cancel()
	}
	if err != nil {
		return fail(errOut, err)
	}
	fmt.Fprintf(errOut, "%d uploaded, %d unchanged, %d deleted, %d failed in %.1fs\n",
		res.stats.Uploaded, res.stats.Unchanged, res.stats.Deleted, res.skipped, time.Since(start).Seconds())
	if res.skipped > 0 {
		return 1
	}
	return 0
}

// pushWatchdog bounds one push, refresh included. Five of them fit in the
// systemd unit's 60 minute TimeoutStartSec.
const pushWatchdog = 11 * time.Minute

type pushResult struct {
	stats   remote.Stats
	skipped int // sessions left out because they could not be cleaned
}

// publicError keeps the full message for the terminal and a fixed one for the
// shared heartbeat. Doppler errors name projects and configs, and a project can
// be named like a value it stores.
type publicError struct {
	public string
	err    error
}

func (e publicError) Error() string { return e.err.Error() }
func (e publicError) Unwrap() error { return e.err }

func runPush(errOut io.Writer) (pushResult, error) {
	var res pushResult
	db, cleaner, err := openIndex()
	if err != nil {
		return res, publicError{public: "could not open the index or read redaction secrets from Doppler (is doppler logged in?)", err: err}
	}
	defer db.Close()
	warnSecretsOverride(errOut)
	stats, err := indexer.Refresh(db, cleaner)
	if err != nil {
		return res, err
	}
	printStats(errOut, stats)
	res.skipped = stats.Failed
	fingerprint, err := store.Meta(db, "clean_fingerprint")
	if err != nil {
		return res, err
	}
	if err := remote.CheckFingerprint(fingerprint); err != nil {
		return res, err
	}
	checker, ok := cleaner.(textCleaner)
	if !ok {
		return res, errors.New("redaction unavailable")
	}
	rows, err := store.List(db)
	if err != nil {
		return res, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	machine, err := remote.Machine()
	if err != nil || machine == "" {
		return res, errors.New("host name unavailable")
	}
	// Each row about to be uploaded must still redact to itself.
	verify := func(row store.ExportRow) bool { return rowStillClean(checker, row) }
	res.stats, err = remote.Push(ctx, rows, fingerprint, machine, stats.Failed == 0, verify)
	for _, path := range res.stats.SkippedPaths {
		fmt.Fprintln(errOut, "redaction failed, skipped "+path)
	}
	res.skipped += res.stats.Failed
	return res, err
}

// pushTrigger is what agent hooks call at the end of a turn. It must return
// at once and never fail the agent: it records the request and, unless a push
// is already running (which will pick the request up), starts one detached
// from the hook, with output in the cache folder's push.log.
func pushTrigger(errOut io.Writer) int {
	dir := config.CacheDir()
	if err := pushlock.MarkPending(dir); err != nil {
		fmt.Fprintln(errOut, "push not queued: "+err.Error())
		return 0
	}
	if pushlock.Held(dir) {
		return 0
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(errOut, "push not started: "+err.Error())
		return 0
	}
	logFile, err := openPushLog(dir)
	if err != nil {
		fmt.Fprintln(errOut, "push not started: "+err.Error())
		return 0
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "== %s trigger, %s\n", time.Now().Format(time.RFC3339), version.String())
	cmd := exec.Command(self, "push")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(errOut, "push not started: "+err.Error())
		return 0
	}
	_ = cmd.Process.Release()
	return 0
}

// openPushLog appends to push.log and starts a new one past 1 MB.
func openPushLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "push.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func healthCmd(args []string, out, errOut io.Writer) int {
	expected := splitList(os.Getenv("SESSIONS_EXPECTED_MACHINES"))
	maxAge := health.DefaultMaxAge
	channel := ""
	statePath := filepath.Join(config.CacheDir(), "health-alert.json")
	for i := 0; i < len(args); i++ {
		flag := args[i]
		switch flag {
		case "--expect", "--max-age", "--notify-slack", "--state":
			i++
			if i >= len(args) {
				return usage(errOut, "argument "+flag+": expected one argument")
			}
		default:
			return usage(errOut, "unrecognized arguments: "+flag)
		}
		switch flag {
		case "--expect":
			expected = splitList(args[i])
		case "--max-age":
			d, err := time.ParseDuration(args[i])
			if err != nil || d <= 0 {
				return usage(errOut, "argument --max-age: invalid duration: '"+args[i]+"'")
			}
			maxAge = d
		case "--notify-slack":
			channel = args[i]
		case "--state":
			statePath = args[i]
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	now := time.Now()
	rows, readErr := remote.Heartbeats(ctx)
	if readErr != nil && channel == "" {
		return fail(errOut, readErr)
	}
	var machines []health.Machine
	if readErr == nil {
		machines = health.Build(rows, expected, now, maxAge)
		if err := health.Write(out, machines, now); err != nil {
			return fail(errOut, err)
		}
	}
	if channel == "" {
		for _, m := range machines {
			if m.Bad() {
				return 1
			}
		}
		return 0
	}
	prev, err := health.LoadState(statePath)
	if err != nil {
		return fail(errOut, err)
	}
	var changes []health.Change
	var next map[string]string
	if readErr != nil {
		fmt.Fprintln(errOut, readErr)
		changes, next = health.Unreachable(prev, readErr.Error())
	} else {
		changes, next = health.Transitions(prev, machines)
	}
	if len(changes) > 0 {
		project := envOr("SESSIONS_SLACK_DOPPLER_PROJECT", "claudito")
		cfg := envOr("SESSIONS_SLACK_DOPPLER_CONFIG", "prd")
		token, err := remote.DopplerSecret(ctx, "SLACK_BOT_TOKEN", project, cfg)
		if err != nil {
			return fail(errOut, err)
		}
		// State is saved only after Slack accepts the message, so a failed post
		// is retried on the next run instead of being lost.
		if err := health.PostSlack(ctx, token, channel, health.Message(changes, now)); err != nil {
			return fail(errOut, err)
		}
		fmt.Fprintf(errOut, "posted %d change(s) to Slack\n", len(changes))
	}
	if err := health.SaveState(statePath, next); err != nil {
		return fail(errOut, err)
	}
	if readErr != nil {
		return 1
	}
	return 0
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
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
