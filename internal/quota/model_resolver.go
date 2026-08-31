package quota

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const modelCatalogURL = "https://raw.githubusercontent.com/router-for-me/models/refs/heads/main/models.json"
const priceCatalogURL = "https://openrouter.ai/api/v1/models"
const catalogRefreshInterval = 24 * time.Hour
const maxCatalogBytes = 8 << 20

type ModelChoice struct {
	Model           string `json:"model"`
	InputNanoUSD    int64  `json:"input_nano_usd"`
	OutputNanoUSD   int64  `json:"output_nano_usd"`
	CombinedNanoUSD int64  `json:"combined_nano_usd"`
	Source          string `json:"source"`
}

type CatalogStatus struct {
	Ready                  bool                   `json:"ready"`
	Source                 string                 `json:"source"`
	FetchedAt              time.Time              `json:"fetched_at"`
	AgeSeconds             int64                  `json:"age_seconds"`
	ModelCatalogSource     string                 `json:"model_catalog_source"`
	PriceCatalogSource     string                 `json:"price_catalog_source"`
	ModelCatalogAgeSeconds int64                  `json:"model_catalog_age_seconds"`
	PriceCatalogAgeSeconds int64                  `json:"price_catalog_age_seconds"`
	SelectedByPlan         map[string]ModelChoice `json:"selected_by_plan"`
	ErrorCode              string                 `json:"error_code,omitempty"`
}

type catalogModel struct {
	ID                        string   `json:"id"`
	Type                      string   `json:"type"`
	OwnedBy                   string   `json:"owned_by"`
	OwnedByCamel              string   `json:"ownedBy"`
	SupportedOutputModalities []string `json:"supportedOutputModalities"`
	OutputModalities          []string `json:"output_modalities"`
}

type modelPrice struct {
	InputNanoUSD  int64 `json:"input_nano_usd"`
	OutputNanoUSD int64 `json:"output_nano_usd"`
}

type resolverSnapshot struct {
	Plans     map[string][]string   `json:"plans"`
	Prices    map[string]modelPrice `json:"prices"`
	Source    string                `json:"source"`
	FetchedAt time.Time             `json:"fetched_at"`
}

type ModelResolver struct {
	mu       sync.Mutex
	service  *Service
	store    *Store
	snapshot resolverSnapshot
	lastErr  string
	loadedAt time.Time
}

func NewModelResolver(service *Service, store *Store) *ModelResolver {
	return &ModelResolver{service: service, store: store}
}

func (r *ModelResolver) Resolve(ctx context.Context, plan, override, hostCallbackID string) (ModelChoice, error) {
	snapshot, err := r.load(ctx, hostCallbackID)
	if err != nil {
		if strings.TrimSpace(override) != "" {
			return ModelChoice{Model: strings.TrimSpace(override), Source: "override_recovery"}, nil
		}
		return ModelChoice{}, err
	}
	section := normalizePlan(plan)
	models := snapshot.Plans[section]
	if len(models) == 0 {
		return ModelChoice{}, codedError("model_catalog_unavailable")
	}
	if value := strings.TrimSpace(override); value != "" {
		for _, model := range models {
			if model == value {
				price := snapshot.Prices["openai/"+model]
				return choiceFor(model, price, snapshot.Source), nil
			}
		}
		return ModelChoice{}, codedError("override_not_available_for_plan")
	}
	type candidate struct {
		choice ModelChoice
		order  int
	}
	candidates := make([]candidate, 0, len(models))
	for order, model := range models {
		price, ok := snapshot.Prices["openai/"+model]
		if !ok || price.InputNanoUSD < 0 || price.OutputNanoUSD < 0 {
			continue
		}
		candidates = append(candidates, candidate{choice: choiceFor(model, price, snapshot.Source), order: order})
	}
	if len(candidates) == 0 {
		return ModelChoice{}, codedError("no_priced_model")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].choice.CombinedNanoUSD == candidates[j].choice.CombinedNanoUSD {
			return candidates[i].order < candidates[j].order
		}
		return candidates[i].choice.CombinedNanoUSD < candidates[j].choice.CombinedNanoUSD
	})
	return candidates[0].choice, nil
}

