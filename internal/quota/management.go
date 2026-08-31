package quota

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"strings"
	"time"
)

func (s *Service) Registration() pluginapi.ManagementRegistrationResponse {
	base := "/plugins/codex-quota-activation/"
	return pluginapi.ManagementRegistrationResponse{Routes: []pluginapi.ManagementRoute{{Method: http.MethodGet, Path: base + "status"}, {Method: http.MethodGet, Path: base + "accounts"}, {Method: http.MethodGet, Path: base + "runs"}, {Method: http.MethodPost, Path: base + "scan"}, {Method: http.MethodGet, Path: base + "preview"}, {Method: http.MethodPost, Path: base + "preview"}, {Method: http.MethodGet, Path: base + "run"}, {Method: http.MethodPost, Path: base + "run"}, {Method: http.MethodGet, Path: base + "health"}}, Resources: []pluginapi.ResourceRoute{{Path: "/status", Menu: "Codex quota", Description: "Codex quota windows and activation status."}, {Path: "/status.js"}}}
}

type safeAccount struct {
	Key, ID, AuthIndex, Label, Plan string
	Snapshot                        Snapshot
	Diagnostic                      string       `json:"diagnostic,omitempty"`
	SelectedModel                   *ModelChoice `json:"selected_model,omitempty"`
	LastOutcome                     *RunOutcome  `json:"last_outcome,omitempty"`
}

