// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command gcp-audit-source ships the GCP management-plane connector (S165)
// — read-only Resource Manager/IAM inventory + Cloud Audit Logs activity — as a
// standalone go-plugin binary.
package main

import (
	gcpaudit "github.com/olivaresai/olivares/connectors/gcp-audit"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(gcpaudit.New())
}
