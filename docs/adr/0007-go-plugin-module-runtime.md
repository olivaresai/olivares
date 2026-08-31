# ADR-0007: Out-of-process module/connector runtime via go-plugin (gRPC)

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Olivares AI
- **References:** stack design (module runtime); license-boundary design

## Context and problem statement

The platform must let first-party and third-party connectors and modules extend it
without dragging their dependency trees into the engine, and without contaminating the
permissive connector ecosystem with the engine's copyleft license.

## Decision drivers

- Isolate connector dependencies from the engine's build/SBOM.
- A stable, versioned contract across the process boundary.
- Keep the Apache-2.0 connector boundary clean (a connector never links the AGPL engine).

## Considered options

- **`hashicorp/go-plugin` over gRPC** for out-of-process modules/connectors, plus
  compiled core modules in-process.
- **In-process plugins only** (Go `plugin` package or compiled-in).

## Decision outcome

Chosen option: **`hashicorp/go-plugin` (gRPC)** for out-of-process connectors/modules,
with first-party connectors embedded and launched as isolated subprocesses, and core
modules compiled in. The connector SDK is a Go interface plus a versioned gRPC/protobuf
contract.

### Consequences

- **Good:** a connector's dependencies do not enter the engine's binary/SBOM; the
  Apache/AGPL boundary stays clean and is enforced in CI; third parties can ship
  connectors independently.
- **Bad / trade-offs:** a gRPC contract to version and an IPC hop for out-of-process
  components.
- **Neutral:** the single binary still embeds first-party connectors (subprocess-
  isolated) so it stays one artifact.

## Why the alternatives were rejected

- **In-process only** — pulls every connector's dependencies into the engine and makes
  the license boundary impossible to enforce mechanically.
