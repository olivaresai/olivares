// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package modelrouter is the native model routing/fallback contract of Olivares AI
// (module X, the "gateway" facet). It selects a primary model/provider and an
// ordered fallback chain from a modelprovider.Catalog under a policy
// (cost / latency / capability / pinned), without embedding a heavyweight external
// gateway in the single Go binary — the routing is native Go behind the Router
// interface (decision: native interface over embedding LiteLLM).
//
// Delegation is still possible behind the SAME interface: NewGatewayRouter wraps
// the native selection and marks every chosen target as routed through a
// configured external gateway endpoint (LiteLLM/OpenRouter-style). The operator
// thus gets native selection logic with the option to send the actual inference
// call through their gateway, and the product keeps a clean Apache license
// boundary and a dependency-free binary.
//
// This package is a SELECTION contract — it decides which model to use and in what
// fallback order — not a proxy. It performs no inference and sees no prompts; it
// reads only the catalog (capabilities/pricing/latency metadata). It is consumed
// by module X /. Apache-2.0; imports only the standard library and the
// Apache modelprovider contract, never the AGPL engine.
package modelrouter
