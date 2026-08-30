# Security policy

Report vulnerabilities privately through GitHub Security Advisories. Never attach production credentials or databases.

Access and refresh tokens are read through the CLIProxyAPI host callback for the duration of a probe or activation and are neither persisted nor logged. Public resource routes contain no account data. All state and actions use Management-authenticated routes.

Because the upstream subscription protocol is undocumented, any unknown, missing, stale, contradictory, or malformed state fails closed.
