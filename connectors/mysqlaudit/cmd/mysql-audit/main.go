// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command mysql-audit ships the MySQL/MariaDB audit source connector as a
// standalone go-plugin binary. The engine launches it and talks to it over gRPC.
package main

import (
	"github.com/olivaresai/olivares/connectors/mysqlaudit"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(mysqlaudit.New())
}
