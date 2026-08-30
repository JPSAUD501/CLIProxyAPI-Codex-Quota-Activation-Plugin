package quota

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const usageURL = "https://chatgpt.com/backend-api/wham/usage"
const activationURL = "https://chatgpt.com/backend-api/codex/responses/compact"
const modelCatalogURL = "https://raw.githubusercontent.com/router-for-me/models/refs/heads/main/models.json"
const priceCatalogURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

type HostCaller func(method string, payload any) (json.RawMessage, error)
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}
func (d Duration) String() string { return time.Duration(d).String() }

type Config struct {
	Enabled                 bool     `yaml:"enabled"`
	AutoActivate            bool     `yaml:"auto_activate"`
	ScanInterval            Duration `yaml:"scan_interval"`
	MaxConcurrency          int      `yaml:"max_concurrency"`
	ActivationModelMode     string   `yaml:"activation_model_mode"`
	ActivationModelOverride string   `yaml:"activation_model_override"`
	DataDir                 string   `yaml:"data_dir"`
}
type Account struct {
	Key, ID, AuthIndex, Label, Plan, AccessToken, AccountID string
	Disabled, Unavailable                                   bool
	Snapshot                                                Snapshot
}
type confirmation struct {
	Accounts []string
	Expires  time.Time
	Used     bool
}
type Service struct {
	mu            sync.RWMutex
	cfg           Config
	store         *Store
	call          HostCaller
	done          chan struct{}
	scanMu        sync.Mutex
	confirmations map[string]*confirmation
	lastScan      time.Time
	lastError     string
	started       bool
}

func New(call HostCaller) *Service {
	return &Service{call: call, done: make(chan struct{}), confirmations: map[string]*confirmation{}}
}
func (s *Service) Configure(raw []byte) error {
	cfg := Config{Enabled: true, AutoActivate: false, ScanInterval: Duration(30 * time.Minute), MaxConcurrency: 1, ActivationModelMode: "auto"}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if cfg.ScanInterval == 0 {
			cfg.ScanInterval = Duration(30 * time.Minute)
		}
		if cfg.MaxConcurrency == 0 {
			cfg.MaxConcurrency = 1
		}
		if cfg.ActivationModelMode == "" {
			cfg.ActivationModelMode = "auto"
		}
	}
	if time.Duration(cfg.ScanInterval) < 5*time.Minute {
		return errors.New("scan-interval must be at least 5m")
	}
	if cfg.MaxConcurrency != 1 {
		return errors.New("max-concurrency must be 1 in v1")
	}
	if cfg.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		cfg.DataDir = filepath.Join(home, ".cli-proxy-api", "plugins", "codex-quota-activation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil {
		store, err := OpenStore(cfg.DataDir)
		if err != nil {
			return err
		}
		s.store = store
	}
	s.cfg = cfg
	if !s.started {
		s.started = true
		go s.scheduler()
	}
	return nil
}
func (s *Service) scheduler() {
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.done:
		return
	}
	for {
		s.mu.RLock()
		cfg := s.cfg
		s.mu.RUnlock()
		if cfg.Enabled && cfg.AutoActivate {
			_, _ = s.Scan(context.Background(), "auto", nil, true)
		}
		timer.Reset(time.Duration(cfg.ScanInterval))
		select {
		case <-timer.C:
		case <-s.done:
			return
		}
	}
}
func (s *Service) accounts() ([]Account, error) {
	raw, err := s.call(pluginabi.MethodHostAuthList, map[string]any{})
	if err != nil {
		return nil, err
	}
	var listed struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, err
	}
	out := []Account{}
	for _, entry := range listed.Files {
		provider := strings.ToLower(strings.TrimSpace(entry.Provider))
		typ := strings.ToLower(strings.TrimSpace(entry.Type))
		if provider != "codex" && typ != "codex" {
			continue
		}
		// Exhausted Codex credentials are reported as unavailable/error by the
		// scheduler. They must remain visible to quota monitoring; only an
		// explicit disable removes an account from consideration.
		if entry.Disabled {
			continue
		}
		authRaw, err := s.call(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: entry.AuthIndex})
		if err != nil {
			continue
		}
		var got pluginapi.HostAuthGetResponse
		if json.Unmarshal(authRaw, &got) != nil {
			continue
		}
		account, ok := parseAccount(entry, got.JSON)
		if ok {
			out = append(out, account)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func parseAccount(entry pluginapi.HostAuthFileEntry, raw []byte) (Account, bool) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return Account{}, false
	}
	access := firstString(doc, "access_token", "accessToken", "token")
	accountID := firstString(doc, "account_id", "chatgpt_account_id", "accountId")
	plan := strings.ToLower(firstString(doc, "plan_type", "chatgpt_plan_type", "plan"))
	if idToken := firstString(doc, "id_token", "idToken"); idToken != "" {
		jwtAccount, jwtPlan := codexJWTInfo(idToken)
		if accountID == "" {
			accountID = jwtAccount
		}
		if plan == "" {
			plan = jwtPlan
		}
	}
	if nested, ok := doc["tokens"]; ok {
		var tokens map[string]json.RawMessage
		if json.Unmarshal(nested, &tokens) == nil {
			if access == "" {
				access = firstString(tokens, "access_token", "accessToken", "token")
			}
			if accountID == "" {
				accountID = firstString(tokens, "account_id", "chatgpt_account_id", "accountId")
			}
			if plan == "" {
				plan = strings.ToLower(firstString(tokens, "plan_type", "chatgpt_plan_type", "plan"))
			}
		}
	}
	if access == "" || accountID == "" || !validPlan(plan) {
		return Account{}, false
	}
	key := entry.AuthIndex
	if key == "" {
		key = entry.ID
	}
	if key == "" {
		key = entry.Name
	}
	return Account{Key: key, ID: entry.ID, AuthIndex: entry.AuthIndex, Label: entry.Label, Plan: plan, AccessToken: access, AccountID: accountID}, true
}

