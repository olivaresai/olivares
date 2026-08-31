// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command azurekeyvault-source ships the Azure Key Vault / Managed HSM audit
// connector as a standalone go-plugin binary.
package main

import (
	"github.com/olivaresai/olivares/connectors/azurekeyvault"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(azurekeyvault.New())
}
