// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command mysql-audit ships the MySQL/MariaDB audit source connector as a standalone
// go-plugin binary. The engine runs this connector in-process, so the release does not
// build this binary; build it to run the connector in its own process as an external
// plugin, over gRPC with AutoMTLS.
package main

import (
	"github.com/olivaresai/olivares/connectors/mysqlaudit"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(mysqlaudit.New())
}
