package source

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/usuario/sessions/internal/config"
	"github.com/usuario/sessions/internal/model"
)

// Sources for conversations imported from chat app exports. Provider is the
// same as source: the app both hosted and ran the conversation.
const (
	SourceChatGPT  = "chatgpt"
	SourceClaudeAI = "claude-ai"
	SourceGrok     = "grok"
)

// ImportSources lists the sources stored under config.ImportsRoot.
func ImportSources() []string { return []string{SourceChatGPT, SourceClaudeAI, SourceGrok} }

func isImportSource(source string) bool {
	for _, s := range ImportSources() {
		if source == s {
			return true
		}
	}
	return false
}

// Imported is one normalized conversation as written to disk. The text is the
// raw export text: redaction happens when the index reads it, like every
// other source, never here.
type Imported struct {
	Source    string       `json:"source"`
	ID        string       `json:"id"`
	Title     string       `json:"title,omitempty"`
	StartedAt string       `json:"started_at,omitempty"`
	UpdatedAt string       `json:"updated_at,omitempty"`
	Turns     []model.Turn `json:"turns"`
}

// Imports indexes the files under config.ImportsRoot.
type Imports struct{}

func (Imports) Discover() ([]Candidate, error) {
	root := config.ImportsRoot()
	var out []Candidate
	for _, source := range ImportSources() {
		dir := filepath.Join(root, source)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("import discovery incomplete: %w", err)
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, fmt.Errorf("import discovery incomplete: %w", err)
			}
			path := filepath.Join(dir, entry.Name())
			mtime := float64(info.ModTime().Unix()) + float64(info.ModTime().Nanosecond())/1e9
			out = append(out, Candidate{
				Locator:    path,
				Source:     source,
				OriginPath: path,
				RecordID:   strings.TrimSuffix(entry.Name(), ".json"),
				Revision:   FileRevision(mtime, info.Size()),
				MTime:      mtime,
				Size:       info.Size(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Locator < out[j].Locator })
	return out, nil
}

// ErrSkipped marks a session the index leaves out on purpose. It is not a
// failure: retrying will not fix it, so it must not block deletes or fail
// every push.
var ErrSkipped = errors.New("skipped")

// SkipError says which file was set aside and why.
type SkipError struct{ Path, Reason string }

func (e *SkipError) Error() string        { return e.Path + ": " + e.Reason }
func (e *SkipError) Is(target error) bool { return target == ErrSkipped }

// quarantine moves an unusable import out of the way as <name>.bad, so later
// runs do not trip on it. The file is kept for a person to look at.
func quarantine(path, why string) error {
	if err := os.Rename(path, path+".bad"); err != nil {
		return &SkipError{Path: path, Reason: why + " (could not move it aside)"}
	}
	return &SkipError{Path: path, Reason: why + ", moved to " + filepath.Base(path) + ".bad"}
}

func (Imports) Load(c Candidate) (*model.Session, error) {
	if !isImportSource(c.Source) {
		return nil, fmt.Errorf("unknown source: %s", c.Source)
	}
	info, err := os.Stat(c.OriginPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &SkipError{Path: c.OriginPath, Reason: "removed while indexing"}
	}
	if err != nil {
		return nil, quarantine(c.OriginPath, "unreadable")
	}
	raw, err := os.ReadFile(c.OriginPath)
	if err != nil {
		return nil, quarantine(c.OriginPath, "unreadable")
	}
	var conv Imported
	if err := json.Unmarshal(raw, &conv); err != nil {
		return nil, quarantine(c.OriginPath, "not a stored conversation")
	}
	if conv.Source != c.Source || strings.TrimSpace(conv.ID) == "" {
		return nil, quarantine(c.OriginPath, "does not match its folder")
	}
	mtime := float64(info.ModTime().Unix()) + float64(info.ModTime().Nanosecond())/1e9
	s := &model.Session{
		ID:         conv.ID,
		Source:     conv.Source,
		Provider:   conv.Source,
		Path:       c.Locator,
		OriginPath: c.OriginPath,
		RecordID:   conv.ID,
		Title:      strings.TrimSpace(conv.Title),
		StartedAt:  stringPtr(conv.StartedAt),
		UpdatedAt:  stringPtr(conv.UpdatedAt),
		MTime:      mtime,
		Size:       info.Size(),
		Revision:   FileRevision(mtime, info.Size()),
	}
	for _, t := range conv.Turns {
		text := strings.TrimSpace(t.Text)
		if text == "" || (t.Role != "user" && t.Role != "assistant") {
			continue
		}
		s.Turns = append(s.Turns, model.Turn{Role: t.Role, Text: text})
	}
	if s.Title == "" {
		s.Title = firstTitle(s.Turns)
	}
	if s.UpdatedAt == nil {
		s.UpdatedAt = s.StartedAt
	}
	if s.StartedAt == nil {
		s.StartedAt = s.UpdatedAt
	}
	var body []string
	for _, t := range s.Turns {
		body = append(body, t.Role+": "+t.Text)
	}
	s.Body = strings.Join(body, "\n\n")
	return s, nil
}

var safeFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// ImportedPath is where a conversation is stored. Ids come from an untrusted
// export, so one that is not a plain file name is stored under its hash.
func ImportedPath(root string, conv Imported) string {
	name := conv.ID
	if !safeFileName.MatchString(name) || strings.Contains(name, "..") {
		sum := sha256.Sum256([]byte(conv.ID))
		name = "sha256-" + hex.EncodeToString(sum[:])
	}
	return filepath.Join(root, conv.Source, name+".json")
}

// WriteImported stores one conversation under root, replacing an older copy
// atomically. It reports false, and leaves the file alone, when the stored
// copy is already identical, so a re-import does not re-upload anything.
func WriteImported(root string, conv Imported) (bool, error) {
	if !isImportSource(conv.Source) {
		return false, fmt.Errorf("unknown source: %s", conv.Source)
	}
	if strings.TrimSpace(conv.ID) == "" {
		return false, errors.New("conversation has no id")
	}
	if conv.Turns == nil {
		conv.Turns = []model.Turn{}
	}
	raw, err := json.MarshalIndent(conv, "", " ")
	if err != nil {
		return false, err
	}
	raw = append(raw, '\n')
	path := ImportedPath(root, conv)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, raw) {
		return false, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".import-*.tmp")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}
