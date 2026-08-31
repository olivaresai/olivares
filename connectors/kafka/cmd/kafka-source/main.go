// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command kafka-source ships the Kafka consumer-group observer as a standalone
// go-plugin binary: the engine launches it out-of-process (CB-1 transport B,
// AutoMTLS) so franz-go's dependency tree never links into the core. The connector
// code is identical to the in-process case (rt.AddSource(kafka.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/kafka"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(kafka.New())
}
