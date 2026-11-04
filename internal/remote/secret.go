package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
)

const (
	dopplerProject = "sessions"
	dopplerConfig  = "dev"
)

// Secret reads a Doppler value for the sessions project. A value already in
// the environment wins, which is how `doppler run` and tests supply it.
// The value is never included in an error.
func Secret(ctx context.Context, name string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, nil
	}
	// One push opens the database twice (rows, then heartbeat). Each Doppler CLI
	// call costs about a second, so a value is read once per process.
	if value, ok := secretCache.Load(name); ok {
		return value.(string), nil
	}
	config := dopplerConfig
	if value := strings.TrimSpace(os.Getenv("SESSIONS_DOPPLER_CONFIG")); value != "" {
		config = value
	}
	value, err := DopplerSecret(ctx, name, dopplerProject, config)
	if err != nil {
		return "", errors.New("remote database unavailable")
	}
	secretCache.Store(name, value)
	return value, nil
}

var secretCache sync.Map

// DopplerSecret reads one value with the logged-in Doppler CLI. An environment
// variable of the same name wins. Neither stderr nor the value reaches an error.
func DopplerSecret(ctx context.Context, name, project, config string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, nil
	}
	cmd := exec.CommandContext(ctx, "doppler", "secrets", "get", name, "-p", project, "-c", config, "--plain")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = ioDiscard{}
	if err := cmd.Run(); err != nil {
		return "", errors.New("doppler could not read " + name + " from " + project + "/" + config)
	}
	value := strings.TrimSpace(stdout.String())
	if value == "" {
		return "", errors.New("doppler returned an empty " + name)
	}
	return value, nil
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
