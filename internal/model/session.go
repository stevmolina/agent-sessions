package model

type Turn struct{ Role, Text string }

type Session struct {
	ID, Source, Path, Title, Body       string
	CWD, StartedAt, UpdatedAt, ParentID *string
	MTime                               float64
	Size                                int64
	Turns                               []Turn
}

type Row struct {
	ID, Source, Path, Title             string
	CWD, StartedAt, UpdatedAt, ParentID *string
}
