// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "encoding/json"

// ConnectionSecretPrefix is the only part of the deployment's secret store a governed
// connection may name: store:connection/<name>. The engine's closed secret door
// (cmd/olivares/connectionsecrets.go) and the Business module both read it from here.
const ConnectionSecretPrefix = "connection/"

// Governed connections are a Business capability: a database, an SSH host or a
// service an administrator names once, with its credential by reference (a
// connection/ entry of the secret store) and the departments that may use it. A
// session never opens the connection and never sees the credential: the engine does,
// for a run whose department, or one above it, holds a grant, and audits each use.
//
// Both editions share the two types below because the session tool endpoint
// (cmd/olivares) is the same code in both. Community offers no tool and takes no call;
// the Business build lists and serves the brokered surface (connections_enterprise.go).

// ConnectionTool is one tool of the brokered connection surface a session is offered.
type ConnectionTool struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of the tool's arguments.
	InputSchema json.RawMessage
	// ReadOnly is true for a tool that changes nothing on the connection's target.
	ReadOnly bool
}

// ConnectionOutcome is what a brokered call answers to the session: Text is the
// JSON body the model reads, IsError marks a refusal or a failure of the call.
type ConnectionOutcome struct {
	Text    string
	IsError bool
}
