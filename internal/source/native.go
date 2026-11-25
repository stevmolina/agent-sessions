package source

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/usuario/sessions/internal/config"
	"github.com/usuario/sessions/internal/digest"
	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/pbwire"
)

// Sources for chats that agent apps keep on this computer. Grok CLI chats use
// grok-cli so they never collide with Grok conversations imported from an
// export (source grok); their provider is grok.
const (
	SourceAntigravity = "antigravity"
	SourceGrokCLI     = "grok-cli"
	SourceMuse        = "muse"
)

func fileMeta(path string, extra ...string) (mtime float64, size int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	mtime = float64(info.ModTime().Unix()) + float64(info.ModTime().Nanosecond())/1e9
	size = info.Size()
	for _, e := range extra {
		if i, err := os.Stat(e); err == nil {
			size += i.Size()
			if m := float64(i.ModTime().Unix()) + float64(i.ModTime().Nanosecond())/1e9; m > mtime {
				mtime = m
			}
		}
	}
	return mtime, size, nil
}

// finish fills the fields every native session shares, builds the body, and
// sets a clean title from the turns.
func finish(s *model.Session, c Candidate, mtime float64, size int64, fallbackTitle string) {
	s.Provider = firstNonEmpty(s.Provider, s.Source)
	s.OriginPath = c.OriginPath
	s.RecordID = s.ID
	s.Path = c.Locator
	s.MTime, s.Size = mtime, size
	s.Revision = FileRevision(mtime, size)
	fallback := time.Unix(int64(mtime), 0).UTC().Format("2006-01-02T15:04:05Z")
	if s.UpdatedAt == nil {
		s.UpdatedAt = &fallback
	}
	if s.StartedAt == nil {
		s.StartedAt = s.UpdatedAt
	}
	s.Title = digest.Title(s.Turns, fallbackTitle)
	var body []string
	for _, t := range s.Turns {
		body = append(body, t.Role+": "+t.Text)
	}
	s.Body = strings.Join(body, "\n\n")
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func addTurn(s *model.Session, role, text string) {
	text = strings.TrimSpace(text)
	if text != "" {
		s.Turns = append(s.Turns, model.Turn{Role: role, Text: text})
	}
}

func isoTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := t.UTC().Format("2006-01-02T15:04:05Z")
	return &v
}

// ---------------------------------------------------------------- Grok CLI

// GrokCLI reads Grok CLI sessions under config.GrokRoot. Most folders there are
// background jobs with no typed prompt; only sessions with a prompt are indexed.
type GrokCLI struct{}

func (GrokCLI) Discover() ([]Candidate, error) {
	root := config.GrokRoot()
	projects, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("grok discovery incomplete: %w", err)
	}
	var out []Candidate
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		cwd, _ := url.PathUnescape(project.Name())
		if grokInternal(cwd) {
			continue
		}
		sessions, err := os.ReadDir(filepath.Join(root, project.Name()))
		if err != nil {
			return nil, fmt.Errorf("grok discovery incomplete: %w", err)
		}
		for _, sess := range sessions {
			if !sess.IsDir() {
				continue
			}
			file := filepath.Join(root, project.Name(), sess.Name(), "chat_history.jsonl")
			mtime, size, err := fileMeta(file)
			if err != nil {
				continue
			}
			if !hasPrompt(file) {
				continue
			}
			out = append(out, Candidate{
				Locator: file, Source: SourceGrokCLI, OriginPath: file, RecordID: sess.Name(),
				Revision: FileRevision(mtime, size), MTime: mtime, Size: size,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Locator < out[j].Locator })
	return out, nil
}

// grokInternal is a folder Grok uses for its own memory jobs.
func grokInternal(cwd string) bool { return strings.Contains(cwd, "dusk-acp-memory") }

