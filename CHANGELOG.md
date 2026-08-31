# Changelog

## 1.2.1 - 2026-08-30

- Show the percentage and quota rail as remaining capacity instead of consumed capacity.

## 1.2.0 - 2026-08-30

- Reuse the authenticated Management Center session when the dashboard is embedded.
- Keep the bridged management key in memory only and reject cross-origin messages.
- Retain direct-URL key entry as a protected standalone fallback.

## 1.1.0 - 2026-08-30

- Redesign the status page with quota rails, reset countdowns, scan history, and a compact T3-inspired layout.
- Allow same-origin embedding in the CLIProxyAPI Management console while continuing to block third-party framing.

## 1.0.7 - 2026-08-30

- Accept the observed Codex window aliases while rejecting conflicting aliases.
- Treat token counters as optional when all are absent and reject partial or inconsistent accounting.
- Report bounded field-level reasons for unknown windows without exposing upstream payloads.

## 1.0.6 - 2026-08-30

- Accept both the current core `HTTPResponse` wire shape and canonical snake_case callback fields.
- Keep the advertised plugin version and upstream user agent sourced from one constant.

## 1.0.5 - 2026-08-30

- Add a bounded, redacted management diagnostic for otherwise unknown quota transport failures.

## 1.0.4 - 2026-08-30

- Forward the official management `host_callback_id` through quota probes, catalog reads and activation streams.

## 1.0.3 - 2026-08-30

- Classify quota probe transport failures into bounded, non-sensitive diagnostic codes.

## 1.0.2 - 2026-08-30

- Send the observed Codex quota protocol header while retaining honest plugin and platform identification.
- Let explicit manual dry-runs bypass persisted automatic backoff and expose only safe HTTP failure classes.

## 1.0.1 - 2026-08-30

- Discover exhausted Codex accounts that the host marks unavailable.
- Resolve plan and ChatGPT account ID from the credential ID-token claims.

## 1.0.0 - 2026-08-30

- Initial Codex-only release with fail-closed detection, persistent cycle reservation, automatic and confirmed manual activation, and embedded status UI.
