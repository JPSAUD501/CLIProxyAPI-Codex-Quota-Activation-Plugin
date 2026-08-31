package quota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestCurrentCatalogAndOpenRouterPricesSelectLunaForEveryPlan(t *testing.T) {
	catalog := map[string][]map[string]any{}
	for _, plan := range []string{"free", "plus", "team", "pro"} {
		catalog["codex-"+plan] = []map[string]any{
			{"id": "gpt-5.4-mini", "type": "openai", "owned_by": "openai", "supportedOutputModalities": []string{"text"}},
			{"id": "gpt-5.6-luna", "type": "openai", "owned_by": "openai", "supportedOutputModalities": []string{"text"}},
		}
	}
	catalogRaw, _ := json.Marshal(catalog)
	plans, err := parseModelCatalog(catalogRaw)
	if err != nil {
		t.Fatal(err)
	}
	pricesRaw := []byte(`{"data":[{"id":"openai/gpt-5.4-mini","pricing":{"prompt":"0.00000075","completion":"0.0000045"}},{"id":"openai/gpt-5.6-luna","pricing":{"prompt":"0.0000002","completion":"0.0000012"}}]}`)
	prices, err := parseOpenRouterPrices(pricesRaw)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &ModelResolver{snapshot: resolverSnapshot{Plans: plans, Prices: prices, Source: "fixture", FetchedAt: time.Now()}, loadedAt: time.Now()}
	for _, plan := range []string{"free", "plus", "team", "business", "pro"} {
		choice, resolveErr := resolver.Resolve(context.Background(), plan, "", "")
		if resolveErr != nil || choice.Model != "gpt-5.6-luna" || choice.CombinedNanoUSD != 1400 {
			t.Fatalf("plan=%s choice=%+v err=%v", plan, choice, resolveErr)
		}
	}
}

func TestCatalogAcceptsLegacyCodexAndRejectsInvalidModels(t *testing.T) {
	raw := []byte(`{"codex-free":[
		{"id":"legacy","type":"codex","owned_by":"openai","supportedOutputModalities":["text"]},
		{"id":"wrong-owner","type":"openai","owned_by":"other","supportedOutputModalities":["text"]},
		{"id":"image-only","type":"openai","owned_by":"openai","supportedOutputModalities":["image"]}
	]}`)
	plans, err := parseModelCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans["free"]) != 1 || plans["free"][0] != "legacy" {
		t.Fatalf("unexpected models: %#v", plans["free"])
	}
}

func TestOverrideMustBelongToPlanWhenCatalogIsAvailable(t *testing.T) {
	resolver := &ModelResolver{snapshot: embeddedSnapshot(), loadedAt: time.Now()}
	choice, err := resolver.Resolve(context.Background(), "plus", "gpt-5.6-luna", "")
	if err != nil || choice.Model != "gpt-5.6-luna" {
		t.Fatalf("valid override failed: %+v %v", choice, err)
	}
	if _, err := resolver.Resolve(context.Background(), "plus", "not-in-plan", ""); errorCode(err) != "override_not_available_for_plan" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEqualPriceUsesCatalogOrder(t *testing.T) {
	snapshot := embeddedSnapshot()
	snapshot.Plans["plus"] = []string{"first", "second"}
	snapshot.Prices = map[string]modelPrice{
		"openai/first":  {InputNanoUSD: 1, OutputNanoUSD: 2},
		"openai/second": {InputNanoUSD: 2, OutputNanoUSD: 1},
	}
	resolver := &ModelResolver{snapshot: snapshot, loadedAt: time.Now()}
	choice, err := resolver.Resolve(context.Background(), "plus", "", "")
	if err != nil || choice.Model != "first" {
		t.Fatalf("choice=%+v err=%v", choice, err)
	}
}

func TestDecimalNanoUSDRejectsExcessPrecision(t *testing.T) {
	if value, err := decimalNanoUSD("0.0000002"); err != nil || value != 200 {
		t.Fatalf("value=%d err=%v", value, err)
	}
	if _, err := decimalNanoUSD("0.0000000001"); err == nil {
		t.Fatal("expected precision error")
	}
}

func TestResolverUsesRemoteThenPersistentLastKnownGood(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	catalog := []byte(`{"codex-free":[{"id":"gpt-5.6-luna","type":"openai","owned_by":"openai","supportedOutputModalities":["text"]}],"codex-plus":[{"id":"gpt-5.6-luna","type":"openai","owned_by":"openai","supportedOutputModalities":["text"]}],"codex-team":[{"id":"gpt-5.6-luna","type":"openai","owned_by":"openai","supportedOutputModalities":["text"]}],"codex-pro":[{"id":"gpt-5.6-luna","type":"openai","owned_by":"openai","supportedOutputModalities":["text"]}]}`)
	prices := []byte(`{"data":[{"id":"openai/gpt-5.6-luna","pricing":{"prompt":"0.0000002","completion":"0.0000012"}}]}`)
	service := New(func(method string, payload any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return nil, errors.New("unexpected method")
		}
		request := payload.(hostHTTPRequest)
		body := catalog
		if request.URL == priceCatalogURL {
			body = prices
		}
		return json.Marshal(hostHTTPResponse{StatusCode: http.StatusOK, Body: body})
	})
	resolver := NewModelResolver(service, store)
	choice, err := resolver.Resolve(context.Background(), "team", "", "callback")
	if err != nil || choice.Model != "gpt-5.6-luna" || choice.Source != "remote" {
		t.Fatalf("remote choice=%+v err=%v", choice, err)
	}
	_ = store.Close()

	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	offline := New(func(string, any) (json.RawMessage, error) { return nil, errors.New("offline") })
	resolver = NewModelResolver(offline, store)
	choice, err = resolver.Resolve(context.Background(), "team", "", "callback")
	if err != nil || choice.Model != "gpt-5.6-luna" || choice.Source != "cache:remote" {
		t.Fatalf("cached choice=%+v err=%v", choice, err)
	}
}

func TestResolverFallsBackToVersionedEmbeddedSnapshot(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	offline := New(func(string, any) (json.RawMessage, error) { return nil, errors.New("offline") })
	choice, err := NewModelResolver(offline, store).Resolve(context.Background(), "pro", "", "")
	if err != nil || choice.Model != "gpt-5.6-luna" || choice.Source != "embedded_v1.3.0" {
		t.Fatalf("choice=%+v err=%v", choice, err)
	}
}
