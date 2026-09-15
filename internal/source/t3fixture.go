package source

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
)

type T3ThreadFixture struct {
	ID, ProjectID, Title, CWD, Provider, ProviderInstance, ProviderSession   string
	CreatedAt, UpdatedAt, DeletedAt, ArchivedAt, Branch, Worktree, ModelJSON string
	Messages                                                                 []T3MessageFixture
}

type T3MessageFixture struct {
	ID, Role, Text, CreatedAt string
}

func WriteT3Fixture(dir string, envID string, threads []T3ThreadFixture) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if envID != "" {
		if err := os.WriteFile(filepath.Join(dir, "environment-id"), []byte(envID+"\n"), 0o644); err != nil {
			return "", err
		}
	}
	dbPath := filepath.Join(dir, "state.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	if _, err := db.Exec(t3FixtureSchema); err != nil {
		return "", err
	}
	for _, th := range threads {
		if th.ProjectID == "" {
			th.ProjectID = "proj-1"
		}
		if th.CWD == "" {
			th.CWD = "/tmp/t3-proj"
		}
		if th.CreatedAt == "" {
			th.CreatedAt = "2026-08-26T03:00:00.000Z"
		}
		if th.UpdatedAt == "" {
			th.UpdatedAt = "2026-08-26T03:00:10.000Z"
		}
		if th.ProviderInstance == "" {
			th.ProviderInstance = th.Provider
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO projection_projects(project_id,title,workspace_root,scripts_json,created_at,updated_at) VALUES(?,?,?,'{}',?,?)`, th.ProjectID, "proj", th.CWD, th.CreatedAt, th.UpdatedAt); err != nil {
			return "", err
		}
		var deleted, archived any
		if th.DeletedAt != "" {
			deleted = th.DeletedAt
		}
		if th.ArchivedAt != "" {
			archived = th.ArchivedAt
		}
		if _, err := db.Exec(`INSERT INTO projection_threads(thread_id,project_id,title,branch,worktree_path,created_at,updated_at,deleted_at,archived_at,model_selection_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, th.ID, th.ProjectID, th.Title, nullEmpty(th.Branch), nullEmpty(th.Worktree), th.CreatedAt, th.UpdatedAt, deleted, archived, nullEmpty(th.ModelJSON)); err != nil {
			return "", err
		}
		if th.Provider != "" || th.ProviderSession != "" {
			if _, err := db.Exec(`INSERT INTO projection_thread_sessions(thread_id,status,provider_name,provider_session_id,provider_thread_id,updated_at,provider_instance_id) VALUES(?,?,?,?,?,?,?)`, th.ID, "idle", nullEmpty(th.Provider), nullEmpty(th.ProviderSession), nil, th.UpdatedAt, nullEmpty(th.ProviderInstance)); err != nil {
				return "", err
			}
		}
		for j, msg := range th.Messages {
			id := msg.ID
			if id == "" {
				id = th.ID + "-m" + strconv.Itoa(j)
			}
			created := msg.CreatedAt
			if created == "" {
				created = th.CreatedAt
			}
			if _, err := db.Exec(`INSERT INTO projection_thread_messages(message_id,thread_id,role,text,is_streaming,created_at,updated_at) VALUES(?,?,?,?,0,?,?)`, id, th.ID, msg.Role, msg.Text, created, created); err != nil {
				return "", err
			}
		}
	}
	return dbPath, nil
}

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const t3FixtureSchema = `
CREATE TABLE projection_projects (
  project_id TEXT PRIMARY KEY, title TEXT NOT NULL, workspace_root TEXT NOT NULL,
  scripts_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, deleted_at TEXT
);
CREATE TABLE projection_threads (
  thread_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, title TEXT NOT NULL,
  branch TEXT, worktree_path TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  deleted_at TEXT, archived_at TEXT, model_selection_json TEXT
);
CREATE TABLE projection_thread_messages (
  message_id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, turn_id TEXT, role TEXT NOT NULL,
  text TEXT NOT NULL, is_streaming INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE projection_thread_sessions (
  thread_id TEXT PRIMARY KEY, status TEXT NOT NULL, provider_name TEXT,
  provider_session_id TEXT, provider_thread_id TEXT, active_turn_id TEXT,
  last_error TEXT, updated_at TEXT NOT NULL, provider_instance_id TEXT
);`