func codexJWTInfo(token string) (string, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 64*1024 {
		return "", ""
	}
	var claims struct {
		Codex struct {
			AccountID string `json:"chatgpt_account_id"`
			Plan      string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", ""
	}
	return strings.TrimSpace(claims.Codex.AccountID), strings.ToLower(strings.TrimSpace(claims.Codex.Plan))
}
func firstString(m map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var v string
		if raw, ok := m[key]; ok && json.Unmarshal(raw, &v) == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func validPlan(plan string) bool {
	switch plan {
	case "free", "team", "business", "plus", "pro":
		return true
	}
	return false
}

type hostHTTPRequest struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
	Method, URL    string
	Headers        http.Header
	Body           []byte
}
type hostHTTPResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	Body       []byte      `json:"body"`
}
type hostHTTPStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	StreamID   string      `json:"stream_id"`
}
type hostHTTPStreamChunk struct {
	Payload []byte `json:"payload"`
	Done    bool   `json:"done"`
	Error   string `json:"error"`
}

func (s *Service) do(method, url string, headers http.Header, body []byte) (hostHTTPResponse, error) {
	raw, err := s.call(pluginabi.MethodHostHTTPDo, hostHTTPRequest{Method: method, URL: url, Headers: headers, Body: body})
	if err != nil {
		return hostHTTPResponse{}, err
	}
	var resp hostHTTPResponse
	err = json.Unmarshal(raw, &resp)
	return resp, err
}
func (s *Service) doStream(method, url string, headers http.Header, body []byte) (int, error) {
	raw, err := s.call(pluginabi.MethodHostHTTPDoStream, hostHTTPRequest{Method: method, URL: url, Headers: headers, Body: body})
	if err != nil {
		return 0, err
	}
	var opened hostHTTPStreamResponse
	if json.Unmarshal(raw, &opened) != nil || opened.StreamID == "" {
		return 0, errors.New("invalid host stream response")
	}
	defer s.call(pluginabi.MethodHostHTTPStreamClose, map[string]string{"stream_id": opened.StreamID})
	for chunks := 0; chunks < 10000; chunks++ {
		raw, err = s.call(pluginabi.MethodHostHTTPStreamRead, map[string]string{"stream_id": opened.StreamID})
		if err != nil {
			return opened.StatusCode, err
		}
		var chunk hostHTTPStreamChunk
		if json.Unmarshal(raw, &chunk) != nil {
			return opened.StatusCode, errors.New("invalid stream chunk")
		}
		if chunk.Error != "" {
			return opened.StatusCode, errors.New("upstream stream failed")
		}
		if chunk.Done {
			return opened.StatusCode, nil
		}
	}
	return opened.StatusCode, errors.New("stream chunk limit exceeded")
}
func headersFor(account Account) http.Header {
	h := http.Header{"Accept": {"application/json"}, "Authorization": {"Bearer " + account.AccessToken}, "User-Agent": {"CLIProxyAPI-Codex-Quota-Activation-Plugin/1.0.1 (" + runtime.GOOS + "; " + runtime.GOARCH + ")"}}
	h.Set("Chatgpt-Account-Id", account.AccountID)
	return h
}
func (s *Service) probe(account Account) (Snapshot, int, error) {
	resp, err := s.do(http.MethodGet, usageURL, headersFor(account), nil)
	if err != nil {
		return Snapshot{}, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Snapshot{}, resp.StatusCode, fmt.Errorf("quota endpoint returned %d", resp.StatusCode)
	}
	snapshot, err := ParseSnapshot(resp.Body, account.Key, time.Now())
	return snapshot, resp.StatusCode, err
}
func (s *Service) selectModel(account Account) (string, error) {
	s.mu.RLock()
	override := strings.TrimSpace(s.cfg.ActivationModelOverride)
	s.mu.RUnlock()
	if override != "" {
		return override, nil
	}
	catalogResp, err := s.do(http.MethodGet, modelCatalogURL, http.Header{"Accept": {"application/json"}}, nil)
	if err != nil || catalogResp.StatusCode != 200 {
		return "", errors.New("model catalog unavailable")
	}
	priceResp, err := s.do(http.MethodGet, priceCatalogURL, http.Header{"Accept": {"application/json"}}, nil)
	if err != nil || priceResp.StatusCode != 200 {
		return "", errors.New("price catalog unavailable")
	}
	var catalog map[string][]struct {
		ID                        string   `json:"id"`
		Type                      string   `json:"type"`
		SupportedOutputModalities []string `json:"supportedOutputModalities"`
	}
	if json.Unmarshal(catalogResp.Body, &catalog) != nil {
		return "", errors.New("invalid model catalog")
	}
	section := "codex-" + account.Plan
	if account.Plan == "business" {
		section = "codex-team"
	}
	models := catalog[section]
	if len(models) == 0 {
		return "", errors.New("plan catalog unavailable")
	}
	var prices map[string]struct {
		Input  float64 `json:"input_cost_per_token"`
		Output float64 `json:"output_cost_per_token"`
	}
	if json.Unmarshal(priceResp.Body, &prices) != nil {
		return "", errors.New("invalid price catalog")
	}
	type choice struct {
		id    string
		cost  float64
		order int
	}
	choices := []choice{}
	for i, m := range models {
		if m.ID == "" || m.Type != "codex" {
			continue
		}
		p, ok := prices[m.ID]
		if !ok {
			p, ok = prices["openai/"+m.ID]
		}
		if !ok || p.Input < 0 || p.Output < 0 {
			continue
		}
		choices = append(choices, choice{m.ID, p.Input + p.Output, i})
	}
	if len(choices) == 0 {
		return "", errors.New("no priced text model for plan")
	}
	sort.SliceStable(choices, func(i, j int) bool {
		if choices[i].cost == choices[j].cost {
			return choices[i].order < choices[j].order
		}
		return choices[i].cost < choices[j].cost
	})
	return choices[0].id, nil
}
func (s *Service) activate(account Account, model string) (int, string, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "instructions": "Reply briefly.", "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}}}, "store": false, "stream": true})
	h := headersFor(account)
	h.Set("Accept", "text/event-stream")
	h.Set("Content-Type", "application/json")
	h.Set("OpenAI-Beta", "responses=v1")
	h.Set("Originator", "cliproxyapi_codex_quota_activation_plugin")
	status, err := s.doStream(http.MethodPost, activationURL, h, body)
	if err != nil {
		return status, "network", err
	}
	if status < 200 || status >= 300 {
		return status, fmt.Sprintf("http_%d", status), errors.New("activation rejected")
	}
	return status, "", nil
}
func (s *Service) Scan(ctx context.Context, mode string, selected []string, activate bool) (RunRow, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	started := time.Now().UTC()
	run := RunRow{StartedAt: started.Format(time.RFC3339Nano), Mode: mode}
	accounts, err := s.accounts()
	if err != nil {
		s.setScan(err)
		return run, err
	}
	wanted := map[string]bool{}
	for _, x := range selected {
		wanted[x] = true
	}
	for _, account := range accounts {
		if len(wanted) > 0 && !wanted[account.Key] {
			continue
		}
		run.Scanned++
		s.mu.RLock()
		store := s.store
		s.mu.RUnlock()
		if until, backoffErr := store.BackoffUntil(ctx, account.Key); backoffErr != nil {
			run.Failed++
			continue
		} else if until.After(time.Now()) {
			run.Skipped++
			continue
		}
		snapshot, status, err := s.probe(account)
		if err != nil {
			_ = store.SetBackoff(ctx, account.Key, time.Now().Add(backoffDuration(status)))
			run.Failed++
			continue
		}
		snapshot, err = store.ResolveCycle(ctx, account.Key, snapshot)
		if err != nil {
			run.Failed++
			continue
		}
		account.Snapshot = snapshot
		_ = store.Observe(ctx, account, snapshot)
		if !snapshot.Eligible {
			run.Skipped++
			continue
		}
		run.Eligible++
		if !activate {
			continue
		}
		model, err := s.selectModel(account)
		if err != nil {
			run.Skipped++
			continue
		}
		reserved, err := store.Reserve(ctx, account.Key, snapshot.CycleID, model)
		if err != nil {
			run.Failed++
			continue
		}
		if !reserved {
			run.Skipped++
			continue
		}
		status, code, sendErr := s.activate(account, model)
		if sendErr != nil {
			_ = store.SetBackoff(ctx, account.Key, time.Now().Add(backoffDuration(status)))
			state := "failed_before_send"
			if status > 0 {
				state = "sent_unknown"
			}
			_ = store.SetCycle(ctx, account.Key, snapshot.CycleID, state, status, code)
			if state == "sent_unknown" {
				run.Partial++
			} else {
				run.Failed++
			}
			continue
		}
		after, _, probeErr := s.probe(account)
		if probeErr != nil {
			_ = store.SetCycle(ctx, account.Key, snapshot.CycleID, "sent_unknown", status, "verify_failed")
			run.Partial++
			continue
		}
		if !after.Eligible {
			_ = store.SetCycle(ctx, account.Key, snapshot.CycleID, "verified", status, "")
			run.Verified++
		} else {
			_ = store.SetCycle(ctx, account.Key, snapshot.CycleID, "partial", status, "quota_unchanged")
			run.Partial++
		}
	}
	run.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_ = s.store.SaveRun(ctx, run)
	s.mu.Lock()
	s.lastScan = time.Now()
	s.lastError = ""
	s.mu.Unlock()
	return run, nil
}

func backoffDuration(status int) time.Duration {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return time.Hour
	case status == http.StatusTooManyRequests:
		return 30 * time.Minute
	default:
		return 5 * time.Minute
	}
}
func (s *Service) setScan(err error) {
	s.mu.Lock()
	s.lastScan = time.Now()
	s.lastError = err.Error()
	s.mu.Unlock()
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (s *Service) Shutdown() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Lock()
	if s.store != nil {
		_ = s.store.Close()
		s.store = nil
	}
	s.mu.Unlock()
}
