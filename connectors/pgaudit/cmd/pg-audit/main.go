// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command pg-audit ships the PostgreSQL pgAudit source connector as a standalone
// go-plugin binary. The engine launches it and talks to it over gRPC; the
// connector code is identical to the in-process case.
package main

import (
	"github.com/olivaresai/olivares/connectors/pgaudit"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(pgaudit.New())
}
