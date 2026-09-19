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
// This package is the invocation seam. It does not replace LiteLLM or Bifrost,
// and it does not make Olivares a general-purpose AI gateway. The live PEP
// (cmd/olivares/inferenceproxy.go) and models.Executor still speak
// protocol-specific clients; they should adopt this seam. That wiring is not
// in this slice.
//
// Transports: HTTP JSON (CreateMessage) and SSE or NDJSON (StreamMessage).
// WebSocket is not used for these protocols.
package gateway
