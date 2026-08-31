// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command amqp-source ships the AMQP 1.0 observation receiver as a standalone
// go-plugin binary: the engine launches it out-of-process (CB-1 transport B,
// AutoMTLS) so go-amqp's dependency tree never links into the core. The connector
// code is identical to the in-process case (rt.AddSource(amqp.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/amqp"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(amqp.New())
}
