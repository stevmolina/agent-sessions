// Package repotag works out which GitHub repos a chat belongs to, from facts
// every chat has: the folder it ran in, and the commands and files its tool
// calls touched. It reads git config files only. It never runs git and never
// uses the network.
package repotag

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/usuario/sessions/internal/store"
)

// MaxRepos caps the tags on one chat.
const MaxRepos = 10

// pathMentions is how many separate tool calls must name a checkout before a
// path alone tags its repo. A single read of a shared skill file is noise; a
// chat that works in a repo touches it many times.
const pathMentions = 3

// Resolver turns folders into repos and caches the answer per folder, so a
// push over thousands of chats reads each git config once.
type Resolver struct {
	home string
	mu   sync.Mutex
	dirs map[string]string // folder -> repo, "" when none
}

// New returns a Resolver. home is skipped as a repo root, so a dotfiles repo in
// the home folder does not tag every chat that ran under it.
func New() *Resolver {
	home, _ := os.UserHomeDir()
	return &Resolver{home: filepath.Clean(home), dirs: map[string]string{}}
}

var reserved = map[string]bool{
	"orgs": true, "settings": true, "topics": true, "marketplace": true, "features": true,
	"apps": true, "sponsors": true, "users": true, "notifications": true, "login": true,
	"about": true, "pricing": true, "enterprise": true, "search": true, "new": true,
	"collections": true, "explore": true, "trending": true, "codespaces": true,
	"issues": true, "pulls": true, "actions": true, "repos": true, "user": true,
}

const nameChars = `[A-Za-z0-9][A-Za-z0-9_.-]*`

var (
	// github.com/o/n, github.com:o/n (ssh), api.github.com/repos/o/n
	urlRE = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9.-])(?:api\.)?github\.com[/:](?:repos/)?(` + nameChars + `)/(` + nameChars + `)`)
	// gh ... -R o/n, --repo o/n, --repo=o/n
	flagRE = regexp.MustCompile(`(?:^|\s)(?:-R|--repo)[\s=]+["']?(` + nameChars + `)/(` + nameChars + `)`)
	// gh repo clone|view|fork o/n, gh api repos/o/n
	ghRE = regexp.MustCompile(`\bgh\s+(?:repo\s+(?:clone|view|fork|sync|edit)\s+|api\s+(?:-\S+\s+)*["']?/?repos/)["']?/?(` + nameChars + `)/(` + nameChars + `)`)
	// git clone [opts] o/n is not valid git; clone URLs are caught by urlRE.
	pathRE = regexp.MustCompile(`(?:^|[\s"'=(])((?:~|/)[A-Za-z0-9_@%+=:,.~/-]+)`)
)

// NormalizeRemote turns a git remote URL into "owner/name" in lower case.
// Only github.com remotes count.
func NormalizeRemote(url string) (string, bool) {
	url = strings.TrimSpace(url)
	m := urlRE.FindStringSubmatch(url)
	if m == nil {
		return "", false
	}
	return clean(m[1], m[2])
}

func clean(owner, name string) (string, bool) {
	name = strings.TrimSuffix(strings.TrimRight(name, "."), ".git")
	owner = strings.TrimRight(owner, ".")
	if owner == "" || name == "" || reserved[strings.ToLower(owner)] {
		return "", false
	}
	return strings.ToLower(owner + "/" + name), true
}

// FromText finds repos a command or tool input names outright: gh -R, gh repo
// clone, git clone and github.com URLs.
func FromText(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(m []string) {
		if repo, ok := clean(m[1], m[2]); ok && !seen[repo] {
			seen[repo] = true
			out = append(out, repo)
		}
	}
	for _, re := range []*regexp.Regexp{urlRE, flagRE, ghRE} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			add(m)
		}
	}
	return out
}