func (r *ModelResolver) Status(ctx context.Context, hostCallbackID string) CatalogStatus {
	snapshot, err := r.load(ctx, hostCallbackID)
	status := CatalogStatus{SelectedByPlan: map[string]ModelChoice{}}
	if err != nil {
		status.ErrorCode = errorCode(err)
		return status
	}
	status.Ready = true
	status.Source = snapshot.Source
	status.FetchedAt = snapshot.FetchedAt
	status.AgeSeconds = max(0, int64(time.Since(snapshot.FetchedAt).Seconds()))
	status.ModelCatalogSource = snapshot.Source
	status.PriceCatalogSource = snapshot.Source
	status.ModelCatalogAgeSeconds = status.AgeSeconds
	status.PriceCatalogAgeSeconds = status.AgeSeconds
	for _, plan := range []string{"free", "plus", "team", "business", "pro"} {
		choice, choiceErr := r.Resolve(ctx, plan, "", hostCallbackID)
		if choiceErr != nil {
			status.Ready = false
			status.ErrorCode = errorCode(choiceErr)
			continue
		}
		status.SelectedByPlan[plan] = choice
	}
	return status
}

func (r *ModelResolver) load(ctx context.Context, hostCallbackID string) (resolverSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.loadedAt.IsZero() && time.Since(r.loadedAt) < catalogRefreshInterval && len(r.snapshot.Plans) > 0 {
		return r.snapshot, nil
	}
	remote, err := r.fetchRemote(hostCallbackID)
	if err == nil {
		r.snapshot = remote
		r.loadedAt = time.Now()
		r.lastErr = ""
		raw, _ := json.Marshal(remote)
		_ = r.store.SaveCatalog(ctx, "model_resolver_v1", raw, remote.Source, remote.FetchedAt)
		return remote, nil
	}
	r.lastErr = errorCode(err)
	if raw, source, fetchedAt, cacheErr := r.store.LoadCatalog(ctx, "model_resolver_v1"); cacheErr == nil {
		var cached resolverSnapshot
		if json.Unmarshal(raw, &cached) == nil && validateSnapshot(cached) == nil {
			cached.Source = "cache:" + source
			cached.FetchedAt = fetchedAt
			r.snapshot = cached
			r.loadedAt = time.Now().Add(-catalogRefreshInterval + 30*time.Minute)
			return cached, nil
		}
	}
	fallback := embeddedSnapshot()
	if validateSnapshot(fallback) != nil {
		return resolverSnapshot{}, codedError("model_catalog_unavailable")
	}
	r.snapshot = fallback
	r.loadedAt = time.Now().Add(-catalogRefreshInterval + 30*time.Minute)
	return fallback, nil
}

func (r *ModelResolver) fetchRemote(hostCallbackID string) (resolverSnapshot, error) {
	headers := http.Header{"Accept": {"application/json"}, "User-Agent": {"CLIProxyAPI-Codex-Quota-Activation-Plugin/" + Version}}
	catalogResp, err := r.service.do(http.MethodGet, modelCatalogURL, headers, nil, hostCallbackID)
	if err != nil || catalogResp.StatusCode != http.StatusOK || len(catalogResp.Body) == 0 || len(catalogResp.Body) > maxCatalogBytes {
		return resolverSnapshot{}, codedError("model_catalog_unavailable")
	}
	priceResp, err := r.service.do(http.MethodGet, priceCatalogURL, headers, nil, hostCallbackID)
	if err != nil || priceResp.StatusCode != http.StatusOK || len(priceResp.Body) == 0 || len(priceResp.Body) > maxCatalogBytes {
		return resolverSnapshot{}, codedError("price_catalog_unavailable")
	}
	plans, err := parseModelCatalog(catalogResp.Body)
	if err != nil {
		return resolverSnapshot{}, err
	}
	prices, err := parseOpenRouterPrices(priceResp.Body)
	if err != nil {
		return resolverSnapshot{}, err
	}
	snapshot := resolverSnapshot{Plans: plans, Prices: prices, Source: "remote", FetchedAt: time.Now().UTC()}
	if err := validateSnapshot(snapshot); err != nil {
		return resolverSnapshot{}, err
	}
	return snapshot, nil
}

func parseModelCatalog(raw []byte) (map[string][]string, error) {
	var catalog map[string][]catalogModel
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, codedError("model_catalog_malformed")
	}
	plans := map[string][]string{}
	for _, plan := range []string{"free", "plus", "team", "pro"} {
		section := catalog["codex-"+plan]
		for _, model := range section {
			ownedBy := model.OwnedBy
			if ownedBy == "" {
				ownedBy = model.OwnedByCamel
			}
			modalities := model.SupportedOutputModalities
			if len(modalities) == 0 {
				modalities = model.OutputModalities
			}
			if model.ID == "" || (model.Type != "openai" && model.Type != "codex") || ownedBy != "openai" || !contains(modalities, "text") {
				continue
			}
			plans[plan] = append(plans[plan], model.ID)
		}
	}
	plans["business"] = append([]string(nil), plans["team"]...)
	return plans, nil
}

