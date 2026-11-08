package mcpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fixture struct{ server *httptest.Server }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	server := httptest.NewServer(NewHandler())
	t.Cleanup(server.Close)
	return &fixture{server: server}
}

func (f *fixture) do(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (f *fixture) get(t *testing.T, path string) *http.Response {
	t.Helper()
	return f.do(t, http.MethodGet, path, "")
}

// Every old MCP and OAuth discovery path, any method, says where the tools
// went instead of asking the client to sign in.
func TestMovedPathsAnswerGone(t *testing.T) {
	f := newFixture(t)
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/mcp", initialize},
		{http.MethodGet, "/mcp", ""},
		{http.MethodDelete, "/mcp", ""},
		{http.MethodOptions, "/mcp", ""},
		{http.MethodPost, "/mcp/", initialize},
		{http.MethodGet, "/.well-known/oauth-protected-resource", ""},
		{http.MethodGet, "/.well-known/oauth-protected-resource/mcp", ""},
	}
	for _, c := range cases {
		resp := f.do(t, c.method, c.path, c.body)
		if resp.StatusCode != http.StatusGone {
			t.Errorf("%s %s: status %d", c.method, c.path, resp.StatusCode)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s %s: content-type %q", c.method, c.path, ct)
		}
		if resp.Header.Get("WWW-Authenticate") != "" {
			t.Errorf("%s %s: still asks for a token", c.method, c.path)
		}
		var body map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		want := map[string]string{
			"error":   "moved",
			"message": "The sessions MCP moved into Agent Brain. Use https://brain.mcpbox.dev/mcp (tools search_sessions, show_session).",
			"url":     "https://brain.mcpbox.dev/mcp",
		}
		for k, v := range want {
			if body[k] != v {
				t.Errorf("%s %s: %s=%q, want %q", c.method, c.path, k, body[k], v)
			}
		}
	}
	// Paths that only look similar are not caught.
	if resp := f.get(t, "/mcpx"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/mcpx status %d", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	f := newFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		resp := f.do(t, method, "/healthz", "")
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		want := "ok\n"
		if method == http.MethodHead {
			want = ""
		}
		if resp.StatusCode != http.StatusOK || string(body) != want {
			t.Fatalf("%s health %d %q", method, resp.StatusCode, body)
		}
	}
}