// Dir returns the GitHub repo of the checkout that holds dir, or "". A folder
// that does not exist on this machine is resolved through its nearest parent
// that does. Worktrees resolve to the repo they belong to.
func (r *Resolver) Dir(dir string) string {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." || !filepath.IsAbs(dir) {
		return ""
	}
	r.mu.Lock()
	if repo, ok := r.dirs[dir]; ok {
		r.mu.Unlock()
		return repo
	}
	r.mu.Unlock()
	var walked []string
	repo := ""
	for cur := dir; ; cur = filepath.Dir(cur) {
		r.mu.Lock()
		cached, ok := r.dirs[cur]
		r.mu.Unlock()
		if ok {
			repo = cached
			break
		}
		walked = append(walked, cur)
		if cur != r.home {
			if cfg := gitConfigPath(cur); cfg != "" {
				repo = repoFromConfig(cfg)
				break
			}
		}
		if parent := filepath.Dir(cur); parent == cur {
			break
		}
	}
	r.mu.Lock()
	for _, d := range walked {
		r.dirs[d] = repo
	}
	r.mu.Unlock()
	return repo
}

// gitConfigPath returns the config file of the repo whose working tree root is
// dir, or "" when dir is not a root. A worktree's .git is a file that points at
// the main repo, whose config holds the remotes.
func gitConfigPath(dir string) string {
	dot := filepath.Join(dir, ".git")
	info, err := os.Lstat(dot)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return filepath.Join(dot, "config")
	}
	raw, err := os.ReadFile(dot)
	if err != nil {
		return ""
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir := strings.TrimSpace(target)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	if common, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		c := strings.TrimSpace(string(common))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitdir, c)
		}
		return filepath.Join(c, "config")
	}
	return filepath.Join(gitdir, "config")
}

// repoFromConfig reads a git config. origin wins when it is on GitHub; else the
// first other GitHub remote by name.
func repoFromConfig(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return RepoFromConfigText(string(raw))
}

// RepoFromConfigText is repoFromConfig for config text.
func RepoFromConfigText(text string) string {
	remotes := map[string]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = ""
			if rest, ok := strings.CutPrefix(line, `[remote "`); ok {
				if end := strings.Index(rest, `"`); end >= 0 {
					section = rest[:end]
				}
			}
			continue
		}
		if section == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "url" {
			if _, seen := remotes[section]; !seen {
				if repo, ok := NormalizeRemote(strings.TrimSpace(value)); ok {
					remotes[section] = repo
				}
			}
		}
	}
	if repo, ok := remotes["origin"]; ok {
		return repo
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return remotes[names[0]]
	}
	return ""
}

// CWD tags a chat by the folder it ran in, and its worktree. It is cheap.
func (r *Resolver) CWD(row store.ExportRow) []string {
	var out []string
	for _, dir := range []*string{row.CWD, row.WorktreePath} {
		if dir == nil {
			continue
		}
		if repo := r.Dir(*dir); repo != "" && !contains(out, repo) {
			out = append(out, repo)
		}
	}
	return out
}

// ForPush is the shape the push wants: the full pass when asked, else the cheap one.
func (r *Resolver) ForPush(row store.ExportRow, full bool) []string {
	if full {
		return r.Full(row)
	}
	return r.CWD(row)
}

// Full tags a chat by its folder first, then by what its tool calls touched.
// The transcript file is read from disk, so this costs a pass over the file.
func (r *Resolver) Full(row store.ExportRow) []string {
	out := r.CWD(row)
	for _, path := range []string{row.Path, row.OriginPath} {
		if strings.HasSuffix(path, ".jsonl") {
			if _, err := os.Stat(path); err == nil {
				out = r.merge(out, r.scanFile(path))
				break
			}
		}
	}
	return out
}

func (r *Resolver) merge(base []string, found []string) []string {
	for _, repo := range found {
		if len(base) >= MaxRepos {
			break
		}
		if !contains(base, repo) {
			base = append(base, repo)
		}
	}
	return base
}

