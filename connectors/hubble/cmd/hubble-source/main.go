// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command hubble-source ships the Cilium Hubble flow observation connector as a
// standalone go-plugin binary: the engine launches it and talks to it over gRPC
// (CB-1 transport B, AutoMTLS). It runs OUT-OF-PROCESS so the Cilium API dependency
// tree never links into the pure-Go core (ARCHITECTURE.md, §4). The connector code is
// identical to the in-process case (rt.AddSource(hubble.New())).
package main

import (
	conn "github.com/olivaresai/olivares/connectors/hubble"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(conn.New())
}
