# Changelog

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
