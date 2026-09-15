package source

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/usuario/sessions/internal/config"
	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/transcript"
)

type JSONL struct{}

func (JSONL) Discover() ([]Candidate, error) {
	var out []Candidate
	walk := func(root, source string, accept func(string) bool) {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if source == "claude" && path != root && (entry.Name() == "memory" || entry.Name() == "debug" || entry.Name() == "tool-results") {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".jsonl" || !accept(path) {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				info, err = os.Stat(path)
				if err != nil {
					return nil
				}
			}
			mtime := float64(info.ModTime().Unix()) + float64(info.ModTime().Nanosecond())/1e9
			out = append(out, Candidate{
				Locator:    path,
				Source:     source,
				OriginPath: path,
				RecordID:   strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
				Revision:   FileRevision(mtime, info.Size()),
				MTime:      mtime,
				Size:       info.Size(),
			})
			return nil
		})
	}
	walk(config.CursorRoot(), "cursor", func(path string) bool { return strings.Contains(filepath.ToSlash(path), "/agent-transcripts/") })
	walk(config.ClaudeRoot(), "claude", func(string) bool { return true })
	walk(config.CodexRoot(), "codex", func(string) bool { return true })
	walk(config.CodexArchiveRoot(), "codex", func(path string) bool { return filepath.Dir(path) == config.CodexArchiveRoot() })
	sort.Slice(out, func(i, j int) bool { return out[i].Locator < out[j].Locator })
	return out, nil
}

func (JSONL) Load(c Candidate) (*model.Session, error) {
	if c.Source != "cursor" && c.Source != "claude" && c.Source != "codex" {
		return nil, fmt.Errorf("unknown source: %s", c.Source)
	}
	s, err := transcript.ParseFile(c.OriginPath, c.Source)
	if err != nil {
		return nil, err
	}
	s.Provider = s.Source
	s.OriginPath = c.OriginPath
	s.RecordID = s.ID
	s.Path = c.Locator
	s.Revision = FileRevision(s.MTime, s.Size)
	return s, nil
}

func FileRevision(mtime float64, size int64) string {
	return fmt.Sprintf("%v:%d", mtime, size)
}
