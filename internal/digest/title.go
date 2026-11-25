// Package digest turns a cleaned transcript into a short title and a session
// card. Both are deterministic: the same turns always give the same text, and
// nothing here reads a file or calls a model. The input is turns that were
// already redacted, so the output carries no secret the turns did not.
package digest

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/usuario/sessions/internal/model"
)

// TitleMax is the longest title, in characters, ellipsis included.
const TitleMax = 80

// Blocks that agents and apps inject around what the person typed. A block is
// removed whole, tags and content. The names are the ones seen in real chats;
// the prefix rules catch the families (openbot_*, t3_code_*) that grow new names.
var (
	dropOpener = regexp.MustCompile(`(?i)<(system-reminder|openbot[_-][a-z0-9_-]*|t3[_-]code[_-][a-z0-9_-]*|t3_context|user_info|rules|user_rules|user_rule|fork-boilerplate|environment_context|local-command-(?:stdout|stderr|caveat)|command-(?:name|message|args)|in-app-browser-context|skill|runtime_info|pull_request_linking|user-prompt-submit-hook|hook_output|hook_context|permissions_instructions|collaboration_mode|agent_memories|attachments?|context|timestamp|developer-instructions|task-notification|system-notification)(?:\s[^>]*)?>`)

	agentsHeader   = regexp.MustCompile(`(?is)#\s*AGENTS\.md instructions[^\n]*\n\s*<INSTRUCTIONS>.*?</INSTRUCTIONS>`)
	agentsOpen     = regexp.MustCompile(`(?is)^\s*#\s*AGENTS\.md instructions`)
	agentsBare     = regexp.MustCompile(`(?is)<INSTRUCTIONS>.*?</INSTRUCTIONS>`)
	pasted         = regexp.MustCompile(`(?is)<pasted_content[^>]*>(.*?)</pasted_content[^>]*>`)
	userQuery      = regexp.MustCompile(`(?is)<user_query>\s*(.*?)\s*</user_query>`)
	hookLine       = regexp.MustCompile(`(?im)^.*\bhook (?:additional context|output|feedback)\b.*$`)
	attachedLine   = regexp.MustCompile(`(?im)^\s*(?:\[(?:attached image|attachment|image|messageid)[^\]]*\]|attached (?:local )?files?:.*|- [^\n]*: /[^\n]*|!\[[^\]]*\]\([^)]*\))\s*$`)
	stamp          = regexp.MustCompile(`(?im)^\s*\[\d{4}-\d{2}-\d{2}T[^\]]*\]\s*(?:user|assistant):?\s*$`)
	currentMessage = "--- current message ---"
	anyTag         = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9_:-]*(?:\s[^>]*)?>`)
	sentenceEnd    = regexp.MustCompile(`([.!?])\s+`)
	markdownLead   = regexp.MustCompile(`^[#>*\-\s]+`)
	rulesLead      = regexp.MustCompile(`(?i)^(do not|don't|never|no )`)
	actAs          = regexp.MustCompile(`(?i)^act as (the|a) [a-z -]*(sub-?agent|worker)[a-z ,]*\.?$`)
)

// Text removes injected blocks, attachment lines and wrappers from one user
// turn and returns what the person actually asked, whitespace trimmed. It
// returns "" when nothing real is left.
func Text(in string) string {
	s := in
	if i := strings.LastIndex(s, currentMessage); i >= 0 {
		s = s[i+len(currentMessage):]
	}
	s = agentsHeader.ReplaceAllString(s, " ")
	s = agentsBare.ReplaceAllString(s, " ")
	// An instructions header whose block was cut off: the whole turn is rules.
	if agentsOpen.MatchString(s) {
		return ""
	}
	// A pasted block is the person's own text: keep it, tags removed, after
	// anything they typed outside it.
	var inner []string
	s = pasted.ReplaceAllStringFunc(s, func(m string) string {
		if sub := pasted.FindStringSubmatch(m); len(sub) > 1 {
			inner = append(inner, sub[1])
		}
		return " "
	})
	if m := userQuery.FindStringSubmatch(s); len(m) > 1 {
		s = m[1]
	}
	s = stripBlocks(s)
	s = hookLine.ReplaceAllString(s, "")
	s = stamp.ReplaceAllString(s, "")
	s = attachedLine.ReplaceAllString(s, "")
	s = tidy(s)
	if s == "" {
		for _, p := range inner {
			if t := tidy(stripBlocks(p)); t != "" {
				return t
			}
		}
	}
	return s
}

