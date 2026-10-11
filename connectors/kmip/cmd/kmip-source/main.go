// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command kmip-source ships the OASIS KMIP v2.1 key-inventory connector as a standalone go-plugin binary. The engine runs this connector in-process, so
// the release does not build this binary; build it to run the connector in its own
// process as an external plugin, over gRPC with AutoMTLS.
package main

import (
	"github.com/olivaresai/olivares/connectors/kmip"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(kmip.New())
}
