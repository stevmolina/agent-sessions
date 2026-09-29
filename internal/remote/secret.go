package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
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
	config := dopplerConfig
	if value := strings.TrimSpace(os.Getenv("SESSIONS_DOPPLER_CONFIG")); value != "" {
		config = value
	}
	cmd := exec.CommandContext(ctx, "doppler", "secrets", "get", name, "-p", dopplerProject, "-c", config, "--plain")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = ioDiscard{}
	if err := cmd.Run(); err != nil {
		return "", errors.New("remote database unavailable")
	}
	value := strings.TrimSpace(stdout.String())
	if value == "" {
		return "", errors.New("remote database unavailable")
	}
	return value, nil
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
