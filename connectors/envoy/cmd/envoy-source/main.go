// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command envoy-source ships the Envoy mesh observation connector as a standalone
// go-plugin binary: the engine launches it and talks to it over gRPC (CB-1 transport
// B, AutoMTLS). It runs OUT-OF-PROCESS so the Envoy go-control-plane dependency tree
// never links into the pure-Go core (ARCHITECTURE.md, §4). The connector code is identical
// to the in-process case (rt.AddSource(envoy.New())).
package main

import (
	conn "github.com/olivaresai/olivares/connectors/envoy"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(conn.New())
}
