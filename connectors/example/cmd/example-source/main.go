// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command example-source ships the reference source connector as a standalone
// go-plugin binary. A connector author's main is exactly this: build the
// connector and hand it to plugin.ServeSource. The engine launches this binary
// and talks to it over gRPC; the connector code is unchanged from the in-process
// case. This is the worked example for "how to ship a connector as a plugin".
package main

import (
	"github.com/olivaresai/olivares/connectors/example"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(example.New())
}
