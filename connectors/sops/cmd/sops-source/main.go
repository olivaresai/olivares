// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command sops-source ships the SOPS+age GitOps metadata connector
// as a standalone go-plugin binary. The engine runs this connector in-process, so the
// release does not build this binary; build it to run the connector in its own process
// as an external plugin, over gRPC with AutoMTLS. The connector code is identical to
// the in-process case (rt.AddSource(sops.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/sops"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(sops.New())
}
