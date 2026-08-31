// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command cursor-source ships the Cursor governance connector as a standalone go-plugin
// binary: the engine launches it and talks to it over gRPC. The connector code is
// identical to the in-process case (rt.AddSource(cursor.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/cursor"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(cursor.New())
}
