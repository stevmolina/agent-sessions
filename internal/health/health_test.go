package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usuario/sessions/internal/remote"
)

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { t := now.Add(-d); return &t }

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		row    *remote.HeartbeatRow
		status Status
		reason string
	}{
		{"no heartbeat", nil, Never, "no push has reached the database"},
		{"fresh success", &remote.HeartbeatRow{LastAttempt: *at(5 * time.Minute), LastSuccess: at(5 * time.Minute)}, OK, ""},
		{"one failure is a blip", &remote.HeartbeatRow{LastAttempt: *at(time.Minute), LastSuccess: at(time.Hour), LastError: "remote database unavailable", ConsecutiveFailures: 1}, OK, "last attempt failed once: remote database unavailable"},
		{"two failures in a row", &remote.HeartbeatRow{LastAttempt: *at(time.Minute), LastSuccess: at(time.Hour), LastError: "remote database unavailable", ConsecutiveFailures: 2}, Failing, "remote database unavailable (2 attempts in a row)"},
		{"failed and never succeeded", &remote.HeartbeatRow{LastAttempt: *at(time.Minute), LastError: "upsert failed (23505)", ConsecutiveFailures: 3}, Failing, "upsert failed (23505) (3 attempts in a row)"},
		{"one failure, never succeeded", &remote.HeartbeatRow{LastAttempt: *at(time.Minute), LastError: "upsert failed (23505)", ConsecutiveFailures: 1}, Never, "no successful push yet; last error: upsert failed (23505)"},
		{"skipped sessions are a note", &remote.HeartbeatRow{LastAttempt: *at(time.Minute), LastSuccess: at(time.Minute), Skipped: 3}, OK, "3 session(s) skipped: could not be cleaned"},
		{"failing beats stale", &remote.HeartbeatRow{LastAttempt: *at(time.Minute), LastSuccess: at(72 * time.Hour), LastError: "x", ConsecutiveFailures: 9}, Failing, "x (9 attempts in a row)"},
		{"old error, newer success", &remote.HeartbeatRow{LastAttempt: *at(time.Minute), LastSuccess: at(time.Minute), LastError: ""}, OK, ""},
		{"silent for two days", &remote.HeartbeatRow{LastAttempt: *at(49 * time.Hour), LastSuccess: at(49 * time.Hour)}, Stale, "no successful push for 2d"},
		{"just inside the window", &remote.HeartbeatRow{LastAttempt: *at(23 * time.Hour), LastSuccess: at(23 * time.Hour)}, OK, ""},
		{"attempts but never a success", &remote.HeartbeatRow{LastAttempt: *at(time.Minute)}, Never, "no successful push yet"},
	}
	for _, c := range cases {
		status, reason := Classify(c.row, now, DefaultMaxAge)
		if status != c.status || reason != c.reason {
			t.Errorf("%s: got %s %q, want %s %q", c.name, status, reason, c.status, c.reason)
		}
	}
}

func TestBuildAddsExpectedMachinesThatNeverPushed(t *testing.T) {
	rows := []remote.HeartbeatRow{{Machine: "mac", LastAttempt: now, LastSuccess: &now}}
	got := Build(rows, []string{"mac", " vps-b ", ""}, now, DefaultMaxAge)
	if len(got) != 2 || got[0].Name != "mac" || got[1].Name != "vps-b" {
		t.Fatalf("unexpected machines: %+v", got)
	}
	if got[0].Status != OK || got[1].Status != Never {
		t.Fatalf("unexpected statuses: %s %s", got[0].Status, got[1].Status)
	}
}

func TestBuildForgetsOldUnexpectedNames(t *testing.T) {
	old := now.Add(-8 * 24 * time.Hour)
	rows := []remote.HeartbeatRow{
		{Machine: "192.168.1.10", LastAttempt: old, LastSuccess: &old},
		{Machine: "mac", LastAttempt: now, LastSuccess: &now},
	}
	got := Build(rows, []string{"mac"}, now, DefaultMaxAge)
	if len(got) != 1 || got[0].Name != "mac" {
		t.Fatalf("old unexpected name should be hidden: %+v", got)
	}
	if got = Build(rows, nil, now, DefaultMaxAge); len(got) != 2 {
		t.Fatalf("without --expect every machine is shown: %+v", got)
	}
}