// scanFile reads tool calls from a Claude, Codex or Cursor transcript. Named
// repos count at once. A repo found only through a path needs pathMentions
// separate tool calls.
func (r *Resolver) scanFile(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	named := []string{}
	namedSeen := map[string]bool{}
	pathCount := map[string]int{}
	var pathOrder []string
	reader := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && len(line) < 32<<20 && hasToolMarker(line) {
			for _, call := range toolInputs(line) {
				for _, repo := range FromText(call) {
					if !namedSeen[repo] {
						namedSeen[repo] = true
						named = append(named, repo)
					}
				}
				seenInCall := map[string]bool{}
				for _, p := range pathsIn(call, r.home) {
					if repo := r.Dir(p); repo != "" && !seenInCall[repo] {
						seenInCall[repo] = true
						if pathCount[repo] == 0 {
							pathOrder = append(pathOrder, repo)
						}
						pathCount[repo]++
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	out := named
	sort.SliceStable(pathOrder, func(i, j int) bool { return pathCount[pathOrder[i]] > pathCount[pathOrder[j]] })
	for _, repo := range pathOrder {
		if pathCount[repo] >= pathMentions && !contains(out, repo) {
			out = append(out, repo)
		}
	}
	return out
}

var toolMarkers = [][]byte{[]byte(`tool_use`), []byte(`function_call`), []byte(`custom_tool_call`), []byte(`local_shell_call`), []byte(`"toolCall"`)}

func hasToolMarker(line []byte) bool {
	for _, m := range toolMarkers {
		if bytes.Contains(line, m) {
			return true
		}
	}
	return false
}

// inputKeys are the tool input fields that hold commands and paths. Prose
// fields (file contents, prompts, descriptions) are left out on purpose.
var inputKeys = map[string]bool{
	"command": true, "cmd": true, "file_path": true, "path": true, "url": true,
	"cwd": true, "workdir": true, "directory": true, "notebook_path": true,
}

// toolInputs returns one string per tool call on a transcript line, made of the
// command and path fields of that call's input.
func toolInputs(line []byte) []string {
	var v any
	if err := json.Unmarshal(line, &v); err != nil {
		return nil
	}
	var out []string
	var walk func(any, int)
	walk = func(n any, depth int) {
		if depth > 8 {
			return
		}
		switch t := n.(type) {
		case map[string]any:
			typ, _ := t["type"].(string)
			switch {
			case typ == "tool_use" || typ == "toolCall":
				if in, ok := t["input"]; ok {
					out = append(out, flatten(in))
				} else if in, ok := t["arguments"]; ok {
					out = append(out, flatten(in))
				}
				return
			case typ == "function_call" || typ == "custom_tool_call":
				if in, ok := t["arguments"]; ok {
					out = append(out, flatten(in))
				} else if in, ok := t["input"]; ok {
					out = append(out, flatten(in))
				}
				return
			case typ == "local_shell_call":
				if in, ok := t["action"]; ok {
					out = append(out, flatten(in))
				}
				return
			}
			for _, child := range t {
				walk(child, depth+1)
			}
		case []any:
			for _, child := range t {
				walk(child, depth+1)
			}
		}
	}
	walk(v, 0)
	return out
}

// flatten joins the command and path fields of one tool input. Codex sends
// arguments as a JSON string, so a string is parsed first.
func flatten(in any) string {
	if s, ok := in.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(s), &parsed) == nil {
			in = parsed
		} else {
			return clip(s)
		}
	}
	var parts []string
	var collect func(any, string, int)
	collect = func(n any, key string, depth int) {
		if depth > 4 {
			return
		}
		switch t := n.(type) {
		case string:
			if inputKeys[key] {
				parts = append(parts, clip(t))
			}
		case []any:
			for _, c := range t {
				collect(c, key, depth+1)
			}
		case map[string]any:
			for k, c := range t {
				collect(c, k, depth+1)
			}
		}
	}
	collect(in, "", 0)
	return strings.Join(parts, "\n")
}

func clip(s string) string {
	if len(s) > 8192 {
		return s[:8192]
	}
	return s
}

// pathsIn lists absolute and ~/ paths in a command, for resolving checkouts.
func pathsIn(text, home string) []string {
	var out []string
	for _, m := range pathRE.FindAllStringSubmatch(text, 200) {
		p := strings.TrimRight(m[1], ".,:;")
		switch {
		case p == "~":
			p = home
		case strings.HasPrefix(p, "~/"):
			p = filepath.Join(home, p[2:])
		case strings.HasPrefix(p, "//"):
			continue
		}
		if filepath.IsAbs(p) && len(p) > 1 {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
