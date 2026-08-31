<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Connectors

First-party and community observation-source connectors for Olivares AI. Each connector ingests signals from one external system (an audit log, a model provider, an identity source, a secrets vault, a network mesh, a CI/CD pipeline) and emits typed events the engine's modules consume.

**License:** Apache-2.0 (permissive — the same license as the SDK).

## Directory layout

Each subdirectory is one connector, named by its `Kind` (the operator-facing handle in configuration). Examples: `pgaudit`, `claude-api`, `vault`, `ebpf`, `argocd`, `istio-telemetry`.

There are currently **159 connector directories** in the tree — **158 containing Go code** — spanning model providers, identity/auth, data platforms, secrets/KMS, network/mesh, IaC/GitOps, cloud management planes, SIEM/ITSM, defence/tactical, and agent surfaces. Of those, **12 are shared contract/library packages** rather than capabilities; the capability directories are wired as in-process sources (**114 unique kind aliases** across the source, roster and document builders), out-of-process plugin sources (**67 binaries**), output connectors (**22**), identity-roster providers (**22**) or content sources (**11**, of which **10 live**). Those categories overlap — one directory can be both an in-process source and a plugin — so they do not sum to the directory count. A source connector that lands without an activation path fails the counts gate on every push (`scripts/check-public-counts.sh`).

Every number above is re-derived mechanically and enforced on every push by [`scripts/check-public-counts.sh`](../scripts/check-public-counts.sh); connector classification is linted by [`scripts/check-connectors.sh`](../scripts/check-connectors.sh), and the per-connector guides live in [`docs-site/`](../docs-site/).

## Building a new connector

```sh
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ./my-source -name acme.my-source -module example.com/acme/my-source \
  -kind source -sdk-path ./sdk
```

See [`examples/build-a-connector/`](../examples/build-a-connector/) and the [SDK README](../sdk/README.md).

## Boundary rule

A connector may import only from `sdk/`, never from `core/` or `modules/`. This keeps the AGPL / Apache license boundary clean. The CI job [`scripts/check-boundary.sh`](../scripts/check-boundary.sh) enforces this on every push.
