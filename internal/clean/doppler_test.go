package clean

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/usuario/sessions/internal/model"
)

type fakeRunner struct {
	mu  sync.Mutex
	out map[string][]byte
}

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.out[strings.Join(args, "\x00")]; ok {
		return b, nil
	}
	return nil, errors.New("doppler secrets unavailable")
}

func TestLoadFromKeepsDistinctValuesAndDropsTrivialOnes(t *testing.T) {
	widget := "sesame-door-91-quartz"
	alpha := "alpha-token-91-quartz"
	beta := "beta-token-91-quartzxx"
	runner := &fakeRunner{out: map[string][]byte{
		strings.Join([]string{"projects", "--json"}, "\x00"):                                                                            []byte(`[{"name":"beta"},{"name":"alpha"}]`),
		strings.Join([]string{"configs", "--project", "alpha", "--json"}, "\x00"):                                                       []byte(`[{"name":"dev"}]`),
		strings.Join([]string{"configs", "--project", "beta", "--json"}, "\x00"):                                                        []byte(`[{"name":"prd"}]`),
		strings.Join([]string{"secrets", "download", "--project", "alpha", "--config", "dev", "--no-file", "--format", "json"}, "\x00"): []byte(`{"WIDGET":"` + widget + `","TOKEN":"` + alpha + `","PORT":"8080","DOPPLER_CONFIG":"dev","SHORT":"abc"}`),
		strings.Join([]string{"secrets", "download", "--project", "beta", "--config", "prd", "--no-file", "--format", "json"}, "\x00"):  []byte(`{"WIDGET":"` + widget + `","TOKEN":"` + beta + `","FLAG":"true"}`),
	}}
	c, err := LoadFrom(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	s := &model.Session{Turns: []model.Turn{{Role: "user", Text: strings.Join([]string{
		"port 8080 true",
		widget,
		alpha,
		beta,
	}, "\n")}}}
	if err := c.Apply(s); err != nil {
		t.Fatal("redaction failed")
	}
	text := s.Turns[0].Text
	if strings.Contains(text, widget) || strings.Contains(text, alpha) || strings.Contains(text, beta) {
		t.Fatal("secret survived")
	}
	for _, keep := range []string{"8080", "true", "[REDACTED:WIDGET]", "[REDACTED:TOKEN]", "[REDACTED:beta/TOKEN]"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("missing %s", keep)
		}
	}
}

func TestEmptyDopplerSetIsAnError(t *testing.T) {
	runner := &fakeRunner{out: map[string][]byte{
		strings.Join([]string{"projects", "--json"}, "\x00"):                                                                            []byte(`[{"name":"alpha"}]`),
		strings.Join([]string{"configs", "--project", "alpha", "--json"}, "\x00"):                                                       []byte(`[{"name":"dev"}]`),
		strings.Join([]string{"secrets", "download", "--project", "alpha", "--config", "dev", "--no-file", "--format", "json"}, "\x00"): []byte(`{"PORT":"8080","DOPPLER_CONFIG":"dev"}`),
	}}
	_, err := LoadFrom(context.Background(), runner)
	if err == nil {
		t.Fatal("expected empty doppler set to fail")
	}
	if strings.Contains(err.Error(), "8080") {
		t.Fatal("error included a secret value")
	}
}

func TestSecretsFileMarksFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(`{"WIDGET":"sesame-door-91-quartz"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Fingerprint(), "v1-override:") {
		t.Fatal("override fingerprint was not marked")
	}
	plain, err := New(map[string]string{"WIDGET": "sesame-door-91-quartz"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(plain.Fingerprint(), "v1-override:") {
		t.Fatal("doppler fingerprint was marked as an override")
	}
}

func TestExecRunnerHidesStderr(t *testing.T) {
	secret := "hidden-" + mixed(24)
	dir := t.TempDir()
	script := filepath.Join(dir, "doppler")
	body := "#!/bin/sh\nprintf '%s\\n' '" + secret + "' >&2\nexit 7\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := execRunner{bin: script}.Output(context.Background(), "projects")
	if err == nil {
		t.Fatal("expected doppler failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "hidden-") {
		t.Fatal("doppler error leaked command output")
	}
}
