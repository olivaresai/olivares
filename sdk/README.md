<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Olivares AI SDK

The stable connector and module interface — the contract third-party connectors and modules build against.

**License:** Apache-2.0 (permissive — third parties can write connectors without copyleft obligations).

## What's here

| Package | Purpose |
|---|---|
| `sdk` (root) | `SourceConnector`, `OutputConnector`, `Module` interfaces + `Descriptor`, `Config`, `ConfigField` |
| `sdk/event` | The typed event envelope (`Event`, `Type`) and publish/subscribe contract |
| `sdk/model` | Shared domain types: `Gateway`, `EdgeObservation`, `SignalSource`, enums |
| `sdk/plugin` | gRPC plugin protocol (AutoMTLS, `PluginServer` / `PluginClient`) for out-of-process connectors |
| `sdk/scaffold` | `olivares-connector-new` — the code generator that scaffolds a new connector project |

## Building a connector

See [`examples/build-a-connector/`](../examples/build-a-connector/) for a step-by-step tutorial, or run the scaffold:

```sh
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ./my-source -name acme.my-source -module example.com/acme/my-source \
  -kind source -sdk-path ./sdk
```

The boundary rule: a connector may import only from `sdk/`, never from `core/`. This keeps the AGPL / Apache license boundary clean and is enforced by [`scripts/check-boundary.sh`](../scripts/check-boundary.sh) in CI.

## Versioning

See [`VERSIONING.md`](VERSIONING.md) for the SDK stability contract.
