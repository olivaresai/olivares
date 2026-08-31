// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command acme-fabworks-erp ships the connector as a standalone go-plugin binary.
// The engine launches this binary and talks to it over gRPC (AutoMTLS); the
// connector code is identical to the in-process case — main is just the serve
// call, which blocks until the engine that launched it exits.
package main

import (
	fabworkserp "example.com/fabworks/olivares-connector-fabworks-erp"

	// Aliased to a fixed identifier so it can never collide with the connector
	// package name (which is derived from the connector's name).
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	sdkplugin.ServeContentSource(fabworkserp.New())
}
