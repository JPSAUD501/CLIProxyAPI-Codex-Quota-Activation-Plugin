# Threat model

Protected assets are Codex OAuth credentials, account identifiers, quota state, activation cycles, and the Management key. Trust boundaries are the plugin ABI, host auth and HTTP callbacks, persistent volume, browser Management session, model and price catalogs, and the observed ChatGPT backend protocol.

Controls include Management authentication, data-free public shell, no-store and CSP headers, strict payload parsing, explicit absent/null handling, stable cycle identities, transactional reservations, no retry for uncertain sends, per-account outcome state, bounded requests, short-lived one-use confirmations, real OS/architecture identification, and no credential mutation.

The plugin does not claim that the private upstream protocol is stable or supported by OpenAI.
