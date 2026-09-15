package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/usuario/sessions/internal/config"
	"github.com/usuario/sessions/internal/model"
)

var userQuery = regexp.MustCompile(`(?s)<user_query>\s*(.*?)\s*</user_query>`)
var timestamp = regexp.MustCompile(`(?s)<timestamp>\s*(.*?)\s*</timestamp>`)
var command = regexp.MustCompile(`<command-name>|<command-message>|<command-args>`)
var browserContext = regexp.MustCompile(`(?s)<in-app-browser-context\b.*?</in-app-browser-context>`)
var skillDump = regexp.MustCompile(`(?s)<skill\b.*?</skill>`)

func ParseFile(path, source string) (*model.Session, error) {
	for attempt := 0; attempt < 2; attempt++ {
		before, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		s, parseErr := Parse(file, source, path)
		closeErr := file.Close()
		if parseErr != nil {
			return nil, parseErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		after, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if before.ModTime() == after.ModTime() && before.Size() == after.Size() {
			s.Path = path
			// Keep the operations separate so IEEE-754 rounding matches indexes
			// created by earlier releases.
			s.MTime = float64(after.ModTime().Unix()) + float64(after.ModTime().Nanosecond())/1e9
			s.Size = after.Size()
			fallback := after.ModTime().UTC().Format("2006-01-02T15:04:05Z")
			if s.UpdatedAt == nil {
				s.UpdatedAt = &fallback
			}
			if s.StartedAt == nil {
				s.StartedAt = s.UpdatedAt
			}
			return s, nil
		}
	}
	return nil, fmt.Errorf("file changed while parsing: %s", path)
}

func Parse(r io.Reader, source, path string) (*model.Session, error) {
	s := &model.Session{Source: source, Path: path, ID: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var obj map[string]any
			if json.Unmarshal(line, &obj) == nil {
				parseObject(s, obj, path)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if source != "cursor" && source != "claude" && source != "codex" {
		return nil, fmt.Errorf("unknown source: %s", source)
	}
	if source == "cursor" {
		s.CWD = stringPtr(cursorCWD(path))
		s.ParentID = stringPtr(parentDir(path))
	}
	if source == "claude" && strings.Contains(filepath.ToSlash(path), "/subagents/") {
		if s.ParentID == nil {
			s.ParentID = stringPtr(parentDir(path))
		}
	}
	if s.ParentID != nil && *s.ParentID == s.ID {
		s.ParentID = nil
	}
	s.Title = firstTitle(s.Turns)
	var body []string
	for _, t := range s.Turns {
		body = append(body, t.Role+": "+t.Text)
	}
	s.Body = strings.Join(body, "\n\n")
	return s, nil
}

func parseObject(s *model.Session, obj map[string]any, path string) {
	typeName, _ := obj["type"].(string)
	payload, _ := obj["payload"].(map[string]any)
	switch s.Source {
	case "cursor":
		role, _ := obj["role"].(string)
		msg, _ := obj["message"].(map[string]any)
		addTurn(s, role, blockText(msg["content"]), true)
	case "claude":
		if skippedClaude(typeName) {
			return
		}
		if s.CWD == nil {
			s.CWD = mapString(obj, "cwd")
		}
		if sessionID := mapString(obj, "sessionId"); sessionID != nil {
			if strings.Contains(filepath.ToSlash(path), "/subagents/") {
				if s.ParentID == nil {
					s.ParentID = sessionID
				}
			} else if s.ID == strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) {
				s.ID = *sessionID
			}
		}
		if agent := mapString(obj, "agentId"); agent != nil && strings.Contains(filepath.ToSlash(path), "/subagents/") {
			s.ID = *agent
		}
		if ts := mapString(obj, "timestamp"); ts != nil {
			if s.StartedAt == nil {
				s.StartedAt = ts
			}
			s.UpdatedAt = ts
		}
		if meta, _ := obj["isMeta"].(bool); meta {
			return
		}
		msg, _ := obj["message"].(map[string]any)
		role := typeName
		if role != "user" && role != "assistant" {
			role, _ = msg["role"].(string)
		}
		text := blockText(msg["content"])
		if role == "user" && (strings.Contains(text, "<local-command-caveat>") || command.MatchString(text) || strings.HasPrefix(text, "<system-reminder>")) {
			return
		}
		addTurn(s, role, text, false)
	case "codex":
		if ts, ok := obj["timestamp"].(string); ok {
			s.UpdatedAt = &ts
		}
		if typeName == "session_meta" {
			if v := mapString(payload, "id"); v != nil {
				s.ID = *v
			} else if v := mapString(payload, "session_id"); v != nil {
				s.ID = *v
			}
			if v := mapString(payload, "cwd"); v != nil {
				s.CWD = v
			}
			if v := mapString(payload, "timestamp"); v != nil {
				s.StartedAt = v
			}
			if v := mapString(payload, "forked_from_id"); v != nil {
				s.ParentID = v
			} else if v := mapString(payload, "parent_thread_id"); v != nil {
				s.ParentID = v
			}
			return
		}
		if typeName != "response_item" {
			return
		}
		if kind, _ := payload["type"].(string); kind != "message" {
			return
		}
		role, _ := payload["role"].(string)
		text := blockText(payload["content"])
		if role == "user" {
			text = browserContext.ReplaceAllString(text, "")
			text = skillDump.ReplaceAllString(text, "")
			if _, after, ok := strings.Cut(text, "## My request:"); ok {
				text = after
			}
		}
		addTurn(s, role, text, false)
	}
}
func skippedClaude(t string) bool {
	switch t {
	case "mode", "file-history-snapshot", "attachment", "system", "progress", "queue-operation":
		return true
	}
	return false
}
func addTurn(s *model.Session, role, text string, cursor bool) {
	if role != "user" && role != "assistant" {
		return
	}
	if cursor && role == "user" {
		if m := userQuery.FindStringSubmatch(text); len(m) > 0 {
			text = m[1]
		} else {
			text = timestamp.ReplaceAllString(text, "")
		}
	}
	text = strings.TrimSpace(text)
	if text != "" {
		s.Turns = append(s.Turns, model.Turn{Role: role, Text: text})
	}
}
func blockText(v any) string {
	if text, ok := v.(string); ok {
		return strings.TrimSpace(html.UnescapeString(text))
	}
	blocks, ok := v.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, raw := range blocks {
		if text, ok := raw.(string); ok {
			parts = append(parts, text)
			continue
		}
		b, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := b["type"].(string)
		switch kind {
		case "tool_use", "tool_result", "thinking", "server_tool_use", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output":
			continue
		}
		for _, key := range []string{"text", "input_text", "output_text"} {
			if text, ok := b[key].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
				break
			}
		}
	}
	return strings.TrimSpace(html.UnescapeString(strings.Join(parts, "\n")))
}
func mapString(m map[string]any, key string) *string {
	if v, ok := m[key].(string); ok && v != "" {
		return &v
	}
	return nil
}
func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func firstTitle(turns []model.Turn) string {
	for _, t := range turns {
		if t.Role == "user" && strings.TrimSpace(t.Text) != "" {
			line := strings.Join(strings.Fields(t.Text), " ")
			if utf8.RuneCountInString(line) > 160 {
				r := []rune(line)
				line = strings.TrimSpace(string(r[:159])) + "…"
			}
			return line
		}
	}
	return ""
}
func parentDir(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "subagents" && i > 0 {
			return parts[i-1]
		}
	}
	return ""
}
func cursorCWD(path string) string {
	rootAbs, _ := filepath.Abs(config.CursorRoot())
	pathAbs, _ := filepath.Abs(path)
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	slug := strings.Split(filepath.ToSlash(rel), "/")[0]
	tokens := strings.Split(slug, "-")
	current := string(filepath.Separator)
	for i := 0; i < len(tokens); {
		found := -1
		for j := len(tokens); j > i; j-- {
			candidate := filepath.Join(current, strings.Join(tokens[i:j], "-"))
			if st, e := os.Stat(candidate); e == nil && st.IsDir() {
				found = j
				current = candidate
				break
			}
		}
		if found < 0 {
			return filepath.Join(current, strings.Join(tokens[i:], "-"))
		}
		i = found
	}
	return current
}

func Format(s *model.Session, limit int) string {
	lines := []string{fmt.Sprintf("# id=%s source=%s cwd=%s", s.ID, s.Source, value(s.CWD)), fmt.Sprintf("# started=%s updated=%s", value(s.StartedAt), value(s.UpdatedAt)), "# path=" + s.Path}
	if s.ParentID != nil {
		lines = append(lines, "# parent_id="+*s.ParentID)
	}
	out := strings.Join(lines, "\n") + "\n\n"
	used := 0
	truncated := false
	var blocks []string
	for _, t := range s.Turns {
		block := t.Role + ":\n" + t.Text + "\n"
		n := utf8.RuneCountInString(block)
		if used+n > limit {
			truncated = true
			remain := limit - used
			if remain > 32 {
				r := []rune(block)
				blocks = append(blocks, strings.TrimRight(string(r[:remain]), " \n\t")+"\n")
			}
			break
		}
		blocks = append(blocks, block)
		used += n
	}
	out += strings.Join(blocks, "\n")
	if truncated {
		out += "\n[truncated; read " + s.Path + "]\n"
	}
	return strings.TrimRight(out, "\n") + "\n"
}
func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

var _ = time.RFC3339
