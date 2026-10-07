package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/usuario/sessions/internal/model"
)

func TestRemoteShowPreservesPathLocators(t *testing.T) {
	for _, tc := range []struct{ input, source, id string }{{"t3code://environment/thread", "", "t3code://environment/thread"}, {"t3code:thread", "t3code", "thread"}} {
		var out, errOut bytes.Buffer
		called := false
		code := showRemoteWithLookup(tc.input, &out, &errOut, func(ctx context.Context, source, id string) ([]model.Session, error) {
			called = true
			if source != tc.source || id != tc.id {
				t.Fatalf("lookup %q %q", source, id)
			}
			return []model.Session{{ID: "thread", Source: "t3code"}}, nil
		})
		if code != 0 || !called {
			t.Fatalf("code=%d error=%s", code, errOut.String())
		}
	}
}
