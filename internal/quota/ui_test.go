package quota

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestStatusPageAllowsOnlySameOriginEmbedding(t *testing.T) {
	response, err := New(nil).Management(pluginapi.ManagementRequest{Path: "/v0/resource/plugins/codex-quota-activation/status"}, "")
	if err != nil {
		t.Fatal(err)
	}
	policy := response.Headers.Get("Content-Security-Policy")
	if !strings.Contains(policy, "frame-ancestors 'self'") {
		t.Fatalf("unexpected frame policy: %q", policy)
	}
}
