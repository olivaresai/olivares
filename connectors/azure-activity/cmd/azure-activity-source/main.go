// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command azure-activity-source ships the Azure management-plane connector
// (S165) — read-only Resource Graph inventory + Azure Monitor Activity Log
// activity — as a standalone go-plugin binary.
package main

import (
	azureactivity "github.com/olivaresai/olivares/connectors/azure-activity"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(azureactivity.New())
}
