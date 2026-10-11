<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Connectors

First-party and community observation-source connectors for Olivares AI. Each connector ingests signals from one external system (an audit log, a model provider, an identity source, a secrets vault, a network mesh, a CI/CD pipeline) and emits typed events the engine's modules consume.

**License:** Apache-2.0 (permissive — the same license as the SDK).

## Directory layout

Capability subdirectories are named by their `Kind` (the operator-facing handle in configuration). Examples: `pgaudit`, `claude-api`, `vault`, `ebpf`, `argocd`, `istio-telemetry`. This tree also contains contract libraries, shared implementation helpers and the interoperability conformance matrix.

There are currently **151 connector directories** in the tree — **150 containing Go code**. Of those, **13 are shared contract/library packages** rather than capabilities, and `interop` holds the conformance matrix. The remaining Go capability directories are counted as **136 integrations**, spanning model providers, identity/auth, data platforms, secrets/KMS, network/mesh, IaC/GitOps, cloud management planes, defence/tactical, and agent surfaces. `shared/awssig` and `shared/delivery` expose existing internal signing, transport and encoding helpers; they add no user integration. Tests, `testdata/` and `node_modules/` do not establish production Go code, and the non-Go Backstage scaffold is outside this Go integration census.

The capability directories are wired as in-process sources (**128 unique kind aliases** across the source, roster and document builders), standalone plugin programs (**64 binaries**; the release builds only those of the out-of-process kinds, see [process mode per kind](../docs-site/src/content/docs/reference/connectors.md#process-mode-per-kind)), output connectors (**9**), identity-roster providers (**23**) or content sources (**11**, of which **10 live**). Those categories overlap — one directory can hold both an in-process source and a plugin program — so they do not sum to the directory count. A source connector that lands without an activation path fails the counts gate on every push (`scripts/check-public-counts.sh`).

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

SIEM and ITSM push implementations live in the private Business overlay. Community retains generic notifications and the ServiceNow CMDB reader.
