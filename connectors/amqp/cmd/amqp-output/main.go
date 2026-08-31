// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command amqp-output ships the AMQP 1.0 CloudEvents egress connector as a standalone
// go-plugin binary (CB-1 transport B), so go-amqp's dependency tree stays out of the
// core. The connector code is identical to the in-process case
// (rt.AddOutput(amqp.NewOutput())).
package main

import (
	"github.com/olivaresai/olivares/connectors/amqp"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeOutput(amqp.NewOutput())
}
