// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command gcp-audit-source ships the GCP management-plane connector (S165) —
// read-only Resource Manager/IAM inventory + Cloud Audit Logs activity — as a
// standalone go-plugin binary. The engine runs this connector in-process, so the
// release does not build this binary; build it to run the connector in its own process
// as an external plugin, over gRPC with AutoMTLS.
package main

import (
	gcpaudit "github.com/olivaresai/olivares/connectors/gcp-audit"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(gcpaudit.New())
}
