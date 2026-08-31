// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command kmip-source ships the OASIS KMIP v2.1 key-inventory connector as a standalone go-plugin binary.
package main

import (
	"github.com/olivaresai/olivares/connectors/kmip"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(kmip.New())
}
