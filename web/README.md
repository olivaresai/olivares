<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Olivares AI Console (Web UI)

The embedded control console — a React 19 + Vite 8 + Tailwind 4 single-page
application that ships inside the Go binary via `go:embed`. It is the operator's
primary surface for inventory, the access map, sessions, policies, FinOps,
compliance, connectors, roles and delegation.

**License:** AGPL-3.0-only.

## Before adding a screen: two gates that tests do not replace

Both run in `pre-push`, but contributors need to know the constraints before
writing a screen:

- **`task lint:console-perms`** verifies that every permission queried through
  `can()` is declared by the engine. UI unit tests commonly stub `can()` and cannot
  prove that a permission name is reachable in production. An undeclared permission
  evaluates false for every role, hiding the action without producing an explanatory
  `403` response.
- **`task lint:nul`** rejects NUL bytes in source files. TypeScript and unit tests can
  accept them while line-oriented tooling treats the file as binary and stops
  reporting matches, which can blind later validation gates.

## Development

```sh
pnpm install
pnpm dev          # Vite dev server with HMR (proxied to the Go backend)
pnpm build        # production build → dist/ (embedded into the binary by go:embed)
pnpm test         # Vitest unit tests
pnpm e2e          # Playwright end-to-end tests
pnpm lint         # ESLint
pnpm typecheck    # TypeScript strict check
```

## Structure

| Directory | Purpose |
|---|---|
| `src/features/` | Feature modules (console, observability, compliance, security, identity, …) |
| `src/components/ui/` | Shared UI primitives (Badge, Button, Dialog, Field, Spinner, …) |
| `src/lib/` | Auth context, API client, hooks, i18n, routing |
| `e2e/` | Playwright E2E tests |
| `openapi/` | Generated OpenAPI spec (feeds the API reference) |
| `public/` | Static assets (favicon, OG image, webmanifest) |

## Internationalization

The console ships with 7 locales (en, es, de, fr, ja, ru, zh). Translation files live under each feature's `i18n/` directory. Machine-translated with an honest banner; native review is a post-launch pass.
