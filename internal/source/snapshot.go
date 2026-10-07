package source

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/usuario/sessions/internal/config"
)

// CheckSnapshot refuses deletion when a previously indexed source disappears.
// Missing roots that have never contributed rows remain optional.
func CheckSnapshot(locators []string) error {
	roots := []string{config.CursorRoot(), config.ClaudeRoot(), config.CodexRoot(), config.CodexArchiveRoot()}
	for _, locator := range locators {
		if strings.HasPrefix(locator, "t3code://") {
			db, envID, err := t3DB()
			if err != nil || db == "" {
				return errors.New("previously indexed T3 database unavailable")
			}
			previousEnv, _, ok := strings.Cut(strings.TrimPrefix(locator, "t3code://"), "/")
			if !ok || previousEnv != envID {
				return errors.New("previously indexed T3 environment is no longer configured")
			}
			continue
		}
		found := false
		for _, root := range roots {
			rel, err := filepath.Rel(root, locator)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			found = true
			st, err := os.Stat(root)
			if err != nil || !st.IsDir() {
				return errors.New("previously indexed transcript root unavailable")
			}
			break
		}
		if !found {
			return errors.New("previously indexed transcript root is no longer configured")
		}
	}
	return nil
}
