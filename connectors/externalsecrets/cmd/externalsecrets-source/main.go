// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command externalsecrets-source ships the External Secrets Operator (ESO)
// connector as a standalone go-plugin binary: the engine launches
// it and talks to it over gRPC (AutoMTLS). The connector code is identical to the
// in-process case (rt.AddSource(externalsecrets.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/externalsecrets"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(externalsecrets.New())
}
