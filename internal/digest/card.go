package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/usuario/sessions/internal/model"
)

// Version changes when the title or card rules change. It is part of the
// digest, so a new version refreshes every card on the next push.
const Version = "3"

// Input is what a card is built from. Brain and Machine are tags from the
// pusher; everything else comes from the cleaned transcript.
type Input struct {
	Source, Provider, Machine, Brain string
	CWD, StartedAt, UpdatedAt        string
	Title                            string
	Turns                            []model.Turn
}

// Result is the clean title, the card, and a digest that changes only when
// the title or the card's content lines change (not the machine or the brain).
type Result struct {
	Title, Card, Digest string
}

var (
	prURL      = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/(\d+)`)
	shipVerb   = regexp.MustCompile(`(?i)\b(opened|created|merged|shipped|pushed|auto-merge|ready for review|squash)`)
	mergedHash = regexp.MustCompile("(?i)\\bmerge[d]?(?: commit)?[^\\n.]{0,40}?\\b`?([0-9a-f]{7,40})`?\\b")
	decideWord = regexp.MustCompile(`(?i)\b((?:we|i|steven) (?:decided|chose|agreed)|decided (?:to|on|that)|going with|(?:we|i)(?:'ll| will) (?:use|go with|keep)|root cause (?:is|was)|root cause:|the (?:fix|plan) is)\b`)
	hasDigit   = regexp.MustCompile(`[0-9]`)
	worktree   = regexp.MustCompile(`/worktrees/([^/]+)/`)
)

// Build returns the title and card for one session. The same input always
// gives the same output.
func Build(in Input) Result {
	title := Title(in.Turns, in.Title)
	if prefersStored(in.Source) {
		if stored := firstRequest(Text(in.Title)); stored != "" {
			title = stored
		}
	}
	var user, asst []model.Turn
	for _, t := range in.Turns {
		switch t.Role {
		case "user":
			if Text(t.Text) != "" {
				user = append(user, t)
			}
		case "assistant":
			asst = append(asst, t)
		}
	}

	var lines []string
	lines = append(lines, whereLine(in))

	asked := title
	if asked == "" {
		asked = "(no request recorded)"
	}
	if n := len(user); n > 1 {
		asked += fmt.Sprintf(" (+%d follow-ups)", n-1)
	}
	askedLine := "Asked: " + asked
	lines = append(lines, askedLine)
	content := []string{askedLine}

	if d := decided(asst); d != "" {
		l := "Decided: " + d
		lines = append(lines, l)
		content = append(content, l)
	}
	if s := shipped(asst); s != "" {
		l := "Shipped: " + s
		lines = append(lines, l)
		content = append(content, l)
	}
	stateLine := "State: " + finalState(in.Turns)
	lines = append(lines, stateLine)
	content = append(content, stateLine)

	sum := sha256.Sum256([]byte(Version + "\x00" + title + "\x00" + strings.Join(content, "\n")))
	return Result{Title: title, Card: strings.Join(lines, "\n"), Digest: hex.EncodeToString(sum[:8])}
}

// prefersStored is true for sources whose stored title is a name somebody or
// something chose (a T3 thread title, an exported chat's title), which reads
// better than the first sentence of the first message.
func prefersStored(source string) bool {
	switch source {
	case "t3code", "chatgpt", "claude-ai", "grok":
		return true
	}
	return false
}

func whereLine(in Input) string {
	var parts []string
	if in.Brain != "" {
		parts = append(parts, in.Brain)
	} else {
		parts = append(parts, "no brain")
	}
	if f := folder(in.CWD); f != "" {
		parts = append(parts, f)
	}
	if in.Machine != "" {
		parts = append(parts, in.Machine)
	}
	prov := in.Source
	if in.Provider != "" && in.Provider != in.Source {
		prov = in.Source + "/" + in.Provider
	}
	if prov != "" {
		parts = append(parts, prov)
	}
	if d := day(in.StartedAt); d != "" {
		parts = append(parts, d)
	}
	return "Where: " + strings.Join(parts, " · ")
}

// folder names the repo or folder a chat ran in: the repo of a T3 worktree,
// else the last folder name of the working directory.
func folder(cwd string) string {
	cwd = strings.TrimRight(strings.ReplaceAll(cwd, "\\", "/"), "/")
	if cwd == "" {
		return ""
	}
	if m := worktree.FindStringSubmatch(cwd + "/"); m != nil {
		return m[1]
	}
	return path.Base(cwd)
}

func day(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ""
}

// decided is the latest assistant sentence that states a decision or a cause.
func decided(asst []model.Turn) string {
	for i := len(asst) - 1; i >= 0 && i >= len(asst)-6; i-- {
		var hit string
		for _, s := range sentences(asst[i].Text) {
			if decideWord.MatchString(s) && len(strings.Fields(s)) >= 5 {
				hit = s // keep the last one in the turn
			}
		}
		if hit != "" {
			return Shorten(hit, 170)
		}
	}
	return ""
}

// shipped lists pull requests the assistant says it opened or merged, and the
// commits it says were merged. Pull requests that were only mentioned are left out.
func shipped(asst []model.Turn) string {
	var prs, commits []string
	seen := map[string]bool{}
	for _, t := range asst {
		text := t.Text
		for _, loc := range prURL.FindAllStringIndex(text, -1) {
			u := text[loc[0]:loc[1]]
			if seen[u] {
				continue
			}
			lo, hi := loc[0]-160, loc[1]+160
			if lo < 0 {
				lo = 0
			}
			if hi > len(text) {
				hi = len(text)
			}
			if shipVerb.MatchString(text[lo:hi]) {
				seen[u] = true
				prs = append(prs, u)
			}
		}
		for _, m := range mergedHash.FindAllStringSubmatch(text, -1) {
			h := strings.ToLower(m[1])
			if !hasDigit.MatchString(h) || seen[h[:7]] {
				continue
			}
			seen[h[:7]] = true
			commits = append(commits, h[:7])
		}
	}
	var out []string
	if len(prs) > 0 {
		if len(prs) > 3 {
			prs = prs[len(prs)-3:]
		}
		out = append(out, strings.Join(prs, ", "))
	}
	if len(commits) > 0 {
		if len(commits) > 3 {
			commits = commits[len(commits)-3:]
		}
		out = append(out, "merged "+strings.Join(commits, ", "))
	}
	return strings.Join(out, "; ")
}

// finalState says how the chat ended: the first sentence of the last assistant
// message, or that it ended on a request with no reply.
func finalState(turns []model.Turn) string {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.Role == "assistant" {
			ss := sentences(t.Text)
			for _, s := range ss {
				if len(strings.Fields(s)) >= 3 {
					return "ended with: " + Shorten(s, 160)
				}
			}
			continue
		}
		if t.Role == "user" {
			clean := Text(t.Text)
			if clean == "" {
				continue // a notification or injected block, not a request
			}
			if i == len(turns)-1 {
				return "ended on a request with no reply: " + Shorten(clean, 120)
			}
		}
		break
	}
	return "no reply recorded"
}

// sentences splits text into trimmed sentences, markdown marks and code fences removed.
func sentences(text string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" {
			continue
		}
		line = strings.TrimSpace(markdownLead.ReplaceAllString(line, ""))
		line = strings.NewReplacer("**", "", "`", "").Replace(line)
		for _, s := range splitSentences(line) {
			if s = strings.Join(strings.Fields(s), " "); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
