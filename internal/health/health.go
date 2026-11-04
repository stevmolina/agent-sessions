// Package health turns push heartbeats into a per-machine verdict and decides
// when a change is worth a Slack message.
package health

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/usuario/sessions/internal/remote"
)

// Status is the verdict for one machine.
type Status string

const (
	OK      Status = "ok"
	Failing Status = "failing" // the latest attempt failed
	Stale   Status = "stale"   // no successful push within the window
	Never   Status = "never"   // expected, but nothing ever reached the database
)

// DefaultMaxAge is how long a machine may go without a successful push.
const DefaultMaxAge = 24 * time.Hour

// Machine is one row of the report.
type Machine struct {
	Name   string
	Row    *remote.HeartbeatRow // nil when the machine never wrote a heartbeat
	Status Status
	Reason string
}

// Bad reports whether the machine needs attention.
func (m Machine) Bad() bool { return m.Status != OK }

// FailAfter is how many attempts in a row must fail before a machine is failing.
// One failure is often a network blip that the next push fixes.
const FailAfter = 2

// ForgetAfter hides a machine that is not in --expect and has not tried to push
// for this long. Host names drift (the Fedora laptop once pushed as an IP
// address); an old name should not stay red forever. It was reported stale long
// before it disappears.
const ForgetAfter = 7 * 24 * time.Hour

// Classify judges one machine. Repeated failures win over age, because they carry
// the reason; age catches a machine that stopped trying altogether. An ok machine
// can still carry a note (one failed attempt, skipped sessions).
func Classify(row *remote.HeartbeatRow, now time.Time, maxAge time.Duration) (Status, string) {
	if row == nil {
		return Never, "no push has reached the database"
	}
	failedLast := row.LastError != "" && (row.LastSuccess == nil || !row.LastSuccess.After(row.LastAttempt))
	if failedLast && row.ConsecutiveFailures >= FailAfter {
		return Failing, fmt.Sprintf("%s (%d attempts in a row)", row.LastError, row.ConsecutiveFailures)
	}
	if row.LastSuccess == nil {
		if failedLast {
			return Never, "no successful push yet; last error: " + row.LastError
		}
		return Never, "no successful push yet"
	}
	if age := now.Sub(*row.LastSuccess); age > maxAge {
		return Stale, "no successful push for " + Ago(age)
	}
	switch {
	case failedLast:
		return OK, "last attempt failed once: " + row.LastError
	case row.Skipped > 0:
		return OK, fmt.Sprintf("%d session(s) skipped: could not be cleaned", row.Skipped)
	}
	return OK, ""
}

// Build joins heartbeats with the machines that are expected to push, so a
// machine that cannot reach the database at all still shows up.
func Build(rows []remote.HeartbeatRow, expected []string, now time.Time, maxAge time.Duration) []Machine {
	byName := map[string]*remote.HeartbeatRow{}
	for i := range rows {
		byName[rows[i].Machine] = &rows[i]
	}
	wanted := map[string]bool{}
	for _, name := range expected {
		if name = strings.TrimSpace(name); name != "" {
			wanted[name] = true
		}
	}
	names := map[string]bool{}
	for name, row := range byName {
		if len(wanted) > 0 && !wanted[name] && now.Sub(row.LastAttempt) > ForgetAfter {
			continue
		}
		names[name] = true
	}
	for name := range wanted {
		names[name] = true
	}
	out := make([]Machine, 0, len(names))
	for name := range names {
		row := byName[name]
		status, reason := Classify(row, now, maxAge)
		out = append(out, Machine{Name: name, Row: row, Status: status, Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Write prints the report as an aligned table.
func Write(w io.Writer, machines []Machine, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MACHINE\tSTATUS\tLAST SUCCESS\tLAST ATTEMPT\tLAST RUN\tVERSION\tNOTE")
	for _, m := range machines {
		success, attempt, run, version := "-", "-", "-", "-"
		if r := m.Row; r != nil {
			attempt = Ago(now.Sub(r.LastAttempt)) + " ago"
			if r.LastSuccess != nil {
				success = Ago(now.Sub(*r.LastSuccess)) + " ago"
			}
			run = fmt.Sprintf("%d up, %d same, %d skipped, %.1fs", r.Uploaded, r.Unchanged, r.Skipped, float64(r.DurationMS)/1000)
			if r.Version != "" {
				version = r.Version
			}
		}
		note := m.Reason
		if note == "" {
			note = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Name, m.Status, success, attempt, run, version, note)
	}
	return tw.Flush()
}

// Ago renders a duration the way people read it: 40s, 12m, 5h, 3d.
func Ago(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// Change is one machine crossing between good and bad.
type Change struct {
	Machine   Machine
	Recovered bool
}

// Transitions compares the current report with what was last announced.
// prev maps a machine to "bad" or "ok". A machine is announced when it turns bad
// (including the first time it is seen bad) and when a bad one recovers. Nothing
// else is sent, so a machine that stays down produces one message, not one an hour.
func Transitions(prev map[string]string, machines []Machine) ([]Change, map[string]string) {
	next := map[string]string{}
	var changes []Change
	for _, m := range machines {
		state := "ok"
		if m.Bad() {
			state = "bad"
		}
		next[m.Name] = state
		switch {
		case state == "bad" && prev[m.Name] != "bad":
			changes = append(changes, Change{Machine: m})
		case state == "ok" && prev[m.Name] == "bad":
			changes = append(changes, Change{Machine: m, Recovered: true})
		}
	}
	// The heartbeats could be read, so the database is reachable again.
	if prev[DatabaseKey] == "bad" {
		changes = append(changes, Change{Machine: Machine{Name: DatabaseKey, Status: OK}, Recovered: true})
	}
	return changes, next
}

// DatabaseKey is the alert state for the shared database itself.
const DatabaseKey = "shared database"

// Unreachable handles a run that could not read the heartbeats at all. Without
// it the alert would go quiet exactly when the database is broken. Machine states
// are kept as they were, so they are not re-announced when it comes back.
func Unreachable(prev map[string]string, reason string) ([]Change, map[string]string) {
	next := map[string]string{}
	for k, v := range prev {
		next[k] = v
	}
	next[DatabaseKey] = "bad"
	if prev[DatabaseKey] == "bad" {
		return nil, next
	}
	return []Change{{Machine: Machine{Name: DatabaseKey, Status: Failing, Reason: reason}}}, next
}

// Message is the Slack text for a set of changes.
func Message(changes []Change, now time.Time) string {
	var b strings.Builder
	b.WriteString("Session sync")
	for _, c := range changes {
		b.WriteString("\n")
		if c.Machine.Name == DatabaseKey {
			if c.Recovered {
				b.WriteString("• the health check can read the shared database again")
			} else {
				fmt.Fprintf(&b, "• the health check cannot read the shared database: %s", c.Machine.Reason)
			}
			continue
		}
		if c.Recovered {
			fmt.Fprintf(&b, "• %s is pushing again", c.Machine.Name)
			continue
		}
		fmt.Fprintf(&b, "• %s is %s: %s", c.Machine.Name, c.Machine.Status, c.Machine.Reason)
		if r := c.Machine.Row; r != nil && r.LastSuccess != nil {
			fmt.Fprintf(&b, " (last good push %s ago)", Ago(now.Sub(*r.LastSuccess)))
		}
	}
	b.WriteString("\nRun `sessions health` for the table.")
	return b.String()
}
