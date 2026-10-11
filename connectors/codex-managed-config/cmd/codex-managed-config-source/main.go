// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command codex-managed-config-source ships the OpenAI Codex managed-config governance
// connector as a standalone go-plugin binary. The engine runs this connector
// in-process, so the release does not build this binary; build it to run the connector
// in its own process as an external plugin, over gRPC with AutoMTLS. The connector
// code is identical to the in-process case (rt.AddSource(codexmanagedconfig.New())).
package main

import (
	codexmanagedconfig "github.com/olivaresai/olivares/connectors/codex-managed-config"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(codexmanagedconfig.New())
}
