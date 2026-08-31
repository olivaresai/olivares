// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
)

// transport is the wire under a Client: it carries one JSON-RPC request/response
// (roundTrip) and fire-and-forget notifications (notify), framing them for stdio
// or Streamable HTTP. It is used sequentially by the Client (one introspection
// call at a time); implementations need not support concurrent roundTrips.
type transport interface {
	// roundTrip sends req and returns the raw `result` of the matching response,
	// or an error (transport failure, or a JSON-RPC error from the server).
	roundTrip(ctx context.Context, req rpcRequest) (json.RawMessage, error)
	// notify sends a notification (no id, no response).
	notify(ctx context.Context, method string, params any) error
	// setProtocolVersion records the negotiated protocol version so a transport
	// that needs it on the wire (Streamable HTTP) can send it on later requests.
	setProtocolVersion(v string)
	// Close releases the transport (terminates a subprocess, ends an HTTP session).
	Close() error
}
