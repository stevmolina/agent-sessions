package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LoadState reads what was last announced. A missing file is an empty state.
func LoadState(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	state := map[string]string{}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, errors.New("alert state file is not valid JSON: " + path)
	}
	return state, nil
}

// SaveState writes the state atomically.
func SaveState(path string, state map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SlackURL is the chat.postMessage endpoint. Tests point it at a local server.
var SlackURL = "https://slack.com/api/chat.postMessage"

// PostSlack sends text to a channel with a bot token. The token is never part
// of an error.
func PostSlack(ctx context.Context, token, channel, text string) error {
	body, err := json.Marshal(map[string]any{"channel": channel, "text": text, "unfurl_links": false})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SlackURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("slack request failed")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("slack unreachable")
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return errors.New("slack returned an unreadable response")
	}
	if !out.OK {
		return errors.New("slack refused the message: " + out.Error)
	}
	return nil
}
