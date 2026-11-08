package cli

import (
	"bytes"
	"strings"
	"testing"
)

// `sessions mcp` needs no secrets now: the old auth variables are ignored,
// and only PORT is checked before it binds.
func TestMCPNeedsNoAuthEnvironment(t *testing.T) {
	for _, name := range []string{"AUTHKIT_ISSUER", "WORKOS_ALLOWED_USER_ID", "MCP_RESOURCE_URL", "DATABASE_URL_READONLY"} {
		t.Setenv(name, "")
	}
	t.Setenv("PORT", "0")
	var out, errOut bytes.Buffer
	if code := Main([]string{"mcp"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "PORT must be between 1 and 65535") {
		t.Fatalf("code=%d error=%s", code, errOut.String())
	}
	errOut.Reset()
	if code := Main([]string{"mcp", "unexpected"}, &out, &errOut); code != 2 {
		t.Fatalf("code=%d error=%s", code, errOut.String())
	}
}