func parseOpenRouterPrices(raw []byte) (map[string]modelPrice, error) {
	var document struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || len(document.Data) == 0 {
		return nil, codedError("price_catalog_malformed")
	}
	prices := map[string]modelPrice{}
	for _, model := range document.Data {
		if !strings.HasPrefix(model.ID, "openai/") {
			continue
		}
		input, inputErr := decimalNanoUSD(model.Pricing.Prompt)
		output, outputErr := decimalNanoUSD(model.Pricing.Completion)
		if inputErr != nil || outputErr != nil {
			continue
		}
		prices[model.ID] = modelPrice{InputNanoUSD: input, OutputNanoUSD: output}
	}
	if len(prices) == 0 {
		return nil, codedError("price_catalog_malformed")
	}
	return prices, nil
}

func decimalNanoUSD(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") {
		return 0, errors.New("invalid price")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 {
		return 0, errors.New("invalid price")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 9 {
		return 0, errors.New("price exceeds nano-usd precision")
	}
	fraction += strings.Repeat("0", 9-len(fraction))
	frac := int64(0)
	if fraction != "" {
		frac, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, err
		}
	}
	if whole > math.MaxInt64/1_000_000_000 {
		return 0, errors.New("price exceeds int64 range")
	}
	nanos := whole * 1_000_000_000
	if frac > math.MaxInt64-nanos {
		return 0, errors.New("price exceeds int64 range")
	}
	return nanos + frac, nil
}

func embeddedSnapshot() resolverSnapshot {
	free := []string{"gpt-5.4-mini", "gpt-5.5", "gpt-5.6-terra", "gpt-5.6-luna", "codex-auto-review"}
	plusAndPro := []string{"gpt-5.3-codex-spark", "gpt-5.4", "gpt-5.4-mini", "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "codex-auto-review"}
	team := []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "codex-auto-review"}
	return resolverSnapshot{
		Plans: map[string][]string{
			"free": append([]string(nil), free...), "plus": append([]string(nil), plusAndPro...),
			"team": append([]string(nil), team...), "business": append([]string(nil), team...), "pro": append([]string(nil), plusAndPro...),
		},
		Prices: map[string]modelPrice{
			"openai/gpt-5.4":       {InputNanoUSD: 2500, OutputNanoUSD: 15000},
			"openai/gpt-5.4-mini":  {InputNanoUSD: 750, OutputNanoUSD: 4500},
			"openai/gpt-5.5":       {InputNanoUSD: 5000, OutputNanoUSD: 30000},
			"openai/gpt-5.6-sol":   {InputNanoUSD: 2000, OutputNanoUSD: 10000},
			"openai/gpt-5.6-terra": {InputNanoUSD: 2000, OutputNanoUSD: 12000},
			"openai/gpt-5.6-luna":  {InputNanoUSD: 200, OutputNanoUSD: 1200},
		},
		Source: "embedded_v1.3.0", FetchedAt: time.Now().UTC(),
	}
}

func validateSnapshot(snapshot resolverSnapshot) error {
	for _, plan := range []string{"free", "plus", "team", "business", "pro"} {
		if len(snapshot.Plans[plan]) == 0 {
			return codedError("model_catalog_malformed")
		}
	}
	if len(snapshot.Prices) == 0 || snapshot.FetchedAt.IsZero() {
		return codedError("price_catalog_malformed")
	}
	return nil
}

func choiceFor(model string, price modelPrice, source string) ModelChoice {
	return ModelChoice{Model: model, InputNanoUSD: price.InputNanoUSD, OutputNanoUSD: price.OutputNanoUSD, CombinedNanoUSD: price.InputNanoUSD + price.OutputNanoUSD, Source: source}
}

func normalizePlan(plan string) string {
	if strings.ToLower(plan) == "business" {
		return "business"
	}
	return strings.ToLower(plan)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type codeError string

func (e codeError) Error() string { return string(e) }

func codedError(code string) error { return codeError(code) }

func errorCode(err error) string {
	var coded codeError
	if errors.As(err, &coded) {
		return coded.Error()
	}
	return "model_catalog_unavailable"
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
