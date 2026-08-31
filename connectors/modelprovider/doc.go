// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package modelprovider is the shared contract of the Olivares AI model/provider
// connector family (claude-api, openai, gemini, local). It is the Apache-2.0 seam
// that feeds module X (models/providers) and module XI (cost/FinOps): the
// connectors emit usage/cost as sealed SDK observations and expose a typed
// catalog of providers, models, capabilities and pricing through this package.
//
// Two data paths, by design (README.md, modules X/XI):
//
//   - Usage and cost flow as model.CostSample observations over the SDK Sink
//     (already wired end-to-end as the "cost.sampled" event). ProviderRef and
//     ModelRef are natural references the engine resolves to entities, exactly
//     like an EdgeObservation. This package builds those samples (ToCostSample)
//     and derives the monetary amount from declared pricing when a provider's API
//     returns token counts only (DeriveCostMicroUSD).
//
//   - The catalog (providers, models, capabilities, key/workspace inventory) is
//     reference data, not an observation. A connector exposes it through the
//     CatalogProvider interface so module X / can read it directly. The
//     observation sum type in sdk/model is sealed (Edge/Cost/Finding only), so the
//     catalog deliberately does NOT travel as a fourth observation kind; keeping
//     it a typed Go contract holds the connector inside the /connectors+/sdk
//     license boundary without touching the frozen S02 wire contract.
//
// Security posture (docs/SECURITY-HARDENING.md-3): every connector built on this package is
// read-only over the provider API, persists no secrets, and is minimal-data — it
// carries token counts, cost, capabilities and inventory METADATA, never prompts,
// completions, or API-key values. Admin/usage APIs do not return key values; this
// package only ever holds an operator credential in memory for the duration of a
// request and never logs or persists it (see Client).
//
// The package imports only the standard library and the Apache-2.0 sdk/model
// vocabulary — never the AGPL engine (enforced by scripts/check-boundary.sh).
package modelprovider
