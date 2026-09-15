package source

import "github.com/usuario/sessions/internal/model"

type Candidate struct {
	Locator    string
	Source     string
	OriginPath string
	RecordID   string
	Revision   string
	MTime      float64
	Size       int64
}

type SourceAdapter interface {
	Discover() ([]Candidate, error)
	Load(Candidate) (*model.Session, error)
}

func Adapters() []SourceAdapter {
	return []SourceAdapter{JSONL{}, T3{}}
}

func AdapterFor(source string) SourceAdapter {
	if source == "t3code" {
		return T3{}
	}
	return JSONL{}
}
