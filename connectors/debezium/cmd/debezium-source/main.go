// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command debezium-source ships the Debezium CDC observer as a standalone go-plugin
// binary (CB-1 transport B), so franz-go's dependency tree never links into the
// core. The connector code is identical to the in-process case.
package main

import (
	"github.com/olivaresai/olivares/connectors/debezium"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(debezium.New())
}
