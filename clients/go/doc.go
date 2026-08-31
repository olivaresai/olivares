// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package olivares is the first-party Go client for the Olivares AI control
// plane REST API (the published /v1 contract).
//
// The package has two layers:
//
//   - A hand-written core (client.go): endpoint/auth/tenancy wiring for the
//     opaque bearer tokens (olvs_/olvk_), the single error envelope mapped to
//     [APIError], cursor pagination ([Client.ListPages]), Retry-After-aware
//     retries for rate-limited calls, and surfacing of the stability
//     policy's deprecation signal (RFC 9745 Deprecation / RFC 8594 Sunset
//     response headers → [DeprecationNotice], once per endpoint).
//
//   - A generated operation layer (operations.gen.go, version.gen.go),
//     regenerated from the committed OpenAPI snapshot by `task sdk:generate`
//     and drift-checked by `task sdk:check`. Operations represent published
//     JSON schemas with generic Go values and preserve the declared media type
//     for raw request bytes; transport policy stays in the core.
//
// Versioning: [APIVersion] is the API contract major the operation layer was
// generated from; [Version] is the SDK's own semantic version, whose MAJOR
// tracks the API major from GA on. The governing policy is
// https://olivares.ai/docs (also [StabilityPolicy]).
//
// This module is Apache-2.0 and depends only on the Go standard library — it
// never links the AGPL engine (enforced by scripts/check-boundary.sh).
package olivares
