package quota

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestFreshWindowEligible(t *testing.T) {
	now := time.Unix(1000, 0)
	s, err := ParseSnapshot([]byte(`{"rate_limit":{"primary_window":{"used_percent":0,"reset_at":4600,"reset_after_seconds":3600,"limit_window_seconds":3600,"used_tokens":0,"remaining_tokens":100,"limit_tokens":100},"secondary_window":null}}`), "a", now)
	if err != nil || !s.Eligible || s.Secondary.Presence != Absent {
		t.Fatalf("%#v %v", s, err)
	}
}
func TestMissingWindowIsUnknownAndClosed(t *testing.T) {
	s, err := ParseSnapshot([]byte(`{"rate_limit":{"secondary_window":null}}`), "a", time.Now())
	if err != nil || s.Eligible || s.Primary.Presence != Unknown {
		t.Fatalf("%#v %v", s, err)
	}
}
func TestUsedWindowNotEligible(t *testing.T) {
	now := time.Unix(1000, 0)
	s, _ := ParseSnapshot([]byte(`{"rate_limit":{"primary_window":{"used_percent":1,"reset_at":4600,"reset_after_seconds":3600,"limit_window_seconds":3600,"used_tokens":1,"remaining_tokens":99,"limit_tokens":100},"secondary_window":null}}`), "a", now)
	if s.Eligible || s.Reason != "window_already_active" {
		t.Fatalf("%#v", s)
	}
}

func TestCodexJWTInfo(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_plan_type":"Pro"}}`))
	account, plan := codexJWTInfo("header." + payload + ".signature")
	if account != "acct-1" || plan != "pro" {
		t.Fatalf("account=%q plan=%q", account, plan)
	}
}

func TestParseAccountUsesCodexJWTClaims(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_plan_type":"Team"}}`))
	raw := []byte(`{"access_token":"access","id_token":"header.` + payload + `.signature"}`)
	account, ok := parseAccount(pluginapi.HostAuthFileEntry{ID: "codex.json", AuthIndex: "index", Provider: "codex", Unavailable: true}, raw)
	if !ok || account.AccountID != "acct-1" || account.Plan != "team" {
		t.Fatalf("account=%#v ok=%v", account, ok)
	}
}

func TestQuotaHeadersUseProtocolBetaAndHonestPlatformIdentity(t *testing.T) {
	headers := headersFor(Account{AccessToken: "secret", AccountID: "acct"})
	if headers.Get("OpenAI-Beta") != "codex-1" || headers.Get("Chatgpt-Account-Id") != "acct" {
		t.Fatalf("missing observed quota protocol headers")
	}
	userAgent := headers.Get("User-Agent")
	if !strings.Contains(userAgent, runtime.GOOS) || !strings.Contains(userAgent, runtime.GOARCH) {
		t.Fatalf("user agent does not identify the real platform: %q", userAgent)
	}
}

func TestProbeFailureReasonIsDiagnosticWithoutLeakingTheError(t *testing.T) {
	tests := []struct {
		status int
		err    error
		want   string
	}{
		{status: http.StatusUnauthorized, err: errors.New("sensitive upstream body"), want: "probe_failed_http_401"},
		{err: errors.New("execute request: context deadline exceeded: token=secret"), want: "probe_failed_timeout"},
		{err: errors.New("execute request: remote error: tls: token=secret"), want: "probe_failed_tls"},
		{err: errors.New("execute request: unexpected EOF: token=secret"), want: "probe_failed_upstream_eof"},
	}
	for _, test := range tests {
		got := probeFailureReason(test.status, test.err)
		if got != test.want {
			t.Fatalf("probeFailureReason(%d, err) = %q, want %q", test.status, got, test.want)
		}
		if strings.Contains(got, "secret") {
			t.Fatal("diagnostic reason leaked the original error")
		}
	}
}

func TestProbeForwardsManagementHostCallbackID(t *testing.T) {
	service := New(func(method string, payload any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("host method = %q", method)
		}
		request, ok := payload.(hostHTTPRequest)
		if !ok || request.HostCallbackID != "callback-1" {
			t.Fatalf("host request = %#v", payload)
		}
		return json.Marshal(hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":0,"reset_at":4600,"reset_after_seconds":3600,"limit_window_seconds":3600,"used_tokens":0,"remaining_tokens":100,"limit_tokens":100},"secondary_window":null}}`)})
	})
	if _, status, err := service.probe(Account{AccessToken: "secret", AccountID: "acct"}, "callback-1"); err != nil || status != http.StatusOK {
		t.Fatalf("probe status=%d err=%v", status, err)
	}
}
