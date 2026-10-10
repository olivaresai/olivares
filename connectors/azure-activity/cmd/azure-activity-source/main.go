// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command azure-activity-source ships the Azure management-plane connector (S165) — read-only Resource Graph inventory + Azure Monitor Activity Log activity —
// as a standalone go-plugin binary. The engine runs this connector in-process, so the
// release does not build this binary; build it to run the connector in its own process
// as an external plugin, over gRPC with AutoMTLS.
package main

import (
	azureactivity "github.com/olivaresai/olivares/connectors/azure-activity"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(azureactivity.New())
}
