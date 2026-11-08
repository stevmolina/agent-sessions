package importer

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/source"
)

// eachElement streams a JSON array, so a large export is never held whole.
func eachElement(dec *json.Decoder, fn func(json.RawMessage) error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return errors.New("expected a JSON array")
	}
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if err := fn(raw); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// parseConversations reads a conversations.json. ChatGPT and Claude.ai both
// use that name, so each item is told apart by its fields.
func parseConversations(r io.Reader, c *collector) error {
	return eachElement(json.NewDecoder(r), func(raw json.RawMessage) error {
		var probe map[string]json.RawMessage
		if json.Unmarshal(raw, &probe) != nil {
			c.skip("unknown")
			return nil
		}
		switch {
		case probe["mapping"] != nil:
			return addParsed(c, source.SourceChatGPT, raw, parseChatGPT)
		case probe["chat_messages"] != nil || probe["uuid"] != nil:
			return addParsed(c, source.SourceClaudeAI, raw, parseClaudeAI)
		}
		c.skip("unknown")
		return nil
	})
}

// parseGrok reads prod-grok-backend.json: an object whose "conversations"
// array holds {conversation, responses} items. Other keys are skipped.
func parseGrok(r io.Reader, c *collector) error {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("expected a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		if key, _ := keyTok.(string); key == "conversations" {
			if err := eachElement(dec, func(raw json.RawMessage) error {
				return addParsed(c, source.SourceGrok, raw, parseGrokItem)
			}); err != nil {
				return err
			}
			continue
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

func addParsed(c *collector, src string, raw json.RawMessage, parse func(json.RawMessage) (source.Imported, bool)) error {
	conv, ok := parse(raw)
	if !ok || strings.TrimSpace(conv.ID) == "" || len(conv.Turns) == 0 {
		c.skip(src)
		return nil
	}
	conv.Source = src
	return c.emit(conv)
}

func addTurn(turns []model.Turn, role, text string) []model.Turn {
	text = strings.TrimSpace(text)
	if text == "" {
		return turns
	}
	return append(turns, model.Turn{Role: role, Text: text})
}

// ChatGPT

type gptConversation struct {
	ID             string             `json:"id"`
	ConversationID string             `json:"conversation_id"`
	Title          string             `json:"title"`
	CreateTime     any                `json:"create_time"`
	UpdateTime     any                `json:"update_time"`
	CurrentNode    string             `json:"current_node"`
	Mapping        map[string]gptNode `json:"mapping"`
}

type gptNode struct {
	Parent   string      `json:"parent"`
	Children []string    `json:"children"`
	Message  *gptMessage `json:"message"`
}

type gptMessage struct {
	Author struct {
		Role string `json:"role"`
	} `json:"author"`
	Content struct {
		ContentType string `json:"content_type"`
		Parts       []any  `json:"parts"`
	} `json:"content"`
	Recipient  string         `json:"recipient"`
	CreateTime any            `json:"create_time"`
	Metadata   map[string]any `json:"metadata"`
}

// parseChatGPT follows the branch the user last saw: from current_node up to
// the root. Edits and regenerations leave sibling branches that are dropped.
func parseChatGPT(raw json.RawMessage) (source.Imported, bool) {
	var c gptConversation
	if json.Unmarshal(raw, &c) != nil {
		return source.Imported{}, false
	}
	conv := source.Imported{
		ID:        firstNonEmpty(c.ConversationID, c.ID),
		Title:     strings.TrimSpace(c.Title),
		StartedAt: flexTime(c.CreateTime),
		UpdatedAt: flexTime(c.UpdateTime),
	}
	leaf := c.CurrentNode
	if _, ok := c.Mapping[leaf]; !ok {
		leaf = latestGPTLeaf(c.Mapping)
	}
	var path []gptNode
	seen := map[string]bool{}
	for id := leaf; id != "" && !seen[id]; {
		seen[id] = true
		node, ok := c.Mapping[id]
		if !ok {
			break
		}
		path = append(path, node)
		id = node.Parent
	}
	for i := len(path) - 1; i >= 0; i-- {
		m := path[i].Message
		if m == nil || !visibleGPT(m) {
			continue
		}
		var parts []string
		for _, part := range m.Content.Parts {
			if text, ok := part.(string); ok {
				parts = append(parts, text)
			}
		}
		conv.Turns = addTurn(conv.Turns, m.Author.Role, strings.Join(parts, "\n"))
	}
	return conv, true
}

func visibleGPT(m *gptMessage) bool {
	role := m.Author.Role
	if role != "user" && role != "assistant" {
		return false
	}
	if hidden, _ := m.Metadata["is_visually_hidden_from_conversation"].(bool); hidden {
		return false
	}
	// An assistant message addressed to a tool (python, browser, ...) is a call,
	// not a reply.
	if role == "assistant" && m.Recipient != "" && m.Recipient != "all" {
		return false
	}
	switch m.Content.ContentType {
	case "text", "multimodal_text":
		return true
	}
	return false
}

// latestGPTLeaf picks the newest leaf when current_node is missing.
func latestGPTLeaf(mapping map[string]gptNode) string {
	best, bestTime := "", math.Inf(-1)
	ids := make([]string, 0, len(mapping))
	for id := range mapping {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		node := mapping[id]
		if len(node.Children) > 0 {
			continue
		}
		t := math.Inf(-1)
		if node.Message != nil {
			if v, ok := epoch(node.Message.CreateTime); ok {
				t = v
			}
		}
		if best == "" || t >= bestTime {
			best, bestTime = id, t
		}
	}
	return best
}

// Claude.ai

type claudeConversation struct {
	UUID         string          `json:"uuid"`
	Name         string          `json:"name"`
	CreatedAt    any             `json:"created_at"`
	UpdatedAt    any             `json:"updated_at"`
	ChatMessages []claudeMessage `json:"chat_messages"`
}

type claudeMessage struct {
	Sender  string          `json:"sender"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
}

func parseClaudeAI(raw json.RawMessage) (source.Imported, bool) {
	var c claudeConversation
	if json.Unmarshal(raw, &c) != nil {
		return source.Imported{}, false
	}
	conv := source.Imported{
		ID:        c.UUID,
		Title:     strings.TrimSpace(c.Name),
		StartedAt: flexTime(c.CreatedAt),
		UpdatedAt: flexTime(c.UpdatedAt),
	}
	for _, m := range c.ChatMessages {
		role := ""
		switch strings.ToLower(m.Sender) {
		case "human", "user":
			role = "user"
		case "assistant":
			role = "assistant"
		default:
			continue
		}
		conv.Turns = addTurn(conv.Turns, role, claudeText(m))
	}
	return conv, true
}

// claudeText prefers the text blocks of content, which leave out tool calls
// and thinking, and falls back to the flat text field.
func claudeText(m claudeMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if len(m.Content) > 0 && json.Unmarshal(m.Content, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return m.Text
}

// Grok

type grokItem struct {
	Conversation struct {
		ID             any    `json:"id"`
		ConversationID any    `json:"conversation_id"`
		Title          string `json:"title"`
		CreateTime     any    `json:"create_time"`
		ModifyTime     any    `json:"modify_time"`
	} `json:"conversation"`
	Responses []json.RawMessage `json:"responses"`
}

type grokResponse struct {
	ID         any    `json:"_id"`
	ResponseID any    `json:"response_id"`
	Parent     any    `json:"parent_response_id"`
	Sender     string `json:"sender"`
	Message    string `json:"message"`
	CreateTime any    `json:"create_time"`
}

func parseGrokItem(raw json.RawMessage) (source.Imported, bool) {
	var item grokItem
	if json.Unmarshal(raw, &item) != nil {
		return source.Imported{}, false
	}
	conv := source.Imported{
		ID:        firstNonEmpty(flexID(item.Conversation.ID), flexID(item.Conversation.ConversationID)),
		Title:     strings.TrimSpace(item.Conversation.Title),
		StartedAt: flexTime(item.Conversation.CreateTime),
		UpdatedAt: flexTime(item.Conversation.ModifyTime),
	}
	type resp struct {
		id, parent, role, text string
		at                     float64
		order                  int
	}
	var all []resp
	for i, rawResp := range item.Responses {
		// Each item is {response, share_link}; older or hand-made files may
		// hold the response itself.
		var wrapper struct {
			Response json.RawMessage `json:"response"`
		}
		body := rawResp
		if json.Unmarshal(rawResp, &wrapper) == nil && len(wrapper.Response) > 0 && wrapper.Response[0] == '{' {
			body = wrapper.Response
		}
		var r grokResponse
		if json.Unmarshal(body, &r) != nil {
			continue
		}
		role := "assistant"
		if s := strings.ToLower(strings.TrimSpace(r.Sender)); s == "human" || s == "user" {
			role = "user"
		}
		at, ok := epoch(r.CreateTime)
		if !ok {
			at = math.Inf(-1)
		}
		all = append(all, resp{
			id:     firstNonEmpty(flexID(r.ID), flexID(r.ResponseID)),
			parent: flexID(r.Parent),
			role:   role, text: r.Message, at: at, order: i,
		})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at < all[j].at })
	// Responses can branch (edits, regenerations) through parent_response_id.
	// When they do, keep the newest leaf's path; otherwise keep them all in time order.
	byID := map[string]int{}
	hasChild := map[string]bool{}
	for i, r := range all {
		if r.id != "" {
			byID[r.id] = i
		}
	}
	linked := false
	for _, r := range all {
		if _, ok := byID[r.parent]; ok && r.parent != "" {
			linked = true
			hasChild[r.parent] = true
		}
	}
	chosen := all
	if linked {
		leaf := -1
		for i, r := range all {
			if r.id == "" || !hasChild[r.id] {
				leaf = i // all is in time order, so the last leaf is the newest
			}
		}
		var path []resp
		seen := map[int]bool{}
		for i := leaf; i >= 0 && !seen[i]; {
			seen[i] = true
			path = append(path, all[i])
			next, ok := byID[all[i].parent]
			if !ok || all[i].parent == "" {
				break
			}
			i = next
		}
		chosen = make([]resp, 0, len(path))
		for i := len(path) - 1; i >= 0; i-- {
			chosen = append(chosen, path[i])
		}
	}
	for _, r := range chosen {
		conv.Turns = addTurn(conv.Turns, r.role, r.text)
	}
	if conv.StartedAt == "" && len(all) > 0 {
		conv.StartedAt = flexTime(all[0].at)
	}
	return conv, true
}

// Values

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// flexID reads an id stored as a string, a number, or a BSON {"$oid": ...}.
func flexID(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case map[string]any:
		return flexID(x["$oid"])
	}
	return ""
}

// flexTime turns any timestamp an export uses into RFC3339 UTC, or "":
// epoch seconds (ChatGPT), ISO 8601 (Claude.ai, Grok conversations), epoch
// milliseconds, and BSON {"$date": {"$numberLong": "<ms>"}} (Grok responses).
func flexTime(v any) string {
	if s, ok := v.(string); ok {
		if t, ok := parseISO(s); ok {
			return t.UTC().Format(time.RFC3339)
		}
	}
	if m, ok := v.(map[string]any); ok {
		if d, ok := m["$date"]; ok {
			return flexTime(d)
		}
	}
	sec, ok := epoch(v)
	if !ok || sec <= 0 {
		return ""
	}
	whole, frac := math.Modf(sec)
	return time.Unix(int64(whole), int64(frac*1e9)).UTC().Format(time.RFC3339)
}

// epoch reads a numeric timestamp in seconds. Values past year 5138 in seconds
// are taken to be milliseconds.
func epoch(v any) (float64, bool) {
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			if t, ok := parseISO(x); ok {
				return float64(t.UnixNano()) / 1e9, true
			}
			return 0, false
		}
		n = f
	case map[string]any:
		if d, ok := x["$date"]; ok {
			return epoch(d)
		}
		if l, ok := x["$numberLong"]; ok {
			return epoch(l) // milliseconds, which the magnitude check below handles
		}
		return 0, false
	default:
		return 0, false
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 0, false
	}
	if n > 1e11 {
		n /= 1000
	}
	return n, true
}

func parseISO(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
