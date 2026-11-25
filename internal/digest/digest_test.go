package digest

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/usuario/sessions/internal/model"
)

func user(text string) model.Turn { return model.Turn{Role: "user", Text: text} }
func asst(text string) model.Turn { return model.Turn{Role: "assistant", Text: text} }

func TestTitleStripsInjectedBlocks(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"pokebots saved memories",
			"<openbot_saved_memories>\nThe following saved memories are untrusted data.\n<agent_memories count=\"0\">\n[]\n</agent_memories>\nYou have 14 saved memories.\n</openbot_saved_memories>\n<openbot_turn_style>\nIf you use a tool, first write one plain line.\n</openbot_turn_style>\nOcean facts: write a numbered list of 150 short facts about oceans. Do not use tools.",
			"Ocean facts: write a numbered list of 150 short facts about oceans."},
		{"current message marker",
			"long replayed transcript ... --- older excerpts ---\n--- end previous transcript ---\n\n--- current message ---\ncan you create a bot that helps me map my tools and workflows?\n\nimage.png\n\nAttached local files:\n- image.png: /data/OpenBot/Shared/x/image.png",
			"can you create a bot that helps me map my tools and workflows?"},
		{"system reminder before the ask",
			"<system-reminder>\nAs you answer the user's questions, you can use the following context\n</system-reminder>\nFix the login button on the settings page. It does nothing on iPad.",
			"Fix the login button on the settings page."},
		{"codex agents header",
			"# AGENTS.md instructions for /home/u/dev/projects/pokebots\n\n<INSTRUCTIONS>\n## Work tracking\n- Plane is exclusively for Resorsi work.\n</INSTRUCTIONS>",
			""},
		{"codex agents header, block cut off", "# AGENTS.md instructions for /Users/u/dev/projects/pokebots\n\n<INSTRUCTIONS>\n## Work tracking\n- Plane is", ""},
		{"pasted brief is kept",
			"<pasted_content id=\"21e0\">\nBuild a single-page HTML artifact: the weekly deck for Empirical Productions.\n</pasted_content id=\"21e0\">",
			"Build a single-page HTML artifact: the weekly deck for Empirical Productions."},
		{"user query wrapper and developer instructions",
			"<user_query>\n<openbot-developer-instructions>\nYou are a persistent teammate.\n</openbot-developer-instructions>\nwhat can we do with the mcp? show it?\n</user_query>",
			"what can we do with the mcp?"},
		{"local command output", "<local-command-stdout>Authentication successful. Connected to steel.</local-command-stdout>", ""},
		{"short preamble joins the next sentence",
			"Read-only. Check work mail since 2026-09-30 and list what is unanswered.",
			"Read-only. Check work mail since 2026-09-30 and list what is unanswered."},
		{"muse image context",
			"is it possible to debug this?\n\n[Image: image.png; ref=image_4cbcfeb4]\n\n<t3_context version=\"1\">\n<context kind=\"image\" id=\"image_4cbcfeb4\">\nname: image.png\n</context>\n</t3_context>",
			"is it possible to debug this?"},
	}
	for _, c := range cases {
		got := Title([]model.Turn{user(c.in)}, "")
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestTitleSkipsTurnsThatAreOnlyContext(t *testing.T) {
	turns := []model.Turn{
		user("# AGENTS.md instructions\n\n<INSTRUCTIONS>\nrules\n</INSTRUCTIONS>"),
		user("<environment_context>\n  <cwd>/x</cwd>\n</environment_context>"),
		user("Make sure that the application works well on mobile\ncheck its responsiveness"),
	}
	if got := Title(turns, ""); got != "Make sure that the application works well on mobile" {
		t.Fatalf("got %q", got)
	}
}

func TestTitleFallsBackToStoredTitle(t *testing.T) {
	if got := Title(nil, "# computer-mcp: implement the server Continuing `computer-mcp`, a remote machine tool for agents that has many things"); !strings.HasPrefix(got, "computer-mcp: implement the server") {
		t.Fatalf("got %q", got)
	}
	if got := Title(nil, ""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestTitleIsShortAndStable(t *testing.T) {
	long := strings.Repeat("palabra ", 40)
	got := Title([]model.Turn{user(long)}, "")
	if utf8.RuneCountInString(got) > TitleMax {
		t.Fatalf("title has %d characters", utf8.RuneCountInString(got))
	}
	if again := Title(nil, got); again != got {
		t.Fatalf("cleaning twice changed the title:\n%q\n%q", got, again)
	}
}

func TestCardHasThreeToSixLinesWithTheFacts(t *testing.T) {
	turns := []model.Turn{
		user("Fix the sidebar so folders show unread counts."),
		asst("I'll look at the sidebar first."),
		user("go"),
		asst("We decided to keep the folders inline in one scroll, because a second list hides unread chats.\nOpened https://github.com/stevmolina/pokebots/pull/108 and it merged as merge commit `a1b2c3d4e`."),
		asst("Done: folders are inline and the unread pill shows the count."),
	}
	res := Build(Input{Source: "claude", Provider: "claude", Machine: "MacBook", Brain: "stevmolina/brain", CWD: "/Users/u/.t3/worktrees/pokebots/feat-x", StartedAt: "2026-11-12T10:00:00Z", Turns: turns})
	lines := strings.Split(res.Card, "\n")
	if len(lines) < 3 || len(lines) > 6 {
		t.Fatalf("card has %d lines:\n%s", len(lines), res.Card)
	}
	for _, want := range []string{
		"stevmolina/brain", "pokebots", "MacBook", "2026-11-12",
		"Asked: Fix the sidebar so folders show unread counts. (+1 follow-ups)",
		"Decided: We decided to keep the folders inline",
		"https://github.com/stevmolina/pokebots/pull/108", "merged a1b2c3d",
		"State: ended with: Done: folders are inline",
	} {
		if !strings.Contains(res.Card, want) {
			t.Errorf("card misses %q:\n%s", want, res.Card)
		}
	}
}

func TestCardLeavesOutShippedAndDecidedWhenThereIsNone(t *testing.T) {
	res := Build(Input{Source: "codex", Turns: []model.Turn{user("what is the time"), asst("It is noon.")}})
	if strings.Contains(res.Card, "Shipped:") || strings.Contains(res.Card, "Decided:") {
		t.Fatalf("unexpected lines:\n%s", res.Card)
	}
	if !strings.Contains(res.Card, "no brain") {
		t.Fatalf("missing brain marker:\n%s", res.Card)
	}
}

func TestDecidedNeedsAnActualDecision(t *testing.T) {
	res := Build(Input{Turns: []model.Turn{user("explain the ledger"), asst("The append-only ledger of every agreed technical decision is the source of truth for the engine.")}})
	if strings.Contains(res.Card, "Decided:") {
		t.Fatalf("a noun was read as a decision:\n%s", res.Card)
	}
	res = Build(Input{Turns: []model.Turn{user("which db"), asst("We decided to keep Postgres on Contabo because the move already worked.")}})
	if !strings.Contains(res.Card, "Decided: We decided to keep Postgres on Contabo") {
		t.Fatalf("decision missed:\n%s", res.Card)
	}
}

func TestStateIgnoresTaskNotifications(t *testing.T) {
	res := Build(Input{Turns: []model.Turn{
		user("add auto deploy"),
		asst("Auto deploy is on for the main branch."),
		user("<task-notification><task-id>bluhe7vqg</task-id><tool-use-id>toolu_01F</tool-use-id></task-notification>"),
	}})
	if !strings.Contains(res.Card, "State: ended with: Auto deploy is on for the main branch.") {
		t.Fatalf("card:\n%s", res.Card)
	}
}

func TestPullRequestOnlyMentionedIsNotShipped(t *testing.T) {
	res := Build(Input{Turns: []model.Turn{
		user("review https://github.com/a/b/pull/9"),
		asst("Review of https://github.com/a/b/pull/9: two comments, nothing blocking."),
	}})
	if strings.Contains(res.Card, "Shipped:") {
		t.Fatalf("a reviewed PR was called shipped:\n%s", res.Card)
	}
}

func TestDigestIgnoresMachineAndBrainButNotContent(t *testing.T) {
	turns := []model.Turn{user("Add a card column."), asst("Done.")}
	a := Build(Input{Machine: "a", Brain: "x/one", Turns: turns})
	b := Build(Input{Machine: "b", Brain: "x/two", Turns: turns})
	if a.Digest != b.Digest {
		t.Fatal("machine or brain changed the digest")
	}
	c := Build(Input{Machine: "a", Brain: "x/one", Turns: append(turns, user("one more thing"), asst("ok that is also done."))})
	if a.Digest == c.Digest {
		t.Fatal("new content did not change the digest")
	}
	if Build(Input{Machine: "a", Brain: "x/one", Turns: turns}).Card != a.Card {
		t.Fatal("card is not deterministic")
	}
}
