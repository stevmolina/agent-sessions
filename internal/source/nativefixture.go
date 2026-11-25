package source

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// Fixture writers for the native agent formats, used by tests in this and other
// packages. They write the same shapes the real apps write.

// NativeMsg is one message of a fixture chat.
type NativeMsg struct{ Role, Text string }

// WriteGrokFixture writes <root>/<encoded cwd>/<id>/{chat_history.jsonl,summary.json}.
// Each user message carries prompt_index, as a typed prompt does.
func WriteGrokFixture(root, cwd, id, title string, msgs []NativeMsg) (string, error) {
	dir := filepath.Join(root, url.PathEscape(cwd), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var lines []byte
	add := func(v any) {
		raw, _ := json.Marshal(v)
		lines = append(lines, raw...)
		lines = append(lines, '\n')
	}
	add(map[string]any{"type": "system", "content": "You are Grok."})
	add(map[string]any{"type": "user", "synthetic_reason": "system_reminder", "content": []map[string]string{{"type": "text", "text": "<system-reminder>skills</system-reminder>"}}})
	idx := 0
	for _, m := range msgs {
		if m.Role == "user" {
			add(map[string]any{"type": "user", "prompt_index": idx, "content": []map[string]string{{"type": "text", "text": "<user_query>\n" + m.Text + "\n</user_query>"}}})
			idx++
		} else {
			add(map[string]any{"type": "assistant", "content": m.Text, "tool_calls": []any{}})
		}
		add(map[string]any{"type": "tool_result", "content": "tool output that must never be indexed"})
	}
	if err := os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), lines, 0o644); err != nil {
		return "", err
	}
	sum, _ := json.Marshal(map[string]any{"info": map[string]string{"id": id, "cwd": cwd}, "created_at": "2026-11-20T10:00:00Z", "updated_at": "2026-11-20T10:30:00Z", "generated_title": title, "num_chat_messages": len(msgs)})
	return dir, os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644)
}

// WriteMuseFixture writes <root>/2026/11/20/<id>/session.jsonl: a permission
// frame that wraps records, then plain records.
func WriteMuseFixture(root, cwd, id string, msgs []NativeMsg) (string, error) {
	dir := filepath.Join(root, "2026", "11", "20", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var lines []byte
	add := func(v any) {
		raw, _ := json.Marshal(v)
		lines = append(lines, raw...)
		lines = append(lines, '\n')
	}
	rec := func(micro int64, typ string, payload any) map[string]any {
		return map[string]any{"schema_version": 1, "recorded_at": micro, "payload_type": typ, "payload": payload}
	}
	start, _ := json.Marshal(rec(1795391942000000, "runtime.session", map[string]any{"kind": "session_start_intake_v4", "payload": map[string]any{"session_metadata": map[string]string{"workspace_root": cwd}}}))
	add(map[string]any{"retained_frame": "session_permission_transaction", "children": []map[string]any{{"child_index": 0, "record_json": string(start)}}})
	add(rec(1795391942500000, "session.name.changed", map[string]string{"new_name": "tawny-grus"}))
	micro := int64(1795391943000000)
	for _, m := range msgs {
		micro += 1000000
		if m.Role == "user" {
			add(rec(micro, "runtime.user_intent.accepted", map[string]any{"surface": "main", "refill_blocks": []map[string]string{{"kind": "text", "text": m.Text}, {"kind": "asset"}}}))
		} else {
			add(rec(micro, "runtime.session", map[string]any{"kind": "run", "event": map[string]string{"kind": "assistant_message_committed", "text": m.Text}}))
		}
		add(rec(micro+1, "runtime.session", map[string]any{"kind": "task", "event": map[string]string{"kind": "output", "chunk": "tool output that must never be indexed"}}))
	}
	path := filepath.Join(dir, "session.jsonl")
	return path, os.WriteFile(path, lines, 0o644)
}

// pbField encodes one length-delimited protobuf field; pbVarint one varint field.
func pbField(num int, data []byte) []byte {
	out := pbVar(uint64(num<<3 | 2))
	out = append(out, pbVar(uint64(len(data)))...)
	return append(out, data...)
}
func pbVarint(num int, v uint64) []byte {
	return append(pbVar(uint64(num<<3)), pbVar(v)...)
}
func pbVar(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

// WriteAntigravityFixture writes <root>/<home>/conversations/<id>.db with the
// steps and trajectory_metadata_blob tables the app writes. Step type 14 is a
// person's message, 15 the model's reply, 90 a tool step that is never indexed.
func WriteAntigravityFixture(root, home, cwd, id string, msgs []NativeMsg) (string, error) {
	dir := filepath.Join(root, home, "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE steps (idx integer, step_type integer NOT NULL DEFAULT 0, status integer, step_payload blob, PRIMARY KEY (idx)); CREATE TABLE trajectory_metadata_blob (id text DEFAULT 'main', data blob, PRIMARY KEY (id))"); err != nil {
		return "", err
	}
	stamp := func(secs uint64) []byte { return pbField(5, pbField(1, pbVarint(1, secs))) }
	for i, m := range msgs {
		secs := uint64(1795391000 + i*60)
		var payload []byte
		kind := 14
		if m.Role == "user" {
			payload = append(stamp(secs), pbField(19, append(pbField(2, []byte(m.Text)), pbField(3, pbField(1, []byte(m.Text)))...))...)
		} else {
			kind = 15
			payload = append(stamp(secs), pbField(20, append(pbField(1, []byte(m.Text)), pbField(3, []byte("private reasoning that is never indexed"))...))...)
		}
		if _, err := db.Exec("INSERT INTO steps(idx, step_type, status, step_payload) VALUES(?,?,3,?)", i*2, kind, payload); err != nil {
			return "", err
		}
		tool := append(stamp(secs+1), pbField(20, pbField(1, []byte("tool step text that must never be indexed")))...)
		if _, err := db.Exec("INSERT INTO steps(idx, step_type, status, step_payload) VALUES(?,?,3,?)", i*2+1, 90, tool); err != nil {
			return "", err
		}
	}
	meta := pbField(1, pbField(1, []byte(fmt.Sprintf("file://%s", cwd))))
	if _, err := db.Exec("INSERT INTO trajectory_metadata_blob(id, data) VALUES('main', ?)", meta); err != nil {
		return "", err
	}
	// Real databases are larger than the 4 KiB floor Discover uses to skip empties.
	if _, err := db.Exec("CREATE TABLE filler (x blob); INSERT INTO filler VALUES (zeroblob(8192))"); err != nil {
		return "", err
	}
	return path, nil
}