func TestUnreachableDatabaseAnnouncesOnceAndRecovers(t *testing.T) {
	prev := map[string]string{"vps": "bad", "mac": "ok"}
	changes, state := Unreachable(prev, "remote database unavailable")
	if len(changes) != 1 || changes[0].Machine.Name != DatabaseKey {
		t.Fatalf("want one database alert: %+v", changes)
	}
	if state["vps"] != "bad" || state["mac"] != "ok" {
		t.Fatalf("machine states must be kept: %v", state)
	}
	if changes, state = Unreachable(state, "remote database unavailable"); len(changes) != 0 {
		t.Fatalf("still down must be quiet: %+v", changes)
	}
	machines := []Machine{{Name: "vps", Status: Failing, Reason: "x"}, {Name: "mac", Status: OK}}
	changes, state = Transitions(state, machines)
	if len(changes) != 1 || changes[0].Machine.Name != DatabaseKey || !changes[0].Recovered {
		t.Fatalf("want only the database recovery, not a repeat of vps: %+v", changes)
	}
	if _, ok := state[DatabaseKey]; ok {
		t.Fatal("database state should clear after recovery")
	}
	if !strings.Contains(Message(changes, now), "can read the shared database again") {
		t.Fatal("recovery message wrong")
	}
}

func TestTransitionsAnnounceOnlyChanges(t *testing.T) {
	good := Machine{Name: "mac", Status: OK}
	down := Machine{Name: "vps", Status: Failing, Reason: "remote database unavailable"}

	changes, state := Transitions(map[string]string{}, []Machine{good, down})
	if len(changes) != 1 || changes[0].Machine.Name != "vps" || changes[0].Recovered {
		t.Fatalf("first run should announce only the bad machine: %+v", changes)
	}

	changes, state = Transitions(state, []Machine{good, down})
	if len(changes) != 0 {
		t.Fatalf("a machine that stays down must not be announced again: %+v", changes)
	}

	stillDown := down
	stillDown.Status, stillDown.Reason = Stale, "no successful push for 2d"
	changes, state = Transitions(state, []Machine{good, stillDown})
	if len(changes) != 0 {
		t.Fatalf("failing to stale is still down, no message: %+v", changes)
	}

	up := Machine{Name: "vps", Status: OK}
	changes, state = Transitions(state, []Machine{good, up})
	if len(changes) != 1 || !changes[0].Recovered {
		t.Fatalf("recovery should be announced once: %+v", changes)
	}
	if changes, _ = Transitions(state, []Machine{good, up}); len(changes) != 0 {
		t.Fatalf("staying up is quiet: %+v", changes)
	}
}

func TestMessageNamesMachineAndReason(t *testing.T) {
	row := &remote.HeartbeatRow{LastSuccess: at(30 * time.Hour)}
	msg := Message([]Change{
		{Machine: Machine{Name: "vps", Status: Stale, Reason: "no successful push for 30h", Row: row}},
		{Machine: Machine{Name: "mac", Status: OK}, Recovered: true},
	}, now)
	for _, want := range []string{"vps is stale: no successful push for 30h", "last good push 30h ago", "mac is pushing again"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message lacks %q:\n%s", want, msg)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	empty, err := LoadState(path)
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing file should be empty state: %v %v", empty, err)
	}
	if err := SaveState(path, map[string]string{"vps": "bad"}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil || got["vps"] != "bad" {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestPostSlack(t *testing.T) {
	var gotAuth, gotChannel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotChannel, _ = body["channel"].(string)
		if gotChannel == "C-bad" {
			_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	old := SlackURL
	SlackURL = srv.URL
	defer func() { SlackURL = old }()

	if err := PostSlack(context.Background(), "xoxb-test", "C1", "hi"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer xoxb-test" || gotChannel != "C1" {
		t.Fatalf("auth=%q channel=%q", gotAuth, gotChannel)
	}
	err := PostSlack(context.Background(), "xoxb-test", "C-bad", "hi")
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") || strings.Contains(err.Error(), "xoxb") {
		t.Fatalf("unexpected error: %v", err)
	}
}
