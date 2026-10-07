package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestMCPRequiresEnvironment(t *testing.T) {
	for _, name := range []string{"AUTHKIT_ISSUER", "WORKOS_ALLOWED_USER_ID", "MCP_RESOURCE_URL", "DATABASE_URL_READONLY"} {
		t.Setenv(name, "")
	}
	var out, errOut bytes.Buffer
	if code := Main([]string{"mcp"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "AUTHKIT_ISSUER is required") {
		t.Fatalf("code=%d error=%s", code, errOut.String())
	}
	errOut.Reset()
	if code := Main([]string{"mcp", "unexpected"}, &out, &errOut); code != 2 {
		t.Fatalf("code=%d error=%s", code, errOut.String())
	}
}
