package model

type Turn struct{ Role, Text string }

type Session struct {
	ID, Source, Provider, Path, Title, Body string
	ProviderInstanceID, ProviderSessionID   *string
	ProjectID, OriginPath, RecordID         string
	CWD, StartedAt, UpdatedAt, ParentID     *string
	Model, Branch, WorktreePath             *string
	Archived                                bool
	MTime                                   float64
	Size                                    int64
	Revision                                string
	Turns                                   []Turn
}

type Row struct {
	ID, Source, Provider, Path, Title     string
	ProviderInstanceID, ProviderSessionID *string
	OriginPath, RecordID                  string
	CWD, StartedAt, UpdatedAt, ParentID   *string
	Model, Branch, WorktreePath           *string
	Archived                              bool
	Revision                              string
}
