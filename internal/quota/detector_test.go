package quota

import (
	"encoding/base64"
	"runtime"
	"strings"
	"testing"
	"time"

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
