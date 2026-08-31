// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command kafka-output ships the Kafka CloudEvents egress connector as a standalone
// go-plugin binary (CB-1 transport B), so franz-go's dependency tree stays out of
// the core. The connector code is identical to the in-process case
// (rt.AddOutput(kafka.NewOutput())).
package main

import (
	"github.com/olivaresai/olivares/connectors/kafka"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeOutput(kafka.NewOutput())
}
