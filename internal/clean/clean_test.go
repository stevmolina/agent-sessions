package clean

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/usuario/sessions/internal/model"
)

func mixed(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[(i*7+3)%len(alphabet)]
	}
	return string(b)
}

func TestRedactRemovesKnownSecretShapes(t *testing.T) {
	widget := "sesame-door-91-quartz"
	samples := []struct {
		name        string
		value       string
		placeholder string
	}{
		{"github-pat", "ghp_" + mixed(36), "[REDACTED:github-pat]"},
		{"github-oauth", "gho_" + mixed(36), "[REDACTED:github-oauth]"},
		{"stripe-access-token", "sk_live_" + mixed(24), "[REDACTED:stripe-access-token]"},
		{"slack-bot-token", slackBot(), "[REDACTED:slack-bot-token]"},
		{"jwt", jwt(), "[REDACTED:jwt]"},
		{"private-key", pem(), "[REDACTED:private-key]"},
		{"generic-api-key", "api_key = \"" + mixed(20) + "\"", "[REDACTED:generic-api-key]"},
		{"curl-auth-header", "curl -H \"Authorization: Bearer " + mixed(24) + "\"", "[REDACTED:curl-auth-header]"},
	}
	var b strings.Builder
	b.WriteString("Notes from the mango-helix-882 session. Port 8080 is open and true is still true. The word password stays. changeme stays too.\n")
	b.WriteString("bring " + widget + " to the meeting\n")
	for _, s := range samples {
		b.WriteString(s.value)
		b.WriteByte('\n')
	}
	secrets := map[string]string{
		"PORT":     "8080",
		"FLAG":     "true",
		"WORD":     "password",
		"WIDGET":   widget,
		"TINY":     "abc",
		"CHANGEME": "changeme",
	}
	got, err := Redact(b.String(), secrets)
	if err != nil {
		t.Fatal("redaction failed")
	}
	if strings.Contains(got, widget) {
		t.Fatal("secret survived: exact")
	}
	if !strings.Contains(got, "[REDACTED:WIDGET]") {
		t.Fatal("missing placeholder: exact")
	}
	for _, s := range samples {
		if strings.Contains(got, s.value) {
			t.Fatalf("secret survived: %s", s.name)
		}
		if !strings.Contains(got, s.placeholder) {
			t.Fatalf("missing placeholder: %s", s.name)
		}
	}
	for _, keep := range []string{"8080", "true", "mango-helix-882", "changeme"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("ordinary text removed: %s", keep)
		}
	}
	if !strings.Contains(got, "pass"+"word") {
		t.Fatal("ordinary word removed")
	}
}

func TestRedactKeepsLowEntropyConfig(t *testing.T) {
	got, err := Redact("deploy to us-east-1 tonight", map[string]string{"AWS_REGION": "us-east-1"})
	if err != nil {
		t.Fatal("redaction failed")
	}
	if !strings.Contains(got, "us-east-1") {
		t.Fatal("region was redacted")
	}
}

func TestFingerprintIgnoresOrderAndTracksValues(t *testing.T) {
	a, err := New(map[string]string{"B": "sesame-door-91-quartz", "A": "other-token-91-quartz"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(map[string]string{"A": "other-token-91-quartz", "B": "sesame-door-91-quartz"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint() == "" || a.Fingerprint() != b.Fingerprint() {
		t.Fatal("fingerprint should be stable")
	}
	c, err := New(map[string]string{"A": "other-token-91-quartz", "B": "sesame-door-91-changed"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Fingerprint() == a.Fingerprint() {
		t.Fatal("fingerprint should change when a value changes")
	}
	empty, err := New(map[string]string{"PORT": "8080"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Fingerprint(), "sesame") || strings.Contains(empty.Fingerprint(), "8080") {
		t.Fatal("fingerprint contains secret material")
	}
}

func TestApplyRebuildsBodyFromCleanTurns(t *testing.T) {
	token := "widget-token-91-quartz"
	c, err := New(map[string]string{"WIDGET": token})
	if err != nil {
		t.Fatal(err)
	}
	s := &model.Session{
		Title: "see " + token,
		Turns: []model.Turn{{Role: "user", Text: "see " + token}},
		Body:  "user: see " + token,
	}
	if err := c.Apply(s); err != nil {
		t.Fatal("redaction failed")
	}
	if strings.Contains(s.Title, token) || strings.Contains(s.Body, token) || strings.Contains(s.Turns[0].Text, token) {
		t.Fatal("secret survived")
	}
	if !strings.Contains(s.Body, "user: see [REDACTED:WIDGET]") {
		t.Fatal("body was not rebuilt from turns")
	}
}

func slackBot() string {
	return "xox" + "b-" + "1029384756" + "-" + "5647382910" + mixed(8)
}

func pem() string {
	dashes := strings.Repeat("-", 5)
	kind := "PRIVATE KEY"
	return dashes + "BEGIN " + kind + dashes + "\n" + strings.Repeat("Ab3/", 20) + "\n" + dashes + "END " + kind + dashes
}

func jwt() string {
	enc := base64.RawURLEncoding.EncodeToString
	header := enc([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := enc([]byte(`{"sub":"1234567890","name":"Ada"}`))
	return header + "." + payload + "." + mixed(32)
}
