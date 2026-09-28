package clean

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// Runner executes a Doppler CLI invocation and returns stdout only.
type Runner interface {
	Output(ctx context.Context, args ...string) ([]byte, error)
}

type execRunner struct {
	bin string
}

func (e execRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = ioDiscard{}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("doppler secrets unavailable (exit %d)", exit.ExitCode())
		}
		return nil, errors.New("doppler secrets unavailable")
	}
	return stdout.Bytes(), nil
}

// ioDiscard drops stderr so a Doppler error cannot echo a secret into logs.
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

// Load reads exact-match secrets. SESSIONS_SECRETS_JSON, when set to a file
// path, replaces Doppler. Tests use that. A normal index leaves it unset.
func Load(ctx context.Context) (*Cleaner, error) {
	if path := strings.TrimSpace(os.Getenv("SESSIONS_SECRETS_JSON")); path != "" {
		return loadFile(path)
	}
	bin, err := exec.LookPath("doppler")
	if err != nil {
		return nil, errors.New("doppler secrets unavailable")
	}
	return LoadFrom(ctx, execRunner{bin: bin})
}

// LoadFrom reads every project and config the runner can list.
func LoadFrom(ctx context.Context, r Runner) (*Cleaner, error) {
	secrets, err := fetchSecrets(ctx, r)
	if err != nil {
		return nil, err
	}
	return New(secrets)
}

func loadFile(path string) (*Cleaner, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("secrets file unavailable")
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New("secrets file unavailable")
	}
	return New(m)
}

type secretPayload struct {
	project string
	config  string
	vals    map[string]string
}

func fetchSecrets(ctx context.Context, r Runner) (map[string]string, error) {
	raw, err := r.Output(ctx, "projects", "--json")
	if err != nil {
		return nil, err
	}
	projects, err := decodeNames(raw)
	if err != nil {
		return nil, errors.New("doppler project list unavailable")
	}
	sort.Strings(projects)

	var mu sync.Mutex
	var configs []secretPayload
	fns := make([]func() error, 0, len(projects))
	for _, project := range projects {
		project := project
		fns = append(fns, func() error {
			body, err := r.Output(ctx, "configs", "--project", project, "--json")
			if err != nil {
				return fmt.Errorf("project %s: %w", project, err)
			}
			names, err := decodeNames(body)
			if err != nil {
				return errors.New("doppler config list unavailable")
			}
			mu.Lock()
			for _, name := range names {
				configs = append(configs, secretPayload{project: project, config: name})
			}
			mu.Unlock()
			return nil
		})
	}
	if err := runParallel(ctx, 8, fns); err != nil {
		return nil, err
	}
	sort.Slice(configs, func(i, j int) bool {
		if configs[i].project != configs[j].project {
			return configs[i].project < configs[j].project
		}
		return configs[i].config < configs[j].config
	})

	fns = fns[:0]
	for i := range configs {
		i := i
		fns = append(fns, func() error {
			body, err := r.Output(ctx, "secrets", "download", "--project", configs[i].project, "--config", configs[i].config, "--no-file", "--format", "json")
			if err != nil {
				return fmt.Errorf("config %s/%s: %w", configs[i].project, configs[i].config, err)
			}
			var vals map[string]string
			if err := json.Unmarshal(body, &vals); err != nil {
				return errors.New("doppler secret payload unavailable")
			}
			mu.Lock()
			configs[i].vals = vals
			mu.Unlock()
			return nil
		})
	}
	if err := runParallel(ctx, 8, fns); err != nil {
		return nil, err
	}

	out := map[string]string{}
	for _, cfg := range configs {
		names := make([]string, 0, len(cfg.vals))
		for name := range cfg.vals {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := addSecret(out, cfg.project, cfg.config, name, cfg.vals[name]); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func addSecret(dst map[string]string, project, config, name, value string) error {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(name, "DOPPLER_") || !usable(value) {
		return nil
	}
	candidates := []string{
		sanitizeLabel(name),
		sanitizeLabel(project + "/" + name),
		sanitizeLabel(project + "/" + config + "/" + name),
	}
	for _, label := range candidates {
		prev, ok := dst[label]
		if !ok {
			dst[label] = value
			return nil
		}
		if prev == value {
			return nil
		}
	}
	return errors.New("doppler secret label collision")
}

func decodeNames(raw []byte) ([]string, error) {
	var rows []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Name != "" {
			out = append(out, row.Name)
		}
	}
	return out, nil
}

func runParallel(ctx context.Context, limit int, fns []func() error) error {
	if len(fns) == 0 {
		return nil
	}
	if limit < 1 {
		limit = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	for _, fn := range fns {
		fn := fn
		select {
		case <-ctx.Done():
			wg.Wait()
			select {
			case err := <-errCh:
				return err
			default:
				return ctx.Err()
			}
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(); err != nil {
				select {
				case errCh <- err:
					cancel()
				default:
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
