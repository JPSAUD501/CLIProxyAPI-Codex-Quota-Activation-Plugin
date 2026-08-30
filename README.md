# CLIProxyAPI Codex Quota Activation Plugin

Native CLIProxyAPI plugin for inspecting Codex subscription quota windows and activating eligible fresh cycles with a minimal request.

> The ChatGPT subscription quota endpoints are not a documented public OpenAI API. The observed protocol is isolated, versioned, and fail-closed. A changed, incomplete, or contradictory payload is never activated.

## Safety model

- Codex OAuth credentials only; disabled, unavailable, malformed, and unknown-plan accounts are skipped.
- A cycle is reserved transactionally before sending. `partial` and `sent_unknown` cycles are never retried automatically.
- Automatic activation starts 60 seconds after health and runs every 30 minutes with global concurrency 1.
- Model selection requires a matching CLIProxyAPI plan catalog and LiteLLM price. An explicit override is available only for recovery.
- Manual activation requires a one-use confirmation token that expires in ten minutes.
- Credentials are held only for the upstream call and are never persisted or logged.

## Configuration

```yaml
plugins:
  enabled: true
  configs:
    codex-quota-activation:
      enabled: true
      auto_activate: true
      scan_interval: 30m
      max_concurrency: 1
      activation_model_mode: auto
      activation_model_override: ""
```

Open `/v0/resource/plugins/codex-quota-activation/status` and enter the Management key.

## Development

```sh
cd ui && npm ci && npm run build
cd .. && go test ./... && go vet ./...
go build -trimpath -buildmode=c-shared -o codex-quota-activation.so ./cmd/codex-quota-activation-plugin
```

See [SECURITY.md](SECURITY.md). MIT licensed.
