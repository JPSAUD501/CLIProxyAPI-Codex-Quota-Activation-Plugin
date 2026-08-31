package quota

import (
	"encoding/json"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestActivationUsesResponsesStreamingAndRealIdentity(t *testing.T) {
	var opened hostHTTPRequest
	caller := func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			opened = payload.(hostHTTPRequest)
			return json.RawMessage(`{"status_code":200,"stream_id":"stream-1"}`), nil
		case pluginabi.MethodHostHTTPStreamRead:
			return json.RawMessage(`{"done":true}`), nil
		case pluginabi.MethodHostHTTPStreamClose:
			return json.RawMessage(`{}`), nil
		default:
			t.Fatalf("unexpected method %s", method)
			return nil, nil
		}
	}
	service := New(caller)
	status, _, err := service.activate(Account{AccessToken: "secret", AccountID: "account"}, "gpt-5.6-luna", "callback")
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if opened.URL != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("unexpected url %s", opened.URL)
	}
	var body struct {
		Model  string `json:"model"`
		Store  bool   `json:"store"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(opened.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "gpt-5.6-luna" || body.Store || !body.Stream {
		t.Fatalf("unexpected body %+v", body)
	}
	userAgent := opened.Headers.Get("User-Agent")
	if userAgent == "" || !containsString(userAgent, runtime.GOOS) || !containsString(userAgent, runtime.GOARCH) {
		t.Fatalf("user agent does not identify the runtime: %s", userAgent)
	}
}

func TestActivationFailureStates(t *testing.T) {
	cases := map[int]string{401: "failed_retriable", 403: "failed_retriable", 429: "failed_retriable", 400: "failed_terminal", 500: "sent_unknown", 0: "sent_unknown"}
	for status, expected := range cases {
		if got := activationFailureState(status); got != expected {
			t.Fatalf("status=%d got=%s expected=%s", status, got, expected)
		}
	}
}

func containsString(value, part string) bool {
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}
