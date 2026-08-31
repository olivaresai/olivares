<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# cmd/olivares

The composition root — the single `main` package that wires every core component, module and connector into the production binary. This is where the dependency graph is assembled: the store, the auth stack, the API server, the module runtime, the connector reconciler, the audit signer, the secret sealer, and the embedded web UI.

**License:** AGPL-3.0-only.

## Key files

| File | Purpose |
|---|---|
| `main.go` | Entry point, CLI commands (`serve`, `quickstart`, `version`, …) |
| `wire.go` | Module and connector registration (the 30 wired modules + connector catalog) |
| `boot.go` | Boot sequence: store open → schema → auth → modules → API → serve |
| `reconcile.go` | Live source reconciler: diffs the durable roster vs. running engine |
| `connectoronboard.go` | Console connector onboarding: seal + persist + apply |
| `sourcescopegate.go` | Composition-root adapters for source-scoping enforcement |

## Building

```sh
task build                  # builds bin/olivares (community edition)
```

There is **no `-tags enterprise` build in this repository.** The tag used to select
the commercial tree, but that tree now lives in its own private distribution, so
`go build -tags enterprise ./cmd/olivares` fails with undefined symbols — the seam
files here (`wire_noenterprise.go`, `edition_noenterprise.go`) are only half of the
pair. The enterprise edition is built and signed from that separate distribution.

The binary embeds the web UI (`core/internal/webui/dist/`), so a frontend build (`pnpm --prefix web build`) must precede the Go build.