func (s *Service) Management(req pluginapi.ManagementRequest, hostCallbackID string) (pluginapi.ManagementResponse, error) {
	if strings.HasSuffix(req.Path, "/status.js") {
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {"text/javascript; charset=utf-8"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}, Body: StatusJS()}, nil
	}
	if strings.HasSuffix(req.Path, "/status") && strings.Contains(req.Path, "/resource/") {
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}, "Content-Security-Policy": {"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'"}}, Body: StatusHTML()}, nil
	}
	headers := http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}, "Content-Security-Policy": {"default-src 'none'; frame-ancestors 'none'"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch {
	case strings.HasSuffix(req.Path, "/health"):
		s.mu.RLock()
		store := s.store
		cfg := s.cfg
		s.mu.RUnlock()
		if store == nil || store.Health(ctx) != nil {
			return qjson(headers, 503, map[string]any{"status": "unhealthy"})
		}
		catalog := s.catalogStatus(ctx, hostCallbackID)
		if !catalog.Ready {
			return qjson(headers, 503, map[string]any{"status": "unhealthy", "enabled": cfg.Enabled, "auto_activate": cfg.AutoActivate, "activation_ready": false, "catalog": catalog})
		}
		return qjson(headers, 200, map[string]any{"status": "healthy", "enabled": cfg.Enabled, "auto_activate": cfg.AutoActivate, "activation_ready": true, "catalog": catalog})
	case strings.HasSuffix(req.Path, "/status"):
		s.mu.RLock()
		cfg := s.cfg
		last := s.lastScan
		lastErr := s.lastError
		s.mu.RUnlock()
		catalog := s.catalogStatus(ctx, hostCallbackID)
		return qjson(headers, 200, map[string]any{"enabled": cfg.Enabled, "auto_activate": cfg.AutoActivate, "activation_ready": catalog.Ready, "scan_interval": cfg.ScanInterval.String(), "last_scan": last, "last_error": lastErr, "catalog": catalog})
	case strings.HasSuffix(req.Path, "/runs"):
		s.mu.RLock()
		store := s.store
		s.mu.RUnlock()
		runs, err := store.Runs(ctx)
		if err != nil {
			return pluginapi.ManagementResponse{}, err
		}
		return qjson(headers, 200, map[string]any{"data": runs})
	case strings.HasSuffix(req.Path, "/accounts"):
		accounts, err := s.accounts()
		if err != nil {
			return pluginapi.ManagementResponse{}, err
		}
		safe := []safeAccount{}
		for _, account := range accounts {
			snapshot, status, probeErr := s.probe(account, hostCallbackID)
			diagnostic := ""
			if probeErr != nil {
				snapshot.Reason = probeFailureReason(status, probeErr)
				diagnostic = sanitizeTransportDiagnostic(probeErr)
			}
			choice, choiceErr := s.selectModel(ctx, account, hostCallbackID)
			var selected *ModelChoice
			if choiceErr == nil {
				selected = &choice
			}
			var lastOutcome *RunOutcome
			if outcome, outcomeErr := s.store.LastOutcome(ctx, account.Key); outcomeErr == nil {
				lastOutcome = &outcome
			} else if !errors.Is(outcomeErr, sql.ErrNoRows) {
				diagnostic = "outcome_read_failed"
			}
			safe = append(safe, safeAccount{Key: account.Key, ID: account.ID, AuthIndex: account.AuthIndex, Label: account.Label, Plan: account.Plan, Snapshot: snapshot, Diagnostic: diagnostic, SelectedModel: selected, LastOutcome: lastOutcome})
		}
		return qjson(headers, 200, map[string]any{"data": safe})
	case strings.HasSuffix(req.Path, "/scan"):
		if req.Method != http.MethodPost {
			return qjson(headers, 405, map[string]any{"error": "method_not_allowed"})
		}
		run, err := s.Scan(context.Background(), "manual_scan", nil, false, hostCallbackID)
		if err != nil {
			return pluginapi.ManagementResponse{}, err
		}
		return qjson(headers, 200, map[string]any{"data": run})
	case strings.HasSuffix(req.Path, "/preview"):
		if req.Method == http.MethodGet {
			return qjson(headers, 200, map[string]any{"expires_in_seconds": 600})
		}
		var body struct {
			Accounts []string `json:"accounts"`
		}
		if len(req.Body) > 64*1024 || json.Unmarshal(req.Body, &body) != nil || len(body.Accounts) == 0 || len(body.Accounts) > 100 {
			return qjson(headers, 400, map[string]any{"error": "invalid_accounts"})
		}
		run, err := s.Scan(context.Background(), "preview", body.Accounts, false, hostCallbackID)
		if err != nil {
			return pluginapi.ManagementResponse{}, err
		}
		token, err := randomToken()
		if err != nil {
			return pluginapi.ManagementResponse{}, err
		}
		s.mu.Lock()
		s.confirmations[tokenHash(token)] = &confirmation{Accounts: append([]string(nil), body.Accounts...), Expires: time.Now().Add(10 * time.Minute)}
		s.mu.Unlock()
		return qjson(headers, 200, map[string]any{"data": run, "confirmation_token": token, "expires_at": time.Now().Add(10 * time.Minute)})
	case strings.HasSuffix(req.Path, "/run"):
		if req.Method == http.MethodGet {
			return qjson(headers, 200, map[string]any{"requires_confirmation": true, "confirmation_ttl_seconds": 600})
		}
		var body struct {
			ConfirmationToken string `json:"confirmation_token"`
		}
		if len(req.Body) > 4096 || json.Unmarshal(req.Body, &body) != nil {
			return qjson(headers, 400, map[string]any{"error": "invalid_body"})
		}
		hash := tokenHash(body.ConfirmationToken)
		s.mu.Lock()
		confirmation, ok := s.confirmations[hash]
		if !ok || confirmation.Used || time.Now().After(confirmation.Expires) {
			s.mu.Unlock()
			return qjson(headers, 409, map[string]any{"error": "invalid_or_expired_confirmation"})
		}
		confirmation.Used = true
		accounts := append([]string(nil), confirmation.Accounts...)
		delete(s.confirmations, hash)
		s.mu.Unlock()
		run, err := s.Scan(context.Background(), "manual_activation", accounts, true, hostCallbackID)
		if err != nil {
			return pluginapi.ManagementResponse{}, err
		}
		return qjson(headers, 200, map[string]any{"data": run})
	}
	return qjson(headers, 404, map[string]any{"error": "not_found"})
}

func (s *Service) catalogStatus(ctx context.Context, hostCallbackID string) CatalogStatus {
	s.mu.RLock()
	resolver := s.resolver
	s.mu.RUnlock()
	if resolver == nil {
		return CatalogStatus{ErrorCode: "model_catalog_unavailable", SelectedByPlan: map[string]ModelChoice{}}
	}
	return resolver.Status(ctx, hostCallbackID)
}
func qjson(headers http.Header, status int, value any) (pluginapi.ManagementResponse, error) {
	raw, err := json.Marshal(value)
	return pluginapi.ManagementResponse{StatusCode: status, Headers: headers, Body: raw}, err
}
