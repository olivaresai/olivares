// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command sops-source ships the SOPS+age GitOps metadata connector as a standalone go-plugin binary: the engine launches it and talks to it
// over gRPC (AutoMTLS). The connector code is identical to the in-process case
// (rt.AddSource(sops.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/sops"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(sops.New())
}