func TestPublicPages(t *testing.T) {
	f := newFixture(t)
	resp := f.get(t, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{
		"<title>Agent Sessions | One memory for every coding agent</title>",
		`name="description" content="Agent Sessions remembers every conversation you have with Claude Code, Cursor, Codex and more. Any AI you use can pick up right where the last one stopped."`,
		`rel="canonical" href="https://sessions.mcpbox.dev/"`,
		`property="og:title" content="Agent Sessions | One memory for every coding agent"`,
		`property="og:description" content="Agent Sessions remembers every conversation you have with Claude Code, Cursor, Codex and more. Any AI you use can pick up right where the last one stopped."`,
		`property="og:type" content="website"`,
		`property="og:url" content="https://sessions.mcpbox.dev/"`,
		`property="og:site_name" content="Agent Sessions"`,
		`name="twitter:card" content="summary"`,
		`name="twitter:title" content="Agent Sessions | One memory for every coding agent"`,
		`name="twitter:description" content="Agent Sessions remembers every conversation you have with Claude Code, Cursor, Codex and more. Any AI you use can pick up right where the last one stopped."`,
		`"@type":"SoftwareApplication"`,
		`"name":"Agent Sessions"`,
		`"url":"https://sessions.mcpbox.dev/"`,
		`"jobTitle":"Forward Deployed AI Engineer"`,
		`"url":"https://stevmolina.com"`,
		`"https://x.com/stev_molina"`,
		`"https://www.linkedin.com/in/steven-ml"`,
		`"https://github.com/stevmolina"`,
		"Start in one.",
		"Finish in another.",
		"All your history, one search away",
		"Made for how you work",
		"Connect in a minute",
		"the login bug we fixed last week",
		`data-copy="https://sessions.mcpbox.dev/mcp"`,
		`href="https://stevmolina.com">Made by Steven Molina</a>`,
		`href="https://computer.mcpbox.dev">Agent Computer</a>`,
		`href="https://brain.mcpbox.dev">Agent Brain</a>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(html, "MCP") {
		t.Fatal("landing page contains the word MCP")
	}
	var doc map[string]any
	start := strings.Index(html, `<script type="application/ld+json">`)
	end := strings.Index(html, `</script>`)
	if start < 0 || end < start {
		t.Fatal("missing json-ld")
	}
	raw := html[start+len(`<script type="application/ld+json">`) : end]
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["@type"] != "SoftwareApplication" || doc["name"] != "Agent Sessions" || doc["url"] != "https://sessions.mcpbox.dev/" {
		t.Fatalf("json-ld %v", doc)
	}
	creator, _ := doc["creator"].(map[string]any)
	if creator["@type"] != "Person" || creator["name"] != "Steven Molina" || creator["jobTitle"] != "Forward Deployed AI Engineer" || creator["url"] != "https://stevmolina.com" {
		t.Fatalf("creator %v", creator)
	}
	headReq, err := http.NewRequest(http.MethodHead, f.server.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatal(err)
	}
	defer headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK || headResp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("HEAD status %d content-type %q", headResp.StatusCode, headResp.Header.Get("Content-Type"))
	}
	headBody, err := io.ReadAll(headResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(headBody) != 0 {
		t.Fatalf("HEAD body %q", headBody)
	}

	pages := map[string][]string{
		"/robots.txt":  {"User-agent: *", "Allow: /", "Sitemap: https://sessions.mcpbox.dev/sitemap.xml"},
		"/sitemap.xml": {"<loc>https://sessions.mcpbox.dev/</loc>"},
		"/llms.txt": {
			"Search and read past Claude Code, Codex, and Cursor sessions from any AI.",
			"Steven Molina, Forward Deployed AI Engineer",
			"https://stevmolina.com",
			"Agent Computer: https://computer.mcpbox.dev",
			"Agent Brain: https://brain.mcpbox.dev",
			"https://sessions.mcpbox.dev/",
		},
	}
	for path, wants := range pages {
		page := f.get(t, path)
		if page.StatusCode != http.StatusOK {
			t.Fatalf("%s status %d", path, page.StatusCode)
		}
		text, err := io.ReadAll(page.Body)
		if err != nil {
			t.Fatal(err)
		}
		got := string(text)
		if strings.Contains(got, "MCP") {
			t.Fatalf("%s contains the word MCP", path)
		}
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Fatalf("%s missing %q", path, want)
			}
		}
		if path == "/sitemap.xml" && strings.Contains(got, "/mcp") {
			t.Fatalf("sitemap lists the private route: %s", got)
		}
	}

	css := f.get(t, "/_astro/index.DOflx1QF.css")
	if css.StatusCode != http.StatusOK || css.Header.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("css %d %q", css.StatusCode, css.Header.Get("Content-Type"))
	}
	icon := f.get(t, "/favicon.svg")
	if icon.StatusCode != http.StatusOK || icon.Header.Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("icon %d %q", icon.StatusCode, icon.Header.Get("Content-Type"))
	}
	missingAsset := f.get(t, "/_astro/missing.webp")
	if missingAsset.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset %d", missingAsset.StatusCode)
	}

	health := f.get(t, "/healthz")
	healthBody, err := io.ReadAll(health.Body)
	if err != nil {
		t.Fatal(err)
	}
	if health.StatusCode != http.StatusOK || string(healthBody) != "ok\n" {
		t.Fatalf("health %d %q", health.StatusCode, healthBody)
	}
	missing := f.get(t, "/missing")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status %d", missing.StatusCode)
	}
}
