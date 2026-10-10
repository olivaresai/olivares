// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command claude-wif-source ships the Anthropic identity connector (NHI roster +
// PERMITTED grant edges + the WIF footgun finding) as a standalone go-plugin binary.
// The engine runs this connector in-process, so the release does not build this
// binary; build it to run the connector in its own process as an external plugin, over
// gRPC with AutoMTLS. The connector code is identical to the in-process case
// (rt.AddSource(claudewif.New())). The IDN-01 Exchanger is a host-wired primitive, not
// part of the Gather plugin surface.
package main

import (
	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(claudewif.New())
}
