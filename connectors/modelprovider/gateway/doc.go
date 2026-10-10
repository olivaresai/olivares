// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gateway is the model-invocation contract: one CreateMessage
// surface, pull-based streaming, cancellation that closes the upstream
// request, usage accounting, and errors mapped onto the product envelope.
//
// It lives under connectors/modelprovider so it stays on the Apache side of
// the license frontier (no /core import) and does not add a top-level
// connector directory (public-counts derives integrations from connectors/*/).
//
// The standalone Driver and CostHook contracts carry deprecation notices.
// Capability helpers and shared transport/envelope types are not deprecated.
//
// Transports: HTTP JSON (CreateMessage) and SSE or NDJSON (StreamMessage).
// WebSocket is not used for these protocols.
package gateway
