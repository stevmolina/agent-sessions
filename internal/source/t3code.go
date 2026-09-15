package source

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/usuario/sessions/internal/config"
	"github.com/usuario/sessions/internal/model"
	_ "modernc.org/sqlite"
)

type T3 struct{}

type t3Schema struct {
	hasArchived          bool
	hasModelJSON         bool
	hasBranch            bool
	hasWorktree          bool
	hasProjectID         bool
	hasProjects          bool
	hasProviderInstance  bool
	hasProviderSessionID bool
	hasSessionUpdated    bool
}

func (T3) Discover() ([]Candidate, error) {
	dbPath, envID, err := t3DB()
	if err != nil {
		return nil, err
	}
	if dbPath == "" {
		return nil, nil
	}
	db, err := openT3(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	schema, err := inspectT3(tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(t3ThreadQuery(schema))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var threadID, updatedAt string
		var nMessages int
		var sessionUpdated, maxMsg sql.NullString
		if err := rows.Scan(&threadID, &updatedAt, &sessionUpdated, &nMessages, &maxMsg); err != nil {
			return nil, err
		}
		rev := updatedAt + ":" + sessionUpdated.String + ":" + fmt.Sprintf("%d", nMessages) + ":" + maxMsg.String
		out = append(out, Candidate{
			Locator:    t3Locator(envID, threadID),
			Source:     "t3code",
			OriginPath: dbPath,
			RecordID:   threadID,
			Revision:   rev,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (T3) Load(c Candidate) (*model.Session, error) {
	dbPath := c.OriginPath
	if dbPath == "" {
		var err error
		dbPath, _, err = t3DB()
		if err != nil {
			return nil, err
		}
	}
	if dbPath == "" {
		return nil, fmt.Errorf("t3code database not found")
	}
	db, err := openT3(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	schema, err := inspectT3(tx)
	if err != nil {
		return nil, err
	}
	threadID := c.RecordID
	if threadID == "" {
		threadID = t3ThreadID(c.Locator)
	}
	s, err := loadT3Thread(tx, schema, dbPath, c.Locator, threadID)
	if err != nil {
		return nil, err
	}
	s.Revision = c.Revision
	if s.Revision == "" && s.UpdatedAt != nil {
		s.Revision = *s.UpdatedAt + "::"
	}
	return s, nil
}

func loadT3Thread(tx *sql.Tx, schema t3Schema, dbPath, locator, threadID string) (*model.Session, error) {
	row := tx.QueryRow(t3LoadQuery(schema), threadID)
	var title, createdAt, updatedAt string
	var projectID, branch, worktree, archivedAt, modelJSON sql.NullString
	var providerName, providerInstance, providerSession, workspace sql.NullString
	if err := row.Scan(&title, &createdAt, &updatedAt, &projectID, &branch, &worktree, &archivedAt, &modelJSON, &providerName, &providerInstance, &providerSession, &workspace); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("t3code thread not found: %s", threadID)
		}
		return nil, err
	}
	msgRows, err := tx.Query(`SELECT role, text FROM projection_thread_messages WHERE thread_id=? AND role IN ('user','assistant') AND trim(text)!='' ORDER BY created_at ASC, message_id ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer msgRows.Close()
	s := &model.Session{
		ID:         threadID,
		Source:     "t3code",
		Provider:   NormalizeProvider(providerName.String),
		Path:       locator,
		OriginPath: dbPath,
		RecordID:   threadID,
		Title:      strings.TrimSpace(title),
		StartedAt:  stringPtr(createdAt),
		UpdatedAt:  stringPtr(updatedAt),
		Archived:   archivedAt.Valid && archivedAt.String != "",
	}
	if s.Path == "" {
		_, envID, _ := t3DB()
		s.Path = t3Locator(envID, threadID)
	}
	if providerInstance.Valid {
		s.ProviderInstanceID = stringPtr(providerInstance.String)
	}
	if providerSession.Valid && providerSession.String != "" {
		s.ProviderSessionID = stringPtr(providerSession.String)
	}
	if projectID.Valid {
		s.ProjectID = projectID.String
	}
	if workspace.Valid && workspace.String != "" {
		s.CWD = stringPtr(workspace.String)
	}
	if branch.Valid && branch.String != "" {
		s.Branch = stringPtr(branch.String)
	}
	if worktree.Valid && worktree.String != "" {
		s.WorktreePath = stringPtr(worktree.String)
	}
	if modelJSON.Valid {
		if model := parseModel(modelJSON.String); model != "" {
			s.Model = stringPtr(model)
		}
	}
	for msgRows.Next() {
		var role, text string
		if err := msgRows.Scan(&role, &text); err != nil {
			return nil, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		s.Turns = append(s.Turns, model.Turn{Role: role, Text: text})
	}
	if err := msgRows.Err(); err != nil {
		return nil, err
	}
	if s.Title == "" {
		s.Title = firstTitle(s.Turns)
	}
	var body []string
	for _, t := range s.Turns {
		body = append(body, t.Role+": "+t.Text)
	}
	s.Body = strings.Join(body, "\n\n")
	return s, nil
}

func t3ThreadQuery(schema t3Schema) string {
	sessionUpdated := "NULL"
	if schema.hasSessionUpdated {
		sessionUpdated = "s.updated_at"
	}
	return `SELECT t.thread_id, t.updated_at, ` + sessionUpdated + `,
		(SELECT COUNT(*) FROM projection_thread_messages m WHERE m.thread_id=t.thread_id) AS n,
		(SELECT MAX(updated_at) FROM projection_thread_messages m WHERE m.thread_id=t.thread_id) AS max_msg
		FROM projection_threads t
		LEFT JOIN projection_thread_sessions s ON s.thread_id=t.thread_id
		WHERE t.deleted_at IS NULL
		ORDER BY t.thread_id`
}

func t3LoadQuery(schema t3Schema) string {
	projectID := "NULL"
	if schema.hasProjectID {
		projectID = "t.project_id"
	}
	branch := "NULL"
	if schema.hasBranch {
		branch = "t.branch"
	}
	worktree := "NULL"
	if schema.hasWorktree {
		worktree = "t.worktree_path"
	}
	archived := "NULL"
	if schema.hasArchived {
		archived = "t.archived_at"
	}
	modelJSON := "NULL"
	if schema.hasModelJSON {
		modelJSON = "t.model_selection_json"
	}
	instance := "NULL"
	if schema.hasProviderInstance {
		instance = "s.provider_instance_id"
	}
	sessionID := "NULL"
	if schema.hasProviderSessionID {
		sessionID = "s.provider_session_id"
	}
	workspace := "NULL"
	if schema.hasProjects {
		workspace = "p.workspace_root"
	}
	q := `SELECT t.title, t.created_at, t.updated_at, ` + projectID + `, ` + branch + `, ` + worktree + `, ` + archived + `, ` + modelJSON + `,
		s.provider_name, ` + instance + `, ` + sessionID + `, ` + workspace + `
		FROM projection_threads t
		LEFT JOIN projection_thread_sessions s ON s.thread_id=t.thread_id`
	if schema.hasProjects && schema.hasProjectID {
		q += ` LEFT JOIN projection_projects p ON p.project_id=t.project_id`
	}
	q += ` WHERE t.thread_id=? AND t.deleted_at IS NULL`
	return q
}

func inspectT3(q queryer) (t3Schema, error) {
	var schema t3Schema
	tables, err := tableNames(q)
	if err != nil {
		return schema, err
	}
	for _, required := range []string{"projection_threads", "projection_thread_messages", "projection_thread_sessions"} {
		if !tables[required] {
			return schema, fmt.Errorf("unsupported t3code schema: missing table %s", required)
		}
	}
	threadCols, err := columns(q, "projection_threads")
	if err != nil {
		return schema, err
	}
	for _, required := range []string{"thread_id", "title", "created_at", "updated_at", "deleted_at"} {
		if !threadCols[required] {
			return schema, fmt.Errorf("unsupported t3code schema: projection_threads missing column %s", required)
		}
	}
	schema.hasArchived = threadCols["archived_at"]
	schema.hasModelJSON = threadCols["model_selection_json"]
	schema.hasBranch = threadCols["branch"]
	schema.hasWorktree = threadCols["worktree_path"]
	schema.hasProjectID = threadCols["project_id"]
	msgCols, err := columns(q, "projection_thread_messages")
	if err != nil {
		return schema, err
	}
	for _, required := range []string{"thread_id", "role", "text", "created_at"} {
		if !msgCols[required] {
			return schema, fmt.Errorf("unsupported t3code schema: projection_thread_messages missing column %s", required)
		}
	}
	sessCols, err := columns(q, "projection_thread_sessions")
	if err != nil {
		return schema, err
	}
	for _, required := range []string{"thread_id", "provider_name"} {
		if !sessCols[required] {
			return schema, fmt.Errorf("unsupported t3code schema: projection_thread_sessions missing column %s", required)
		}
	}
	schema.hasProviderInstance = sessCols["provider_instance_id"]
	schema.hasProviderSessionID = sessCols["provider_session_id"]
	schema.hasSessionUpdated = sessCols["updated_at"]
	if tables["projection_projects"] {
		projCols, err := columns(q, "projection_projects")
		if err != nil {
			return schema, err
		}
		schema.hasProjects = projCols["workspace_root"] && projCols["project_id"]
	}
	return schema, nil
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func tableNames(q queryer) (map[string]bool, error) {
	rows, err := q.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func columns(q queryer, table string) (map[string]bool, error) {
	rows, err := q.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func t3DB() (dbPath, envID string, err error) {
	root := config.T3Root()
	dbPath = sqlitePath(root)
	if dbPath == "" {
		if !config.T3RootExplicit() {
			if st, e := os.Stat(config.IndexedDBPath()); e == nil && st.IsDir() {
				return "", "", fmt.Errorf("t3code IndexedDB store found at %s; SQLite state.sqlite is required. Raw LevelDB is not indexed", config.IndexedDBPath())
			}
		}
		return "", "", nil
	}
	envID = readEnvID(filepath.Dir(dbPath), dbPath)
	return dbPath, envID, nil
}

func sqlitePath(root string) string {
	if root == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(root), ".sqlite") {
		if st, err := os.Stat(root); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(root)
			return abs
		}
		return ""
	}
	candidate := filepath.Join(root, "state.sqlite")
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		abs, _ := filepath.Abs(candidate)
		return abs
	}
	return ""
}

func readEnvID(dir, dbPath string) string {
	data, err := os.ReadFile(filepath.Join(dir, "environment-id"))
	if err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	sum := sha256.Sum256([]byte(dbPath))
	return hex.EncodeToString(sum[:8])
}

func t3Locator(envID, threadID string) string {
	return "t3code://" + envID + "/" + threadID
}

func t3ThreadID(locator string) string {
	locator = strings.TrimPrefix(locator, "t3code://")
	_, thread, ok := strings.Cut(locator, "/")
	if ok {
		return thread
	}
	return locator
}

func openT3(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	uri := u.String() + "?mode=ro&_pragma=query_only(ON)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func parseModel(raw string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return ""
	}
	if m, ok := obj["model"].(string); ok {
		return m
	}
	return ""
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
			if r := []rune(line); len(r) > 160 {
				line = strings.TrimSpace(string(r[:159])) + "…"
			}
			return line
		}
	}
	return ""
}