// stripBlocks removes each injected block whole, from its opening tag to the
// closing tag of the same name, so nested blocks of other names go with it. An
// opener with no closer drops only the tag; at the very start it drops the rest,
// since an unclosed injected block runs to the end of the text.
func stripBlocks(s string) string {
	for guard := 0; guard < 200; guard++ {
		loc := dropOpener.FindStringSubmatchIndex(s)
		if loc == nil {
			return s
		}
		name := strings.ToLower(s[loc[2]:loc[3]])
		rest := strings.ToLower(s[loc[1]:])
		end := strings.Index(rest, "</"+name)
		if end < 0 {
			if strings.TrimSpace(s[:loc[0]]) == "" {
				return ""
			}
			s = s[:loc[0]] + " " + s[loc[1]:]
			continue
		}
		after := loc[1] + end
		if gt := strings.Index(s[after:], ">"); gt >= 0 {
			after += gt + 1
		} else {
			after = len(s)
		}
		s = s[:loc[0]] + " " + s[after:]
	}
	return s
}

func tidy(s string) string {
	s = anyTag.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Title is the first real request in the chat, as one short line. It looks at
// user turns in order and skips turns that were only injected context. A chat
// with no real user text falls back to fallback (a stored thread title, say),
// cleaned the same way, and then to "".
func Title(turns []model.Turn, fallback string) string {
	for _, t := range turns {
		if t.Role != "user" {
			continue
		}
		if line := firstRequest(Text(t.Text)); line != "" {
			return line
		}
	}
	if line := firstRequest(Text(fallback)); line != "" {
		return line
	}
	return ""
}

// firstRequest picks the first sentence that says something, capped at TitleMax.
func firstRequest(text string) string {
	if text == "" {
		return ""
	}
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(markdownLead.ReplaceAllString(line, ""))
		if line == "" {
			continue
		}
		parts = append(parts, splitSentences(line)...)
		if len(parts) >= 4 {
			break
		}
	}
	var picked []string
	for _, p := range parts {
		p = strings.Join(strings.Fields(p), " ")
		if p == "" {
			continue
		}
		if actAs.MatchString(p) && len(parts) > 1 {
			continue // "Act as the implementation sub-agent for this task." says nothing
		}
		if len(picked) > 0 && rulesLead.MatchString(p) {
			break // "Read-only audit." then "Do not edit files": the rules are not the topic
		}
		picked = append(picked, p)
		if len(strings.Fields(p)) < 4 && len(picked) < len(parts) {
			continue // too short to be a title alone: "Read-only."
		}
		break
	}
	title := strings.Join(picked, " ")
	return Shorten(title, TitleMax)
}

func splitSentences(line string) []string {
	var out []string
	last := 0
	for _, loc := range sentenceEnd.FindAllStringSubmatchIndex(line, -1) {
		end := loc[3] // after the punctuation
		out = append(out, line[last:end])
		last = loc[1]
	}
	if last < len(line) {
		out = append(out, line[last:])
	}
	return out
}

// Shorten cuts s to max characters on a word boundary and adds an ellipsis.
// A string already within max is returned unchanged, so shortening twice is safe.
func Shorten(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	cut := string(r[:max-1])
	if i := strings.LastIndex(cut, " "); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.-") + "…"
}
