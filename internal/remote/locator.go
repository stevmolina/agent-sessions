package remote

import (
	"strings"

	"github.com/usuario/sessions/internal/source"
)

// ParseLocator distinguishes a source:id lookup from a complete path locator.
func ParseLocator(id string) (string, string) {
	if !strings.Contains(id, "://") {
		if src, lookup, ok := strings.Cut(id, ":"); ok && source.ValidSource(src) {
			return src, lookup
		}
	}
	return "", id
}