// hasPrompt reports whether a chat file holds a typed prompt. It stops at the first one.
func hasPrompt(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"prompt_index"`)) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

func (GrokCLI) Load(c Candidate) (*model.Session, error) {
	if c.Source != SourceGrokCLI {
		return nil, fmt.Errorf("unknown source: %s", c.Source)
	}
	mtime, size, err := fileMeta(c.OriginPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &SkipError{Path: c.OriginPath, Reason: "removed while indexing"}
	}
	if err != nil {
		return nil, err
	}
	f, err := os.Open(c.OriginPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dir := filepath.Dir(c.OriginPath)
	s := &model.Session{ID: filepath.Base(dir), Source: SourceGrokCLI, Provider: "grok"}
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, rerr := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var o struct {
				Type        string          `json:"type"`
				Content     json.RawMessage `json:"content"`
				PromptIndex *int            `json:"prompt_index"`
			}
			if json.Unmarshal(line, &o) == nil {
				switch o.Type {
				case "user":
					if o.PromptIndex != nil {
						addTurn(s, "user", unwrapQuery(contentText(o.Content)))
					}
				case "assistant":
					addTurn(s, "assistant", contentText(o.Content))
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	var sum struct {
		Info struct {
			CWD string `json:"cwd"`
		} `json:"info"`
		CreatedAt      string `json:"created_at"`
		UpdatedAt      string `json:"updated_at"`
		GeneratedTitle string `json:"generated_title"`
		SessionSummary string `json:"session_summary"`
		Model          string `json:"current_model_id"`
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "summary.json")); err == nil {
		_ = json.Unmarshal(raw, &sum)
	}
	if sum.Info.CWD == "" {
		project := filepath.Base(filepath.Dir(dir))
		sum.Info.CWD, _ = url.PathUnescape(project)
	}
	s.CWD = ptr(sum.Info.CWD)
	s.StartedAt = ptr(sum.CreatedAt)
	s.UpdatedAt = ptr(sum.UpdatedAt)
	s.Model = ptr(sum.Model)
	finish(s, c, mtime, size, firstNonEmpty(sum.GeneratedTitle, sum.SessionSummary))
	return s, nil
}

// contentText reads message content that is a string or a list of text blocks.
func contentText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Text != "" && (b.Type == "text" || b.Type == "") {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

var userQuery = regexp.MustCompile(`(?s)<user_query>\s*(.*?)\s*</user_query>`)

// unwrapQuery returns what is inside Grok's <user_query> wrapper, or the text as is.
func unwrapQuery(text string) string {
	if m := userQuery.FindStringSubmatch(text); len(m) > 1 {
		return m[1]
	}
	return text
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ------------------------------------------------------------- Antigravity

// Antigravity reads Antigravity and Muse-over-Antigravity conversations: every
// <root>/antigravity*/conversations/<id>.db under config.AntigravityRoot. Rows
// in the steps table hold protobuf; step type 14 is a person's message and 15
// is the model's reply.
type Antigravity struct{}

const (
	agStepUser      = 14
	agStepAssistant = 15
)

func (Antigravity) Discover() ([]Candidate, error) {
	root := config.AntigravityRoot()
	homes, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("antigravity discovery incomplete: %w", err)
	}
	var out []Candidate
	for _, home := range homes {
		if !home.IsDir() || !strings.HasPrefix(home.Name(), "antigravity") {
			continue
		}
		dir := filepath.Join(root, home.Name(), "conversations")
		files, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("antigravity discovery incomplete: %w", err)
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".db" {
				continue
			}
			path := filepath.Join(dir, f.Name())
			mtime, size, err := fileMeta(path, path+"-wal")
			if err != nil || size < 4096 {
				continue
			}
			out = append(out, Candidate{
				Locator: path, Source: SourceAntigravity, OriginPath: path,
				RecordID: strings.TrimSuffix(f.Name(), ".db"),
				Revision: FileRevision(mtime, size), MTime: mtime, Size: size,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Locator < out[j].Locator })
	return out, nil
}

func (Antigravity) Load(c Candidate) (*model.Session, error) {
	if c.Source != SourceAntigravity {
		return nil, fmt.Errorf("unknown source: %s", c.Source)
	}
	mtime, size, err := fileMeta(c.OriginPath, c.OriginPath+"-wal")
	if errors.Is(err, os.ErrNotExist) {
		return nil, &SkipError{Path: c.OriginPath, Reason: "removed while indexing"}
	}
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: c.OriginPath}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT step_type, step_payload FROM steps WHERE step_type IN (?, ?) ORDER BY idx`, agStepUser, agStepAssistant)
	if err != nil {
		// A database with no steps table is an empty conversation, not a failure.
		return nil, &SkipError{Path: c.OriginPath, Reason: "no readable conversation", Quiet: true}
	}
	defer rows.Close()
	s := &model.Session{ID: strings.TrimSuffix(filepath.Base(c.OriginPath), ".db"), Source: SourceAntigravity, Provider: "antigravity"}
	var first, last time.Time
	for rows.Next() {
		var kind int
		var payload []byte
		if err := rows.Scan(&kind, &payload); err != nil {
			return nil, err
		}
		if secs, ok := pbwire.Int(payload, 5, 1, 1); ok && secs > 0 {
			t := time.Unix(int64(secs), 0)
			if first.IsZero() {
				first = t
			}
			last = t
		}
		switch kind {
		case agStepUser:
			addTurn(s, "user", firstNonEmpty(pbwire.Text(payload, 19, 2), pbwire.Text(payload, 19, 3, 1)))
		case agStepAssistant:
			addTurn(s, "assistant", firstNonEmpty(pbwire.Text(payload, 20, 1), pbwire.Text(payload, 20, 8)))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(s.Turns) == 0 {
		return nil, &SkipError{Path: c.OriginPath, Reason: "no messages", Quiet: true}
	}
	var meta []byte
	if err := db.QueryRow(`SELECT data FROM trajectory_metadata_blob LIMIT 1`).Scan(&meta); err == nil {
		uri := pbwire.FindText(meta, func(v string) bool { return strings.HasPrefix(v, "file://") }, 3)
		if p, err := url.Parse(uri); err == nil && p.Path != "" {
			s.CWD = ptr(p.Path)
		}
	}
	s.StartedAt, s.UpdatedAt = isoTime(first), isoTime(last)
	finish(s, c, mtime, size, "")
	return s, nil
}

// -------------------------------------------------------------------- Muse

// Muse reads Muse sessions: YYYY/MM/DD/<id>/session.jsonl under config.MuseRoot.
// The file is an event log. A person's message is a runtime.user_intent.accepted
// event; the model's text is a run assistant_message_committed event.
type Muse struct{}

func (Muse) Discover() ([]Candidate, error) {
	root := config.MuseRoot()
	var out []Candidate
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if path == root && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			// Muse keeps views, locks and tool output beside the log: skip them.
			if path != root && (strings.HasPrefix(name, ".") || name == "tool-outputs" || name == "input-assets") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "session.jsonl" {
			return nil
		}
		mtime, size, err := fileMeta(path)
		if err != nil {
			return err
		}
		out = append(out, Candidate{
			Locator: path, Source: SourceMuse, OriginPath: path,
			RecordID: filepath.Base(filepath.Dir(path)),
			Revision: FileRevision(mtime, size), MTime: mtime, Size: size,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("muse discovery incomplete: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Locator < out[j].Locator })
	return out, nil
}

type museRecord struct {
	RecordedAt  int64           `json:"recorded_at"`
	PayloadType string          `json:"payload_type"`
	Payload     json.RawMessage `json:"payload"`
}

func (Muse) Load(c Candidate) (*model.Session, error) {
	if c.Source != SourceMuse {
		return nil, fmt.Errorf("unknown source: %s", c.Source)
	}
	mtime, size, err := fileMeta(c.OriginPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &SkipError{Path: c.OriginPath, Reason: "removed while indexing"}
	}
	if err != nil {
		return nil, err
	}
	f, err := os.Open(c.OriginPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &model.Session{ID: filepath.Base(filepath.Dir(c.OriginPath)), Source: SourceMuse, Provider: "muse"}
	var first, last time.Time
	var name string
	r := bufio.NewReaderSize(f, 1024*1024)
	handle := func(rec museRecord) {
		if rec.RecordedAt > 0 {
			t := time.UnixMicro(rec.RecordedAt)
			if first.IsZero() {
				first = t
			}
			last = t
		}
		switch rec.PayloadType {
		case "runtime.user_intent.accepted":
			var p struct {
				Surface string `json:"surface"`
				Blocks  []struct {
					Kind string `json:"kind"`
					Text string `json:"text"`
				} `json:"refill_blocks"`
			}
			if json.Unmarshal(rec.Payload, &p) != nil || (p.Surface != "" && p.Surface != "main") {
				return
			}
			var parts []string
			for _, b := range p.Blocks {
				if b.Kind == "text" && b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			addTurn(s, "user", strings.Join(parts, "\n"))
		case "runtime.session":
			var p struct {
				Kind  string `json:"kind"`
				Event struct {
					Kind  string `json:"kind"`
					Text  string `json:"text"`
					Phase string `json:"phase"`
				} `json:"event"`
				Payload struct {
					Meta struct {
						Root string `json:"workspace_root"`
					} `json:"session_metadata"`
				} `json:"payload"`
			}
			if json.Unmarshal(rec.Payload, &p) != nil {
				return
			}
			if p.Kind == "run" && p.Event.Kind == "assistant_message_committed" {
				addTurn(s, "assistant", p.Event.Text)
			}
			if p.Kind == "session_start_intake_v4" && s.CWD == nil {
				s.CWD = ptr(p.Payload.Meta.Root)
			}
		case "session.name.changed":
			var p struct {
				New string `json:"new_name"`
			}
			if json.Unmarshal(rec.Payload, &p) == nil {
				name = p.New
			}
		}
	}
	for {
		line, rerr := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var frame struct {
				museRecord
				Children []struct {
					RecordJSON string `json:"record_json"`
				} `json:"children"`
			}
			if json.Unmarshal(line, &frame) == nil {
				if frame.Children != nil {
					for _, ch := range frame.Children {
						var rec museRecord
						if json.Unmarshal([]byte(ch.RecordJSON), &rec) == nil {
							handle(rec)
						}
					}
				} else {
					handle(frame.museRecord)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	if len(s.Turns) == 0 {
		return nil, &SkipError{Path: c.OriginPath, Reason: "no messages", Quiet: true}
	}
	s.StartedAt, s.UpdatedAt = isoTime(first), isoTime(last)
	finish(s, c, mtime, size, name)
	return s, nil
}
